package plex

import (
	"net/http"

	"github.com/braedonsaunders/ether/internal/frontend"
)

// lanSignIn is Plex Media Server's "networks allowed without auth": a local
// request that carries no token is served as the viewer the owner chose on
// the Plex app page. With no viewer chosen, or from outside the local
// network, it returns nil and the request needs a plex.tv sign-in as before.
// Plex Web served from this server then opens without app.plex.tv.
func (s *Server) lanSignIn(r *http.Request, pr *Request) *frontend.User {
	host := s.getHost()
	if pr.Token != "" || pr.Remote || host.LANViewer == nil {
		return nil
	}
	u, err := host.LANViewer(r.Context(), r)
	if err != nil || u == nil {
		return nil
	}
	if policy, ok := s.policy().(frontend.UserPolicySource); ok {
		u = policy.UserPolicy(u)
	}
	// A transcode belongs to the viewer that started it, as with a token.
	if id := transcodeSessionID(r.URL.Path); id != "" {
		if e, ok := s.transcodes.get(id); !ok || e.userID != u.ID {
			return nil
		}
	}
	return u
}
