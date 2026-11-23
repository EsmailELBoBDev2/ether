package newznab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An indexer that serves Newznab at /api/newznab (Netuno) works from its bare
// host URL and from the full endpoint URL.
func TestNewznabEndpointAtAPINewznab(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimRight(r.URL.Path, "/") != "/api/newznab" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<caps><searching><search available="yes" supportedParams="q"/></searching><categories><category id="2000" name="Movies"/></categories></caps>`))
	}))
	defer srv.Close()
	for _, base := range []string{srv.URL, srv.URL + "/api/newznab", srv.URL + "/api/newznab/"} {
		c := New(testConfig(base), nil, Options{})
		if _, err := c.fetchCaps(context.Background()); err != nil {
			t.Fatalf("%s: caps: %v", base, err)
		}
	}
}
