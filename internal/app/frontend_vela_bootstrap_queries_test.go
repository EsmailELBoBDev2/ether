package app

import (
	"context"
	"encoding/json"
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
	"github.com/braedonsaunders/ether/internal/frontends/vela"
	"github.com/braedonsaunders/ether/internal/identity"
	"github.com/braedonsaunders/ether/internal/plextv"
	"github.com/braedonsaunders/ether/internal/store"
	"github.com/braedonsaunders/ether/internal/tmdb"
)

// Keep a row-serial control using the real single-page admission port. The
// measured HTTP calls share the same catalogue, ranker, policy and artwork;
// only the grouping of page resolution changes.
type serialBootstrapExperience struct{ *surfaceExperience }

func (s serialBootstrapExperience) ResolveRowViews(ctx context.Context, reqs []frontend.RowViewPageRequest) ([]frontend.RowViewPage, error) {
	out := make([]frontend.RowViewPage, 0, len(reqs))
	for _, req := range reqs {
		page, err := s.ResolveRowView(ctx, req)
		if err != nil {
			return nil, err
		}
		out = append(out, page)
	}
	return out, nil
}

func TestVelaBootstrapBoundedQueries(t *testing.T) {
	a := setupTestApp(t, true)
	ctx := t.Context()
	if err := a.users.SetOwner(ctx, plextv.Account{ID: 99, Username: "owner"}, "owner-token"); err != nil {
		t.Fatal(err)
	}
	viewer, err := a.users.Owner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	source := *a.artwork
	source.Metadata = nil
	source.Variants = func(*core.Title, string) (*tmdb.Images, bool) { return &tmdb.Images{}, true }
	source.Images = artworkservice.NewRemoteCache(1024, &http.Client{Transport: disabledArtworkTransport{}})
	a.catalog.FreezeCacheMinute()
	for _, count := range []int{40, 240} {
		for n := 1; n <= count; n++ {
			title := core.Title{Key: core.MovieKey(int64(n)), Type: core.TypeMovie, IDs: core.IDs{TMDB: int64(n)}, Title: fmt.Sprintf("Film %03d", n), Year: 2000, Released: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), Runtime: 90 * time.Minute, Genres: []string{"Drama", "Comedy"}, Rating: 8, VoteCount: 1000, Popularity: 50, PosterPath: fmt.Sprintf("/poster%d.jpg", n), BackdropPath: fmt.Sprintf("/backdrop%d.jpg", n)}
			if err := a.catalog.UpsertTitle(ctx, title, true); err != nil {
				t.Fatal(err)
			}
		}
		for _, serial := range []bool{true, false} {
			name := fmt.Sprintf("titles=%d/serial=%t", count, serial)
			t.Run(name, func(t *testing.T) {
				raw, err := a.newSurfaceExperience("vela", source)
				if err != nil {
					t.Fatal(err)
				}
				exp := raw.(*surfaceExperience)
				var experience frontend.Experience = exp
				if serial {
					experience = serialBootstrapExperience{exp}
				}
				grants, err := identity.NewGrants(a.db, a.users, time.Hour, nil)
				if err != nil {
					t.Fatal(err)
				}
				device := core.Device{ID: name}
				credential, err := grants.Issue(ctx, viewer.ID, nil, "vela", device)
				if err != nil {
					t.Fatal(err)
				}
				images, err := identity.NewArtworkImageGrantService(a.db, "vela", grants, func() frontend.Experience { return experience }, source, time.Minute, nil)
				if err != nil {
					t.Fatal(err)
				}
				inst, err := (vela.Frontend{ReplayKey: make([]byte, 32)}).Mount(ctx, frontend.Host{Experience: experience, Identity: frontendtest.Identity{ResolveDeviceFunc: func(ctx context.Context, token string, _ core.Device) (*core.User, time.Time, error) {
					v, e := grants.Resolve(ctx, "vela", token)
					return v, time.Time{}, e
				}}, ImageGrants: images, Sessions: frontendtest.Sessions{}})
				if err != nil {
					t.Fatal(err)
				}
				defer inst.Close(context.Background())
				measured, stats := store.WithQueryStats(ctx)
				req := httptest.NewRequest("GET", "/vela/v0/bootstrap", nil).WithContext(measured)
				req.Header.Set("Authorization", "Bearer "+credential)
				req.Header.Set("X-Vela-Device-Id", device.ID)
				w := httptest.NewRecorder()
				inst.Handler().ServeHTTP(w, req)
				if w.Code != 200 {
					t.Fatalf("bootstrap: %d %s", w.Code, w.Body.String())
				}
				var body struct {
					Rows   []struct{ Items []json.RawMessage }
					Heroes []json.RawMessage
				}
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				cards := len(body.Heroes)
				for _, row := range body.Rows {
					cards += len(row.Items)
				}
				if cards < 6 {
					t.Fatalf("fixture returned only %d cards: %s", cards, w.Body.String())
				}
				var facets, receipts, snapshots int64
				for _, st := range stats.Statements() {
					if strings.Contains(st.SQL, "SELECT key, tag, a, b") {
						facets += st.Count
					}
					if strings.HasPrefix(st.SQL, "INSERT INTO experience_receipts(") {
						receipts += st.Count
					}
					if strings.HasPrefix(st.SQL, "INSERT INTO experience_row_views(") {
						snapshots += st.Count
					}
					if st.Count > 3 {
						t.Logf("%dx %.160s", st.Count, st.SQL)
					}
				}
				t.Logf("titles=%d serial=%t shelves=%d cards=%d queries=%d writes=%d facets=%d receipt_inserts=%d snapshot_inserts=%d", count, serial, len(body.Rows), cards, stats.Queries(), stats.Writes(), facets, receipts, snapshots)
				if !serial && (stats.Queries() > 200 || stats.Writes() > 30 || snapshots > 1 || receipts > 6) {
					t.Fatalf("unbounded bootstrap: queries=%d writes=%d snapshots=%d receipts=%d", stats.Queries(), stats.Writes(), snapshots, receipts)
				}
			})
		}
	}
}
