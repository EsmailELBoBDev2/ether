package adapters_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plugins"
	"github.com/braedonsaunders/ether/internal/plugins/adapters"
	"github.com/braedonsaunders/ether/internal/resolver"
)

func TestKindVersionsCachingOpenSourceAndRememberedChoice(t *testing.T) {
	h := newKindTestHarness(t, 0, nil)
	ctx := context.Background()

	var nowMu sync.Mutex
	curNow := time.Now().UTC().Truncate(time.Second)
	nowFn := func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return curNow
	}

	const (
		hash4K    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		hash1080p = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		nzb720p   = "https://media.example/releases/concert-720p.nzb"
	)
	bytes4K := []byte("4K-REMUX-PAYLOAD")
	bytes1080p := []byte("1080P-WEBDL-PAYLOAD")
	bytes720p := []byte("720P-USENET-PAYLOAD")

	idx := &kindFailoverIndexer{
		nzbPayloads: map[string][]byte{nzb720p: bytes720p},
	}
	deb := &kindFailoverDebrid{
		payloads: map[string][]byte{
			hash4K:    bytes4K,
			hash1080p: bytes1080p,
		},
	}
	resSvc := resolver.New(idx, nil, nil, config.Defaults(""), resolver.Options{})

	newResolver := func() *adapters.KindResolver {
		return adapters.NewKindResolver(adapters.KindResolverDeps{
			Manager:   h.mgr,
			Libraries: h.libs,
			Items:     h.items,
			Blobs:     h.blobs,
			Catalog:   h.catalog,
			Indexer:   idx,
			Resolver:  resSvc,
			Debrid:    deb,
			Engine:    kindFailoverEngine{},
			Now:       nowFn,
			Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
	}
	kr := newResolver()

	var resolveCalls atomic.Int32
	h.rt.callContext = func(_ context.Context, req plugins.Request) (plugins.Response, error) {
		switch req.Path {
		case "/ether/health":
			return plugins.Response{Status: http.StatusOK, Body: json.RawMessage(`{"status":"ok"}`)}, nil
		case "/ether/v1/kinds/recordings/resolve":
			resolveCalls.Add(1)
			resp := core.KindResolveResponse{
				TTLSec: 600,
				Sources: []core.PlayableSource{
					{
						ID:      "src-4k",
						Label:   "4K Remux",
						Pattern: "release",
						MIME:    "video/x-matroska",
						Release: &core.ReleaseHint{
							InfoHash: hash4K,
							Candidate: &core.SourceCandidate{
								InfoHash:   hash4K,
								Title:      "Concert.2025.2160p.UHD.BluRay.REMUX.HEVC.TrueHD.7.1-GRP",
								Size:       40 << 30,
								Cached:     true,
								Resolution: 2160,
								Codec:      "hevc",
								Source:     "remux",
							},
						},
					},
					{
						ID:      "src-1080p",
						Label:   "1080p WEB-DL",
						Pattern: "release",
						MIME:    "video/x-matroska",
						Release: &core.ReleaseHint{
							InfoHash: hash1080p,
							Candidate: &core.SourceCandidate{
								InfoHash:   hash1080p,
								Title:      "Concert.2025.1080p.WEB-DL.DDP5.1.H.264-GRP",
								Size:       8 << 30,
								Cached:     true,
								Resolution: 1080,
								Codec:      "h264",
								Source:     "web-dl",
							},
						},
					},
					{
						ID:      "src-720p",
						Label:   "720p Usenet",
						Pattern: "release",
						MIME:    "video/x-matroska",
						Release: &core.ReleaseHint{
							NZBURL: nzb720p,
							Candidate: &core.SourceCandidate{
								NZBURL:     nzb720p,
								Title:      "Concert.2025.720p.WEB-DL.H.264-USENET",
								Size:       3 << 30,
								Resolution: 720,
								Codec:      "h264",
								Source:     "web-dl",
							},
						},
					},
				},
			}
			raw, _ := json.Marshal(resp)
			return plugins.Response{Status: http.StatusOK, Body: raw}, nil
		default:
			return plugins.Response{Status: http.StatusNotFound}, nil
		}
	}

	const pluginID = "community.example.kindversions"
	decl := recordingsKindDecl(true)
	decl["sync"] = map[string]any{"mode": "push"}
	installStudioManifest(t, h.mgr, map[string]any{
		"id":      pluginID,
		"version": "1.0.0",
		"name":    "Kind Versions Test",
		"ether": map[string]any{
			"protocol":     1,
			"kind":         "content",
			"summary":      "Kind versions test",
			"runtime":      map[string]any{"kind": "hosted", "entry": "dist/plugin.js"},
			"capabilities": []string{"catalog_provider", "kind_resolver"},
			"permissions":  map[string]any{"network": []string{"media.example"}},
			"strings":      map[string]any{"en": "i18n/en.json"},
			"contributes":  map[string]any{"mediaKinds": []map[string]any{decl}},
		},
	}, map[string][]byte{"i18n/en.json": []byte(`{"lib_name":"Recordings"}`)})

	host := h.mgr.HostFor(pluginID)
	if _, err := host.KindUpsert(ctx, "recordings", []core.KindItem{
		{ID: "alb-1", Level: "album", Title: "Album 1"},
		{ID: "rec-versions", Level: "track", ParentID: "alb-1", Index: 1, Title: "Live Concert 2025", Playable: true},
	}); err != nil {
		t.Fatalf("KindUpsert: %v", err)
	}
	recItem, _, err := h.items.GetByExternal(ctx, pluginID, "recordings", "rec-versions")
	if err != nil {
		t.Fatalf("GetByExternal: %v", err)
	}
	key := recItem.Key
	viewer1 := &core.Viewer{ID: 1, IsOwner: true}
	viewer2 := &core.Viewer{ID: 2}

	// 1. Cold allowResolve=false and LocalResponse must NEVER call plugin /resolve.
	if vs, err := kr.KindVersions(ctx, viewer1, key, nil, false); err != nil || len(vs) != 0 {
		t.Fatalf("cold allowResolve=false = (%+v, %v), want empty", vs, err)
	}
	if vs, err := kr.KindVersions(core.WithLocalResponse(ctx), viewer1, key, nil, true); err != nil || len(vs) != 0 {
		t.Fatalf("cold LocalResponse = (%+v, %v), want empty", vs, err)
	}
	if resolveCalls.Load() != 0 {
		t.Fatalf("resolve called %d times on cold non-resolving listing, want 0", resolveCalls.Load())
	}

	// 2. Detail listing resolves once and returns W152 shape: index 0 Auto backed by 4K,
	// followed by 1080p and 720p without duplicating the 4K release.
	versions, err := kr.KindVersions(ctx, viewer1, key, nil, true)
	if err != nil {
		t.Fatalf("KindVersions: %v", err)
	}
	if len(versions) != 3 {
		t.Fatalf("len(versions) = %d, want 3: %+v", len(versions), versions)
	}
	if !versions[0].Auto || versions[0].ID != core.KindAutoVersionID || versions[0].Release.ID != "kind:"+hash4K {
		t.Fatalf("versions[0] = %+v, want Auto backed by 4K", versions[0])
	}
	if !versions[0].Info.Estimated || versions[0].Release.Info.Height != 2160 || versions[0].Availability != core.VersionCached {
		t.Fatalf("versions[0] metadata = info:%+v rel:%+v avail:%q", versions[0].Info, versions[0].Release.Info, versions[0].Availability)
	}
	if versions[1].Auto || versions[1].ID != "kind:"+hash1080p || versions[1].Release.Info.Height != 1080 {
		t.Fatalf("versions[1] = %+v, want 1080p", versions[1])
	}
	if versions[2].Auto || versions[2].ID != "kind:"+nzb720p || versions[2].Release.Info.Height != 720 {
		t.Fatalf("versions[2] = %+v, want 720p", versions[2])
	}
	if resolveCalls.Load() != 1 {
		t.Fatalf("resolveCalls = %d, want 1", resolveCalls.Load())
	}

	// 3. Repeat KindVersions, persisted-cache lookup on a fresh KindResolver (simulating
	// restart), OpenKindExcluding (Auto), and OpenKindSource (picked 720p) ALL reuse cache
	// without re-running plugin /resolve.
	krFresh := newResolver()
	versions2, err := krFresh.KindVersions(ctx, viewer1, key, nil, false)
	if err != nil || len(versions2) != 3 {
		t.Fatalf("persisted KindVersions = (%d, %v), want 3", len(versions2), err)
	}
	kmAuto, err := krFresh.OpenKindExcluding(ctx, viewer1, key, nil, nil)
	if err != nil {
		t.Fatalf("OpenKindExcluding(Auto): %v", err)
	}
	autoBuf := make([]byte, len(bytes4K))
	_, _ = kmAuto.ReadAt(autoBuf, 0)
	_ = kmAuto.Close()
	if string(autoBuf) != string(bytes4K) || kmAuto.Release.ID != "kind:"+hash4K {
		t.Fatalf("Auto opened %q (%s), want 4K", string(autoBuf), kmAuto.Release.ID)
	}

	// OpenKindSource opens ONLY the picked 720p release and does NOT clobber Auto winner.
	km720, err := krFresh.OpenKindSource(ctx, viewer1, key, "kind:"+nzb720p, nil)
	if err != nil {
		t.Fatalf("OpenKindSource(720p): %v", err)
	}
	buf720 := make([]byte, len(bytes720p))
	_, _ = km720.ReadAt(buf720, 0)
	_ = km720.Close()
	if string(buf720) != string(bytes720p) || km720.Release.ID != "kind:"+nzb720p {
		t.Fatalf("OpenKindSource opened %q (%s), want 720p", string(buf720), km720.Release.ID)
	}
	// Unknown source id returns ErrNotAvailable.
	if _, err := krFresh.OpenKindSource(ctx, viewer1, key, "kind:does-not-exist", nil); !errors.Is(err, core.ErrNotAvailable) {
		t.Fatalf("unknown source err = %v, want ErrNotAvailable", err)
	}
	if resolveCalls.Load() != 1 {
		t.Fatalf("resolveCalls after cached versions+auto+pick = %d, want 1", resolveCalls.Load())
	}

	// 4. Remembered viewer choice: recording viewer1's pick of 1080p promotes 1080p into
	// viewer1's Auto slot and Auto playback without affecting viewer2.
	title := &core.Title{Key: key, Type: core.TypeMovie, Title: "Live Concert 2025"}
	if err := resSvc.RecordViewerChoice(ctx, viewer1.ID, title, versions[1].ID, versions[1].Release, nil); err != nil {
		t.Fatalf("RecordViewerChoice: %v", err)
	}
	v1List, err := krFresh.KindVersions(ctx, viewer1, key, nil, true)
	if err != nil || len(v1List) != 3 {
		t.Fatalf("viewer1 KindVersions after choice = (%+v, %v)", v1List, err)
	}
	if !v1List[0].Auto || !v1List[0].Remembered || v1List[0].Release.ID != "kind:"+hash1080p {
		t.Fatalf("viewer1 Auto slot = %+v, want remembered 1080p", v1List[0])
	}
	if v1List[1].Release.ID != "kind:"+hash4K {
		t.Fatalf("viewer1 slot[1] = %+v, want displaced 4K release", v1List[1])
	}
	// Auto playback for viewer1 opens remembered 1080p; viewer2 still gets 4K default.
	kmV1, err := krFresh.OpenKindExcluding(ctx, viewer1, key, nil, nil)
	if err != nil {
		t.Fatalf("viewer1 Auto open: %v", err)
	}
	_ = kmV1.Close()
	if kmV1.Release.ID != "kind:"+hash1080p {
		t.Fatalf("viewer1 Auto opened %s, want remembered 1080p", kmV1.Release.ID)
	}
	kmV2, err := krFresh.OpenKindExcluding(ctx, viewer2, key, nil, nil)
	if err != nil {
		t.Fatalf("viewer2 Auto open: %v", err)
	}
	_ = kmV2.Close()
	if kmV2.Release.ID != "kind:"+hash4K {
		t.Fatalf("viewer2 Auto opened %s, want default 4K (not viewer1's choice)", kmV2.Release.ID)
	}
	if resolveCalls.Load() != 1 {
		t.Fatalf("resolveCalls after viewer choice flow = %d, want 1", resolveCalls.Load())
	}

	// Clearing viewer1's choice restores default 4K in Auto.
	if cleared, err := resSvc.ClearViewerChoice(ctx, viewer1.ID, title, "all"); err != nil || !cleared {
		t.Fatalf("ClearViewerChoice = (%v, %v), want (true, nil)", cleared, err)
	}
	v1Cleared, err := krFresh.KindVersions(ctx, viewer1, key, nil, true)
	if err != nil || len(v1Cleared) != 3 || v1Cleared[0].Remembered || v1Cleared[0].Release.ID != "kind:"+hash4K {
		t.Fatalf("viewer1 KindVersions after clear = %+v, err=%v", v1Cleared, err)
	}
}
