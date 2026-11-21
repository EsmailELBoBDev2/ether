package plex

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/catalog"
	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/playback"
	"github.com/braedonsaunders/ether/internal/releaseparse"
	"github.com/braedonsaunders/ether/internal/resolver"
	"github.com/braedonsaunders/ether/internal/store"
	"github.com/braedonsaunders/ether/internal/trackprefs"
)

type bedrockLabIndexer struct {
	byEpisode    map[int]core.Release
	playSearches atomic.Int32
	specBlocked  atomic.Int32
}

func (*bedrockLabIndexer) Name() string { return "stremio-usenet-addon" }

func (i *bedrockLabIndexer) Search(ctx context.Context, req core.SearchRequest) ([]core.Release, error) {
	// Speculative warm-ups cannot open uncached on-demand Stremio Usenet streams.
	if core.IsSpeculative(ctx) || core.IndexerPurpose(ctx) == core.IndexerPurposeWarmup || core.SpeculativeIndexerWork(ctx) {
		i.specBlocked.Add(1)
		return nil, core.ErrForegroundOnly
	}
	i.playSearches.Add(1)
	if rel, ok := i.byEpisode[req.Episode]; ok {
		return []core.Release{rel}, nil
	}
	return nil, nil
}

func (i *bedrockLabIndexer) FetchNZB(_ context.Context, r core.Release) ([]byte, error) {
	return []byte("nzb-" + r.ID), nil
}

// Regression for #670: Plex for iOS direct play builds its track list and
// initial direct-play audio track from the pre-play /playQueues and
// /library/metadata responses rather than /decision. On a cold dual-audio
// episode (track 1 Japanese default, track 2 English) from an on-demand source
// where speculative warm-ups cannot open, POST /playQueues and the lean
// pre-play /library/metadata read (including advancing S01E01 -> S01E02 in an
// active continuous PlayQueue) must run the foreground play probe so the very
// first metadata response exposes both real tracks with English selected="1"
// and /decision adopts the same warm flight without re-probing.
func TestBedrockIOSFirstPlayDualAudioSelectsPreferredTrack(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults(dir)
	cfg.Quality.Default = "lab1080"
	cfg.Quality.Remote = ""
	cfg.Quality.Rules = nil
	cfg.Quality.Profiles = []core.QualityProfile{{Name: "lab1080", MaxHeight: 1080}}

	const relSize = int64(2) << 30
	ep1Name := "Boundary.Bay.S01E01.1080p.NF.WEB-DL.H264.DDP5.1.Dual-HONE"
	ep2Name := "Boundary.Bay.S01E02.1080p.BluRay.x264-REDBLADE"
	idx := &bedrockLabIndexer{
		byEpisode: map[int]core.Release{
			1: {
				ID: "ep1-dual", GUID: "ep1-dual", Indexer: "stremio-usenet-addon",
				Name: ep1Name, Size: relSize, PubDate: fixedNow.Add(-24 * time.Hour),
				NZBURL: "offline", Info: releaseparse.Parse(ep1Name),
			},
			2: {
				ID: "ep2-dual", GUID: "ep2-dual", Indexer: "stremio-usenet-addon",
				Name: ep2Name, Size: relSize, PubDate: fixedNow.Add(-24 * time.Hour),
				NZBURL: "offline", Info: releaseparse.Parse(ep2Name),
			},
		},
	}

	var clock atomic.Int64
	clock.Store(fixedNow.UnixNano())
	nowFn := func() time.Time { return time.Unix(0, clock.Load()).UTC() }

	f := newFixture(func(d *testServerDeps) {
		setDepsConfig(d, func() config.Config { return cfg })
		setDepsNow(d, nowFn)
		d.DisableEnrichment = true
		d.HomeCache = &testHomeCache{NoProviderWait: func(ctx context.Context) context.Context {
			return core.WithLocalResponse(catalog.WithReadWaitBudget(ctx, 0))
		}}
	})
	defer f.srv.Close()
	f.srv.setTranscoder(&issue459Transcoder{fakeTranscoder: f.trans})

	prefs := trackprefs.New(store.OpenTest(t))
	f.srv.setTrackPreferences(prefs)
	rules := trackprefs.Default()
	rules.AudioLanguages = []string{"en"}
	rules.PreferOriginal = false
	if err := prefs.PutRules(context.Background(), ownerUser.ID, rules); err != nil {
		t.Fatal(err)
	}

	rs := resolver.New(idx, nil, nil, cfg, resolver.Options{Now: nowFn})
	defer rs.Close()

	db, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var probes atomic.Int32
	probedDual := core.MediaInfo{
		Container: "mkv", Duration: bbE1.Runtime, BitrateKbps: 8000,
		Streams: []core.StreamInfo{
			{Index: 0, Type: "video", Codec: "h264", Profile: "high", Width: 1920, Height: 1080, BitDepth: 8, Default: true, BitrateKbps: 6720},
			{Index: 1, Type: "audio", Codec: "eac3", Language: "jpn", Channels: 6, Default: true, BitrateKbps: 640},
			{Index: 2, Type: "audio", Codec: "eac3", Language: "eng", Channels: 6, Default: false, BitrateKbps: 640},
		},
	}
	mgr, err := playback.New(playback.Deps{
		Catalog:       f.catalog,
		State:         f.state,
		Resolver:      rs,
		Engine:        &shieldLocalEngine{size: relSize},
		Transcoder:    f.trans,
		SelectProfile: func(c core.ClientCaps, u *core.User, _ string) core.QualityProfile { return rs.ProfileFor(c, u) },
		DB:            db,
		NativeProbe: func(_ context.Context, s core.MediaSource) (*core.MediaInfo, error) {
			probes.Add(1)
			info := core.CloneMediaInfo(probedDual)
			info.Size = s.Size()
			return &info, nil
		},
	}, cfg, playback.Options{
		SessionSurface: func(surface string) string {
			if surface == "plex" {
				return ""
			}
			return surface
		},
		DataDir: dir,
		Now:     nowFn,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	f.srv.setPlayback(mgr)
	f.srv.setVariants(rs)

	iosHeaders := []string{
		"X-Plex-Product", "Plex for iOS",
		"X-Plex-Platform", "iOS",
		"X-Plex-Device", "iPhone",
		"X-Plex-Client-Identifier", "iphone-670",
		"X-Plex-Session-Identifier", "sess-670",
	}
	ep1Key := bbE1.Key.String()
	ep2Key := bbE2.Key.String()

	// 1. Browsing the S01E01 detail page before Play must NOT trigger a foreground play search.
	browse := f.get(t, "/library/metadata/"+ep1Key+"?includeRelated=1&includeExtras=1", iosHeaders...)
	if browse.Code != http.StatusOK {
		t.Fatalf("detail browse %d: %s", browse.Code, browse.Body.String())
	}
	if idx.playSearches.Load() != 0 {
		t.Fatalf("detail browse ran foreground play search (%d)", idx.playSearches.Load())
	}

	// 2. Pressing Play creates a continuous PlayQueue [S01E01, S01E02]: POST /playQueues
	// must run ProbePlayMetadata and expose BOTH real tracks with English (index 2) selected.
	ep1URI := url.QueryEscape("server://" + f.srv.identity().MachineIdentifier + "/com.plexapp.plugins.library/library/metadata/" + ep1Key)
	pq := f.request(t, http.MethodPost, "/playQueues?type=video&continuous=1&uri="+ep1URI, headerMap(iosHeaders...))
	if pq.Code != http.StatusOK {
		t.Fatalf("POST /playQueues %d: %s", pq.Code, pq.Body.String())
	}
	pqAudios := selectedMediaAudioTags(pq.Body.String())
	if len(pqAudios) != 2 {
		t.Fatalf("POST /playQueues returned %d audio tracks, want 2 (jpn + eng):\n%s", len(pqAudios), pq.Body.String())
	}
	pqEng := audioByLang(pqAudios, "eng")
	pqJpn := audioByLang(pqAudios, "jpn")
	if pqEng == "" || !strings.Contains(pqEng, `selected="1"`) || streamAttr(pqEng, "index") != "2" {
		t.Fatalf("POST /playQueues did not select English track 2: %q", pqAudios)
	}
	if pqJpn == "" || strings.Contains(pqJpn, `selected="1"`) {
		t.Fatalf("POST /playQueues wrongly selected Japanese default track: %q", pqAudios)
	}

	// 3. Follow-up GET /library/metadata/S01E01 right before /decision returns the
	// exact same probed tracks with English selected="1" and no extra search/probe.
	searchesAfterPQ := idx.playSearches.Load()
	probesAfterPQ := probes.Load()
	meta1 := f.get(t, "/library/metadata/"+ep1Key, iosHeaders...)
	if meta1.Code != http.StatusOK {
		t.Fatalf("GET /library/metadata ep1 %d: %s", meta1.Code, meta1.Body.String())
	}
	meta1Eng := audioByLang(selectedMediaAudioTags(meta1.Body.String()), "eng")
	if meta1Eng == "" || !strings.Contains(meta1Eng, `selected="1"`) || streamAttr(meta1Eng, "index") != "2" {
		t.Fatalf("pre-play metadata ep1 did not select English track 2: %s", meta1.Body.String())
	}
	if idx.playSearches.Load() != searchesAfterPQ || probes.Load() != probesAfterPQ {
		t.Fatalf("pre-play metadata ep1 repeated work: searches %d->%d probes %d->%d",
			searchesAfterPQ, idx.playSearches.Load(), probesAfterPQ, probes.Load())
	}

	// 4. /decision adopts the same warm flight (no second search/probe) and picks English.
	dec1 := f.get(t, "/video/:/transcode/universal/decision?path=/library/metadata/"+ep1Key+
		"&hasMDE=1&directPlay=1&directStream=1&mediaIndex=0&partIndex=0&protocol=hls&session=s670-1", iosHeaders...)
	if dec1.Code != http.StatusOK {
		t.Fatalf("decision ep1 %d: %s", dec1.Code, dec1.Body.String())
	}
	if f.trans.lastOpts.AudioStream != 2 {
		t.Fatalf("decision ep1 audioStream = %d, want 2 (English)", f.trans.lastOpts.AudioStream)
	}
	if idx.playSearches.Load() != searchesAfterPQ || probes.Load() != probesAfterPQ {
		t.Fatalf("decision ep1 repeated work instead of adopting warm flight: searches %d->%d probes %d->%d",
			searchesAfterPQ, idx.playSearches.Load(), probesAfterPQ, probes.Load())
	}

	// 5. Advancing to S01E02 in the continuous PlayQueue (12 min later) sends a lean
	// GET /library/metadata/S01E02 without a new POST /playQueues. It must probe
	// S01E02 and return both real tracks with English selected="1" on first play.
	clock.Store(fixedNow.Add(12 * time.Minute).UnixNano())
	meta2 := f.get(t, "/library/metadata/"+ep2Key, iosHeaders...)
	if meta2.Code != http.StatusOK {
		t.Fatalf("GET /library/metadata ep2 %d: %s", meta2.Code, meta2.Body.String())
	}
	meta2Audios := selectedMediaAudioTags(meta2.Body.String())
	if len(meta2Audios) != 2 {
		t.Fatalf("pre-play metadata ep2 returned %d audio tracks, want 2:\n%s", len(meta2Audios), meta2.Body.String())
	}
	meta2Eng := audioByLang(meta2Audios, "eng")
	if meta2Eng == "" || !strings.Contains(meta2Eng, `selected="1"`) || streamAttr(meta2Eng, "index") != "2" {
		t.Fatalf("pre-play metadata ep2 did not select English track 2: %q", meta2Audios)
	}
	searchesAfterMeta2 := idx.playSearches.Load()
	probesAfterMeta2 := probes.Load()
	dec2 := f.get(t, "/video/:/transcode/universal/decision?path=/library/metadata/"+ep2Key+
		"&hasMDE=1&directPlay=1&directStream=1&mediaIndex=0&partIndex=0&protocol=hls&session=s670-2", iosHeaders...)
	if dec2.Code != http.StatusOK || f.trans.lastOpts.AudioStream != 2 {
		t.Fatalf("decision ep2 code=%d audioStream=%d, want 200 and 2", dec2.Code, f.trans.lastOpts.AudioStream)
	}
	if idx.playSearches.Load() != searchesAfterMeta2 || probes.Load() != probesAfterMeta2 {
		t.Fatalf("decision ep2 repeated work instead of adopting warm flight: searches %d->%d probes %d->%d",
			searchesAfterMeta2, idx.playSearches.Load(), probesAfterMeta2, probes.Load())
	}
}
