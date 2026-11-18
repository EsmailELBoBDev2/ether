package pluginitems

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plugins"
	"github.com/braedonsaunders/ether/internal/store"
)

func TestFacetBucketsCollectionsPeopleAndAddedAtProjection(t *testing.T) {
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
				{Key: "developers", Label: "Developers", Kind: "collections", Facet: "developers", Order: "name"},
				{Key: "cast", Label: "Voice cast", Kind: "people", Facet: "voiceCast", Order: "name"},
			},
		},
		Levels: []plugins.KindLevel{
			{Key: "game", Label: "Game", Playable: true},
		},
		Fields: []plugins.KindField{
			{Key: "platforms", Type: "strings", Label: "Platforms", Indexed: true},
			{Key: "developers", Type: "people", Label: "Developers", Indexed: true},
			{Key: "voiceCast", Type: "people", Label: "Voice cast", Indexed: true},
			{Key: "genres", Type: "strings", Label: "Genres", Indexed: true},
			{Key: "playtime", Type: "duration", Label: "Playtime", Indexed: true},
			{Key: "players", Type: "int", Label: "Players", Indexed: true},
		},
		Browse: plugins.KindBrowseDecl{
			Sorts:  []string{"released", "added", "title", "year", "rating", "index"},
			Facets: []string{"platforms", "developers", "voiceCast", "genres", "released", "playtime", "players"},
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
				"voiceCast": rawJSON(t, []map[string]any{
					{"name": "Laura Bailey", "role": "Heroine", "thumb": "https://cdn.example/cast/laura.jpg"},
					{"name": "ashley Johnson", "role": "Scout"},
				}),
				"genres":   rawJSON(t, []string{"RPG", "Adventure"}),
				"playtime": rawJSON(t, 480),
				"players":  rawJSON(t, 1),
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
				"voiceCast": rawJSON(t, []map[string]any{
					{"name": "Laura Bailey", "role": "Captain"},
					{"name": "Troy Baker", "role": "Pilot", "thumb": "https://cdn.example/cast/troy.jpg"},
				}),
				"genres":   rawJSON(t, []string{"arcade", "Racing"}),
				"playtime": rawJSON(t, 1500),
				"players":  rawJSON(t, 4),
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
				"voiceCast": rawJSON(t, []map[string]any{
					{"name": "Laura Bailey", "role": "Narrator"},
					{"name": "ashley Johnson", "role": "Engineer", "thumb": "https://cdn.example/cast/ashley.jpg"},
				}),
				"genres":   rawJSON(t, []string{"Puzzle"}),
				"playtime": rawJSON(t, 5400),
				"players":  rawJSON(t, 2),
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

	// 3. FacetCollections: default Order="count" vs Order="name" (case-insensitive alphabetical), paged O(page), NewestItem hydrated with art.
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

	// Case-insensitive alphabetical order on genres ("Adventure", "arcade", "Puzzle", "Racing", "RPG").
	fcGenresByName, err := s.FacetCollections(ctx, FacetCollectionsQuery{
		LibraryID: 77,
		Level:     "game",
		FieldKey:  "genres",
		Order:     "name",
		Offset:    0,
		Limit:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantGenreOrder := []string{"Adventure", "arcade", "Puzzle", "Racing", "RPG"}
	if len(fcGenresByName.Items) != len(wantGenreOrder) {
		t.Fatalf("genres by name = %+v", fcGenresByName.Items)
	}
	for i, want := range wantGenreOrder {
		if fcGenresByName.Items[i].Label != want || fcGenresByName.Items[i].NewestItem == nil {
			t.Fatalf("genres[%d] = %+v, want label %q with NewestItem", i, fcGenresByName.Items[i], want)
		}
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

	// 4. FacetPeople on voiceCast: alphabetical case-insensitive default + Order="count" + batched headshot sample selection.
	fpCtx, fpStats := store.WithQueryStats(ctx)
	fpByName, err := s.FacetPeople(fpCtx, FacetPeopleQuery{
		LibraryID: 77,
		Level:     "game",
		FieldKey:  "voiceCast",
		Order:     "name",
		Offset:    0,
		Limit:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fpStats.Queries() > 5 {
		t.Fatalf("FacetPeople issued %d SQL queries, want <= 5 (no per-person loop)", fpStats.Queries())
	}
	if fpByName.More || len(fpByName.Items) != 3 {
		t.Fatalf("FacetPeople by name = %+v", fpByName)
	}
	// Case-insensitive alphabetical: "ashley Johnson" (2), "Laura Bailey" (3), "Troy Baker" (1).
	if fpByName.Items[0].Name != "ashley Johnson" || fpByName.Items[0].Count != 2 || fpByName.Items[0].Thumb != "https://cdn.example/cast/ashley.jpg" || fpByName.Items[0].SampleItem == nil || fpByName.Items[0].SampleItem.ID != "g3" {
		t.Fatalf("FacetPeople[0] = %+v, want ashley Johnson count=2 thumb from g3", fpByName.Items[0])
	}
	// Laura Bailey appears on g2 (newer, no thumb) and g1 (older, has thumb); r.rn <= 4 picks g1 so her headshot is preserved!
	if fpByName.Items[1].Name != "Laura Bailey" || fpByName.Items[1].Count != 3 || fpByName.Items[1].Thumb != "https://cdn.example/cast/laura.jpg" || fpByName.Items[1].SampleItem == nil || fpByName.Items[1].SampleItem.ID != "g1" {
		t.Fatalf("FacetPeople[1] = %+v, want Laura Bailey count=3 thumb from g1", fpByName.Items[1])
	}
	if fpByName.Items[2].Name != "Troy Baker" || fpByName.Items[2].Count != 1 || fpByName.Items[2].Thumb != "https://cdn.example/cast/troy.jpg" || fpByName.Items[2].SampleItem == nil || fpByName.Items[2].SampleItem.ID != "g2" {
		t.Fatalf("FacetPeople[2] = %+v, want Troy Baker count=1 thumb from g2", fpByName.Items[2])
	}
	for _, fp := range fpByName.Items {
		if !core.IsPluginPersonID(fp.PersonID) {
			t.Fatalf("FacetPeople PersonID %d must satisfy IsPluginPersonID", fp.PersonID)
		}
		pm, ok, err := s.PersonByID(ctx, 77, nil, fp.PersonID, 10)
		if err != nil || !ok || pm.CreditCount != fp.Count {
			t.Fatalf("PersonByID(%d) = (%+v, %v, %v), want ok with count %d", fp.PersonID, pm, ok, err, fp.Count)
		}
	}

	fpByCount, err := s.FacetPeople(ctx, FacetPeopleQuery{
		LibraryID: 77,
		Level:     "game",
		FieldKey:  "voiceCast",
		Order:     "count",
		Offset:    0,
		Limit:     2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !fpByCount.More || len(fpByCount.Items) != 2 || fpByCount.Items[0].Name != "Laura Bailey" || fpByCount.Items[1].Name != "ashley Johnson" {
		t.Fatalf("FacetPeople by count = %+v", fpByCount)
	}

	// 5. Int facet > 50 distinct values produces discrete range buckets and filters cleanly.
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

func TestFacetCollectionsScaleQueryCount(t *testing.T) {
	ctx := context.Background()
	s, db := openTestService(t, Options{})

	decl := plugins.MediaKindMeta{
		Key:   "game",
		Label: "Games",
		Library: plugins.KindLibraryDecl{
			Name: "Games",
			Card: "landscape",
			Tabs: []plugins.KindLibraryTab{
				{Key: "developers", Label: "Developers", Kind: "collections", Facet: "developers", Order: "name"},
			},
		},
		Levels: []plugins.KindLevel{{Key: "game", Label: "Game", Playable: true}},
		Fields: []plugins.KindField{
			{Key: "developers", Type: "strings", Label: "Developers", Indexed: true},
		},
		Browse: plugins.KindBrowseDecl{
			Sorts:  []string{"released", "title", "added"},
			Facets: []string{"developers"},
			Search: true,
		},
		Sync: plugins.KindSyncDecl{Mode: plugins.KindSyncPush},
	}
	if _, err := s.UpsertKind(ctx, "community.scale", 88, decl, false); err != nil {
		t.Fatal(err)
	}

	const totalItems = 20000
	const totalBuckets = 300
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	itemStmt, err := tx.PrepareContext(ctx, `INSERT INTO plugin_items (
		item_ref, seq, plugin_id, kind_key, library_id, external_id,
		parent_ref, level, idx, title, sort_title, summary,
		year, released, rating, playable, child_count, leaf_count,
		unavailable, hints, fields, etag, added_at, updated_at, seen_at, missed_walks
	) VALUES (?, ?, 'community.scale', 'game', 88, ?, '', 'game', ?, ?, ?, '', 2025, ?, 8.0, 1, 0, 0, '', '{}', '{}', 'e1', ?, ?, ?, 0)`)
	if err != nil {
		t.Fatal(err)
	}
	defer itemStmt.Close()
	fieldStmt, err := tx.PrepareContext(ctx, `INSERT INTO plugin_item_fields (item_ref, field, v_text, v_int, v_real) VALUES (?, 'developers', ?, 0, 0)`)
	if err != nil {
		t.Fatal(err)
	}
	defer fieldStmt.Close()
	artStmt, err := tx.PrepareContext(ctx, `INSERT INTO plugin_item_art (item_ref, role, url, width, height) VALUES (?, 'thumb', ?, 1280, 720)`)
	if err != nil {
		t.Fatal(err)
	}
	defer artStmt.Close()

	baseReleased := int64(1700000000)
	for i := 1; i <= totalItems; i++ {
		ref := fmt.Sprintf("plugin:community.scale:game:g%d", i)
		extID := fmt.Sprintf("g%d", i)
		title := fmt.Sprintf("Game %05d", i)
		dev := fmt.Sprintf("Studio %03d", (i-1)%totalBuckets)
		rel := baseReleased + int64(i)
		if _, err := itemStmt.ExecContext(ctx, ref, int64(100000+i), extID, i, title, strings.ToLower(title), rel, rel, rel, rel); err != nil {
			t.Fatal(err)
		}
		if _, err := fieldStmt.ExecContext(ctx, ref, dev); err != nil {
			t.Fatal(err)
		}
		if _, err := artStmt.ExecContext(ctx, ref, fmt.Sprintf("https://cdn.example/thumb/%d.jpg", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Capture EXPLAIN QUERY PLAN on the newest-item ranked CTE for 100 buckets.
	holders := make([]string, 100)
	args := make([]any, 0, 103)
	args = append(args, "developers")
	for i := 0; i < 100; i++ {
		holders[i] = "?"
		args = append(args, fmt.Sprintf("Studio %03d", i))
	}
	args = append(args, int64(88), "game")
	planSQL := `EXPLAIN QUERY PLAN
		WITH ranked AS (
			SELECT f.v_text AS bucket_val, i.item_ref,
				ROW_NUMBER() OVER (
					PARTITION BY f.v_text
					ORDER BY i.released DESC, i.added_at DESC, i.sort_title ASC, i.item_ref ASC
				) AS rn
			FROM plugin_item_fields f
			JOIN plugin_items i ON i.item_ref = f.item_ref
			WHERE f.field = ? AND f.v_text IN (` + strings.Join(holders, ",") + `)
				AND i.library_id + 0 = ? AND i.level = ?
		)
		SELECT r.bucket_val, i.item_ref FROM ranked r
		JOIN plugin_items i ON i.item_ref = r.item_ref
		WHERE r.rn = 1`
	pRows, err := db.QueryContext(ctx, planSQL, args...)
	if err != nil {
		t.Fatal(err)
	}
	var planParts []string
	for pRows.Next() {
		var id, parent, notused int
		var detail string
		if err := pRows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		planParts = append(planParts, detail)
	}
	_ = pRows.Close()
	t.Logf("EXPLAIN QUERY PLAN: %s", strings.Join(planParts, " | "))

	for _, order := range []string{"count", "name"} {
		mCtx, stats := store.WithQueryStats(ctx)
		start := time.Now()
		page, err := s.FacetCollections(mCtx, FacetCollectionsQuery{
			LibraryID: 88,
			Level:     "game",
			FieldKey:  "developers",
			Order:     order,
			Offset:    0,
			Limit:     100,
		})
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("FacetCollections(%s): %v", order, err)
		}
		if !page.More || len(page.Items) != 100 {
			t.Fatalf("FacetCollections(%s) len=%d more=%v, want 100 and more=true", order, len(page.Items), page.More)
		}
		for idx, item := range page.Items {
			if item.NewestItem == nil || len(item.NewestItem.Art) == 0 {
				t.Fatalf("FacetCollections(%s)[%d] missing hydrated NewestItem/Art: %+v", order, idx, item)
			}
		}
		t.Logf("FacetCollections(order=%s, 20k items, 300 buckets, limit=100): queries=%d rows=%d elapsed=%s", order, stats.Queries(), stats.Rows(), elapsed)
		if stats.Queries() > 6 {
			t.Fatalf("FacetCollections(%s) executed %d SQL queries, want <= 6 (O(1) queries per page)", order, stats.Queries())
		}
		if elapsed > 300*time.Millisecond {
			t.Fatalf("FacetCollections(%s) took %s on 20k items, want < 300ms", order, elapsed)
		}
	}
}
