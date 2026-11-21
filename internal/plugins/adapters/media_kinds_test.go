package adapters_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/agent/tools"
	"github.com/braedonsaunders/ether/internal/blobcache"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/extend"
	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/libraries"
	"github.com/braedonsaunders/ether/internal/pluginitems"
	"github.com/braedonsaunders/ether/internal/plugins"
	"github.com/braedonsaunders/ether/internal/plugins/adapters"
	"github.com/braedonsaunders/ether/internal/store"
)

func TestKindCatalogParentsSyntheticSeriesAndPartialContainerHydration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var hydrateCalls atomic.Int32
	rt := &listChannelFakeRuntime{routes: map[string]func(plugins.Request) (plugins.Response, error){
		"/ether/v1/kinds/music/browse": func(req plugins.Request) (plugins.Response, error) {
			var body struct {
				ParentID string `json:"parentId"`
			}
			_ = json.Unmarshal(req.Body, &body)
			if body.ParentID == "al1" {
				hydrateCalls.Add(1)
				b, _ := json.Marshal(core.KindPage{
					Items: []core.KindItem{
						{ID: "tr1", Level: "track", ParentID: "al1", Title: "So What", Playable: true, Index: 1},
						{ID: "tr2", Level: "track", ParentID: "al1", Title: "Freddie Freeloader", Playable: true, Index: 2},
						{ID: "tr3", Level: "track", ParentID: "al1", Title: "Blue in Green", Playable: true, Index: 3},
					},
				})
				return plugins.Response{Status: 200, Body: b}, nil
			}
			return plugins.Response{Status: 404}, nil
		},
	}}

	dataDir := t.TempDir()
	cfg, err := config.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	db := store.OpenTest(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	libs, err := libraries.New(ctx, db, libraries.Options{Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := plugins.New(plugins.Deps{
		DB:       db,
		Sealer:   cfg,
		Registry: extend.New(),
		Tools:    tools.NewCatalogue(),
		Logger:   log,
		IsLeader: func() bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	mgr.RegisterRuntime("hosted", func(_ plugins.Installed) (plugins.Runtime, error) { return rt, nil })

	items, err := pluginitems.New(ctx, db, pluginitems.Options{
		Caller:   mgr,
		Health:   mgr,
		Logger:   log,
		IsLeader: func() bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(items.Close)

	blobDir := filepath.Join(dataDir, "blobcache")
	blobs, err := blobcache.Open(blobDir, blobcache.Options{Logger: log, SweepPeriod: -1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blobs.Close() })

	kc := adapters.AttachKindCatalog(ctx, adapters.KindCatalogDeps{
		Manager:   mgr,
		Libraries: libs,
		Items:     items,
		Blobs:     blobs,
		BlobDir:   blobDir,
		Logger:    log,
	})

	musicPlugin := "community.music-hydrate"
	musicMan := map[string]any{
		"id":      musicPlugin,
		"name":    "Music",
		"version": "1.0.0",
		"ether": map[string]any{
			"protocol":     1,
			"kind":         "content",
			"summary":      "Music test plugin",
			"runtime":      map[string]any{"kind": "hosted", "entry": "dist/plugin.js"},
			"capabilities": []string{"catalog_provider", "kind_resolver"},
			"contributes": map[string]any{
				"mediaKinds": []any{
					map[string]any{
						"key":   "music",
						"label": "Music",
						"media": "music",
						"library": map[string]any{
							"name": "Music",
							"card": "square",
						},
						"levels": []any{
							map[string]any{"key": "artist", "label": "Artist", "role": "artist", "container": true},
							map[string]any{"key": "album", "label": "Album", "role": "album", "container": true},
							map[string]any{"key": "track", "label": "Track", "role": "track", "playable": true},
						},
						"fields": []any{
							map[string]any{"key": "genre", "type": "strings", "label": "Genre", "indexed": true},
						},
						"browse": map[string]any{
							"sorts":  []string{"title"},
							"facets": []string{"genre"},
							"expand": map[string]any{"facets": []string{"genre"}, "ttlHours": 24},
						},
						"sync": map[string]any{"mode": "push"},
					},
				},
			},
		},
	}

	bookPlugin := "community.books-series"
	bookMan := map[string]any{
		"id":      bookPlugin,
		"name":    "Books",
		"version": "1.0.0",
		"ether": map[string]any{
			"protocol":     1,
			"kind":         "content",
			"summary":      "Books test plugin",
			"runtime":      map[string]any{"kind": "hosted", "entry": "dist/plugin.js"},
			"capabilities": []string{"catalog_provider", "kind_resolver"},
			"contributes": map[string]any{
				"mediaKinds": []any{
					map[string]any{
						"key":   "book",
						"label": "Books",
						"media": "book",
						"library": map[string]any{
							"name": "Books",
							"tabs": []any{
								map[string]any{"key": "library", "label": "Books", "kind": "library"},
								map[string]any{"key": "series", "label": "Series", "kind": "collections", "facet": "series"},
							},
						},
						"levels": []any{
							map[string]any{"key": "book", "label": "Book", "role": "book", "playable": true},
						},
						"fields": []any{
							map[string]any{"key": "author", "type": "people", "label": "Author", "indexed": true},
							map[string]any{"key": "series", "type": "string", "label": "Series", "indexed": true},
							map[string]any{"key": "series_number", "type": "string", "label": "Number"},
							map[string]any{"key": "format", "type": "string", "label": "Format"},
							map[string]any{"key": "pages", "type": "int", "label": "Pages"},
							map[string]any{"key": "publisher", "type": "string", "label": "Publisher"},
						},
						"browse": map[string]any{"sorts": []string{"title", "index"}, "facets": []string{"series", "author"}},
						"sync":   map[string]any{"mode": "push"},
					},
				},
			},
		},
	}

	installStudioManifest(t, mgr, musicMan, nil)
	installStudioManifest(t, mgr, bookMan, nil)
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	var musicLib, bookLib core.Library
	for _, lib := range mustListLibs(t, libs) {
		switch lib.PluginID {
		case musicPlugin:
			musicLib = lib
		case bookPlugin:
			bookLib = lib
		}
	}
	if musicLib.ID == 0 || bookLib.ID == 0 {
		t.Fatalf("missing libraries: music=%+v book=%+v", musicLib, bookLib)
	}

	// Seed an album declaring ChildCount=3 with only 1 track pre-stored (partial container).
	if _, err := items.KindUpsert(ctx, musicPlugin, "music", []core.KindItem{
		{ID: "ar1", Level: "artist", Title: "Miles Davis", ChildCount: 1},
		{ID: "al1", Level: "album", ParentID: "ar1", Title: "Kind of Blue", ChildCount: 3},
		{ID: "tr1", Level: "track", ParentID: "al1", Title: "So What", Playable: true, Index: 1},
	}); err != nil {
		t.Fatalf("KindUpsert music: %v", err)
	}

	alRec, _, err := items.GetByExternal(ctx, musicPlugin, "music", "al1")
	if err != nil {
		t.Fatalf("GetByExternal al1: %v", err)
	}

	// Children MUST hydrate the remaining 2 tracks because stored count (1) < ChildCount (3).
	chPage, err := kc.Children(ctx, alRec.Key, false, "", 50)
	if err != nil {
		t.Fatalf("Children: %v", err)
	}
	// Wait briefly if hydration ran asynchronously, or verify immediate result.
	deadline := time.Now().Add(2 * time.Second)
	for len(chPage.Items) < 3 && time.Now().Before(deadline) {
		time.Sleep(15 * time.Millisecond)
		chPage, err = kc.Children(ctx, alRec.Key, false, "", 50)
		if err != nil {
			t.Fatalf("Children poll: %v", err)
		}
	}
	if hydrateCalls.Load() != 1 {
		t.Fatalf("hydrateCalls = %d, want 1", hydrateCalls.Load())
	}
	if len(chPage.Items) != 3 {
		t.Fatalf("hydrated children len = %d, want 3", len(chPage.Items))
	}
	// Verify nearest-first MediaParents on hydrated tracks: [album, artist].
	tr := chPage.Items[0]
	if tr.MediaClass != core.MediaClassMusic || tr.MediaRole != core.MediaRoleTrack {
		t.Fatalf("track media class/role = %q/%q", tr.MediaClass, tr.MediaRole)
	}
	if len(tr.MediaParents) != 2 || tr.MediaParents[0].Role != core.MediaRoleAlbum || tr.MediaParents[0].Title != "Kind of Blue" ||
		tr.MediaParents[1].Role != core.MediaRoleArtist || tr.MediaParents[1].Title != "Miles Davis" {
		t.Fatalf("track MediaParents = %+v, want [album, artist]", tr.MediaParents)
	}

	// Subsequent Children call while fresh must NOT re-hydrate.
	if _, err := kc.Children(ctx, alRec.Key, false, "", 50); err != nil {
		t.Fatalf("Children second call: %v", err)
	}
	if hydrateCalls.Load() != 1 {
		t.Fatalf("hydrateCalls after fresh read = %d, want 1", hydrateCalls.Load())
	}

	// 1-level book with series facet + fractional series_number synthesizes facet-backed series parent.
	if _, err := items.KindUpsert(ctx, bookPlugin, "book", []core.KindItem{
		{
			ID:       "b1",
			Level:    "book",
			Title:    "The Churn",
			Summary:  "A novella set in Baltimore.",
			Playable: true,
			Fields: map[string]json.RawMessage{
				"author":        json.RawMessage(`["James S. A. Corey"]`),
				"series":        json.RawMessage(`"The Expanse"`),
				"series_number": json.RawMessage(`"3.5"`),
				"format":        json.RawMessage(`"epub"`),
				"pages":         json.RawMessage(`96`),
				"publisher":     json.RawMessage(`"Orbit"`),
			},
		},
	}); err != nil {
		t.Fatalf("KindUpsert book: %v", err)
	}
	bPage, err := kc.Browse(ctx, bookLib, frontend.ItemQuery{Sort: core.SortTitle}, "", 20)
	if err != nil || len(bPage.Items) != 1 {
		t.Fatalf("Browse books = %+v, err=%v", bPage, err)
	}
	bk := bPage.Items[0]
	if bk.MediaClass != core.MediaClassBook || bk.MediaRole != core.MediaRoleBook {
		t.Fatalf("book media class/role = %q/%q", bk.MediaClass, bk.MediaRole)
	}
	if bk.ItemIndex != 3 || bk.IndexDisplay != "3.5" {
		t.Fatalf("book index/indexDisplay = %d/%q, want 3/3.5", bk.ItemIndex, bk.IndexDisplay)
	}
	wantSeriesFacetID := "facet:" + strconv.FormatInt(int64(bookLib.ID), 10) + ":series:The Expanse"
	if len(bk.MediaParents) != 1 || bk.MediaParents[0].FacetID != wantSeriesFacetID || bk.MediaParents[0].Role != core.MediaRoleSeries || bk.MediaParents[0].Index != 3 || bk.MediaParents[0].IndexDisplay != "3.5" {
		t.Fatalf("book MediaParents = %+v, want FacetID %q", bk.MediaParents, wantSeriesFacetID)
	}
	if bk.MediaReading == nil || bk.MediaReading.Format != "epub" || bk.MediaReading.Pages != 96 {
		t.Fatalf("book MediaReading = %+v", bk.MediaReading)
	}
	// Publisher is kept in facts; series/series_number/format/pages/author are NOT duplicated.
	if len(bk.MediaFacts) != 1 || bk.MediaFacts[0].Label != "Publisher" || bk.MediaFacts[0].Value != "Orbit" {
		t.Fatalf("book MediaFacts = %+v, want [Publisher: Orbit]", bk.MediaFacts)
	}
}
