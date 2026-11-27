package plex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
)

// With a viewer chosen for local sign-in, a local request without a token is
// that viewer (Plex Web opens without app.plex.tv); a remote one, or one
// with no viewer chosen, still needs a token.
func TestLANSignInWithoutToken(t *testing.T) {
	f := newFixture()
	defer f.srv.Close()
	get := func(ip string) int {
		req := httptest.NewRequest(http.MethodGet, "/library/sections", nil)
		req.RemoteAddr = ip + ":40000"
		req.Header.Set("Accept", "application/json")
		rec := httptest.NewRecorder()
		f.srv.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := get("192.168.1.50"); code != http.StatusUnauthorized {
		t.Fatalf("no viewer chosen: %d, want 401", code)
	}
	calls := 0
	f.srv.mutateHost(func(h *frontend.Host) {
		h.LANViewer = func(context.Context, *http.Request) (*core.Viewer, error) {
			calls++
			u := friendUser
			return &u, nil
		}
	})
	if code := get("192.168.1.50"); code != http.StatusOK {
		t.Fatalf("local request with a chosen viewer: %d, want 200", code)
	}
	if code := get("203.0.113.9"); code != http.StatusUnauthorized {
		t.Fatalf("remote request: %d, want 401", code)
	}
	if calls != 1 {
		t.Fatalf("LANViewer consulted %d times, want once (local only)", calls)
	}
	// A token still decides when one is sent, even a stale one.
	req := httptest.NewRequest(http.MethodGet, "/library/sections", nil)
	req.RemoteAddr = "192.168.1.50:40000"
	req.Header.Set("X-Plex-Token", "stale")
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("stale token on the LAN: %d, want 401", rec.Code)
	}
}

func TestPlexSettingsAcceptLANViewer(t *testing.T) {
	r := frontend.NewRegistry(func(frontend.Protocol) frontend.Host { return frontend.Host{} })
	if err := r.Register(Frontend{}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		settings frontend.Settings
		ok       bool
	}{
		{frontend.Settings{"port": 32400.0, "viewerId": 5.0}, true},
		{frontend.Settings{"viewerId": 0.0}, false},
		{frontend.Settings{"discovery": true}, false},
	} {
		if _, err := r.ValidateSettings("plex", tc.settings); (err == nil) != tc.ok {
			t.Errorf("%v: err %v, want ok=%v", tc.settings, err, tc.ok)
		}
	}
}
