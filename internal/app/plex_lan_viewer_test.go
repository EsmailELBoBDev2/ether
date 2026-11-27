package app

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/identity"
)

// The Plex page saves the local sign-in viewer next to the main-listener
// port; saving without it turns local sign-in off.
func TestPlexLANViewerSetting(t *testing.T) {
	ctx := context.Background()
	a := setupTestApp(t, true)
	local, err := identity.NewLocal(a.db)
	if err != nil {
		t.Fatal(err)
	}
	id, err := local.CreateViewer(ctx, "living-room", "password-1234")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.users.RefreshViewers(ctx); err != nil {
		t.Fatal(err)
	}
	cur, err := a.frontends.control.Settings(ctx, "plex")
	if err != nil {
		t.Fatal(err)
	}
	port := cur["port"]
	if err := a.frontends.control.PutSettings(ctx, "plex", frontend.Settings{"port": port, "viewerId": float64(id)}); err != nil {
		t.Fatal(err)
	}
	got, err := a.frontends.control.Settings(ctx, "plex")
	if err != nil || got["viewerId"] != float64(id) || got["port"] != port {
		t.Fatalf("settings after save: %v %v", got, err)
	}
	lan := httptest.NewRequest("GET", "/library/sections", nil)
	lan.RemoteAddr = "192.168.1.20:5000"
	if v, err := a.plexLANViewer(ctx, lan, got); err != nil || v == nil || v.ID != id {
		t.Fatalf("local request: %v %v", v, err)
	}
	// A request relayed by a proxy Ether does not trust is not local.
	relayed := httptest.NewRequest("GET", "/library/sections", nil)
	relayed.RemoteAddr = "172.18.0.5:5000"
	relayed.Header.Set("X-Forwarded-For", "203.0.113.7")
	if v, err := a.plexLANViewer(ctx, relayed, got); err == nil || v != nil {
		t.Fatalf("untrusted relay signed in as %v", v)
	}
	remote := httptest.NewRequest("GET", "/library/sections", nil)
	remote.RemoteAddr = "203.0.113.7:5000"
	if v, err := a.plexLANViewer(ctx, remote, got); err == nil || v != nil {
		t.Fatalf("remote request signed in as %v", v)
	}
	if err := a.frontends.control.PutSettings(ctx, "plex", frontend.Settings{"port": port}); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.frontends.control.Settings(ctx, "plex"); got["viewerId"] != nil {
		t.Fatalf("viewer kept after clearing: %v", got)
	}
	if err := a.frontends.control.PutSettings(ctx, "plex", frontend.Settings{"port": port, "discovery": true}); err == nil {
		t.Fatal("unknown Plex setting accepted")
	}
}

// With ETHER_PORT set, the port is not the console's to change, but the
// local sign-in viewer still saves.
func TestPlexLANViewerSavesUnderEtherPort(t *testing.T) {
	ctx := context.Background()
	a := setupTestApp(t, true)
	cur, err := a.frontends.control.Settings(ctx, "plex")
	if err != nil {
		t.Fatal(err)
	}
	port := cur["port"].(float64)
	t.Setenv("ETHER_PORT", "32400")
	if err := a.frontends.control.PutSettings(ctx, "plex", frontend.Settings{"port": port, "viewerId": float64(1)}); err != nil {
		t.Fatalf("unchanged port with ETHER_PORT: %v", err)
	}
	if got, _ := a.frontends.control.Settings(ctx, "plex"); got["viewerId"] != float64(1) {
		t.Fatalf("viewer not saved: %v", got)
	}
	if err := a.frontends.control.PutSettings(ctx, "plex", frontend.Settings{"port": port + 1}); err == nil {
		t.Fatal("port change accepted under ETHER_PORT")
	}
}
