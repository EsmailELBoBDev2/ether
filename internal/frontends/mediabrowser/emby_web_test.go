package mediabrowser

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/frontend/frontendtest"
)

func TestEmbyWebComesFromTheOwnersEmbyServer(t *testing.T) {
	var gotPath, gotToken, gotAuth string
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotToken, gotAuth = r.URL.Path, r.Header.Get("X-Emby-Token"), r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/web/index.html":
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("Set-Cookie", "emby=1")
			_, _ = w.Write([]byte("<!doctype html><title>Emby</title>"))
		case "/web/modules/apphost.js":
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write([]byte("define([],function(){})"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer src.Close()
	t.Setenv(embyWebSourceEnv, src.URL+"/")

	inst := mountDialect(t, "emby", frontendtest.Experience{})
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-Emby-Token", "viewer-secret")
		r.Header.Set("Authorization", "MediaBrowser Token=\"viewer-secret\"")
		inst.Handler().ServeHTTP(w, r)
		return w
	}

	w := get("/web/")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "<title>Emby</title>") || gotPath != "/web/index.html" {
		t.Fatalf("index: %d %q from %s", w.Code, w.Body.String(), gotPath)
	}
	if w.Header().Get("Set-Cookie") != "" {
		t.Fatal("the source's cookies must not reach the viewer")
	}
	if gotToken != "" || gotAuth != "" {
		t.Fatal("a viewer's credentials must not be sent to the source")
	}
	if w := get("/web/modules/apphost.js"); w.Code != 200 || w.Body.String() != "define([],function(){})" {
		t.Fatalf("apphost.js: %d %q", w.Code, w.Body.String())
	}
	if w := get("/web/missing.js"); w.Code != 404 {
		t.Fatalf("missing file: %d", w.Code)
	}
	if w := get("/web/../../etc/passwd"); w.Code == 200 {
		t.Fatal("a path outside /web must not be fetched")
	}
	if w := get("/"); w.Code != http.StatusFound || !strings.HasSuffix(w.Header().Get("Location"), "/web/") {
		t.Fatalf("root: %d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestEmbyWebWithoutASourceKeepsThePlaceholder(t *testing.T) {
	t.Setenv(embyWebSourceEnv, "")
	inst := mountDialect(t, "emby", frontendtest.Experience{})
	for path, want := range map[string]int{"/web/modules/apphost.js": 404, "/": 404} {
		w := httptest.NewRecorder()
		inst.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != want {
			t.Fatalf("%s: %d, want %d", path, w.Code, want)
		}
	}
	for _, bad := range []string{"emby:8096", "ftp://emby", "http://user:pw@emby:8096"} {
		t.Setenv(embyWebSourceEnv, bad)
		if embyWebSource() != "" {
			t.Fatalf("%q must not be used as a source", bad)
		}
	}
}
