package clientnet

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// A trusted proxy may be named instead of numbered, for example a Docker
// container ("nginx-proxy-manager") whose address changes when it is
// recreated. Names are resolved through the system resolver, which inside a
// Docker network is Docker's own DNS, and re-resolved in the background so a
// request never waits on DNS after the first lookup.
const (
	proxyHostTTL      = 30 * time.Second
	proxyHostRetry    = 5 * time.Second
	proxyLookupBudget = 2 * time.Second
)

// lookupProxyHost resolves a proxy name. Tests replace it.
var lookupProxyHost = func(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

type proxyHostEntry struct {
	prefixes   []netip.Prefix
	expires    time.Time
	refreshing bool
	ready      chan struct{}
}

var proxyHosts struct {
	mu      sync.Mutex
	entries map[string]*proxyHostEntry
}

var errProxyEntry = errors.New("enter an IP address, a CIDR range or a host or container name (for example 192.168.1.5, 10.0.0.0/8 or nginx-proxy-manager)")

// proxyHostName reports whether raw is a host or container name rather than
// an address: letters, digits, '.', '-' and '_', starting with a letter or
// digit and containing at least one letter.
func proxyHostName(raw string) (string, bool) {
	s := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	if s == "" || len(s) > 253 || strings.Contains(s, "..") {
		return "", false
	}
	letter := false
	for i, c := range s {
		switch {
		case c >= 'a' && c <= 'z':
			letter = true
		case c >= '0' && c <= '9':
		case (c == '.' || c == '-' || c == '_') && i > 0:
		default:
			return "", false
		}
	}
	if !letter {
		return "", false
	}
	if _, err := netip.ParseAddr(s); err == nil {
		return "", false
	}
	return s, true
}

// ValidateProxyEntry accepts what ProxyPrefixes can trust: an IP address, a
// CIDR range or a host or container name.
func ValidateProxyEntry(raw string) error {
	if _, err := ParseProxyEntry(raw); err == nil {
		return nil
	} else if strings.Contains(raw, "/") {
		return err
	}
	if _, ok := proxyHostName(raw); ok {
		return nil
	}
	return errProxyEntry
}

// HasProxyHostNames reports whether any entry is a name, so callers that
// cache ProxyPrefixes know the result can change without a config change.
func HasProxyHostNames(entries []string) bool {
	for _, raw := range entries {
		if _, err := ParseProxyEntry(raw); err == nil {
			continue
		}
		if _, ok := proxyHostName(raw); ok {
			return true
		}
	}
	return false
}

// proxyHostPrefixes returns the addresses a proxy name resolves to, as
// single-address prefixes. The first lookup of a name waits up to
// proxyLookupBudget; afterwards the last answer is served while a refresh
// runs. A name that does not resolve grants no trust.
func proxyHostPrefixes(name string) []netip.Prefix {
	now := time.Now()
	proxyHosts.mu.Lock()
	if proxyHosts.entries == nil {
		proxyHosts.entries = make(map[string]*proxyHostEntry)
	}
	e := proxyHosts.entries[name]
	if e == nil {
		e = &proxyHostEntry{refreshing: true, ready: make(chan struct{})}
		proxyHosts.entries[name] = e
		proxyHosts.mu.Unlock()
		go resolveProxyHost(name, e)
		select {
		case <-e.ready:
		case <-time.After(proxyLookupBudget):
		}
		proxyHosts.mu.Lock()
		out := e.prefixes
		proxyHosts.mu.Unlock()
		return out
	}
	out := e.prefixes
	if now.After(e.expires) && !e.refreshing {
		e.refreshing = true
		e.ready = make(chan struct{})
		go resolveProxyHost(name, e)
	}
	proxyHosts.mu.Unlock()
	return out
}

func resolveProxyHost(name string, e *proxyHostEntry) {
	ctx, cancel := context.WithTimeout(context.Background(), proxyLookupBudget)
	addrs, err := lookupProxyHost(ctx, name)
	cancel()
	var prefixes []netip.Prefix
	seen := make(map[netip.Addr]struct{})
	for _, a := range addrs {
		a = a.Unmap().WithZone("")
		if !a.IsValid() || a.IsUnspecified() {
			continue
		}
		if _, dup := seen[a]; dup {
			continue
		}
		seen[a] = struct{}{}
		prefixes = append(prefixes, netip.PrefixFrom(a, a.BitLen()))
	}
	proxyHosts.mu.Lock()
	ttl := proxyHostTTL
	if err != nil || len(prefixes) == 0 {
		ttl = proxyHostRetry
	}
	// A name that no longer exists stops being trusted at once. A lookup
	// that merely timed out keeps the last answer until the retry.
	var dnsErr *net.DNSError
	if err == nil || (errors.As(err, &dnsErr) && dnsErr.IsNotFound) || len(e.prefixes) == 0 {
		e.prefixes = prefixes
	}
	e.expires = time.Now().Add(ttl)
	e.refreshing = false
	close(e.ready)
	proxyHosts.mu.Unlock()
}

// resetProxyHosts forgets every resolved name. Intended for tests.
func resetProxyHosts() {
	proxyHosts.mu.Lock()
	proxyHosts.entries = nil
	proxyHosts.mu.Unlock()
}
