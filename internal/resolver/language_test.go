package resolver

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestLanguageRankingRequiresExplicitPreference(t *testing.T) {
	s := New(&fakeIndexer{}, nil, nil, testCfg(), Options{Now: func() time.Time { return time.Unix(1800000000, 0) }})
	defer s.Close()
	r := withParsed(rel("language", "The.Signal.1999.1080p.WEB-DL.H264-GRP", 4000))
	score := func(lang string, prefs ...string) float64 {
		r.Info.Languages = []string{lang}
		return s.languageScore(r, core.QualityProfile{PreferredLanguages: prefs}, matrixTitle())
	}
	for _, lang := range []string{"french", "german", "japanese", "multi"} {
		if score(lang) != score("english") {
			t.Errorf("default language penalty for %s", lang)
		}
	}
	if score("french", "french") <= score("english", "french") {
		t.Fatal("explicit French preference did not outrank English")
	}
	if score("english", "english") <= score("french", "english") {
		t.Fatal("explicit English preference ignored")
	}
}

// A profile Language is Radarr's hard filter: a specific language rejects
// releases whose audio isn't in it, Original keeps today's behaviour and
// Any accepts everything. The anime case: on a Japanese title an English
// profile rejects the Japanese release however good its quality is.
func TestProfileLanguageGate(t *testing.T) {
	anime := &core.Title{Language: "ja"}
	english := core.QualityProfile{Language: "en"}
	info := func(langs ...string) core.ReleaseInfo { return core.ReleaseInfo{Languages: langs} }

	for _, tt := range []struct {
		name    string
		profile core.QualityProfile
		title   *core.Title
		info    core.ReleaseInfo
		reject  bool
	}{
		{"4K Japanese on an English profile", english, anime, info("japanese"), true},
		{"1080p English dub", english, anime, info("english"), false},
		{"multi audio", english, anime, info("multi"), false},
		{"dual audio", english, anime, info("dual"), false},
		{"untagged counts as the original", english, anime, info(), true},
		{"untagged, original unknown", english, &core.Title{}, info(), false},
		{"untagged English original", english, &core.Title{Language: "en"}, info(), false},
		{"no language set keeps today", core.QualityProfile{}, anime, info("japanese"), false},
		{"Original keeps today", core.QualityProfile{Language: "original"}, anime, info("french"), false},
		{"Any accepts everything", core.QualityProfile{Language: "any"}, anime, info("french"), false},
		{"unrecognised language keeps today", core.QualityProfile{Language: "klingon"}, anime, info("japanese"), false},
		{"regional variant of the profile language", core.QualityProfile{Language: "es"}, anime, info("spanish-latino"), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reason := profileLanguageRejection(tt.info, tt.profile, tt.title)
			if (reason != "") != tt.reject {
				t.Fatalf("reason=%q reject=%v", reason, tt.reject)
			}
		})
	}

	// A dub into the profile's language is what the language asks for, so
	// the dub-mismatch check must not reject it; an explicit pick (a pin)
	// bypasses the gate.
	r := core.Release{Name: "Primetime (2026) 1080p WEB-DL [Eng Dub]", Info: core.ReleaseInfo{Source: core.SourceWEBDL}}
	if reason := sourceSuitability(r, anime, english, false); reason != "" {
		t.Fatalf("English dub on an English profile: %q", reason)
	}
	r = core.Release{Name: "Primetime (2026) 2160p WEB-DL [Jpn Dub]", Info: core.ReleaseInfo{Source: core.SourceWEBDL}}
	if reason := sourceSuitability(r, anime, english, true); reason != "" {
		t.Fatalf("explicit pick bypasses the gate: %q", reason)
	}

	// Accepted languages follow the mode: Original adds the title's own
	// language, a specific language accepts only itself, Any all.
	if got := acceptedAudio(core.QualityProfile{}, nil, "en", anime); len(got) != 2 || got[1] != "ja" {
		t.Fatalf("original accepted=%v", got)
	}
	if got := acceptedAudio(english, nil, "en", anime); len(got) != 1 || got[0] != "english" {
		t.Fatalf("specific accepted=%v", got)
	}
	if got := acceptedAudio(core.QualityProfile{Language: "any"}, nil, "en", anime); got != nil {
		t.Fatalf("any accepted=%v", got)
	}
	if got := wantedAudio(english, nil, "fr"); len(got) != 1 || got[0] != "english" {
		t.Fatalf("specific wanted=%v", got)
	}

	// #674: a viewer with PreferOriginal and no AudioLanguages must accept the
	// title's original language without falling back to the server language.
	prefOrig := &core.User{ID: 1, PreferOriginal: true}
	if got := wantedAudio(core.QualityProfile{}, prefOrig, "en-US"); len(got) != 0 {
		t.Fatalf("preferOriginal wanted=%v, want empty (no serverLang fallback)", got)
	}
	if got := acceptedAudio(core.QualityProfile{}, prefOrig, "en-US", anime); len(got) != 1 || got[0] != "ja" {
		t.Fatalf("preferOriginal accepted=%v, want [ja]", got)
	}
}

// #674: ExcludeReleases drops content-equivalent reposts (same release name
// across indexers), while ID-only Exclude still lets another indexer's repost
// serve transport/article failover.
func TestResolveExcludeReleasesSkipsReposts(t *testing.T) {
	repost1 := rel("idx1-repost", "The.Signal.1999.1080p.WEB-DL.H264-DUP", 4000)
	repost1.Indexer = "idx1"
	repost2 := rel("idx2-repost", "The.Signal.1999.1080p.WEB-DL.H264-DUP", 4000)
	repost2.Indexer = "idx2"
	other := rel("idx1-other", "The.Signal.1999.1080p.WEB-DL.H264-ALT", 3500)
	other.Indexer = "idx1"

	idx := &fakeIndexer{releases: []core.Release{repost1, repost2, other}}
	s := New(idx, nil, nil, testCfg(), Options{Now: func() time.Time { return time.Unix(1800000000, 0) }})
	defer s.Close()

	// ID-only Exclude still picks idx2-repost for broken-upload failover.
	res, err := s.Resolve(context.Background(), core.ResolveRequest{
		Title:   matrixTitle(),
		Exclude: []string{repost1.ID},
	})
	if err != nil || res.Release.ID != repost2.ID {
		t.Fatalf("ID-only Exclude got (%+v, %v), want %s", res, err, repost2.ID)
	}

	// ExcludeReleases excludes both repost1 and idx2-repost so failover advances to other.
	res, err = s.Resolve(context.Background(), core.ResolveRequest{
		Title:           matrixTitle(),
		Exclude:         []string{repost1.ID},
		ExcludeReleases: []core.Release{repost1},
	})
	if err != nil || res.Release.ID != other.ID {
		t.Fatalf("ExcludeReleases got (%+v, %v), want %s", res, err, other.ID)
	}
}

func TestViewerSubtitlesScoringAndRemoval(t *testing.T) {
	viewer := &core.User{ID: 7, SubtitleLanguages: []string{"ar"}}
	plain := rel("plain", "The.Signal.1999.1080p.WEB-DL.H264-AAA", 4000)
	withSubs := rel("subs", "The.Signal.1999.1080p.WEB-DL.H264-BBB", 3800)
	withSubs.IndexerSubtitles = []string{"english", "arabic"}

	// When the profile scores FormatViewerSubtitles, the release listing the
	// viewer's subtitle language wins the tie over the slightly larger release.
	idx := &fakeIndexer{releases: []core.Release{plain, withSubs}}
	cfg := testCfg()
	s := New(idx, nil, nil, cfg, Options{Now: func() time.Time { return time.Unix(1800000000, 0) }})
	defer s.Close()

	res, err := s.Resolve(context.Background(), core.ResolveRequest{Title: matrixTitle(), User: viewer})
	if err != nil || res.Release.ID != withSubs.ID {
		t.Fatalf("scored viewer-subtitles got (%+v, %v), want %s", res, err, withSubs.ID)
	}

	// Removing FormatViewerSubtitles from CustomFormatScores (as the console
	// Remove button does) must genuinely turn it off.
	cfgNoSubs := testCfg()
	for i := range cfgNoSubs.Quality.Profiles {
		delete(cfgNoSubs.Quality.Profiles[i].CustomFormatScores, core.FormatViewerSubtitles)
	}
	sOff := New(idx, nil, nil, cfgNoSubs, Options{Now: func() time.Time { return time.Unix(1800000000, 0) }})
	defer sOff.Close()

	res, err = sOff.Resolve(context.Background(), core.ResolveRequest{Title: matrixTitle(), User: viewer})
	if err != nil || res.Release.ID != plain.ID {
		t.Fatalf("removed viewer-subtitles got (%+v, %v), want %s", res, err, plain.ID)
	}

	// Default score (15) must not force a video transcode (-20) over DirectPlay.
	direct := rel("direct", "The.Signal.1999.1080p.WEB-DL.H264-AAA", 4000)
	transcodeWithSubs := rel("xvid-subs", "The.Signal.1999.1080p.WEB-DL.XviD-BBB", 4000)
	transcodeWithSubs.IndexerSubtitles = []string{"arabic"}
	idx2 := &fakeIndexer{releases: []core.Release{transcodeWithSubs, direct}}
	s2 := New(idx2, nil, nil, testCfg(), Options{Now: func() time.Time { return time.Unix(1800000000, 0) }})
	defer s2.Close()

	res, err = s2.Resolve(context.Background(), core.ResolveRequest{
		Title:  matrixTitle(),
		User:   viewer,
		Client: core.ClientCaps{VideoCodecs: []string{"h264"}, AudioCodecs: []string{"aac"}},
	})
	if err != nil || res.Release.ID != direct.ID {
		t.Fatalf("transcode with subs beat DirectPlay: got (%+v, %v), want %s", res, err, direct.ID)
	}
}
