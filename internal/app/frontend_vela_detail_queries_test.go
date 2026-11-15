package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	artworkservice "github.com/braedonsaunders/ether/internal/artwork/service"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/frontend/frontendtest"
	"github.com/braedonsaunders/ether/internal/frontend/kit/ids"
	"github.com/braedonsaunders/ether/internal/frontends/vela"
	"github.com/braedonsaunders/ether/internal/identity"
	"github.com/braedonsaunders/ether/internal/plextv"
	"github.com/braedonsaunders/ether/internal/resolver"
	"github.com/braedonsaunders/ether/internal/store"
	"github.com/braedonsaunders/ether/internal/tmdb"
)

type detailQueryIndexer struct{ releases []core.Release }

func (detailQueryIndexer) Name() string { return "offline" }
func (f detailQueryIndexer) Search(context.Context, core.SearchRequest) ([]core.Release, error) {
	return f.releases, nil
}
func (detailQueryIndexer) FetchNZB(context.Context, core.Release) ([]byte, error) {
	return nil, core.ErrNotAvailable
}

// Measure the actual HTTP detail and app ports with cold verdict caches. Only
// identity at the protocol boundary, indexers and external artwork are faked.
func TestVelaDetailBoundedQueries(t *testing.T) {
	a := setupTestApp(t, true)
	ctx := t.Context()
	if err := a.users.SetOwner(ctx, plextv.Account{ID: 99, Username: "owner"}, "owner-token"); err != nil {
		t.Fatal(err)
	}
	viewer, err := a.users.Owner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	title := core.Title{Key: core.MovieKey(44), Type: core.TypeMovie, IDs: core.IDs{TMDB: 44}, Title: "Inception", Year: 2010, Released: time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC)}
	for n := 1; n <= 27; n++ {
		title.Cast = append(title.Cast, core.Person{TMDB: int64(n), Name: fmt.Sprint("Actor ", n), ThumbPath: fmt.Sprintf("/person%d.jpg", n)})
	}
	title.Crew = []core.Person{{TMDB: 100, Name: "Director", Role: "Director", ThumbPath: "/director.jpg"}}
	if err := a.catalog.UpsertTitle(ctx, title, true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.ExecContext(ctx, `UPDATE catalog_titles SET detail=1,detail_at=? WHERE key=?`, time.Now().Unix(), int64(title.Key)); err != nil {
		t.Fatal(err)
	}
	source := *a.artwork
	source.Metadata = nil
	source.Variants = func(*core.Title, string) (*tmdb.Images, bool) { return &tmdb.Images{}, true }
	source.Images = artworkservice.NewRemoteCache(1024, &http.Client{Transport: disabledArtworkTransport{}})
	originalResolver := a.resolver
	defer func() { a.resolver = originalResolver }()
	for _, count := range []int{1, 442} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			releases := make([]core.Release, count)
			for n := range releases {
				releases[n] = core.Release{ID: fmt.Sprint("release-", n), Name: fmt.Sprintf("Inception.2010.1080p.WEB-DL.H264-G%d", n), Size: 8 << 30}
			}
			a.resolver = resolver.New(detailQueryIndexer{releases}, nil, a.db, a.cfg.Read(), resolver.Options{})
			defer a.resolver.Close()
			if done := a.resolver.RefillSearchCache(&title); done != nil {
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("offline cache fill timed out")
				}
			}
			exp, err := a.newSurfaceExperience("vela", source)
			if err != nil {
				t.Fatal(err)
			}
			grants, err := identity.NewGrants(a.db, a.users, time.Hour, nil)
			if err != nil {
				t.Fatal(err)
			}
			device := core.Device{ID: "tv"}
			credential, err := grants.Issue(ctx, viewer.ID, nil, "vela", device)
			if err != nil {
				t.Fatal(err)
			}
			images, err := identity.NewArtworkImageGrantService(a.db, "vela", grants, func() frontend.Experience { return exp }, source, time.Minute, nil)
			if err != nil {
				t.Fatal(err)
			}
			inst, err := (vela.Frontend{ReplayKey: make([]byte, 32)}).Mount(ctx, frontend.Host{Experience: exp, Identity: frontendtest.Identity{ResolveDeviceFunc: func(ctx context.Context, token string, _ core.Device) (*core.User, time.Time, error) {
				v, e := grants.Resolve(ctx, "vela", token)
				return v, time.Time{}, e
			}}, ImageGrants: images, Sessions: frontendtest.Sessions{}})
			if err != nil {
				t.Fatal(err)
			}
			defer inst.Close(context.Background())
			measured, stats := store.WithQueryStats(ctx)
			req := httptest.NewRequest("GET", "/vela/v0/titles/"+ids.RatingKeyGUID(title.Key), nil).WithContext(measured)
			req.Header.Set("Authorization", "Bearer "+credential)
			req.Header.Set("X-Vela-Device-Id", "tv")
			w := httptest.NewRecorder()
			inst.Handler().ServeHTTP(w, req)
			if w.Code != 200 {
				t.Fatalf("detail: %d %s", w.Code, w.Body.String())
			}
			var verdictReads, creditReads int64
			for _, st := range stats.Statements() {
				if strings.Contains(st.SQL, "FROM resolver_hardsub") {
					verdictReads += st.Count
				}
				if strings.Contains(st.SQL, "COALESCE(p.imdb,'') AS imdb") {
					creditReads += st.Count
				}
				if strings.HasPrefix(st.SQL, "INSERT") || strings.HasPrefix(st.SQL, "UPDATE") || strings.HasPrefix(st.SQL, "DELETE") {
					t.Logf("write %dx %s", st.Count, st.SQL)
				}
			}
			t.Logf("candidates=%d queries=%d writes=%d verdict_reads=%d joined_credit_reads=%d", count, stats.Queries(), stats.Writes(), verdictReads, creditReads)
			if verdictReads != 1 || creditReads != 1 || stats.Queries() > 100 {
				t.Fatalf("unbounded detail: queries=%d verdict=%d credits=%d", stats.Queries(), verdictReads, creditReads)
			}
		})
	}
}
