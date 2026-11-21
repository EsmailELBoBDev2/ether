package adapters

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/stream"
)

type stubUsenetIndexer struct {
	mu      sync.Mutex
	nzbs    map[string][]byte
	fetchN  int
	fetchFn func(ctx context.Context, rel core.Release) ([]byte, error)
}

func (s *stubUsenetIndexer) Name() string { return "stub-usenet" }

func (s *stubUsenetIndexer) Search(_ context.Context, _ core.SearchRequest) ([]core.Release, error) {
	return nil, nil
}

func (s *stubUsenetIndexer) FetchNZB(ctx context.Context, rel core.Release) ([]byte, error) {
	s.mu.Lock()
	s.fetchN++
	fn := s.fetchFn
	data := s.nzbs[rel.NZBURL]
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, rel)
	}
	if len(data) == 0 {
		return nil, core.ErrNotFound
	}
	return data, nil
}

func (s *stubUsenetIndexer) fetches() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fetchN
}

type stubUsenetEngine struct {
	mu         sync.Mutex
	checkN     int
	checkBG    []bool
	checkRes   core.UsenetCheckResult
	checkErr   error
	openErr    error
	openSource core.MediaSource
}

func (e *stubUsenetEngine) Open(_ context.Context, id string, data []byte) (core.MediaSource, error) {
	e.mu.Lock()
	err := e.openErr
	src := e.openSource
	e.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if src != nil {
		return src, nil
	}
	return stubMediaSource{id: id, data: data}, nil
}

func (e *stubUsenetEngine) Prefetch(_ context.Context, _ string, _ []byte, _, _ int64) error {
	return nil
}

func (e *stubUsenetEngine) CheckArticles(ctx context.Context, _ string, _ []byte) (core.UsenetCheckResult, error) {
	e.mu.Lock()
	e.checkN++
	e.checkBG = append(e.checkBG, core.IsBackgroundCheck(ctx))
	res := e.checkRes
	err := e.checkErr
	e.mu.Unlock()
	return res, err
}

func (e *stubUsenetEngine) checks() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.checkN
}

type failingReadMediaSource struct {
	id      string
	readErr error
}

func (s *failingReadMediaSource) ReleaseID() string { return s.id }
func (s *failingReadMediaSource) Name() string      { return "track01.flac" }
func (s *failingReadMediaSource) Size() int64       { return 4096 }
func (s *failingReadMediaSource) Close() error      { return nil }
func (s *failingReadMediaSource) ReadAt(_ []byte, _ int64) (int, error) {
	return 0, s.readErr
}
func (s *failingReadMediaSource) ReadAtContext(_ context.Context, _ []byte, _ int64) (int, error) {
	return 0, s.readErr
}

func sampleTestNZB(tag string) []byte {
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <file poster="p" date="1700000000" subject="%s.flac (1/1)">
    <groups><group>alt.binaries.test</group></groups>
    <segments><segment bytes="1024" number="1">%s-1@test</segment></segments>
  </file>
</nzb>`, tag, tag))
}

func TestKindResolverUsenetCheckCacheHashAndTTL(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC)

	nzbBytes := sampleTestNZB("album-a")
	idx := &stubUsenetIndexer{
		nzbs: map[string][]byte{
			"https://idx1.example/a.nzb": nzbBytes,
			"https://idx2.example/a.nzb": nzbBytes, // different URL, identical NZB bytes
		},
	}
	eng := &stubUsenetEngine{
		checkRes: core.UsenetCheckResult{Available: true, Checked: 12, Missing: 0},
	}

	kr := NewKindResolver(KindResolverDeps{
		Indexer: idx,
		Engine:  eng,
		Now:     func() time.Time { return now },
	})

	// 1. First check fetches NZB and runs CheckArticles at background priority.
	got, err := kr.ResolveSources(ctx, "p.music", core.SourceQuery{
		Kind:      "usenet_check",
		Candidate: &core.SourceCandidate{NZBURL: "https://idx1.example/a.nzb", Indexer: "idx1"},
	})
	if err != nil {
		t.Fatalf("first usenet_check: %v", err)
	}
	if len(got) != 1 || got[0].UsenetCheck == nil {
		t.Fatalf("unexpected candidates: %+v", got)
	}
	uc := got[0].UsenetCheck
	if !uc.Available || uc.Checked != 12 || uc.Missing != 0 || uc.Cached || uc.PlaybackFailed || uc.CheckedAt != now.Unix() {
		t.Fatalf("first verdict = %+v", uc)
	}
	if idx.fetches() != 1 || eng.checks() != 1 {
		t.Fatalf("fetches=%d checks=%d, want 1, 1", idx.fetches(), eng.checks())
	}
	if len(eng.checkBG) != 1 || !eng.checkBG[0] {
		t.Fatalf("CheckArticles did not receive background priority context: %v", eng.checkBG)
	}

	// 2. Second check on same URL hits cache before FetchNZB (0 extra fetches, 0 extra NNTP checks).
	got, err = kr.ResolveSources(ctx, "p.music", core.SourceQuery{
		Kind:   "usenet_check",
		NZBURL: "https://idx1.example/a.nzb",
	})
	if err != nil {
		t.Fatalf("cached usenet_check: %v", err)
	}
	if !got[0].UsenetCheck.Cached || !got[0].UsenetCheck.Available || got[0].UsenetCheck.Checked != 12 {
		t.Fatalf("cached verdict = %+v", got[0].UsenetCheck)
	}
	if idx.fetches() != 1 || eng.checks() != 1 {
		t.Fatalf("cached hit triggered extra work: fetches=%d checks=%d", idx.fetches(), eng.checks())
	}

	// 3. Second URL carrying identical NZB bytes hits cache by NZB hash (1 FetchNZB, 0 extra NNTP checks).
	got, err = kr.ResolveSources(ctx, "p.music", core.SourceQuery{
		Kind:   "usenet_check",
		NZBURL: "https://idx2.example/a.nzb",
	})
	if err != nil {
		t.Fatalf("hash-deduped usenet_check: %v", err)
	}
	if !got[0].UsenetCheck.Cached || !got[0].UsenetCheck.Available {
		t.Fatalf("hash-deduped verdict = %+v", got[0].UsenetCheck)
	}
	if idx.fetches() != 2 || eng.checks() != 1 {
		t.Fatalf("hash dedupe expected fetches=2 checks=1, got %d, %d", idx.fetches(), eng.checks())
	}

	// 4. Subsequent check on second URL is now aliased by releaseKey (0 extra FetchNZB too).
	got, err = kr.ResolveSources(ctx, "p.music", core.SourceQuery{
		Kind:   "usenet_check",
		NZBURL: "https://idx2.example/a.nzb",
	})
	if err != nil || !got[0].UsenetCheck.Cached || idx.fetches() != 2 || eng.checks() != 1 {
		t.Fatalf("aliased check: err=%v res=%+v fetches=%d checks=%d", err, got, idx.fetches(), eng.checks())
	}

	// 5. After TTL expires, next check re-runs against the indexer and engine.
	now = now.Add(usenetCheckPositiveTTL + time.Second)
	eng.checkRes = core.UsenetCheckResult{Available: false, Checked: 4, Missing: 1, Reason: "articles_missing"}
	got, err = kr.ResolveSources(ctx, "p.music", core.SourceQuery{
		Kind:   "usenet_check",
		NZBURL: "https://idx1.example/a.nzb",
	})
	if err != nil {
		t.Fatalf("post-TTL usenet_check: %v", err)
	}
	if got[0].UsenetCheck.Cached || got[0].UsenetCheck.Available || got[0].UsenetCheck.Missing != 1 {
		t.Fatalf("post-TTL verdict = %+v", got[0].UsenetCheck)
	}
	if idx.fetches() != 3 || eng.checks() != 2 {
		t.Fatalf("post-TTL expected fetches=3 checks=2, got %d, %d", idx.fetches(), eng.checks())
	}

	// 6. Transient errors are never cached as negative verdicts.
	now = now.Add(usenetCheckNegativeTTL + time.Second)
	eng.checkErr = errors.New("provider timeout")
	if _, err := kr.ResolveSources(ctx, "p.music", core.SourceQuery{
		Kind:   "usenet_check",
		NZBURL: "https://idx1.example/a.nzb",
	}); err == nil {
		t.Fatal("expected transient error")
	}
	eng.checkErr = nil
	eng.checkRes = core.UsenetCheckResult{Available: true, Checked: 6, Missing: 0}
	got, err = kr.ResolveSources(ctx, "p.music", core.SourceQuery{
		Kind:   "usenet_check",
		NZBURL: "https://idx1.example/a.nzb",
	})
	if err != nil || got[0].UsenetCheck.Cached || !got[0].UsenetCheck.Available {
		t.Fatalf("after transient error: err=%v res=%+v", err, got)
	}
}

func TestKindResolverUsenetCheckPlaybackFailureRecording(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 20, 16, 0, 0, 0, time.UTC)

	nzbBytes := sampleTestNZB("album-broken")
	idx := &stubUsenetIndexer{
		nzbs: map[string][]byte{
			"https://idx1.example/broken.nzb":   nzbBytes,
			"https://idx2.example/broken.nzb":   nzbBytes,
			"https://idx1.example/openfail.nzb": sampleTestNZB("openfail"),
		},
	}
	eng := &stubUsenetEngine{
		checkRes: core.UsenetCheckResult{Available: true, Checked: 12, Missing: 0},
	}

	kr := NewKindResolver(KindResolverDeps{
		Indexer: idx,
		Engine:  eng,
		Now:     func() time.Time { return now },
	})

	// 1. Sampled check initially passes for idx1 and dedupes for idx2.
	for _, u := range []string{"https://idx1.example/broken.nzb", "https://idx2.example/broken.nzb"} {
		got, err := kr.ResolveSources(ctx, "p.music", core.SourceQuery{Kind: "usenet_check", NZBURL: u})
		if err != nil || !got[0].UsenetCheck.Available {
			t.Fatalf("initial check %s = %+v, %v", u, got, err)
		}
	}

	// 2. Real playback opens with an explicit fileIndex target (non-video) and
	//    encounters a mid-stream missing article on ReadAtContext.
	eng.openSource = &failingReadMediaSource{
		id:      "rel-broken",
		readErr: fmt.Errorf("segment 42: %w", core.ErrArticleMissing),
	}
	fileIdx := 0
	ms, err := kr.openRelease(ctx, core.ReleaseHint{
		NZBURL:    "https://idx1.example/broken.nzb",
		FileIndex: &fileIdx,
	})
	if err != nil {
		t.Fatalf("openRelease: %v", err)
	}
	cr, ok := ms.(core.ContextReaderAt)
	if !ok {
		t.Fatal("wrapped Usenet MediaSource must preserve core.ContextReaderAt")
	}
	if _, err := cr.ReadAtContext(ctx, make([]byte, 16), 0); !errors.Is(err, core.ErrArticleMissing) {
		t.Fatalf("ReadAtContext err = %v, want ErrArticleMissing", err)
	}
	_ = ms.Close()

	// 3. Subsequent usenet_check on BOTH idx1 and the hash-aliased idx2 URL now
	//    agrees with playback without any extra NNTP check.
	fetchesBefore := idx.fetches()
	checksBefore := eng.checks()
	for _, u := range []string{"https://idx1.example/broken.nzb", "https://idx2.example/broken.nzb"} {
		got, err := kr.ResolveSources(ctx, "p.music", core.SourceQuery{Kind: "usenet_check", NZBURL: u})
		if err != nil {
			t.Fatalf("post-failure usenet_check %s: %v", u, err)
		}
		uc := got[0].UsenetCheck
		if uc.Available || !uc.Cached || !uc.PlaybackFailed || uc.Missing < 1 || uc.Checked < uc.Missing {
			t.Fatalf("post-failure verdict for %s = %+v, want unavailable playbackFailed cached", u, uc)
		}
		if uc.Reason != "playback_article_missing" {
			t.Fatalf("Reason = %q, want playback_article_missing", uc.Reason)
		}
	}
	if idx.fetches() != fetchesBefore || eng.checks() != checksBefore {
		t.Fatalf("post-failure checks hit indexer/engine: fetches %d->%d checks %d->%d",
			fetchesBefore, idx.fetches(), checksBefore, eng.checks())
	}

	// 4. Open-time playback failure on a never-checked NZB is also recorded and exposed.
	eng.openSource = nil
	eng.openErr = fmt.Errorf("archive damaged: %w", stream.ErrCorrupt)
	if _, err := kr.openRelease(ctx, core.ReleaseHint{NZBURL: "https://idx1.example/openfail.nzb"}); !errors.Is(err, stream.ErrCorrupt) {
		t.Fatalf("openRelease err = %v, want ErrCorrupt", err)
	}
	got, err := kr.ResolveSources(ctx, "p.music", core.SourceQuery{
		Kind:   "usenet_check",
		NZBURL: "https://idx1.example/openfail.nzb",
	})
	if err != nil {
		t.Fatalf("usenet_check after open failure: %v", err)
	}
	uc := got[0].UsenetCheck
	if uc.Available || !uc.Cached || !uc.PlaybackFailed || uc.Reason != "playback_article_corrupt" {
		t.Fatalf("open-failure verdict = %+v", uc)
	}

	// 5. Transient playback errors (cancel, EOF, provider outage) never taint the cache.
	eng.openErr = nil
	idx.nzbs["https://idx1.example/transient.nzb"] = sampleTestNZB("transient")
	for _, transient := range []error{context.Canceled, io.EOF, stream.ErrClosed, core.ErrSourceUnavailable} {
		eng.openSource = &failingReadMediaSource{id: "rel-transient", readErr: transient}
		src, err := kr.openRelease(ctx, core.ReleaseHint{NZBURL: "https://idx1.example/transient.nzb"})
		if err != nil {
			t.Fatalf("openRelease transient: %v", err)
		}
		_, _ = src.ReadAt(make([]byte, 8), 0)
		_ = src.Close()
	}
	if _, hit := kr.lookupUsenetCheck("https://idx1.example/transient.nzb", ""); hit {
		t.Fatal("transient playback errors must not record a playbackFailed verdict")
	}
}

func TestKindResolverUsenetCheckCacheBoundedSize(t *testing.T) {
	now := time.Date(2026, 11, 20, 17, 0, 0, 0, time.UTC)
	kr := NewKindResolver(KindResolverDeps{
		Now: func() time.Time { return now },
	})
	for i := 0; i < maxUsenetCheckCacheEntries+50; i++ {
		now = now.Add(time.Millisecond)
		kr.storeUsenetCheck(
			fmt.Sprintf("https://idx.example/%d.nzb", i),
			fmt.Sprintf("hash-%d", i),
			core.UsenetCheckResult{Available: true, Checked: 12, Missing: 0},
		)
	}
	kr.usenetCheckMu.Lock()
	entries := len(kr.usenetEntries)
	byRel := len(kr.usenetByRelease)
	byHash := len(kr.usenetByHash)
	kr.usenetCheckMu.Unlock()
	if entries > maxUsenetCheckCacheEntries || byRel > maxUsenetCheckCacheEntries || byHash > maxUsenetCheckCacheEntries {
		t.Fatalf("cache exceeded bound %d: entries=%d byRel=%d byHash=%d", maxUsenetCheckCacheEntries, entries, byRel, byHash)
	}
}
