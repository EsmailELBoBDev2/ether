package clientnet

import (
	"context"
	"net"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
	"time"
)

// fakeProxyDNS swaps the proxy-name resolver for a table the test controls.
func fakeProxyDNS(t *testing.T, table map[string][]netip.Addr) *sync.Mutex {
	t.Helper()
	var mu sync.Mutex
	prev := lookupProxyHost
	lookupProxyHost = func(_ context.Context, host string) ([]netip.Addr, error) {
		mu.Lock()
		defer mu.Unlock()
		addrs, ok := table[host]
		if !ok {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		return addrs, nil
	}
	resetProxyHosts()
	t.Cleanup(func() {
		lookupProxyHost = prev
		resetProxyHosts()
	})
	return &mu
}

func TestValidateProxyEntryAcceptsAddressesRangesAndNames(t *testing.T) {
	for _, ok := range []string{"192.168.1.5", "10.0.0.0/8", "fd00::/8", "nginx-proxy-manager", "npm", "proxy.lan", "caddy_1", "NPM."} {
		if err := ValidateProxyEntry(ok); err != nil {
			t.Errorf("ValidateProxyEntry(%q) = %v, want ok", ok, err)
		}
	}
	for _, bad := range []string{"", "0.0.0.0/0", "::/0", "1.2.3", "-proxy", "prox y", "proxy..lan", "http://npm", "npm:81", "10.0.0.0/33"} {
		if err := ValidateProxyEntry(bad); err == nil {
			t.Errorf("ValidateProxyEntry(%q) = nil, want an error", bad)
		}
	}
	if !HasProxyHostNames([]string{"10.0.0.1", "npm"}) || HasProxyHostNames([]string{"10.0.0.1", "10.0.0.0/8"}) {
		t.Fatal("HasProxyHostNames must report only real names")
	}
}

func TestProxyPrefixesResolvesContainerNames(t *testing.T) {
	fakeProxyDNS(t, map[string][]netip.Addr{
		"nginx-proxy-manager": {netip.MustParseAddr("172.18.0.2")},
	})
	got := ProxyPrefixes([]string{"192.168.1.5", "nginx-proxy-manager", "gone-container"})
	want := []netip.Prefix{netip.MustParsePrefix("192.168.1.5/32"), netip.MustParsePrefix("172.18.0.2/32")}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ProxyPrefixes = %v, want %v", got, want)
	}

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.18.0.2:41000"
	r.Header.Set("X-Forwarded-For", "203.0.113.10")
	ip, local := ClientNet(r, got...)
	if ip != netip.MustParseAddr("203.0.113.10") || local {
		t.Fatalf("ClientNet behind a named proxy = %v local=%v, want 203.0.113.10 remote", ip, local)
	}
}

func TestProxyNameFollowsTheContainerToANewAddress(t *testing.T) {
	table := map[string][]netip.Addr{"npm": {netip.MustParseAddr("172.18.0.2")}}
	mu := fakeProxyDNS(t, table)
	if got := proxyHostPrefixes("npm"); len(got) != 1 || got[0].Addr() != netip.MustParseAddr("172.18.0.2") {
		t.Fatalf("first lookup = %v", got)
	}

	// The container is recreated with a new address. Once the cached answer
	// expires, the next read starts a refresh and a later read sees it.
	mu.Lock()
	table["npm"] = []netip.Addr{netip.MustParseAddr("172.18.0.9")}
	mu.Unlock()
	proxyHosts.mu.Lock()
	proxyHosts.entries["npm"].expires = time.Now().Add(-time.Second)
	proxyHosts.mu.Unlock()
	_ = proxyHostPrefixes("npm")
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := proxyHostPrefixes("npm")
		if len(got) == 1 && got[0].Addr() == netip.MustParseAddr("172.18.0.9") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after the container moved, prefixes = %v", got)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The container is removed: its name must stop granting trust.
	mu.Lock()
	delete(table, "npm")
	mu.Unlock()
	proxyHosts.mu.Lock()
	proxyHosts.entries["npm"].expires = time.Now().Add(-time.Second)
	proxyHosts.mu.Unlock()
	_ = proxyHostPrefixes("npm")
	deadline = time.Now().Add(2 * time.Second)
	for len(proxyHostPrefixes("npm")) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("a removed container is still trusted")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestUntrustedForwarderIsReportedNotBelieved(t *testing.T) {
	resetForwarders()
	t.Cleanup(resetForwarders)
	send := func(peer, xff string) (netip.Addr, bool) {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = peer + ":5000"
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return ClientNet(r)
	}
	ip, local := send("172.18.0.2", "203.0.113.10")
	if ip != netip.MustParseAddr("172.18.0.2") || !local {
		t.Fatalf("untrusted proxy header must not be believed: got %v local=%v", ip, local)
	}
	send("172.18.0.2", "203.0.113.11")
	send("192.168.1.7", "")              // no header: not a proxy
	send("192.168.1.8", "10.0.0.4")      // only private hops: nothing to fix
	send("203.0.113.50", "203.0.113.10") // public peer: not a home proxy

	got := UntrustedForwarders()
	if len(got) != 1 || got[0].Peer != netip.MustParseAddr("172.18.0.2") || got[0].Requests != 2 {
		t.Fatalf("UntrustedForwarders = %+v, want only 172.18.0.2 with 2 requests", got)
	}
	if len(UntrustedForwarders(netip.MustParsePrefix("172.18.0.2/32"))) != 0 {
		t.Fatal("a proxy that is now trusted must not be reported")
	}
}

func TestUntrustedForwardersStayBounded(t *testing.T) {
	resetForwarders()
	t.Cleanup(resetForwarders)
	for i := 1; i <= maxForwarders+10; i++ {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = netip.AddrFrom4([4]byte{10, 0, 1, byte(i)}).String() + ":5000"
		r.Header.Set("X-Real-IP", "203.0.113.10")
		ClientNet(r)
	}
	if n := len(UntrustedForwarders()); n != maxForwarders {
		t.Fatalf("remembered %d forwarders, want %d", n, maxForwarders)
	}
}
