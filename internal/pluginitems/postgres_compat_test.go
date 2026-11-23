package pluginitems

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plugins"
)

func TestPostgresCompatQueries(t *testing.T) {
	ctx := context.Background()
	s, db := openTestService(t, Options{})

	const libID = core.LibraryID(91)
	decl := plugins.MediaKindMeta{
		Key:   "movie",
		Label: "Movies",
		Library: plugins.KindLibraryDecl{
			Name: "Movies",
		},
		Levels: []plugins.KindLevel{{Key: "movie", Label: "Movie", Playable: true}},
		Fields: []plugins.KindField{
			{Key: "genre", Type: "string", Label: "Genre", Indexed: true},
			{Key: "runtime", Type: "int", Label: "Runtime", Indexed: true},
			{Key: "score", Type: "float", Label: "Score", Indexed: true},
			{Key: "cast", Type: "people", Label: "Cast", Indexed: true},
		},
		Browse: plugins.KindBrowseDecl{Sorts: []string{"title", "year", "rating"}},
		Sync:   plugins.KindSyncDecl{Mode: plugins.KindSyncPull},
	}

	if _, err := s.UpsertKind(ctx, "test.pgcompat", libID, decl, false); err != nil {
		t.Fatalf("UpsertKind fresh: %v", err)
	}

	rawJSON := func(v any) json.RawMessage {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	items := []core.KindItem{
		{
			ID:       "m1",
			Level:    "movie",
			Title:    "Alpha",
			Year:     2020,
			Rating:   7.5,
			Playable: true,
			Fields: map[string]json.RawMessage{
				"genre":   rawJSON("Action"),
				"runtime": rawJSON(100),
				"score":   rawJSON(7.5),
				"cast":    rawJSON([]core.Person{{Name: "Alice", TMDB: core.PluginPersonIDBase + 1}}),
			},
		},
		{
			ID:       "m2",
			Level:    "movie",
			Title:    "Beta",
			Year:     2021,
			Rating:   8.5,
			Playable: true,
			Fields: map[string]json.RawMessage{
				"genre":   rawJSON("Drama"),
				"runtime": rawJSON(120),
				"score":   rawJSON(8.5),
				"cast":    rawJSON([]core.Person{{Name: "Bob", TMDB: core.PluginPersonIDBase + 2}}),
			},
		},
	}
	if _, err := s.UpsertItems(ctx, "test.pgcompat", "movie", items); err != nil {
		t.Fatalf("UpsertItems: %v", err)
	}

	// Exercise browseTallyProbe when plugin_item_tally is empty but plugin_items has rows.
	if _, err := db.ExecContext(ctx, `DELETE FROM plugin_item_tally WHERE library_id = ?`, int64(libID)); err != nil {
		t.Fatal(err)
	}
	page, err := s.Browse(ctx, BrowseQuery{LibraryID: libID, Level: "movie", Limit: 10})
	if err != nil {
		t.Fatalf("Browse without tally: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("Browse len = %d, want 2", len(page.Items))
	}

	// Exercise UpsertKind with pre-existing plugin_items rows (EXISTS() -> bool).
	if _, err := db.ExecContext(ctx, `DELETE FROM plugin_kinds WHERE plugin_id = ? AND kind_key = ?`, "test.pgcompat", "movie"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertKind(ctx, "test.pgcompat", libID, decl, false); err != nil {
		t.Fatalf("UpsertKind with pre-existing items: %v", err)
	}

	// Exercise libraryHasAtMostItems subquery alias.
	small, err := s.libraryHasAtMostItems(ctx, `i.library_id = ?`, []any{int64(libID)}, 10)
	if err != nil || !small {
		t.Fatalf("libraryHasAtMostItems = (%v, %v), want (true, nil)", small, err)
	}

	// Exercise all 6 WITH RECURSIVE large-library helpers (smallLib=false).
	itemWhere := `i.library_id = ? AND i.level = ?`
	itemArgs := []any{int64(libID), "movie"}
	if yrs, err := s.scanEnvelopeYearCounts(ctx, libID, itemWhere, itemArgs, false); err != nil || len(yrs) != 2 {
		t.Fatalf("scanEnvelopeYearCounts(large) = (%d, %v), want 2", len(yrs), err)
	}
	if rats, err := s.scanEnvelopeRatingCounts(ctx, libID, itemWhere, itemArgs, false); err != nil || len(rats) != 2 {
		t.Fatalf("scanEnvelopeRatingCounts(large) = (%d, %v), want 2", len(rats), err)
	}
	if ints, err := s.scanFieldIntCounts(ctx, "runtime", itemWhere, itemWhere, itemArgs, false); err != nil || len(ints) != 2 {
		t.Fatalf("scanFieldIntCounts(large) = (%d, %v), want 2", len(ints), err)
	}
	if flts, err := s.scanFieldFloatCounts(ctx, "score", itemWhere, itemWhere, itemArgs, false); err != nil || len(flts) != 2 {
		t.Fatalf("scanFieldFloatCounts(large) = (%d, %v), want 2", len(flts), err)
	}
	if txts, err := s.scanFieldTextCountsLarge(ctx, "genre", itemWhere, itemArgs, 2001); err != nil || len(txts) != 2 {
		t.Fatalf("scanFieldTextCountsLarge = (%d, %v), want 2", len(txts), err)
	}
	if ppl, err := s.scanPeopleCandidatesLarge(ctx, "cast", itemWhere, itemArgs, 2001); err != nil || len(ppl) != 2 {
		t.Fatalf("scanPeopleCandidatesLarge = (%d, %v), want 2", len(ppl), err)
	}

	// Exercise resolveNewestByPredicateBuckets with >= 2 buckets (UNION ALL CAST).
	fc, err := s.FacetCollections(ctx, FacetCollectionsQuery{
		LibraryID: libID,
		Level:     "movie",
		FieldKey:  "year",
		Limit:     10,
	})
	if err != nil || len(fc.Items) != 2 {
		t.Fatalf("FacetCollections(year) = (%d items, %v), want 2", len(fc.Items), err)
	}
}
