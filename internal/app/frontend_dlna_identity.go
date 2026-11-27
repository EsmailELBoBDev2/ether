package app

import (
	"context"
	"math"
	"net/http"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/identity"
)

// lanViewer shares the DLNA trust rule across credential-free LAN surfaces.
// Every surface requires its own explicit viewer selection; there is no owner fallback.
func (a *App) lanViewer(ctx context.Context, req *http.Request, settings frontend.Settings) (*core.Viewer, error) {
	_, lan := identity.ClientNet(req, identity.ProxyPrefixes(a.cfg.Read().Server.TrustedProxyCIDRs)...)
	if !lan {
		return nil, core.ErrUnauthorized
	}
	value, ok := settings["viewerId"].(float64)
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) || value < 1 || value > 9007199254740991 || math.Trunc(value) != value {
		return nil, core.ErrUnauthorized
	}
	viewer, err := a.users.ActiveUser(ctx, int64(value))
	if err != nil || viewer == nil || viewer.ID != int64(value) {
		return nil, core.ErrUnauthorized
	}
	return viewer, nil
}

// plexLANViewer signs a Plex request that carries no token in as the
// owner-selected viewer, like Plex Media Server's "networks allowed without
// auth": Plex Web on the local network then opens without a plex.tv sign-in.
// A request relayed by a proxy Ether does not trust is refused, because its
// real client cannot be told apart from the proxy's local address.
func (a *App) plexLANViewer(ctx context.Context, req *http.Request, settings frontend.Settings) (*core.Viewer, error) {
	trusted := identity.ProxyPrefixes(a.cfg.Read().Server.TrustedProxyCIDRs)
	if !identity.TrustedPeer(req, trusted...) && forwardedRequest(req) {
		return nil, core.ErrUnauthorized
	}
	return a.lanViewer(ctx, req, settings)
}

func forwardedRequest(r *http.Request) bool {
	return r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Real-IP") != "" || r.Header.Get("Forwarded") != ""
}
