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
	"github.com/braedonsaunders/ether/internal/experience"
	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/frontend/frontendtest"
	"github.com/braedonsaunders/ether/internal/frontend/kit/ids"
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

// Keep the replay scope fixture small while using real credential checks.
type bootstrapBudgetIdentity struct {
	frontendtest.Identity
	grants *identity.Grants
	token  string
}

func (f bootstrapBudgetIdentity) GrantScope(ctx context.Context, token string, device core.Device) (string, error) {
	_, err := f.grants.Resolve(ctx, "vela", token)
	return "budget-" + device.ID, err
}
func (f bootstrapBudgetIdentity) GrantScopeCurrent(ctx context.Context, scope string, device core.Device) (*core.User, error) {
	return f.grants.Resolve(ctx, "vela", f.token)
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
	for _, count := range []int{40, 240, 400, 1600} {
		for n := 1; n <= count; n++ {
			title := core.Title{Key: core.MovieKey(int64(n)), Type: core.TypeMovie, IDs: core.IDs{TMDB: int64(n)}, Title: fmt.Sprintf("Film %03d", n), Year: 2000, Released: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), Runtime: 90 * time.Minute, Genres: []string{"Drama", "Comedy"}, Rating: 8, VoteCount: 1000, Popularity: 50, PosterPath: fmt.Sprintf("/poster%d.jpg", n), BackdropPath: fmt.Sprintf("/backdrop%d.jpg", n)}
			if err := a.catalog.UpsertTitle(ctx, title, true); err != nil {
				t.Fatal(err)
			}
		}
		for _, serial := range []bool{true, false} {
			if count >= 400 && serial {
				continue
			}
			name := fmt.Sprintf("titles=%d/serial=%t", count, serial)
			t.Run(name, func(t *testing.T) {
				raw, err := a.newSurfaceExperience("vela", source)
				if err != nil {
					t.Fatal(err)
				}
				exp := raw.(*surfaceExperience)
				// Hold ranking fixed for the 20x20 admission gate: persistence, current
				// policy, catalogue hydration, receipts, artwork and HTTP remain real.
				if count >= 400 {
					libs := a.configuredLibraries()
					vis := experienceVisibility(a.safety, a.catalog.CachedGet, nil, nil, libs)
					exp.rowViews, err = experience.NewRuntimeRowViews(experience.RuntimeRowViews{DB: a.db, Protocol: "vela", Receipts: exp.receipts, Active: a.users.PolicyUser, CachedGet: a.catalog.CachedGet, CachedGetMany: a.catalog.CachedGetMany, Visibility: vis, Libraries: libs, Situation: situationFor, Snapshot: func(ctx context.Context, req frontend.RowRequest, _ core.HomeSituation) ([]frontend.Row, error) {
						keys := make([]core.RatingKey, count)
						for n := range keys {
							keys[n] = core.MovieKey(int64(n + 1))
						}
						titles, e := a.catalog.CachedGetMany(ctx, keys)
						if e != nil {
							return nil, e
						}
						rows := make([]frontend.Row, count/20)
						for n := range rows {
							row := frontend.Row{ID: fmt.Sprintf("fixture-%d", n), Kind: "trending", Title: "Trending", Purpose: frontend.RowRecommendation, Rank: n}
							for j := 0; j < 20; j++ {
								title := titles[keys[n*20+j]]
								row.Entries = append(row.Entries, frontend.RowEntry{Kind: frontend.RowEntryTitle, Title: &title})
							}
							rows[n] = row
						}
						return rows, nil
					}})
					if err != nil {
						t.Fatal(err)
					}
				}
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
				inst, err := (vela.Frontend{DB: a.db, ReplayKey: make([]byte, 32)}).Mount(ctx, frontend.Host{Experience: experience, Identity: bootstrapBudgetIdentity{grants: grants, token: credential, Identity: frontendtest.Identity{ResolveDeviceFunc: func(ctx context.Context, token string, _ core.Device) (*core.User, time.Time, error) {
					v, e := grants.Resolve(ctx, "vela", token)
					return v, time.Time{}, e
				}}}, ImageGrants: images, Sessions: frontendtest.Sessions{}})
				if err != nil {
					t.Fatal(err)
				}
				defer inst.Close(context.Background())
				// Keep each large fixture at the same cold activity state.
				if count >= 400 {
					if _, err := a.db.ExecContext(ctx, `DELETE FROM identity_viewer_times WHERE viewer_id=?`, viewer.ID); err != nil {
						t.Fatal(err)
					}
				}
				measured, stats := store.WithQueryStats(ctx)
				req := httptest.NewRequest("GET", "/vela/v0/bootstrap?limit=100", nil).WithContext(measured)
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
				// 45 includes the one library read that lists the viewer's plugin search kinds.
				if count >= 400 && (len(body.Rows) != count/20 || cards != count || stats.Queries() != 45 || stats.Writes() != 5) {
					for _, st := range stats.Statements() {
						t.Logf("%dx %s", st.Count, st.SQL)
					}
					t.Errorf("bootstrap budget: rows=%d cards=%d queries=%d writes=%d", len(body.Rows), cards, stats.Queries(), stats.Writes())
				}
				// Measure neighboring routes with the same real runtime and title set.
				if count >= 400 {
					for _, route := range []struct{ method, path, payload string }{
						{"GET", "/vela/v0/rows?limit=100", ""},
						{"GET", "/vela/v0/titles/" + ids.RatingKeyGUID(core.MovieKey(1)), ""},
						{"POST", "/vela/v0/search", `{"idempotencyKey":"search","query":"Film","mode":"fast","limit":20}`},
					} {
						routeCtx, routeStats := store.WithQueryStats(ctx)
						request := httptest.NewRequest(route.method, route.path, strings.NewReader(route.payload)).WithContext(routeCtx)
						request.Header.Set("Authorization", "Bearer "+credential)
						request.Header.Set("X-Vela-Device-Id", device.ID)
						if route.payload != "" {
							request.Header.Set("Content-Type", "application/json")
						}
						response := httptest.NewRecorder()
						inst.Handler().ServeHTTP(response, request)
						t.Logf("titles=%d route=%s status=%d queries=%d writes=%d", count, route.path, response.Code, routeStats.Queries(), routeStats.Writes())
						if routeStats.Queries() > 40 || (route.method == "GET" && routeStats.Writes() != 0) {
							t.Errorf("route budget: %s queries=%d writes=%d", route.path, routeStats.Queries(), routeStats.Writes())
						}
						if response.Code != 200 {
							t.Errorf("%s: %s", route.path, response.Body.String())
						}
					}
				}
				if !serial && (stats.Queries() > 200 || stats.Writes() > 30 || snapshots > 1 || receipts > 6) {
					t.Fatalf("unbounded bootstrap: queries=%d writes=%d snapshots=%d receipts=%d", stats.Queries(), stats.Writes(), snapshots, receipts)
				}
			})
		}
	}
}
