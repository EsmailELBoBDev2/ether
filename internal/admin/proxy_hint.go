package admin

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/identity"
)

// proxyHintLookups resolves names for the reverse-proxy hint. Tests replace it.
var proxyHintLookups = struct {
	addr func(ctx context.Context, addr string) ([]string, error)
	host func(ctx context.Context, host string) ([]netip.Addr, error)
}{
	addr: net.DefaultResolver.LookupAddr,
	host: func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	},
}

// untrustedProxy is one reverse proxy Ether sees but does not trust yet.
type untrustedProxy struct {
	Address string `json:"address"`
	// Name is a host or container name that resolves back to Address
	// (Docker's DNS names a container this way), so trusting it survives
	// the container getting a new address. Empty when there is none.
	Name     string    `json:"name,omitempty"`
	Requests int64     `json:"requests"`
	LastSeen time.Time `json:"lastSeen"`
}

// handleProxyHint lists home-network peers that forward public client
// addresses without being trusted, so the console can offer to trust them.
// Nothing is trusted here: the owner adds the entry and saves.
func (h *Handler) handleProxyHint(w http.ResponseWriter, r *http.Request) {
	sightings := identity.UntrustedForwarders(h.proxyPrefixes()...)
	if len(sightings) > 3 {
		sightings = sightings[:3]
	}
	out := make([]untrustedProxy, 0, len(sightings))
	for _, s := range sightings {
		out = append(out, untrustedProxy{
			Address:  s.Peer.String(),
			Name:     proxyName(r.Context(), s.Peer),
			Requests: s.Requests,
			LastSeen: s.LastSeen.UTC(),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"proxies": out})
}

// proxyName returns the first label of addr's reverse DNS name when that
// label resolves back to addr. Docker answers "container.network." for a
// container's address; the bare container name is what the owner knows.
func proxyName(ctx context.Context, addr netip.Addr) string {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	names, err := proxyHintLookups.addr(ctx, addr.String())
	if err != nil {
		return ""
	}
	for _, full := range names {
		label, _, _ := strings.Cut(strings.TrimSuffix(full, "."), ".")
		if label == "" || identity.ValidateProxyEntry(label) != nil || !identity.HasProxyHostNames([]string{label}) {
			continue
		}
		addrs, err := proxyHintLookups.host(ctx, label)
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if a.Unmap() == addr {
				return label
			}
		}
	}
	return ""
}
