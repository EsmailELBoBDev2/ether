package adapters_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plugins"
	"github.com/braedonsaunders/ether/internal/plugins/adapters"
)

func TestHostKindLibraryPlaysFilesInsideItsRoot(t *testing.T) {
	h := newKindTestHarness(t, 0, nil)
	ctx := context.Background()
	root := t.TempDir()
	inside := filepath.Join(root, "TV", "Show", "a.ts")
	if err := os.MkdirAll(filepath.Dir(inside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, []byte("recorded transport stream"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "b.ts")
	if err := os.WriteFile(outside, []byte("not a recording"), 0o644); err != nil {
		t.Fatal(err)
	}
	kind := plugins.MediaKindMeta{
		Key:     "recorded-tv",
		Label:   "Recorded TV",
		Media:   "video",
		Library: plugins.KindLibraryDecl{Name: "Recorded TV"},
		Levels: []plugins.KindLevel{
			{Key: "show", Label: "Show", Role: "show"},
			{Key: "season", Label: "Season"},
			{Key: "episode", Label: "Episode", Role: "episode", Playable: true},
		},
		Present: core.Presentations{MediaBrowser: &core.MediaBrowserPresentation{CollectionType: "tvshows", ItemTypes: map[string]string{"show": "Series", "season": "Season", "episode": "Episode"}}},
		Sync:    plugins.KindSyncDecl{Mode: plugins.KindSyncPush},
	}
	resolve := func(_ context.Context, item core.KindItem) ([]core.PlayableSource, error) {
		var hint struct{ Path string }
		_ = json.Unmarshal(item.Hints, &hint)
		info, err := os.Stat(hint.Path)
		if err != nil {
			return nil, core.ErrSourceUnavailable
		}
		return []core.PlayableSource{{ID: "file", Pattern: "file", Path: hint.Path, MIME: "video/mp2t", Size: info.Size()}}, nil
	}
	lib, err := h.catalog.RegisterHostKind(ctx, adapters.HostKind{PluginID: "ether.test-dvr", Kind: kind, Root: func() string { return root }, Resolve: resolve})
	if err != nil {
		t.Fatal(err)
	}
	if lib.Source != core.LibraryPlugin || !h.catalog.IsPluginLibraryActive(ctx, lib) {
		t.Fatalf("library = %+v, active=%v", lib, h.catalog.IsPluginLibraryActive(ctx, lib))
	}
	hint := func(p string) json.RawMessage {
		b, _ := json.Marshal(map[string]string{"path": p})
		return b
	}
	items := []core.KindItem{
		{ID: "show-1", Level: "show", Title: "Show"},
		{ID: "show-1-s1", Level: "season", ParentID: "show-1", Index: 1, Title: "Season 1"},
		{ID: "rec-in", Level: "episode", ParentID: "show-1-s1", Index: 1, Title: "Inside", Playable: true, Hints: hint(inside)},
		{ID: "rec-out", Level: "episode", ParentID: "show-1-s1", Index: 2, Title: "Outside", Playable: true, Hints: hint(outside)},
	}
	if _, err := h.items.UpsertItems(ctx, "ether.test-dvr", "recorded-tv", items); err != nil {
		t.Fatal(err)
	}
	in, _, err := h.items.GetByExternal(ctx, "ether.test-dvr", "recorded-tv", "rec-in")
	if err != nil {
		t.Fatal(err)
	}
	km, err := h.resolver.OpenKind(ctx, h.users[1], in.Key, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(io.NewSectionReader(km, 0, km.Size))
	km.Close()
	if err != nil || string(data) != "recorded transport stream" {
		t.Fatalf("read %q, %v", data, err)
	}
	out, _, err := h.items.GetByExternal(ctx, "ether.test-dvr", "recorded-tv", "rec-out")
	if err != nil {
		t.Fatal(err)
	}
	if km, err := h.resolver.OpenKind(ctx, h.users[1], out.Key, nil); err == nil {
		km.Close()
		t.Fatal("a file outside the host kind's root was opened")
	}
}
