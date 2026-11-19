package app

import (
	"context"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/frontends/vela"
	"github.com/braedonsaunders/ether/internal/store"
)

// The revision and owner choice must commit together and survive restarts.
func TestVelaAppearanceRevision(t *testing.T) {
	dir := t.TempDir()
	cfg, err := config.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	db := store.OpenTest(t)
	registry := frontend.NewRegistry(func(frontend.Protocol) frontend.Host { return frontend.Host{} })
	if err := registry.Register(vela.Frontend{DB: db}); err != nil {
		t.Fatal(err)
	}
	control := newFrontendControl(registry, cfg)
	control.db = db
	for _, tc := range []struct {
		skin     string
		revision uint64
	}{{"cinematic", 0}, {"lounge", 1}, {"lounge", 1}, {"cinematic", 2}} {
		if err := control.PutSettings(context.Background(), "vela", frontend.Settings{"defaultSkinId": tc.skin}); err != nil {
			t.Fatal(err)
		}
		reopened, err := config.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		got := reopened.Read().Frontends["vela"]
		if got.AppearanceRevision != tc.revision || got.Settings["defaultSkinId"] != tc.skin {
			t.Fatalf("persisted: %#v", got)
		}
	}
	if err := control.PutSettings(context.Background(), "vela", frontend.Settings{"defaultSkinId": "../secret"}); err == nil {
		t.Fatal("invalid skin accepted")
	}

	// Branding name + uploaded wordmarks also advance AppearanceRevision and normalize whitespace.
	wm, err := control.UploadVelaWordmark(context.Background(), strings.NewReader(`<svg xmlns="http://www.w3.org/2000/svg" width="300" height="75"><rect width="300" height="75"/></svg>`))
	if err != nil {
		t.Fatal(err)
	}
	if err := control.PutSettings(context.Background(), "vela", frontend.Settings{
		"defaultSkinId": "cinematic",
		"appName":       "  Home Theatre  ",
		"wordmark":      map[string]any{"url": wm.URL, "width": float64(wm.Width), "height": float64(wm.Height)},
	}); err != nil {
		t.Fatal(err)
	}
	got := cfg.Read().Frontends["vela"]
	if got.AppearanceRevision != 3 || got.Settings["appName"] != "Home Theatre" {
		t.Fatalf("after branding save: %#v", got)
	}
	// Saving identical trimmed values does not bump revision.
	if err := control.PutSettings(context.Background(), "vela", frontend.Settings{
		"defaultSkinId": "cinematic",
		"appName":       "Home Theatre",
		"wordmark":      map[string]any{"url": wm.URL, "width": float64(wm.Width), "height": float64(wm.Height)},
	}); err != nil {
		t.Fatal(err)
	}
	if cfg.Read().Frontends["vela"].AppearanceRevision != 3 {
		t.Fatalf("expected revision 3 on no-op branding save, got %d", cfg.Read().Frontends["vela"].AppearanceRevision)
	}
	// Unknown digest is rejected when DB is present.
	if err := control.PutSettings(context.Background(), "vela", frontend.Settings{
		"wordmark": map[string]any{"url": "/vela/v0/branding/" + strings.Repeat("f", 64) + ".svg", "width": 300.0, "height": 75.0},
	}); err == nil {
		t.Fatal("expected unknown wordmark digest to be rejected")
	}
}
