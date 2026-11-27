package mediabrowser

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/frontend/frontendtest"
)

func TestEmbyWebServesTheBundledApp(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><html lang=\"en\"><title>Emby</title></html>"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "modules", "apphost.js"), []byte("define([],function(){})"), 0o644)
	t.Setenv(embyWebDirEnv, dir)
	inst := mountDialect(t, "emby", frontendtest.Experience{})
	get := func(p string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		inst.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		return w
	}
	if w := get("/web/"); w.Code != 200 || !strings.Contains(w.Body.String(), "<title>Emby</title>") {
		t.Fatalf("index: %d %q", w.Code, w.Body.String())
	}
	// Emby Web reads its version from the page and refuses to start without one.
	if w := get("/web/index.html"); w.Code != 200 || !strings.Contains(w.Body.String(), `<html data-appversion="4.10.2.0" lang="en">`) {
		t.Fatalf("index.html: %d %q", w.Code, w.Body.String())
	}
	if w := get("/web/modules/apphost.js"); w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("apphost.js: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if w := get("/web/modules"); w.Code == 200 {
		t.Fatal("a directory must not be listed")
	}
	if w := get("/"); w.Code != http.StatusFound || !strings.HasSuffix(w.Header().Get("Location"), "/web/") {
		t.Fatalf("root: %d %q", w.Code, w.Header().Get("Location"))
	}

	t.Setenv(embyWebDirEnv, t.TempDir())
	if w := get("/web/modules/apphost.js"); w.Code != 404 {
		t.Fatalf("without a bundle: %d", w.Code)
	}
	if w := get("/"); w.Code != 404 {
		t.Fatalf("root without a bundle: %d", w.Code)
	}
}
