package plex

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPlexWebRedirectsToTheHostedApp(t *testing.T) {
	for _, path := range []string{"/web", "/web/index.html"} {
		w := httptest.NewRecorder()
		redirectPlexWeb(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusFound || w.Header().Get("Location") != plexWebURL {
			t.Fatalf("%s: %d %q", path, w.Code, w.Header().Get("Location"))
		}
	}
}
