package plex

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestPlexWebServesTheBundledApp(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "js"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "index.html"), []byte("<title>Plex</title>"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "js", "main.js"), []byte("console.log(1)"), 0o644)
	_ = os.WriteFile(filepath.Join(t.TempDir(), "secret"), []byte("no"), 0o644)
	t.Setenv(plexWebDirEnv, dir)
	r := chi.NewRouter()
	// Like the Plex surface's commonHeaders.
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("Content-Security-Policy", "script-src 'none'; sandbox")
			next.ServeHTTP(w, req)
		})
	})
	r.Get("/web", servePlexWeb)
	r.Get("/web/*", servePlexWeb)
	get := func(p string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		return w
	}
	if w := get("/web"); w.Code != http.StatusFound || w.Header().Get("Location") != "/web/index.html" {
		t.Fatalf("/web: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := get("/web/index.html"); w.Code != 200 || w.Body.String() != "<title>Plex</title>" {
		t.Fatalf("index: %d %q", w.Code, w.Body.String())
	} else if w.Header().Get("Content-Security-Policy") != "" {
		t.Fatal("Plex Web must not be served with the script-blocking policy")
	}
	if w := get("/web/js/main.js"); w.Code != 200 || w.Body.String() != "console.log(1)" {
		t.Fatalf("asset: %d %q", w.Code, w.Body.String())
	}
	for _, p := range []string{"/web/js", "/web/missing.js", "/web/../secret"} {
		if w := get(p); w.Code == 200 {
			t.Fatalf("%s must not be served", p)
		}
	}
	t.Setenv(plexWebDirEnv, t.TempDir())
	if w := get("/web/index.html"); w.Code != 404 {
		t.Fatalf("without a bundle: %d", w.Code)
	}
}
