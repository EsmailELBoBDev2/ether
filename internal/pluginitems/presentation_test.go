package pluginitems

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plugins"
)

func TestFacetBucketsCollectionsAndAddedAtProjection(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	s, _ := openTestService(t, Options{Now: func() time.Time { return now }})

	decl := plugins.MediaKindMeta{
		Key:   "game",
		Label: "Video Games",
		Library: plugins.KindLibraryDecl{
			Name: "Video Games",
			Card: "landscape",
			Tabs: []plugins.KindLibraryTab{
				{Key: "recommended", Label: "Recommended", Kind: "recommended"},
				{Key: "library", Label: "Library", Kind: "library"},
				{Key: "developers", Label: "Developers", Kind: "collections", Facet: "developers"},
			},
		},
		Levels: []plugins.KindLevel{
			{Key: "game", Label: "Game", Playable: true},
		},
		Fields: []plugins.KindField{
			{Key: "platforms", Type: "strings", Label: "Platforms", Indexed: true},
			{Key: "developers", Type: "people", Label: "Developers", Indexed: true},
			{Key: "genres", Type: "strings", Label: "Genres", Indexed: true},
			{Key: "playtime", Type: "duration", Label: "Playtime", Indexed: true},
			{Key: "players", Type: "int", Label: "Players", Indexed: true},
		},
		Browse: plugins.KindBrowseDecl{
			Sorts:  []string{"released", "added", "title", "year", "rating", "index"},
			Facets: []string{"platforms", "developers", "genres", "released", "playtime", "players"},
			Search: true,
		},
		Sync: plugins.KindSyncDecl{Mode: plugins.KindSyncPush},
	}

	if _, err := s.UpsertKind(ctx, "community.games", 77, decl, false); err != nil {
		t.Fatal(err)
	}
	kb := s.KindBackends()

	rel2024 := time.Date(2024, 5, 10, 0, 0, 0, 0, time.UTC).Unix()
	rel2025Early := time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC).Unix()
	rel2025Late := time.Date(2025, 9, 15, 0, 0, 0, 0, time.UTC).Unix()

	items := []core.KindItem{
		{
			ID:       "g1",
			Level:    "game",
			Index:    1,
			Title:    "Chrono Quest",
			Year:     2024,
			Released: rel2024,
			Rating:   8.0,
			Playable: true,
			Art:      []core.KindArtRef{{Role: "thumb", URL: "https://cdn.example/g1-thumb.jpg"}},
			Fields: map[string]json.RawMessage{
				"platforms":  rawJSON(t, []string{"SNES"}),
				"developers": rawJSON(t, []string{"Square"}),
				"genres":     rawJSON(t, []string{"RPG", "Adventure"}),
				"playtime":   rawJSON(t, 480),
				"players":    rawJSON(t, 1),
			},
		},
		{
			ID:       "g2",
			Level:    "game",
			Index:    2,
			Title:    "Galaxy Racers",
			Year:     2025,
			Released: rel2025Late,
			Rating:   9.2,
			Playable: true,
			Art:      []core.KindArtRef{{Role: "thumb", URL: "https://cdn.example/g2-thumb.jpg"}},
			Fields: map[string]json.RawMessage{
				"platforms":  rawJSON(t, []string{"SNES", "GBA"}),
				"developers": rawJSON(t, []string{"Nintendo"}),
				"genres":     rawJSON(t, []string{"Racing"}),
				"playtime":   rawJSON(t, 1500),
				"players":    rawJSON(t, 4),
			},
		},
		{
			ID:       "g3",
			Level:    "game",
			Index:    3,
			Title:    "Star Puzzle",
			Year:     2025,
			Released: rel2025Early,
			Rating:   7.5,
			Playable: true,
			Art:      []core.KindArtRef{{Role: "backdrop", URL: "https://cdn.example/g3-bg.jpg"}},
			Fields: map[string]json.RawMessage{
				"platforms":  rawJSON(t, []string{"SNES"}),
				"developers": rawJSON(t, []string{"Nintendo"}),
				"genres":     rawJSON(t, []string{"Puzzle"}),
				"playtime":   rawJSON(t, 5400),
				"players":    rawJSON(t, 2),
			},
		},
	}
	if _, err := kb.KindUpsert(ctx, "community.games", "game", items); err != nil {
		t.Fatal(err)
	}

	// Verify AddedAt is scanned and mapped to PluginAddedAt without touching Title.AddedAt.
	storedG1, _, err := s.GetByExternal(ctx, "community.games", "game", "g1")
	if err != nil {
		t.Fatal(err)
	}
	if storedG1.AddedAt != now.Unix() {
		t.Fatalf("stored AddedAt = %d, want %d", storedG1.AddedAt, now.Unix())
	}
	proj := Project(storedG1, decl, core.Presentations{})
	if proj.PluginAddedAt != now.Unix() || !proj.AddedAt.IsZero() {
		t.Fatalf("Project PluginAddedAt=%d AddedAt=%v, want %d and zero", proj.PluginAddedAt, proj.AddedAt, now.Unix())
	}

	// 1. Discrete date facet ("released") -> UTC year buckets + Browse filter.
	relBuckets, err := s.FacetBuckets(ctx, 77, "game", nil, "released")
	if err != nil {
		t.Fatal(err)
	}
	if len(relBuckets) != 2 || relBuckets[0].ID != "2025" || relBuckets[0].Count != 2 || relBuckets[1].ID != "2024" || relBuckets[1].Count != 1 {
		t.Fatalf("released buckets = %+v", relBuckets)
	}
	relBrowse, err := s.Browse(ctx, BrowseQuery{
		LibraryID: 77,
		Level:     "game",
		Facets:    map[string][]string{"released": {"2025"}},
		Sort:      "released:desc",
		Limit:     10,
	})
	if err != nil || len(relBrowse.Items) != 2 || relBrowse.Items[0].ID != "g2" || relBrowse.Items[1].ID != "g3" {
		t.Fatalf("released=2025 browse = %+v, err=%v", relBrowse, err)
	}

	// 2. Discrete duration buckets + Browse filter.
	durBuckets, err := s.FacetBuckets(ctx, 77, "game", nil, "playtime")
	if err != nil {
		t.Fatal(err)
	}
	if len(durBuckets) != 3 || durBuckets[0].ID != "under-10m" || durBuckets[0].Label != "Under 10 min" || durBuckets[1].ID != "10-30m" || durBuckets[2].ID != "1-2h" {
		t.Fatalf("playtime buckets = %+v", durBuckets)
	}
	durBrowse, err := s.Browse(ctx, BrowseQuery{
		LibraryID: 77,
		Level:     "game",
		Facets:    map[string][]string{"playtime": {"10-30m", "1-2h"}},
		Sort:      "index:asc",
		Limit:     10,
	})
	if err != nil || len(durBrowse.Items) != 2 || durBrowse.Items[0].ID != "g2" || durBrowse.Items[1].ID != "g3" {
		t.Fatalf("playtime OR browse = %+v, err=%v", durBrowse, err)
	}

	// 3. FacetCollections on platforms and people ("developers"): ordered by Count DESC, Label ASC, paged O(page), NewestItem hydrated with art.
	fcPage1, err := s.FacetCollections(ctx, FacetCollectionsQuery{
		LibraryID: 77,
		Level:     "game",
		FieldKey:  "platforms",
		Offset:    0,
		Limit:     1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !fcPage1.More || len(fcPage1.Items) != 1 {
		t.Fatalf("FacetCollections page1 = %+v", fcPage1)
	}
	if fcPage1.Items[0].ID != "SNES" || fcPage1.Items[0].Count != 3 || fcPage1.Items[0].NewestItem == nil || fcPage1.Items[0].NewestItem.ID != "g2" {
		t.Fatalf("expected SNES count=3 newest=g2, got %+v", fcPage1.Items[0])
	}
	if len(fcPage1.Items[0].NewestItem.Art) != 1 || fcPage1.Items[0].NewestItem.Art[0].URL != "https://cdn.example/g2-thumb.jpg" {
		t.Fatalf("expected hydrated art on NewestItem, got %+v", fcPage1.Items[0].NewestItem.Art)
	}

	fcDevs, err := s.FacetCollections(ctx, FacetCollectionsQuery{
		LibraryID: 77,
		Level:     "game",
		FieldKey:  "developers",
		Offset:    0,
		Limit:     5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fcDevs.More || len(fcDevs.Items) != 2 || fcDevs.Items[0].Label != "Nintendo" || fcDevs.Items[0].Count != 2 || fcDevs.Items[0].NewestItem == nil || fcDevs.Items[0].NewestItem.ID != "g2" {
		t.Fatalf("FacetCollections developers = %+v", fcDevs)
	}

	// 4. Int facet > 50 distinct values produces discrete range buckets and filters cleanly.
	var many []core.KindItem
	for i := 1; i <= 55; i++ {
		many = append(many, core.KindItem{
			ID:       fmt.Sprintf("m%d", i),
			Level:    "game",
			Index:    100 + i,
			Title:    fmt.Sprintf("Arcade Title %d", i),
			Playable: true,
			Fields: map[string]json.RawMessage{
				"players": rawJSON(t, i*2),
			},
		})
	}
	if _, err := kb.KindUpsert(ctx, "community.games", "game", many); err != nil {
		t.Fatal(err)
	}
	intBuckets, err := s.FacetBuckets(ctx, 77, "game", nil, "players")
	if err != nil || len(intBuckets) == 0 || len(intBuckets) > 10 {
		t.Fatalf("expected ranged int buckets when >50 distinct values, got len=%d %+v err=%v", len(intBuckets), intBuckets, err)
	}
	rangeBrowse, err := s.Browse(ctx, BrowseQuery{
		LibraryID: 77,
		Level:     "game",
		Facets:    map[string][]string{"players": {intBuckets[0].ID}},
		Limit:     200,
	})
	if err != nil || len(rangeBrowse.Items) != intBuckets[0].Count {
		t.Fatalf("range browse count = %d, want bucket count %d (bucket=%+v, err=%v)", len(rangeBrowse.Items), intBuckets[0].Count, intBuckets[0], err)
	}
}
