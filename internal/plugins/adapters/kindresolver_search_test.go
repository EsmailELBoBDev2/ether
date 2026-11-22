package adapters

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/newznab"
	"github.com/braedonsaunders/ether/internal/plugins"
	"github.com/braedonsaunders/ether/internal/resolver"
	"github.com/braedonsaunders/ether/internal/store"
)

type stubSearchIndexer struct {
	lastReq core.SearchRequest
	rels    []core.Release
}

func (s *stubSearchIndexer) Name() string { return "stub" }

func (s *stubSearchIndexer) Search(_ context.Context, req core.SearchRequest) ([]core.Release, error) {
	s.lastReq = req
	return append([]core.Release(nil), s.rels...), nil
}

func (s *stubSearchIndexer) FetchNZB(_ context.Context, _ core.Release) ([]byte, error) {
	return nil, core.ErrNotFound
}

func TestKindResolverIndexerSearchRecentCapAndScoring(t *testing.T) {
	ctx := context.Background()

	defCfg := config.Defaults("")
	defCfg.Quality = config.Quality{
		Default: "Main",
		Profiles: []core.QualityProfile{
			{
				Name:             "Main",
				MaxHeight:        2160,
				MinHeight:        720,
				AllowHDR:         true,
				AllowDolbyVision: true,
				QualityOrder:     config.ResolutionGroups(2160, 720),
			},
			{
				Name:         "HDOnly",
				MaxHeight:    1080,
				MinHeight:    720,
				QualityOrder: config.ResolutionGroups(1080, 720),
			},
		},
	}

	base := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	rels := make([]core.Release, 0, 135)
	for i := 0; i < 135; i++ {
		title := fmt.Sprintf("Show.S01E%03d.1080p.WEB-DL.H264-GRP", i+1)
		if i == 134 {
			// Newest item is non-video to verify mixed feed handling.
			title = "Magazine Issue 135 (2026) [EPUB]"
		} else if i == 133 {
			title = "Show.S01E134.2160p.WEB-DL.HEVC-GRP"
		}
		rels = append(rels, core.Release{
			ID:        fmt.Sprintf("rel-%d", i),
			Name:      title,
			Protocol:  core.ProtocolUsenet,
			NZBURL:    fmt.Sprintf("https://idx.example/nzb/%d", i),
			Indexer:   "NZBIndex",
			IndexerID: "nzbidx",
			PubDate:   base.Add(time.Duration(i) * time.Minute),
		})
	}

	idx := &stubSearchIndexer{rels: rels}
	resSvc := resolver.New(idx, nil, nil, defCfg, resolver.Options{})
	kr := NewKindResolver(KindResolverDeps{
		Indexer:  idx,
		Resolver: resSvc,
	})

	// 1. Empty query without category is refused with ValidationError.
	var ve *plugins.ValidationError
	if _, err := kr.ResolveSources(ctx, "plugin.example", core.SourceQuery{
		Kind: "indexer_search",
	}); !errors.As(err, &ve) {
		t.Fatalf("empty query + empty category err = %v, want *plugins.ValidationError", err)
	}

	// 2. Empty query with category succeeds, sorts newest-first, and caps at 100.
	cands, err := kr.ResolveSources(ctx, "plugin.example", core.SourceQuery{
		Kind:     "indexer_search",
		Category: "7000,5000",
	})
	if err != nil {
		t.Fatalf("empty query + category err: %v", err)
	}
	if idx.lastReq.Title != "" || idx.lastReq.Category != "7000,5000" {
		t.Fatalf("forwarded SearchRequest = %+v, want empty Title and Category=7000,5000", idx.lastReq)
	}
	if len(cands) != 100 {
		t.Fatalf("len(cands) = %d, want 100", len(cands))
	}
	// Item 134 (newest) is first and non-video; item 133 (2160p) is second and scored; item 35 is 100th.
	if cands[0].Title != "Magazine Issue 135 (2026) [EPUB]" || cands[0].Score != nil || cands[0].Resolution != 0 {
		t.Fatalf("newest non-video candidate = %+v, want unscored EPUB", cands[0])
	}
	if cands[1].Resolution != 2160 || cands[1].Source != "webdl" || cands[1].Codec != "hevc" || cands[1].Score == nil {
		t.Fatalf("second candidate = %+v, want scored 2160p webdl hevc", cands[1])
	}
	if cands[2].Resolution != 1080 || cands[2].Score == nil || *cands[1].Score <= *cands[2].Score {
		t.Fatalf("Main profile should score 2160p (%v) > 1080p (%v)", cands[1].Score, cands[2].Score)
	}
	if cands[99].Title != "Show.S01E036.1080p.WEB-DL.H264-GRP" {
		t.Fatalf("100th newest candidate = %q, want Show.S01E036...", cands[99].Title)
	}

	// 3. Named profile ("HDOnly") flips ranking so 1080p outranks 2160p.
	hdCands, err := kr.ResolveSources(ctx, "plugin.example", core.SourceQuery{
		Kind:     "indexer_search",
		Category: "5000",
		Profile:  "HDOnly",
	})
	if err != nil {
		t.Fatalf("HDOnly search: %v", err)
	}
	if *hdCands[2].Score <= *hdCands[1].Score {
		t.Fatalf("HDOnly profile should score 1080p (%d) > 2160p (%d)", *hdCands[2].Score, *hdCands[1].Score)
	}

	// 4. Unknown profile is refused with ValidationError on field "profile".
	ve = nil
	if _, err := kr.ResolveSources(ctx, "plugin.example", core.SourceQuery{
		Kind:     "indexer_search",
		Category: "5000",
		Profile:  "DoesNotExist",
	}); !errors.As(err, &ve) || len(ve.Fields) == 0 || ve.Fields[0].Field != "profile" {
		t.Fatalf("unknown profile err = %v (fields=%+v), want ValidationError on profile", err, ve)
	}
}

func TestKindResolverIndexerSearchOptionsAndStatus(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const capsXML = `<?xml version="1.0" encoding="UTF-8"?><caps><searching><search available="yes" supportedParams="q"/></searching></caps>`
	fastSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") == "caps" {
			fmt.Fprint(w, capsXML)
			return
		}
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><item><title>Fast.Track.FLAC</title><guid>f1</guid><link>http://fast.example/1.nzb</link><enclosure url="http://fast.example/1.nzb" length="1024" type="application/x-nzb"/></item></channel></rss>`)
	}))
	defer fastSrv.Close()

	releaseSlow := make(chan struct{})
	slowDone := make(chan struct{})
	slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") == "caps" {
			fmt.Fprint(w, capsXML)
			return
		}
		select {
		case <-releaseSlow:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><item><title>Slow.Track.FLAC</title><guid>s1</guid><link>http://slow.example/1.nzb</link><enclosure url="http://slow.example/1.nzb" length="2048" type="application/x-nzb"/></item></channel></rss>`)
		close(slowDone)
	}))
	defer slowSrv.Close()

	mgr := newznab.NewManager(db, []config.Indexer{
		{ID: "fast-idx", Name: "Fast", URL: fastSrv.URL, APIKey: "k1", Enabled: true},
		{ID: "slow-idx", Name: "Slow", URL: slowSrv.URL, APIKey: "k2", Enabled: true},
	}, newznab.Options{})
	defer mgr.Close()

	kr := NewKindResolver(KindResolverDeps{
		Indexer:  mgr,
		Resolver: resolver.New(mgr, nil, nil, config.Defaults(""), resolver.Options{}),
	})

	// Validation errors for negative numeric options and oversized filter lists.
	neg := -1
	for _, tc := range []struct {
		name  string
		q     core.SourceQuery
		field string
	}{
		{"negative settleMs", core.SourceQuery{Kind: "indexer_search", Query: "x", SettleMS: &neg}, "settleMs"},
		{"negative minResults", core.SourceQuery{Kind: "indexer_search", Query: "x", MinResults: -1}, "minResults"},
		{"negative timeoutMs", core.SourceQuery{Kind: "indexer_search", Query: "x", TimeoutMS: -1}, "timeoutMs"},
		{"too many skipIndexers", core.SourceQuery{Kind: "indexer_search", Query: "x", SkipIndexers: make([]string, plugins.MaxSourceIndexerFilters+1)}, "skipIndexers"},
	} {
		var ve *plugins.ValidationError
		if _, err := kr.ResolveSourcesWithResult(ctx, "plugin.example", tc.q); !errors.As(err, &ve) || len(ve.Fields) == 0 || ve.Fields[0].Field != tc.field {
			t.Fatalf("%s: err = %v, want ValidationError on %q", tc.name, err, tc.field)
		}
	}

	// FirstResults with settleMs=0 returns fast candidate immediately while slow stays in flight.
	zero := 0
	res, err := kr.ResolveSourcesWithResult(ctx, "plugin.example", core.SourceQuery{
		Kind:         "indexer_search",
		Query:        "Track",
		FirstResults: true,
		SettleMS:     &zero,
	})
	if err != nil {
		t.Fatalf("ResolveSourcesWithResult(firstResults): %v", err)
	}
	if len(res.Candidates) != 1 || res.Candidates[0].Title != "Fast.Track.FLAC" {
		t.Fatalf("candidates = %+v, want only Fast.Track.FLAC", res.Candidates)
	}
	byID := map[string]core.IndexerSearchStatus{}
	for _, st := range res.Indexers {
		byID[st.IndexerID] = st
	}
	if byID["fast-idx"].Status != core.IndexerStatusAnswered || byID["slow-idx"].Status != core.IndexerStatusTimedOut {
		t.Fatalf("statuses = %+v, want fast=answered slow=timed_out", res.Indexers)
	}

	// Unblock slow and verify OnlyIndexers reads its result and skips fast.
	close(releaseSlow)
	<-slowDone
	res2, err := kr.ResolveSourcesWithResult(ctx, "plugin.example", core.SourceQuery{
		Kind:         "indexer_search",
		Query:        "Track",
		OnlyIndexers: []string{"Slow"},
	})
	if err != nil {
		t.Fatalf("ResolveSourcesWithResult(onlyIndexers): %v", err)
	}
	if len(res2.Candidates) != 1 || res2.Candidates[0].Title != "Slow.Track.FLAC" {
		t.Fatalf("onlyIndexers candidates = %+v, want Slow.Track.FLAC", res2.Candidates)
	}
	byID2 := map[string]core.IndexerSearchStatus{}
	for _, st := range res2.Indexers {
		byID2[st.IndexerID] = st
	}
	if byID2["fast-idx"].Status != core.IndexerStatusSkipped || byID2["slow-idx"].Status != core.IndexerStatusAnswered {
		t.Fatalf("statuses2 = %+v, want fast=skipped slow=answered", res2.Indexers)
	}

	// Non-indexer_search omits indexers.
	dc, err := kr.ResolveSourcesWithResult(ctx, "plugin.example", core.SourceQuery{
		Kind:     "debrid_check",
		InfoHash: "0123456789abcdef0123456789abcdef01234567",
	})
	if err != nil {
		t.Fatalf("debrid_check: %v", err)
	}
	if len(dc.Indexers) != 0 {
		t.Fatalf("debrid_check indexers = %+v, want omitted", dc.Indexers)
	}
}
