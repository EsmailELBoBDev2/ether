package experience

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
)

type fakePresentationPluginLibraries struct {
	fakePluginLibraries
	pres     PluginPresentation
	items    []core.Title
	buckets  map[string][]frontend.FacetBucket
	colPages map[string]frontend.FacetCollectionPage
}

func (f *fakePresentationPluginLibraries) RootMediaType(context.Context, core.Library) core.MediaType {
	return core.TypeMovie
}

func (f *fakePresentationPluginLibraries) Presentation(context.Context, core.Library) (PluginPresentation, bool) {
	return f.pres, true
}

func (f *fakePresentationPluginLibraries) FacetBuckets(_ context.Context, _ core.Library, field string) ([]frontend.FacetBucket, error) {
	return append([]frontend.FacetBucket(nil), f.buckets[field]...), nil
}

func (f *fakePresentationPluginLibraries) FacetCollections(_ context.Context, _ core.Library, field string, offset, limit int) (frontend.FacetCollectionPage, error) {
	p := f.colPages[field]
	end := min(offset+limit, len(p.Items))
	if offset >= len(p.Items) {
		return frontend.FacetCollectionPage{Items: []frontend.FacetCollection{}}, nil
	}
	return frontend.FacetCollectionPage{
		Items: append([]frontend.FacetCollection(nil), p.Items[offset:end]...),
		More:  end < len(p.Items),
	}, nil
}

func (f *fakePresentationPluginLibraries) ItemsMany(_ context.Context, keys []core.RatingKey) (map[core.RatingKey]PluginItemView, error) {
	out := make(map[core.RatingKey]PluginItemView, len(keys))
	for _, k := range keys {
		for _, it := range f.items {
			if it.Key == k {
				out[k] = PluginItemView{Library: f.libs[0], Title: it, Playable: true}
			}
		}
	}
	return out, nil
}

func (f *fakePresentationPluginLibraries) Browse(_ context.Context, _ core.Library, q frontend.ItemQuery, _ string, limit int) (PluginBrowsePage, error) {
	var matched []core.Title
	for _, it := range f.items {
		if devs := q.Filter.Facets["developers"]; len(devs) > 0 {
			ok := false
			for _, g := range it.Genres {
				if slices.Contains(devs, g) {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
		}
		matched = append(matched, it)
	}
	s := &Service{}
	_ = s.sortTitles(context.Background(), matched, q)
	if limit > 0 && len(matched) > limit {
		matched = matched[:limit]
	}
	return PluginBrowsePage{Items: matched, Total: len(matched)}, nil
}

type fakeInProgressState struct {
	core.State
	inProgress []core.WatchStatus
}

func (f fakeInProgressState) InProgress(context.Context, int64, int) ([]core.WatchStatus, error) {
	return f.inProgress, nil
}

func TestPluginLibraryPresentationBucketsCollectionsRowsAndSorts(t *testing.T) {
	ctx := context.Background()
	t1 := core.Title{
		Key:           core.PluginKey(1),
		LibraryID:     9,
		Type:          core.TypeMovie,
		Title:         "Chrono Quest",
		ItemIndex:     2,
		Year:          2024,
		Released:      time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC),
		PluginAddedAt: 1000,
		Rating:        7.4,
		Genres:        []string{"nintendo"},
		ContentRating: "PG-13",
		LandscapeCard: true,
	}
	t2 := core.Title{
		Key:           core.PluginKey(2),
		LibraryID:     9,
		Type:          core.TypeMovie,
		Title:         "Galaxy Racers",
		ItemIndex:     1,
		Year:          2025,
		Released:      time.Date(2025, 8, 1, 0, 0, 0, 0, time.UTC),
		PluginAddedAt: 2000,
		Rating:        9.1,
		Genres:        []string{"nintendo", "capcom"},
		ContentRating: "R",
		LandscapeCard: true,
	}

	plug := &fakePresentationPluginLibraries{
		fakePluginLibraries: fakePluginLibraries{
			libs: []core.Library{{
				ID:        9,
				Key:       "plugin:community.games:game",
				Name:      "Video Games",
				Source:    core.LibraryPlugin,
				PluginID:  "community.games",
				KindKey:   "game",
				MediaType: core.TypeMovie,
			}},
		},
		pres: PluginPresentation{
			Card: "landscape",
			Tabs: []frontend.ViewTab{
				{ID: "recommended", Label: "Recommended", Kind: "recommended"},
				{ID: "library", Label: "Library", Kind: "library"},
				{ID: "developers", Label: "Developers", Kind: "collections", Facet: "developers", Sort: "released", Direction: "desc"},
			},
			Sorts: []string{"released", "added", "title", "year", "rating", "index"},
			Facets: []frontend.ViewFacet{
				{ID: "developers", Label: "Developers", Type: "strings", Multi: true},
				{ID: "platforms", Label: "Platforms", Type: "strings", Multi: true},
				{ID: "genres", Label: "Genres", Type: "strings", Multi: true},
			},
		},
		items: []core.Title{t1, t2},
		buckets: map[string][]frontend.FacetBucket{
			"developers": {
				{ID: "capcom", Label: "Capcom", Count: 1},
				{ID: "nintendo", Label: "Nintendo", Count: 2},
				{ID: "sega", Label: "Sega", Count: 1},
				{ID: "konami", Label: "Konami", Count: 1},
				{ID: "namco", Label: "Namco", Count: 1},
				{ID: "taito", Label: "Taito", Count: 1},
			},
		},
		colPages: map[string]frontend.FacetCollectionPage{
			"developers": {
				Items: []frontend.FacetCollection{
					{ID: "nintendo", Name: "Nintendo", ItemCount: 2, NewestItem: &t2},
					{ID: "capcom", Name: "Capcom", ItemCount: 1, NewestItem: &t2},
				},
			},
		},
	}

	svc, _ := peopleFixture()
	svc.d.PluginLibraries = plug
	svc.d.LibraryPresentation = func(_ core.Presentations, mt core.MediaType) (core.MediaType, string, bool) {
		return mt, "movies", true
	}
	svc.d.State = fakeInProgressState{
		State:      svc.d.State,
		inProgress: []core.WatchStatus{{Key: t1.Key, ViewOffset: 30 * time.Second}},
	}

	owner := &core.User{ID: 1, IsOwner: true}
	views, err := svc.Views(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	var pView frontend.View
	for _, v := range views {
		if v.ID == "9" {
			pView = v
		}
	}
	if pView.Card != "landscape" || len(pView.Tabs) != 3 || pView.Tabs[2].Facet != "developers" || len(pView.Sorts) != 6 || len(pView.Facets) != 3 {
		t.Fatalf("unexpected plugin View metadata: %+v", pView)
	}

	// FacetBuckets validates declared facets and library access.
	buckets, err := svc.LibraryFacetBuckets(ctx, owner, core.Device{}, "9", "developers")
	if err != nil || len(buckets) != 6 {
		t.Fatalf("LibraryFacetBuckets = %+v, err=%v", buckets, err)
	}
	if _, err := svc.LibraryFacetBuckets(ctx, owner, core.Device{}, "9", "undeclared"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("undeclared facet err = %v, want ErrNotFound", err)
	}

	// FacetCollections clears NewestItem if viewer content rating forbids it.
	pg13Viewer := &core.User{ID: 2, MaxContentRating: "PG-13", AllowedLibraries: []core.LibraryID{9}}
	svc.d.Active = func(_ context.Context, id int64) (*core.User, error) {
		if id == 2 {
			return pg13Viewer, nil
		}
		return owner, nil
	}
	cols, err := svc.LibraryFacetCollections(ctx, pg13Viewer, core.Device{}, "9", "developers", frontend.PageQuery{Limit: 10})
	if err != nil || len(cols.Items) != 2 || cols.Items[0].NewestItem != nil {
		t.Fatalf("expected R-rated NewestItem cleared for PG-13 viewer, got %+v err=%v", cols, err)
	}

	// PluginLibraryRows: Continue Watching, Recently Added, Newest, Top Rated, and "From <Developer>" rows.
	for _, dev := range []string{"sega", "konami", "namco", "taito"} {
		plug.items = append(plug.items, core.Title{
			Key:           core.PluginKey(int64(100 + len(plug.items))),
			LibraryID:     9,
			Type:          core.TypeMovie,
			Title:         "Arcade " + dev,
			Released:      time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC),
			PluginAddedAt: 500,
			Rating:        6.0,
			Genres:        []string{dev},
		})
	}
	rows, err := svc.PluginLibraryRows(ctx, owner, core.Device{}, "9")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 8 {
		t.Fatalf("expected 8 rows cap, got %d: %+v", len(rows), rows)
	}
	wantTitles := []string{"Continue Watching", "Recently Added", "Newest", "Top Rated", "From Nintendo"}
	for i, want := range wantTitles {
		if rows[i].Title != want {
			t.Fatalf("row[%d].Title = %q, want %q", i, rows[i].Title, want)
		}
	}
	if rows[1].Items[0].Key != t2.Key {
		t.Fatalf("Recently Added should order by PluginAddedAt desc (t2 first), got %v", rows[1].Items[0].Key)
	}

	// Verify sortTitles supports SortIndex and SortYear.
	arr := []core.Title{t1, t2}
	if err := svc.sortTitles(ctx, arr, frontend.ItemQuery{Sort: core.SortIndex, Desc: false}); err != nil {
		t.Fatal(err)
	}
	if arr[0].Key != t2.Key || arr[1].Key != t1.Key {
		t.Fatalf("SortIndex asc = [%v, %v], want [t2(ItemIndex=1), t1(ItemIndex=2)]", arr[0].Key, arr[1].Key)
	}
	if err := svc.sortTitles(ctx, arr, frontend.ItemQuery{Sort: core.SortYear, Desc: true}); err != nil {
		t.Fatal(err)
	}
	if arr[0].Key != t2.Key {
		t.Fatalf("SortYear desc first = %v, want t2(2025)", arr[0].Key)
	}
}
