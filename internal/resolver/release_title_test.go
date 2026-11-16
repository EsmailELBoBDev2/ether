package resolver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func metadataEpisode() *core.Title {
	return &core.Title{Key: core.EpisodeKey(456, 7, 3), Type: core.TypeEpisode, ShowTitle: "Los Simpson", ShowOriginalTitle: "The Simpsons", ShowYear: 1989,
		ShowIDs: core.IDs{TMDB: 456, IMDB: "tt0096697"}, Season: 7, Episode: 3, Title: "Episodio", Language: "en"}
}

func TestMetadataReleaseTitleIdentity(t *testing.T) {
	for _, tc := range []struct {
		name          string
		ids, reported core.IDs
		err           error
		release, want string
	}{
		{"TMDB", core.IDs{TMDB: 456}, core.IDs{}, nil, "Los Simpsons - Temporada 7 [HDTV][Cap.703][Castellano][www.PCTRELOAD.COM]", ""},
		{"IMDb", core.IDs{IMDB: "tt0096697"}, core.IDs{}, nil, "Los Simpsons S07E03 HDTV Castellano", ""},
		{"different show", core.IDs{TMDB: 999}, core.IDs{}, nil, "Los Simpsons S07E03 HDTV Castellano", "title mismatch"},
		{"ambiguous", core.IDs{}, core.IDs{}, nil, "Los Simpsons S07E03 HDTV Castellano", "title mismatch"},
		{"unavailable", core.IDs{}, core.IDs{}, errors.New("offline"), "Los Simpsons S07E03 HDTV Castellano", "title mismatch"},
		{"metadata conflict", core.IDs{TMDB: 456, IMDB: "tt9999999"}, core.IDs{}, nil, "Los Simpsons S07E03 HDTV Castellano", "title mismatch"},
		{"reported conflict", core.IDs{TMDB: 456}, core.IDs{TMDB: 999}, nil, "Los Simpsons S07E03 HDTV Castellano", "indexer title identifier mismatch"},
		{"wrong episode", core.IDs{TMDB: 456}, core.IDs{}, nil, "Los Simpsons S07E04 HDTV Castellano", "season/episode mismatch"},
		{"wrong season", core.IDs{TMDB: 456}, core.IDs{}, nil, "Los Simpsons S08E03 HDTV Castellano", "season/episode mismatch"},
		{"wrong year", core.IDs{TMDB: 456}, core.IDs{}, nil, "Los Simpsons 2024 S07E03 HDTV Castellano", "series year mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New(&fakeIndexer{}, nil, nil, testCfg(), Options{ReleaseTitleIDs: func(_ context.Context, kind core.MediaType, name string) (core.IDs, error) {
				if kind != core.TypeShow || name != "Los Simpsons" {
					t.Errorf("lookup %s %q", kind, name)
				}
				return tc.ids, tc.err
			}})
			defer s.Close()
			title := metadataEpisode()
			req := searchRequestFor(title)
			r := withParsed(rel("r", tc.release, 1500))
			r.TitleIDs = tc.reported
			s.prepareReleaseTitles(t.Context(), title, req, []core.Release{r})
			profile := SelectProfile(s.quality, webCaps(), nil)
			got := s.rejectReason(r, title, req, profile, s.resolverCfg(), webCaps(), nil, false)
			if (tc.want == "" && got != "") || (tc.want != "" && !strings.HasPrefix(got, tc.want)) {
				t.Fatalf("rejection=%q, want %q", got, tc.want)
			}
			if r.TitleIDs != tc.reported {
				t.Fatal("metadata overwrote reported IDs")
			}
		})
	}
}

func TestMetadataReleaseTitleCache(t *testing.T) {
	now := time.Unix(1800000000, 0)
	calls := 0
	s := New(&fakeIndexer{}, nil, nil, testCfg(), Options{Now: func() time.Time { return now }, ReleaseTitleIDs: func(context.Context, core.MediaType, string) (core.IDs, error) {
		calls++
		return core.IDs{TMDB: 456}, nil
	}})
	defer s.Close()
	title := metadataEpisode()
	req := searchRequestFor(title)
	r := withParsed(rel("r", "Los Simpsons S07E03 HDTV Castellano", 1500))
	for range 2 {
		s.prepareReleaseTitles(t.Context(), title, req, []core.Release{r, r})
	}
	if calls != 1 || !s.metadataTitleMatches(r, req) {
		t.Fatalf("calls=%d verified=%v", calls, s.metadataTitleMatches(r, req))
	}
	other := req
	other.IDs = core.IDs{TMDB: 999}
	if s.metadataTitleMatches(r, other) {
		t.Fatal("cached mapping accepted for a different show")
	}
	now = now.Add(25 * time.Hour)
	if s.metadataTitleMatches(r, req) {
		t.Fatal("expired mapping accepted")
	}
	s.prepareReleaseTitles(t.Context(), title, req, []core.Release{r})
	if calls != 2 {
		t.Fatalf("expired lookup calls=%d", calls)
	}
	cfg := testCfg()
	cfg.Catalog.Language = "fr-FR"
	s.UpdateConfig(cfg)
	if s.metadataTitleMatches(r, req) {
		t.Fatal("cached mapping reused across languages")
	}
	s.prepareReleaseTitles(t.Context(), title, req, []core.Release{r})
	if calls != 3 {
		t.Fatalf("language lookup calls=%d", calls)
	}
	movie := core.SearchRequest{Type: core.TypeMovie, Title: "Unrelated", IDs: req.IDs}
	if s.metadataTitleMatches(r, movie) {
		t.Fatal("TV mapping reused in movie namespace")
	}
	s.prepareReleaseTitles(t.Context(), title, movie, []core.Release{r})
	if calls != 4 {
		t.Fatalf("movie namespace lookup calls=%d", calls)
	}
}

func TestMetadataReleaseTitleLookupBudgetAndKnownTitles(t *testing.T) {
	calls := 0
	s := New(&fakeIndexer{}, nil, nil, testCfg(), Options{ReleaseTitleIDs: func(context.Context, core.MediaType, string) (core.IDs, error) { calls++; return core.IDs{}, nil }})
	defer s.Close()
	title := metadataEpisode()
	title.ShowAlternativeTitles = []string{"Familia Simpson"}
	req := searchRequestFor(title)
	known := withParsed(rel("known", "Los Simpson S07E03 HDTV", 1500))
	alias := withParsed(rel("alias", "Familia Simpson S07E03 HDTV", 1500))
	verified := withParsed(rel("id", "Unfamiliar S07E03 HDTV", 1500))
	verified.TitleIDs = req.IDs
	conflict := verified
	conflict.TitleIDs = core.IDs{TMDB: 999}
	s.prepareReleaseTitles(t.Context(), title, req, []core.Release{known, alias, verified, conflict})
	if calls != 0 {
		t.Fatalf("known/conflicting results made %d lookups", calls)
	}
	var results []core.Release
	for _, name := range []string{"Alpha", "Bravo", "Charlie", "Delta", "Echo", "Foxtrot"} {
		results = append(results, withParsed(rel(name, name+" S07E03 HDTV", 1500)))
	}
	s.prepareReleaseTitles(t.Context(), title, req, results)
	if calls != 4 {
		t.Fatalf("lookup budget=%d, want 4", calls)
	}
}

func TestMetadataReleaseTitleConcurrentLookups(t *testing.T) {
	var calls atomic.Int32
	started, finish := make(chan struct{}), make(chan struct{})
	s := New(&fakeIndexer{}, nil, nil, testCfg(), Options{ReleaseTitleIDs: func(ctx context.Context, _ core.MediaType, _ string) (core.IDs, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-finish:
			return core.IDs{TMDB: 456}, nil
		case <-ctx.Done():
			return core.IDs{}, ctx.Err()
		}
	}})
	defer s.Close()
	title := metadataEpisode()
	req := searchRequestFor(title)
	r := withParsed(rel("r", "Los Simpsons S07E03 HDTV", 1500))
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { s.prepareReleaseTitles(t.Context(), title, req, []core.Release{r}) })
	}
	<-started
	close(finish)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("concurrent calls=%d", calls.Load())
	}
}

func TestMetadataReleaseTitleRanking(t *testing.T) {
	cfg := testCfg()
	s := New(&fakeIndexer{}, nil, nil, cfg, Options{ReleaseTitleIDs: func(_ context.Context, kind core.MediaType, name string) (core.IDs, error) {
		if kind != core.TypeMovie || name != "Los Viajeros" {
			t.Errorf("lookup %s %q", kind, name)
		}
		return core.IDs{TMDB: 123}, nil
	}})
	defer s.Close()
	title := &core.Title{Key: core.MovieKey(123), Type: core.TypeMovie, Title: "The Travelers", IDs: core.IDs{TMDB: 123}, Year: 2016, Language: "en", Runtime: 90 * time.Minute}
	r := withParsed(rel("r", "Los Viajeros 2016 1080p WEB-DL H264 AAC", 3000))
	profile := SelectProfile(cfg.Quality, webCaps(), nil)
	candidates, _, err := s.rankCandidates(t.Context(), core.ResolveRequest{Title: title, Client: webCaps()}, searchRequestFor(title), profile, []core.Release{r}, nil, "")
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates=%d err=%v", len(candidates), err)
	}
	if candidates[0].rel.TitleIDs != (core.IDs{}) {
		t.Fatal("metadata became reported IDs")
	}
}

func TestMetadataReleaseTitlePreservesRegion(t *testing.T) {
	s := New(&fakeIndexer{}, nil, nil, testCfg(), Options{ReleaseTitleIDs: func(context.Context, core.MediaType, string) (core.IDs, error) { return core.IDs{TMDB: 456}, nil }})
	defer s.Close()
	title := metadataEpisode()
	title.Countries = []string{"US"}
	req := searchRequestFor(title)
	r := withParsed(rel("r", "Los Simpsons UK S07E03 HDTV", 1500))
	s.prepareReleaseTitles(t.Context(), title, req, []core.Release{r})
	profile := SelectProfile(s.quality, webCaps(), nil)
	if got := s.rejectReason(r, title, req, profile, s.resolverCfg(), webCaps(), nil, false); !strings.HasPrefix(got, "title mismatch") {
		t.Fatalf("regional conflict accepted: %q", got)
	}
}

func TestMetadataReleaseTitleCacheMissAndErrors(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			now := time.Unix(1800000000, 0)
			calls := 0
			s := New(&fakeIndexer{}, nil, nil, testCfg(), Options{Now: func() time.Time { return now }, ReleaseTitleIDs: func(context.Context, core.MediaType, string) (core.IDs, error) {
				calls++
				if fail {
					return core.IDs{}, errors.New("offline")
				}
				return core.IDs{}, nil
			}})
			defer s.Close()
			title := metadataEpisode()
			req := searchRequestFor(title)
			r := withParsed(rel("r", "Los Simpsons S07E03 HDTV", 1500))
			for range 2 {
				s.prepareReleaseTitles(t.Context(), title, req, []core.Release{r})
			}
			want := 1
			if fail {
				want = 2
			}
			if calls != want {
				t.Fatalf("calls=%d want=%d", calls, want)
			}
			now = now.Add(11 * time.Minute)
			s.prepareReleaseTitles(t.Context(), title, req, []core.Release{r})
			if calls != want+1 {
				t.Fatalf("negative cache did not expire: calls=%d", calls)
			}
		})
	}
}

func TestMetadataReleaseTitleCacheCapacity(t *testing.T) {
	now := time.Unix(1800000000, 0)
	s := New(&fakeIndexer{}, nil, nil, testCfg(), Options{Now: func() time.Time { return now }, ReleaseTitleIDs: func(context.Context, core.MediaType, string) (core.IDs, error) { return core.IDs{TMDB: 456}, nil }})
	defer s.Close()
	s.releaseTitles.seen = map[releaseTitleKey]releaseTitleVerdict{}
	for i := range maxReleaseTitleEntries {
		s.releaseTitles.seen[releaseTitleKey{title: fmt.Sprint(i)}] = releaseTitleVerdict{expires: now.Add(time.Hour)}
	}
	title := metadataEpisode()
	req := searchRequestFor(title)
	r := withParsed(rel("r", "Los Simpsons S07E03 HDTV", 1500))
	s.prepareReleaseTitles(t.Context(), title, req, []core.Release{r})
	if len(s.releaseTitles.seen) != maxReleaseTitleEntries || !s.metadataTitleMatches(r, req) {
		t.Fatalf("cache size=%d verified=%v", len(s.releaseTitles.seen), s.metadataTitleMatches(r, req))
	}
}

func TestMetadataReleaseTitleCancellation(t *testing.T) {
	calls := 0
	s := New(&fakeIndexer{}, nil, nil, testCfg(), Options{ReleaseTitleIDs: func(ctx context.Context, _ core.MediaType, _ string) (core.IDs, error) {
		calls++
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 3*time.Second {
			t.Error("lookup is not bounded to three seconds")
		}
		return core.IDs{TMDB: 456}, nil
	}})
	defer s.Close()
	title := metadataEpisode()
	req := searchRequestFor(title)
	r := withParsed(rel("r", "Los Simpsons S07E03 HDTV", 1500))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s.prepareReleaseTitles(ctx, title, req, []core.Release{r})
	if calls != 0 || s.metadataTitleMatches(r, req) {
		t.Fatal("canceled lookup cached identity")
	}
	s.prepareReleaseTitles(t.Context(), title, req, []core.Release{r})
	if calls != 1 || !s.metadataTitleMatches(r, req) {
		t.Fatal("live lookup did not recover")
	}
}
