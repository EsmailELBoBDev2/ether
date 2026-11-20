package webclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServeVelaWithBrandManifestAndHeaders(t *testing.T) {
	dir := t.TempDir()
	htmlDoc := `<!doctype html><html><head><title>Default Release Title</title><link rel="manifest" href="/vela/manifest.webmanifest" /><link rel="icon" href="/vela/vela-icon.svg" /><link rel="apple-touch-icon" href="/vela/vela-icon-180.png" /></head><body></body></html>`
	manifestDoc := `{"id":"/vela","name":"Default Release","short_name":"DefRel","start_url":"/vela","scope":"/vela","display":"standalone","icons":[{"src":"/vela/vela-icon-192.png","sizes":"192x192","type":"image/png","purpose":"any"}]}`
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(htmlDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.webmanifest"), []byte(manifestDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vela-icon-180.png"), []byte("release-180"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Empty brand keeps release title, manifest name/short_name, and release icons with no-cache + ETag.
	w := httptest.NewRecorder()
	ServeVelaWithBrand(w, httptest.NewRequest(http.MethodGet, "/vela/manifest.webmanifest", nil), dir, VelaBrand{})
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-cache" || w.Header().Get("ETag") == "" {
		t.Fatalf("manifest headers: code=%d cc=%q etag=%q", w.Code, w.Header().Get("Cache-Control"), w.Header().Get("ETag"))
	}
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["name"] != "Default Release" || m["short_name"] != "DefRel" {
		t.Fatalf("expected release manifest fallback, got %#v", m)
	}

	// Custom brand patches name, short_name, icons, HTML title, and PNG favicon link.
	brand := VelaBrand{
		AppName:     "Cinema House",
		IconVersion: "v1",
		IconIsSVG:   false,
		IconAsset: func(name string) (VelaIconAsset, bool) {
			return VelaIconAsset{
				ContentType: "image/png",
				ETag:        `"icon-` + name + `"`,
				Data:        []byte("custom-" + name),
			}, true
		},
	}

	wHTML := httptest.NewRecorder()
	ServeVelaWithBrand(wHTML, httptest.NewRequest(http.MethodGet, "/vela/index.html", nil), dir, brand)
	if !strings.Contains(wHTML.Body.String(), "<title>Cinema House</title>") || !strings.Contains(wHTML.Body.String(), `href="/vela/favicon-32.png"`) {
		t.Fatalf("patched index.html unexpected: %s", wHTML.Body.String())
	}

	wIcon := httptest.NewRecorder()
	ServeVelaWithBrand(wIcon, httptest.NewRequest(http.MethodGet, "/vela/vela-icon-180.png", nil), dir, brand)
	if wIcon.Code != http.StatusOK || wIcon.Body.String() != "custom-vela-icon-180.png" || wIcon.Header().Get("Cache-Control") != "no-cache" || wIcon.Header().Get("ETag") != `"icon-vela-icon-180.png"` {
		t.Fatalf("custom icon serve: code=%d cc=%q etag=%q body=%q", wIcon.Code, wIcon.Header().Get("Cache-Control"), wIcon.Header().Get("ETag"), wIcon.Body.String())
	}

	condReq := httptest.NewRequest(http.MethodGet, "/vela/vela-icon-180.png", nil)
	condReq.Header.Set("If-None-Match", wIcon.Header().Get("ETag"))
	w304 := httptest.NewRecorder()
	ServeVelaWithBrand(w304, condReq, dir, brand)
	if w304.Code != http.StatusNotModified {
		t.Fatalf("conditional icon status = %d, want 304", w304.Code)
	}
}
