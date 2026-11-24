package plex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

// blockingSearchCatalog holds the search for "fj" until a newer search
// from the same client supersedes it.
type blockingSearchCatalog struct {
	*fakeCatalog
	started chan struct{}
}

func (c *blockingSearchCatalog) Search(ctx context.Context, query string, types []core.MediaType, limit int) ([]core.Title, error) {
	if strings.EqualFold(query, "fj") {
		c.started <- struct{}{}
		<-ctx.Done()
		return nil, context.Cause(ctx)
	}
	return c.fakeCatalog.Search(ctx, query, types, limit)
}

// A search the client's next keystroke superseded answers in the
// endpoint's own no-results shape. For /hubs/search that is PMS's 17 empty
// hubs, never a container without hubs, which PMS never sends (W373).
func TestSupersededSearchAnswersNoResultsShape(t *testing.T) {
	for _, path := range []string{"/hubs/search", "/library/search"} {
		for _, accept := range []string{"", "application/json"} {
			bc := &blockingSearchCatalog{started: make(chan struct{}, 1)}
			f := newFixture(func(d *testServerDeps) {
				bc.fakeCatalog = d.Catalog.(*fakeCatalog)
				d.Catalog = bc
			})
			search := func(query string) *httptest.ResponseRecorder {
				hdr := map[string]string{"X-Plex-Client-Identifier": "roku-1", "X-Plex-Platform": "Roku"}
				if accept != "" {
					hdr["Accept"] = accept
				}
				return f.request(t, http.MethodGet, path+"?query="+query+"&limit=10", hdr)
			}
			done := make(chan *httptest.ResponseRecorder)
			go func() { done <- search("Fj") }()
			<-bc.started
			search("Fjo")
			superseded := <-done
			noResults := search("zzzz")
			if superseded.Code != http.StatusOK || superseded.Body.String() != noResults.Body.String() {
				t.Errorf("%s [%s]: superseded answer\n%d %s\nwant the no-results answer\n%s", path, accept, superseded.Code, superseded.Body.String(), noResults.Body.String())
			}
			f.srv.Close()
		}
	}
}
