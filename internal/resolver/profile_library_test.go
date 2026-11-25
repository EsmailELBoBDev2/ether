package resolver

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/releaseparse"
)

// TestSelectProfileForTitlePrecedence pins the per-library release
// profiles: a title ranks with the profiles its library chose, a client rule
// still wins, the library's Away profile wins over Default away for a
// remote client, Default away wins over the library's At home profile, and
// an unknown name falls through to the Defaults.
func TestSelectProfileForTitlePrecedence(t *testing.T) {
	base := config.Quality{
		Profiles: []core.QualityProfile{
			{Name: "Default", MaxHeight: 2160}, {Name: "Anime", MaxHeight: 1080},
			{Name: "Away", MaxHeight: 720}, {Name: "Rule", MaxHeight: 480}, {Name: "Travel", MaxHeight: 576},
		},
		Default: "Default",
		Remote:  "Away",
	}
	title := &core.Title{Key: core.EpisodeKey(2, 1, 1), Type: core.TypeEpisode}
	remote := core.ClientCaps{Remote: true}
	tests := []struct {
		name string
		q    config.Quality
		caps core.ClientCaps
		lib  core.LibraryQuality
		want string
	}{
		{name: "the library's profile wins over Default", q: base, lib: core.LibraryQuality{Home: "Anime"}, want: "Anime"},
		{name: "no choice uses Default", q: base, want: "Default"},
		{name: "an unknown name falls through", q: base, lib: core.LibraryQuality{Home: "Nope"}, want: "Default"},
		{name: "a client rule beats the library", q: config.Quality{
			Profiles: base.Profiles, Default: "Default",
			Rules: []config.QualityRule{{Product: "Apple TV", Profile: "Rule"}},
		}, caps: core.ClientCaps{Product: "Apple TV"}, lib: core.LibraryQuality{Home: "Anime"}, want: "Rule"},
		{name: "Default away beats the library's At home profile", q: base, caps: remote, lib: core.LibraryQuality{Home: "Anime"}, want: "Away"},
		{name: "the library's Away profile beats Default away", q: base, caps: remote, lib: core.LibraryQuality{Home: "Anime", Away: "Travel"}, want: "Travel"},
		{name: "with no Default away, away uses the library's profile", q: config.Quality{Profiles: base.Profiles, Default: "Default"}, caps: remote, lib: core.LibraryQuality{Home: "Anime"}, want: "Anime"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SelectProfileForTitle(tc.q, tc.caps, nil, title, tc.lib)
			if got.Name != tc.want {
				t.Fatalf("SelectProfileForTitle = %q, want %q", got.Name, tc.want)
			}
		})
	}
}

type invariantLibraryProfile struct{ name string }

func (p *invariantLibraryProfile) LibraryQualityForTitle(context.Context, *core.Title) core.LibraryQuality {
	return core.LibraryQuality{Home: p.name}
}
func (p *invariantLibraryProfile) PolicyKey(context.Context) string { return p.name }

func TestLibraryProfileEvidenceInvariant(t *testing.T) {
	cfg := testCfg()
	cfg.Quality = config.Quality{Profiles: []core.QualityProfile{
		{Name: "Home", MaxHeight: 2160, AllowHDR: true},
		{Name: "Library", MaxHeight: 720},
	}, Default: "Home"}
	lookup := &invariantLibraryProfile{name: "Library"}
	s := New(&fakeIndexer{}, nil, nil, cfg, Options{LibraryProfiles: lookup})
	defer s.Close()
	ctx := t.Context()
	title := matrixTitle()
	r := rel("1080", "The.Signal.1999.1080p.WEB-DL.x264-GRP", 4000)
	r.Info = releaseparse.Parse(r.Name)
	_, reason, err := s.FeedCandidate(ctx, title, r)
	if err != nil || reason == "" {
		t.Fatalf("feed ignored library: %q %v", reason, err)
	}
	ev, err := s.FeedEvaluation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	oldKey := ev.Key()
	verdicts, err := ev.Evaluate(ctx, title, []core.Release{r})
	ev.Close()
	if err != nil || len(verdicts) != 1 || verdicts[0].Reason == "" {
		t.Fatalf("batch feed ignored library: %+v %v", verdicts, err)
	}
	s.setAvailability(ctx, int64(title.Key), false, time.Hour)
	lookup.name = "Home"
	if known, _ := s.Available(ctx, title.Key); known {
		t.Fatal("old library availability survived profile change")
	}
	if got := s.KnownUnavailableSince(ctx, []core.RatingKey{title.Key}); len(got) != 0 {
		t.Fatalf("old negative policy survived: %v", got)
	}
	ev, err = s.FeedEvaluation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Close()
	if oldKey == ev.Key() {
		t.Fatal("library policy did not invalidate durable feed evidence")
	}
	verdicts, err = ev.Evaluate(ctx, title, []core.Release{r})
	if err != nil || verdicts[0].Reason != "" {
		t.Fatalf("home policy rejected release: %+v %v", verdicts, err)
	}
	for _, title := range []*core.Title{{Type: core.TypeShow}, {Type: core.TypeSeason}, {Key: core.ShowKey(1)}, {Key: core.SeasonKey(1, 1)}} {
		if got := titleMedia(title); got != core.MediaTV {
			t.Fatalf("show media = %q", got)
		}
	}
}
