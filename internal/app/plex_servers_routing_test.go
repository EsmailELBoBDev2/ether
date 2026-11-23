package app

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/plextv"
)

// routingEnv is an enrollment env with one started extra server (p2), one
// added but not started (p3), and a main server certificate hash.
func routingEnv(t *testing.T) *enrollEnv {
	t.Helper()
	e := newEnrollEnv(t)
	if err := e.app.cfg.Update(func(c *config.Config) error {
		c.Plex.CertHash = "mainhash"
		c.PlexExtraServers = []config.PlexExtraServer{
			{ID: "p2", MachineIdentifier: "machine-p2", ClientIdentifier: "client-p2", State: config.PlexServerActive,
				Claimed: true, OwnerUsername: "second", CertHash: "abc123", AddedAt: 1},
			{ID: "p3", MachineIdentifier: "machine-p3", ClientIdentifier: "client-p3", State: config.PlexServerStandby,
				CertHash: "def456", AddedAt: 2},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestRequestRoutingPicksTheServerAsked(t *testing.T) {
	e := routingEnv(t)
	ps := e.app.plexExtras
	route := func(host, token string) string {
		r := httptest.NewRequest("GET", "/identity", nil)
		r.Host = host
		if token != "" {
			r.Header.Set("X-Plex-Token", token)
		}
		return ps.serverForRequest(r)
	}
	// Each server's own plex.direct name picks it; the main name and any
	// name that is not a started server stay on the main server.
	for host, want := range map[string]string{
		"1-2-3-4.abc123.plex.direct:32400":   "p2",
		"1-2-3-4.ABC123.plex.direct":         "p2",
		"1-2-3-4.mainhash.plex.direct:32400": "",
		"1-2-3-4.def456.plex.direct:32400":   "", // p3 is not started
		"192.168.1.5:32400":                  "",
		"media.example.com":                  "",
	} {
		if got := route(host, ""); got != want {
			t.Errorf("host %s routed to %q, want %q", host, got, want)
		}
	}

	// A token issued for p2's share routes to p2 on a plain address.
	ctx := context.Background()
	if err := e.app.users.SyncServerFriends(ctx, "p2", []plextv.Friend{
		{PlexID: 300, Username: "viewer", Token: "viewer-token-p2"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := route("192.168.1.5:32400", "viewer-token-p2"); got != "p2" {
		t.Fatalf("p2 token routed to %q", got)
	}
	if got := route("192.168.1.5:32400", "unknown-token"); got != "" {
		t.Fatalf("unknown token routed to %q", got)
	}

	// The identity for a p2 request is p2's own: machine id, name, owner.
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "1-2-3-4.abc123.plex.direct:32400"
	id, ok := ps.identityForRequest(r)
	if !ok || id.MachineID != "machine-p2" || id.Account == nil || id.Account.OwnerUsername != "second" || !id.Account.Claimed {
		t.Fatalf("identity = %+v ok=%v", id, ok)
	}
	if id.MachineID == e.app.cfg.Get().Server.MachineIdentifier {
		t.Fatal("p2 answered with the main machine identifier")
	}
	r.Host = "1-2-3-4.mainhash.plex.direct:32400"
	if _, ok := ps.identityForRequest(r); ok {
		t.Fatal("a main-server request got an extra identity")
	}
}

func TestRequestRoutingOffWhenFeatureOff(t *testing.T) {
	e := routingEnv(t)
	if err := e.app.cfg.Update(func(c *config.Config) error {
		c.Plex.ExtraServersEnabled = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/identity", nil)
	r.Host = "1-2-3-4.abc123.plex.direct:32400"
	if got := e.app.plexExtras.serverForRequest(r); got != "" {
		t.Fatalf("routed to %q with the feature off", got)
	}
	if _, ok := e.app.plexExtras.identityForRequest(r); ok {
		t.Fatal("an extra identity answered with the feature off")
	}
}

func TestPickSendsNewViewersToTheMainServerFirst(t *testing.T) {
	e := routingEnv(t)
	ps := e.app.plexExtras
	ctx := context.Background()
	// Main has room: it takes the viewer.
	if id, err := ps.pick(ctx); err != nil || id != "" {
		t.Fatalf("pick with room on main = %q, %v", id, err)
	}
	// Main full; p2 is started in config but not running here, and p3 is
	// not started: nothing takes the viewer, and nothing starts on its own.
	ps.noteSeats("", 1000)
	if id, err := ps.pick(ctx); err == nil {
		t.Fatalf("pick with main full = %q, want an error", id)
	}
	for _, s := range e.app.cfg.Get().PlexExtraServers {
		if s.ID == "p3" && s.State != config.PlexServerStandby {
			t.Fatal("a server the owner did not start was started")
		}
	}
}
