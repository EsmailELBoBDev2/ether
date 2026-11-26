package mediabrowser

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/frontend/frontendtest"
)

func TestUserNameNeverEmpty(t *testing.T) {
	for _, tc := range []struct {
		v    frontend.Viewer
		want string
	}{
		{frontend.Viewer{ID: 4, Username: "sam", Title: "Sam Rivera"}, "sam"},
		{frontend.Viewer{ID: 4, Username: "  ", Title: "Sam Rivera"}, "Sam Rivera"},
		{frontend.Viewer{ID: 4}, "Viewer 4"},
	} {
		if got := userName(&tc.v); got != tc.want {
			t.Errorf("userName(%+v) = %q, want %q", tc.v, got, tc.want)
		}
	}
}

func TestBrandingPointsViewersAtThePortal(t *testing.T) {
	for _, tc := range []struct {
		name     string
		protocol string
		quick    any
		portal   string
		want     string
	}{
		{"jellyfin", "jellyfin", nil, "https://ether.example/ether/me", "Quick Connect"},
		{"emby has no Quick Connect", "emby", true, "https://ether.example/ether/me", "username and password"},
		{"quick connect off", "jellyfin", false, "https://ether.example/ether/me", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := frontend.Settings{}
			if tc.quick != nil {
				cfg["quickConnect"] = tc.quick
			}
			host := frontend.Host{
				Identity: frontendtest.Identity{ResolveFunc: func(context.Context, string) (*frontend.Viewer, error) {
					return &frontend.Viewer{ID: 1}, nil
				}},
				Experience: frontendtest.Experience{},
				Config:     func() frontend.Settings { return cfg },
				ViewerPortalURL: func(context.Context, *http.Request) (string, error) {
					return tc.portal, nil
				},
			}
			var front frontend.Frontend = Jellyfin{MachineID: "test"}
			if tc.protocol == "emby" {
				front = Emby{MachineID: "test"}
			}
			inst, err := front.Mount(context.Background(), host)
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			inst.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/Branding/Configuration", nil))
			var body map[string]any
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			got, _ := body["LoginDisclaimer"].(string)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("unexpected hint %q", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) || !strings.Contains(got, "viewer page") {
				t.Fatalf("hint %q should mention %q and the viewer page", got, tc.want)
			}
			if tc.protocol == "emby" && strings.Contains(got, "Quick Connect") {
				t.Fatalf("Emby apps have no server Quick Connect, hint %q", got)
			}
			// A derived address can be a host that does not serve the page.
			if strings.Contains(got, "http") {
				t.Fatalf("hint %q names an address", got)
			}
		})
	}
}
