package plex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// plezyColdPlayback estimates without subtitle tracks until the
// play-time probe runs, then serves the probed facts as a warm probe —
// the cold-title shape of #618. Nothing probes unless ProbePlayMetadata
// is called, so any probed track in a response was waited for.
type plezyColdPlayback struct {
	*fakePlayback
	probed     core.MediaInfo
	ready      atomic.Bool
	calls      atomic.Int32
	block      <-chan struct{}
	entered    chan struct{}
	mu         sync.Mutex
	lastCtx    context.Context
	lastBudget time.Duration
	hasDL      bool
	wasLocal   bool
}

func (c *plezyColdPlayback) Estimate(context.Context, *core.Title, core.ClientCaps) core.MediaInfo {
	return estimateInfo
}

func (c *plezyColdPlayback) ProbePlayMetadata(ctx context.Context, _ *core.User, _ core.RatingKey, _ core.ClientCaps) error {
	c.calls.Add(1)
	dl, hasDL := ctx.Deadline()
	var budget time.Duration
	if hasDL {
		budget = time.Until(dl)
	}
	c.mu.Lock()
	c.lastCtx = ctx
	c.lastBudget = budget
	c.hasDL = hasDL
	c.wasLocal = core.LocalResponse(ctx)
	c.mu.Unlock()
	if c.entered != nil {
		select {
		case c.entered <- struct{}{}:
		default:
		}
	}
	if c.block != nil {
		select {
		case <-c.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	c.ready.Store(true)
	return nil
}

func (c *plezyColdPlayback) WarmProbeInfo(*core.User, core.ClientCaps, core.RatingKey) (core.MediaInfo, bool) {
	if !c.ready.Load() {
		return core.MediaInfo{}, false
	}
	return c.probed, true
}

// TestPlezyFirstPlayRefetchSelectsRuleSubtitle reproduces the exact #618
// Plezy 2.26.0 TV-mode request pattern on a never-played title:
//
//   - Opening the detail screen sends 3 GET /library/metadata/<id> within 1 s.
//     None of the burst fetches may wait or trigger a probe, and the arm must
//     survive the burst.
//   - Pressing Play 14 s later sends a 4th identical fetch. That re-ask must
//     wait on the probe (with a >4 s / <=12 s wait budget and without
//     LocalResponse) and return the probed streams with the viewer's
//     en/Always subtitle selected and accessible="1".
func TestPlezyFirstPlayRefetchSelectsRuleSubtitle(t *testing.T) {
	info := core.MediaInfo{
		Container: "mkv", Duration: 25 * time.Minute, BitrateKbps: 8000,
		Streams: []core.StreamInfo{
			{Index: 0, Type: "video", Codec: "h264", Width: 1920, Height: 1080, Default: true},
			{Index: 1, Type: "audio", Codec: "eac3", Language: "en", Channels: 6, Default: true},
			{Index: 2, Type: "subtitle", Codec: "subrip", Language: "en"},
		},
	}
	f, _, _ := newIssue459Fixture(t, info, "en")
	var clock atomic.Int64
	clock.Store(fixedNow.UnixNano())
	f.srv.setTestNow(func() time.Time { return time.Unix(0, clock.Load()).UTC() })

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	cold := &plezyColdPlayback{
		fakePlayback: f.playback,
		probed:       info,
		block:        release,
		entered:      entered,
	}
	f.srv.setPlayback(cold)
	f.playback.info = estimateInfo
	key := fightClub.Key.String()
	hdr := []string{
		"X-Plex-Product", "Plezy",
		"X-Plex-Version", "2.26.0",
		"X-Plex-Platform", "Android",
		"X-Plex-Device", "AFTKRT",
		"X-Plex-Client-Identifier", "plezy-618",
		"X-Plex-Session-Identifier", "plezy-618-session",
	}
	detail := "/library/metadata/" + key + "?includeChapters=1&includeMarkers=1&checkFiles=1&includeStreams=1"

	// Opening the detail screen sends three fetches within ~1 s. Because
	// release is still open, any wait here would block forever.
	for i, step := range []time.Duration{0, 300 * time.Millisecond, 600 * time.Millisecond} {
		if step > 0 {
			clock.Add(int64(step))
		}
		resp := f.get(t, detail, hdr...)
		if resp.Code != 200 {
			t.Fatalf("burst fetch %d: %d %s", i+1, resp.Code, resp.Body.String())
		}
		if cold.calls.Load() != 0 || cold.ready.Load() {
			t.Fatalf("burst fetch %d waited/probed while browsing (calls=%d)", i+1, cold.calls.Load())
		}
		if strings.Contains(partTag(resp.Body.String()), `accessible=`) {
			t.Fatalf("burst fetch %d claims file-checked completeness: %s", i+1, partTag(resp.Body.String()))
		}
		for _, tag := range subtitleTags(resp.Body.String()) {
			if strings.Contains(tag, `index="2"`) {
				t.Fatalf("burst fetch %d already lists the embedded track: %s", i+1, tag)
			}
		}
	}

	// 14 s after the arming fetch, the user presses Play.
	clock.Store(fixedNow.Add(14 * time.Second).UnixNano())
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- f.get(t, detail, hdr...)
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("play-time re-ask at +14 s did not start/wait on the probe")
	}
	close(release)

	playResp := <-done
	if playResp.Code != 200 {
		t.Fatalf("play re-fetch %d: %s", playResp.Code, playResp.Body.String())
	}
	if cold.calls.Load() != 1 || !cold.ready.Load() {
		t.Fatalf("play re-fetch probe calls=%d ready=%v, want 1/true", cold.calls.Load(), cold.ready.Load())
	}
	cold.mu.Lock()
	hasDL, budget, wasLocal := cold.hasDL, cold.lastBudget, cold.wasLocal
	cold.mu.Unlock()
	if wasLocal {
		t.Fatal("play re-fetch passed LocalResponse context to the probe")
	}
	if !hasDL {
		t.Fatal("play re-fetch ran without a bounded deadline")
	}
	if budget <= 4*time.Second || budget > plezyPlayProbeWait {
		t.Fatalf("play re-fetch wait budget = %s, want within (4s, %s]", budget, plezyPlayProbeWait)
	}
	selected := selectedTag(subtitleTags(playResp.Body.String()))
	if !strings.Contains(selected, `index="2"`) || !strings.Contains(selected, `languageCode="eng"`) {
		t.Fatalf("play-time re-fetch did not select the English subtitle: %q", selected)
	}
	if !strings.Contains(partTag(playResp.Body.String()), `accessible="1"`) {
		t.Fatalf("probed Part does not carry file-checked completeness: %s", partTag(playResp.Body.String()))
	}
}

// TestPlezyFirstPlayWaitBoundedByRequestContext ensures cancelling the HTTP
// request stops the play-time metadata wait promptly without hanging.
func TestPlezyFirstPlayWaitBoundedByRequestContext(t *testing.T) {
	info := core.MediaInfo{
		Container: "mkv", Duration: 25 * time.Minute, BitrateKbps: 8000,
		Streams: []core.StreamInfo{
			{Index: 0, Type: "video", Codec: "h264", Width: 1920, Height: 1080, Default: true},
			{Index: 1, Type: "audio", Codec: "eac3", Language: "en", Channels: 6, Default: true},
			{Index: 2, Type: "subtitle", Codec: "subrip", Language: "en"},
		},
	}
	f, _, _ := newIssue459Fixture(t, info, "en")
	var clock atomic.Int64
	clock.Store(fixedNow.UnixNano())
	f.srv.setTestNow(func() time.Time { return time.Unix(0, clock.Load()).UTC() })

	never := make(chan struct{})
	entered := make(chan struct{}, 1)
	cold := &plezyColdPlayback{fakePlayback: f.playback, probed: info, block: never, entered: entered}
	f.srv.setPlayback(cold)
	f.playback.info = estimateInfo

	detail := "/library/metadata/" + fightClub.Key.String() + "?includeChapters=1&includeMarkers=1&checkFiles=1&includeStreams=1"
	hdr := []string{
		"X-Plex-Product", "Plezy",
		"X-Plex-Version", "2.26.0",
		"X-Plex-Platform", "Android",
		"X-Plex-Client-Identifier", "plezy-cancel",
	}
	if r := f.get(t, detail, hdr...); r.Code != 200 {
		t.Fatalf("arm fetch %d", r.Code)
	}

	clock.Store(fixedNow.Add(14 * time.Second).UnixNano())
	rctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, detail, nil).WithContext(rctx)
	req.Header.Set("X-Plex-Token", ownerToken)
	for i := 0; i < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		f.srv.ServeHTTP(rec, req)
		close(done)
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("play re-ask did not enter probe")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled play re-ask did not return promptly")
	}
}

type plezyLabIndexer struct {
	release      core.Release
	specFailed   chan struct{}
	rejectSpec   bool
	playSearches atomic.Int32
	specBlocked  atomic.Int32
}

func (*plezyLabIndexer) Name() string { return "offline-plezy-lab" }

func (i *plezyLabIndexer) Search(ctx context.Context, _ core.SearchRequest) ([]core.Release, error) {
	if core.IsSpeculative(ctx) || core.IndexerPurpose(ctx) == core.IndexerPurposeWarmup || core.SpeculativeIndexerWork(ctx) {
		if i.rejectSpec {
			i.specBlocked.Add(1)
			if i.specFailed != nil {
				select {
				case i.specFailed <- struct{}{}:
				default:
				}
			}
			return nil, core.ErrForegroundOnly
		}
		return []core.Release{i.release}, nil
	}
	i.playSearches.Add(1)
	return []core.Release{i.release}, nil
}

func (i *plezyLabIndexer) FetchNZB(_ context.Context, r core.Release) ([]byte, error) {
	return []byte("nzb-" + r.ID), nil
}

// TestPlezyFirstPlayRealManagerLabPattern exercises the real playback.Manager,
// resolver, trackprefs, and production NoProviderWait (LocalResponse) against
// the lab pattern: 3 metadata fetches in 1 s, then Play 14 s later, then
// file.mkv adopting the same resolved+probed warm flight.
func TestPlezyFirstPlayRealManagerLabPattern(t *testing.T) {
	for _, tc := range []struct {
		name               string
		disableSpeculation bool
		detailWarmup       string
		rejectSpec         bool
	}{
		{
			name:       "after-speculative-warm-miss",
			rejectSpec: true,
		},
		{
			name:               "detail-warmup-disabled",
			disableSpeculation: true,
			detailWarmup:       "off",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := config.Defaults(dir)
			cfg.Quality.Default = "lab1080"
			cfg.Quality.Remote = ""
			cfg.Quality.Rules = nil
			cfg.Quality.Profiles = []core.QualityProfile{{Name: "lab1080", MaxHeight: 1080}}
			cfg.Cache.DisableSpeculation = tc.disableSpeculation
			cfg.Cache.DetailWarmup = tc.detailWarmup

			relName := "Fjord.Harbor.1999.1080p.BluRay.REMUX.AVC.DTS-HD.MA.5.1-LAB"
			const relSize = int64(4) << 30
			blockProbe := make(chan struct{})
			probeStarted := make(chan struct{}, 1)
			specFailed := make(chan struct{}, 4)
			idx := &plezyLabIndexer{
				release: core.Release{
					ID: "plezy-remux-rel", GUID: "plezy-remux-rel", Indexer: "offline-plezy-lab",
					Name: relName, Size: relSize, PubDate: fixedNow.Add(-24 * time.Hour),
					NZBURL: "offline", Info: releaseparse.Parse(relName),
				},
				specFailed: specFailed,
				rejectSpec: tc.rejectSpec,
			}
			engine := &shieldLocalEngine{size: relSize}

			var clock atomic.Int64
			clock.Store(fixedNow.UnixNano())
			nowFn := func() time.Time { return time.Unix(0, clock.Load()).UTC() }

			f := newFixture(func(d *testServerDeps) {
				setDepsConfig(d, func() config.Config { return cfg })
				setDepsNow(d, nowFn)
				d.DisableEnrichment = true
				// Match production: handleMetadata wraps ctx with LocalResponse.
				d.HomeCache = &testHomeCache{NoProviderWait: func(ctx context.Context) context.Context {
					return core.WithLocalResponse(catalog.WithReadWaitBudget(ctx, 0))
				}}
			})
			defer f.srv.Close()

			prefs := trackprefs.New(store.OpenTest(t))
			f.srv.setTrackPreferences(prefs)
			rules := trackprefs.Default()
			rules.SubtitleLanguages = []string{"en"}
			rules.SubtitleMode = "always"
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
			probedInfo := core.MediaInfo{
				Container: "mkv", Duration: fightClub.Runtime, BitrateKbps: 25000,
				Streams: []core.StreamInfo{
					{Index: 0, Type: "video", Codec: "h264", Width: 1920, Height: 1080, Default: true},
					{Index: 1, Type: "audio", Codec: "dca", Language: "en", Channels: 6, Default: true},
					{Index: 2, Type: "subtitle", Codec: "subrip", Language: "en"},
				},
			}
			mgr, err := playback.New(playback.Deps{
				Catalog:       f.catalog,
				State:         f.state,
				Resolver:      rs,
				Engine:        engine,
				Transcoder:    f.trans,
				SelectProfile: func(c core.ClientCaps, u *core.User, _ string) core.QualityProfile { return rs.ProfileFor(c, u) },
				DB:            db,
				NativeProbe: func(ctx context.Context, s core.MediaSource) (*core.MediaInfo, error) {
					probes.Add(1)
					select {
					case probeStarted <- struct{}{}:
					default:
					}
					select {
					case <-blockProbe:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					info := core.CloneMediaInfo(probedInfo)
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

			key := fightClub.Key.String()
			detail := "/library/metadata/" + key + "?includeChapters=1&includeMarkers=1&checkFiles=1&includeStreams=1"
			hdr := []string{
				"X-Plex-Product", "Plezy",
				"X-Plex-Version", "2.26.0",
				"X-Plex-Platform", "Android",
				"X-Plex-Device", "AFTKRT",
				"X-Plex-Client-Identifier", "plezy-real-618",
			}

			// 3 detail fetches within 1 s: none may wait on blockProbe or return probed streams.
			for i, step := range []time.Duration{0, 300 * time.Millisecond, 600 * time.Millisecond} {
				if step > 0 {
					clock.Add(int64(step))
				}
				resp := f.get(t, detail, hdr...)
				if resp.Code != 200 {
					t.Fatalf("burst fetch %d: %d %s", i+1, resp.Code, resp.Body.String())
				}
				if strings.Contains(partTag(resp.Body.String()), `accessible=`) {
					t.Fatalf("burst fetch %d claims completeness: %s", i+1, partTag(resp.Body.String()))
				}
			}
			if idx.playSearches.Load() != 0 || probes.Load() != 0 {
				t.Fatalf("browsing burst ran play probe: playSearches=%d probes=%d", idx.playSearches.Load(), probes.Load())
			}
			if tc.rejectSpec {
				// Let the background speculative detail warm-up fail and record its warmMiss.
				select {
				case <-specFailed:
				case <-time.After(5 * time.Second):
					t.Fatal("speculative detail warm-up did not run")
				}
				// Wait for the failed speculative warm-up goroutine to publish its warmMiss and exit.
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					if !mgr.Prewarm(context.Background(), &ownerUser, fightClub.Key, core.ClientCaps{Protocol: "plex", Product: "Plezy", ClientID: "plezy-real-618"}, core.PrewarmOptions{Reason: core.PrewarmDetail}) {
						break
					}
					time.Sleep(5 * time.Millisecond)
				}
			}

			// 14 s later: Play re-ask must join/start the foreground resolve+probe flight and wait.
			clock.Store(fixedNow.Add(14 * time.Second).UnixNano())
			playDone := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				playDone <- f.get(t, detail, hdr...)
			}()

			select {
			case <-probeStarted:
			case <-time.After(5 * time.Second):
				t.Fatal("play re-ask at +14 s short-circuited instead of running foreground resolve+probe")
			}
			close(blockProbe)

			playResp := <-playDone
			if playResp.Code != 200 {
				t.Fatalf("play re-ask: %d %s", playResp.Code, playResp.Body.String())
			}
			selected := selectedTag(subtitleTags(playResp.Body.String()))
			if !strings.Contains(selected, `index="2"`) || !strings.Contains(selected, `languageCode="eng"`) {
				t.Fatalf("play re-ask did not select English SRT: %q (body=%s)", selected, playResp.Body.String())
			}
			if !strings.Contains(partTag(playResp.Body.String()), `accessible="1"`) {
				t.Fatalf("play re-ask Part missing accessible=1: %s", partTag(playResp.Body.String()))
			}

			// Immediately following file.mkv (libmpv drops Plex headers, sends only token
			// and User-Agent: libmpv) must adopt the same warm entry with no second
			// resolve or probe.
			searchesBeforePart := idx.playSearches.Load()
			probesBeforePart := probes.Load()
			partReq := httptest.NewRequest(http.MethodGet, "/library/parts/"+key+"/0/file.mkv?X-Plex-Token="+ownerToken, nil)
			partReq.Header.Set("User-Agent", "libmpv")
			partReq.Header.Set("Range", "bytes=0-15")
			partRec := httptest.NewRecorder()
			f.srv.ServeHTTP(partRec, partReq)
			if partRec.Code != http.StatusPartialContent && partRec.Code != http.StatusOK {
				t.Fatalf("file.mkv %d: %s", partRec.Code, partRec.Body.String())
			}
			if idx.playSearches.Load() != searchesBeforePart || probes.Load() != probesBeforePart {
				t.Fatalf("file.mkv repeated work instead of adopting warm flight: searches %d->%d probes %d->%d",
					searchesBeforePart, idx.playSearches.Load(), probesBeforePart, probes.Load())
			}
		})
	}
}

// TestPlezyFirstPlayDoesNotAffectOtherClients ensures non-Plezy clients keep
// their existing detail behaviour on the same 3+1 fetch pattern.
func TestPlezyFirstPlayDoesNotAffectOtherClients(t *testing.T) {
	info := core.MediaInfo{
		Container: "mkv", Duration: 25 * time.Minute, BitrateKbps: 8000,
		Streams: []core.StreamInfo{
			{Index: 0, Type: "video", Codec: "h264", Width: 1920, Height: 1080, Default: true},
			{Index: 1, Type: "audio", Codec: "eac3", Language: "en", Channels: 6, Default: true},
			{Index: 2, Type: "subtitle", Codec: "subrip", Language: "en"},
		},
	}
	f, _, _ := newIssue459Fixture(t, info, "en")
	var clock atomic.Int64
	clock.Store(fixedNow.UnixNano())
	f.srv.setTestNow(func() time.Time { return time.Unix(0, clock.Load()).UTC() })

	cold := &plezyColdPlayback{fakePlayback: f.playback, probed: info}
	f.srv.setPlayback(cold)
	f.playback.info = estimateInfo

	detail := "/library/metadata/" + fightClub.Key.String() + "?includeChapters=1&includeMarkers=1&checkFiles=1&includeStreams=1"
	hdr := []string{"X-Plex-Product", "Plex Web", "X-Plex-Client-Identifier", "plex-web-618"}
	for _, at := range []time.Duration{0, 300 * time.Millisecond, 900 * time.Millisecond, 14 * time.Second} {
		clock.Store(fixedNow.Add(at).UnixNano())
		if r := f.get(t, detail, hdr...); r.Code != 200 {
			t.Fatalf("Plex Web at +%s: %d", at, r.Code)
		}
	}
	if cold.calls.Load() != 0 {
		t.Fatalf("Plezy play probe ran for Plex Web (%d calls)", cold.calls.Load())
	}
}

func partTag(body string) string {
	start := strings.Index(body, "<Part")
	if start < 0 {
		return ""
	}
	rest := body[start:]
	if end := strings.Index(rest, ">"); end >= 0 {
		return rest[:end+1]
	}
	return rest
}

// Plezy on Android has no episode detail page: a season row plays directly,
// so the play-time fetch is the title's first. Its query shape (Plezy's
// playback read lists includeMarkers first) is play intent on its own, while
// the browsing shape (season-row focus header) never waits.
func TestPlezyFirstPlaySeasonRowPlaybackShape(t *testing.T) {
	info := core.MediaInfo{
		Container: "mkv", Duration: 25 * time.Minute, BitrateKbps: 8000,
		Streams: []core.StreamInfo{
			{Index: 0, Type: "video", Codec: "h264", Width: 1920, Height: 1080, Default: true},
			{Index: 1, Type: "audio", Codec: "eac3", Language: "en", Channels: 6, Default: true},
			{Index: 2, Type: "subtitle", Codec: "subrip", Language: "en"},
		},
	}
	f, _, _ := newIssue459Fixture(t, info, "en")
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	cold := &plezyColdPlayback{fakePlayback: f.playback, probed: info, block: release, entered: entered}
	f.srv.setPlayback(cold)
	f.playback.info = estimateInfo
	hdr := []string{"X-Plex-Product", "Plezy", "X-Plex-Version", "2.26.0", "X-Plex-Platform", "Android", "X-Plex-Client-Identifier", "plezy-row", "X-Plex-Session-Identifier", "plezy-row-session"}
	base := "/library/metadata/" + fightClub.Key.String()

	if resp := f.get(t, base+"?includeChapters=1&includeMarkers=1&checkFiles=1&includeStreams=1", hdr...); resp.Code != 200 || cold.calls.Load() != 0 {
		t.Fatalf("focus fetch waited or failed: %d calls=%d", resp.Code, cold.calls.Load())
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- f.get(t, base+"?includeMarkers=1&includeChapters=1&checkFiles=1&includeStreams=1", hdr...)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("playback-shaped first fetch did not wait on the probe")
	}
	close(release)
	resp := <-done
	selected := selectedTag(subtitleTags(resp.Body.String()))
	if resp.Code != 200 || !strings.Contains(selected, `index="2"`) {
		t.Fatalf("season-row play did not select the English subtitle: %d %q", resp.Code, selected)
	}
}
