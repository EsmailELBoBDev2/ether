package personalize

import (
	"context"
	"reflect"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestLibraryLayoutInheritanceAndIsolation(t *testing.T) {
	hc := hubContext(testNow, core.DeviceTV)
	dedupeOff := false

	doc := core.HomeLayout{
		Global: []core.LayoutPage{
			{
				Section:    "movies",
				MaxRows:    2,
				DedupeHome: &dedupeOff,
				Rows: []core.RowPolicy{
					{Row: "movies-anchor", Status: "always", Slot: 1},
					{Row: "hidden-in-movies", Status: "off"},
				},
			},
		},
		Viewers: []core.ViewerLayout{
			{
				UserID: 7,
				Pages: []core.LayoutPage{
					{
						Section: "movies",
						Rows:    []core.RowPolicy{{Row: "viewer-movies", Status: "always", Slot: 2}},
					},
				},
			},
		},
	}

	normalized, err := normalizeHomeLayout(doc)
	if err != nil {
		t.Fatalf("normalizeHomeLayout: %v", err)
	}

	// Uncustomised library:3 inherits both Global "movies" and Viewer "movies".
	inherited := arrangeLayoutFor(
		sampleRows("plain", "hidden-in-movies", "viewer-movies", "movies-anchor"),
		normalized, 7, hc, "library:3", "movies", 10,
	)
	if got := visibleIDs(inherited); !reflect.DeepEqual(got, []string{"movies-anchor", "viewer-movies"}) {
		t.Fatalf("inherited library:3 visibleIDs = %v, want [movies-anchor viewer-movies]", got)
	}
	if resolveDedupeHomeFor(normalized, 7, "library:3", "movies") {
		t.Fatal("expected library:3 to inherit DedupeHome=false from movies")
	}

	// Once library:3 defines its own Global page, it stops inheriting "movies"
	// while library:1 keeps inheriting "movies".
	dedupeOn := true
	doc.Global = append(doc.Global, core.LayoutPage{
		Section:    "library:3",
		MaxRows:    3,
		DedupeHome: &dedupeOn,
		Rows: []core.RowPolicy{
			{Row: "hidden-in-movies", Status: "always", Slot: 1},
			{Row: "movies-anchor", Status: "off"},
		},
	})
	normalized, err = normalizeHomeLayout(doc)
	if err != nil {
		t.Fatalf("normalizeHomeLayout with library:3: %v", err)
	}

	lib3 := arrangeLayoutFor(
		sampleRows("plain", "hidden-in-movies", "viewer-movies", "movies-anchor"),
		normalized, 7, hc, "library:3", "movies", 10,
	)
	if got := visibleIDs(lib3); !reflect.DeepEqual(got, []string{"hidden-in-movies", "plain", "viewer-movies"}) {
		t.Fatalf("customised library:3 visibleIDs = %v, want [hidden-in-movies plain viewer-movies]", got)
	}
	if !resolveDedupeHomeFor(normalized, 7, "library:3", "movies") {
		t.Fatal("expected customised library:3 DedupeHome=true")
	}

	lib1 := arrangeLayoutFor(
		sampleRows("plain", "hidden-in-movies", "viewer-movies", "movies-anchor"),
		normalized, 7, hc, "library:1", "movies", 10,
	)
	if got := visibleIDs(lib1); !reflect.DeepEqual(got, []string{"movies-anchor", "viewer-movies"}) {
		t.Fatalf("uncustomised library:1 visibleIDs = %v, want [movies-anchor viewer-movies]", got)
	}

	for _, bad := range []string{"library:", "library:0", "library:-2", "library:abc"} {
		if _, err := normalizeHomeLayout(core.HomeLayout{
			Global: []core.LayoutPage{{Section: bad}},
		}); err == nil {
			t.Fatalf("expected normalizeHomeLayout to reject section %q", bad)
		}
	}
}

func TestSectionHubsAndPreviewHonorPerLibraryLayout(t *testing.T) {
	h := newHarness(t, func(o *Options) {
		o.LibrarySections = func(context.Context) map[int]core.MediaType {
			return map[int]core.MediaType{
				1: core.TypeMovie,
				2: core.TypeShow,
				3: core.TypeMovie,
			}
		}
	})
	ctx := context.Background()
	sciFiFan(h, 1)
	u := core.User{ID: 1, IsOwner: true}
	h.rebuild(u)

	hc1 := hubContext(testNow, core.DeviceTV)
	hc1.SectionID = 1
	initial, err := h.e.SectionHubsFor(ctx, &u, core.TypeMovie, hc1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(initial) < 2 {
		t.Fatalf("need at least 2 movie hubs to test per-library layouts, got %d", len(initial))
	}
	firstID, secondID := initial[0].ID, initial[1].ID

	_, err = h.e.SetHomeLayout(ctx, core.HomeLayout{
		Global: []core.LayoutPage{
			{
				Section: "movies",
				Rows:    []core.RowPolicy{{Row: firstID, Status: "always", Slot: 1}},
			},
			{
				Section: "library:3",
				MaxRows: 1,
				Rows: []core.RowPolicy{
					{Row: firstID, Status: "off"},
					{Row: secondID, Status: "always", Slot: 1},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("SetHomeLayout: %v", err)
	}

	hubs1, err := h.e.SectionHubsFor(ctx, &u, core.TypeMovie, hc1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hubs1) == 0 || hubs1[0].ID != firstID {
		t.Fatalf("library 1 first hub = %+v, want %q", hubs1, firstID)
	}

	hc3 := hubContext(testNow, core.DeviceTV)
	hc3.SectionID = 3
	hubs3, err := h.e.SectionHubsFor(ctx, &u, core.TypeMovie, hc3, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hubs3) != 1 || hubs3[0].ID != secondID {
		t.Fatalf("library 3 hubs = %+v, want single hub %q", hubs3, secondID)
	}

	prev3, err := h.e.PreviewHome(ctx, &u, hubContext(testNow, core.DeviceTV), "library:3", 10)
	if err != nil {
		t.Fatalf("PreviewHome(library:3): %v", err)
	}
	vis3 := visibleIDs(prev3.Rows)
	if !reflect.DeepEqual(vis3, []string{secondID}) {
		t.Fatalf("PreviewHome(library:3) visible = %v, want [%s]", vis3, secondID)
	}

	_, err = h.e.SetHomeLayout(ctx, core.HomeLayout{
		Global: []core.LayoutPage{
			{
				Section: "movies",
				Rows:    []core.RowPolicy{{Row: firstID, Status: "always", Slot: 1}},
			},
			{
				Section: "library:1",
				MaxRows: 1,
				Rows:    []core.RowPolicy{{Row: secondID, Status: "always", Slot: 1}, {Row: firstID, Status: "off"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	prevMovies, err := h.e.PreviewHome(ctx, &u, hubContext(testNow, core.DeviceTV), "movies", 10)
	if err != nil {
		t.Fatal(err)
	}
	if visMovies := visibleIDs(prevMovies.Rows); len(visMovies) == 0 || visMovies[0] != firstID {
		t.Fatalf("PreviewHome(movies) visible = %v, want %q first", visMovies, firstID)
	}
	prevLib1, err := h.e.PreviewHome(ctx, &u, hubContext(testNow, core.DeviceTV), "library:1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if visLib1 := visibleIDs(prevLib1.Rows); !reflect.DeepEqual(visLib1, []string{secondID}) {
		t.Fatalf("PreviewHome(library:1) visible = %v, want [%s]", visLib1, secondID)
	}
}
