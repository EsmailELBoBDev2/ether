package resolver

import (
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
	"github.com/braedonsaunders/ether/internal/core"
)

// Client rules may still be scoped to films or episodes.
func TestApplyClientRulesForMediaScopesRules(t *testing.T) {
	q := config.Quality{
		Profiles: []core.QualityProfile{{Name: "Movies", MaxHeight: 2160}, {Name: "Shows", MaxHeight: 1080}},
		Rules: []config.QualityRule{
			{MediaType: core.MediaMovie, Product: "Apple TV", Profile: "Movies", ConvertSurroundAudio: true},
		},
	}
	hasTrueHD := func(caps core.ClientCaps) bool {
		for _, c := range caps.AudioCodecs {
			if c == "truehd" {
				return true
			}
		}
		return false
	}
	if caps := ApplyClientRules(q, appleTVCaps(), nil); !hasTrueHD(caps) {
		t.Fatal("title-neutral capabilities applied a movie-only audio rule")
	}
	if caps, _ := applyClientRules(q, appleTVCaps(), nil, core.MediaMovie, core.LibraryQuality{}); hasTrueHD(caps) {
		t.Fatalf("movie rule did not convert surround audio: %+v", caps.AudioCodecs)
	}
	if caps, _ := applyClientRules(q, appleTVCaps(), nil, core.MediaTV, core.LibraryQuality{}); !hasTrueHD(caps) {
		t.Fatalf("tv should not apply the movie rule: %+v", caps.AudioCodecs)
	}
}

func TestTitleMedia(t *testing.T) {
	tests := []struct {
		name  string
		title *core.Title
		want  string
	}{
		{name: "nil", title: nil, want: core.MediaAny},
		{name: "movie type", title: &core.Title{Type: core.TypeMovie, Key: core.MovieKey(603)}, want: core.MediaMovie},
		{name: "episode type", title: &core.Title{Type: core.TypeEpisode, Key: core.EpisodeKey(1399, 1, 1)}, want: core.MediaTV},
		{name: "movie key only", title: &core.Title{Key: core.MovieKey(603)}, want: core.MediaMovie},
		{name: "episode key only", title: &core.Title{Key: core.EpisodeKey(1399, 1, 1)}, want: core.MediaTV},
		{name: "type wins over key", title: &core.Title{Type: core.TypeEpisode, Key: core.MovieKey(603)}, want: core.MediaTV},
		{name: "show uses the TV profile", title: &core.Title{Type: core.TypeShow, Key: core.ShowKey(1399)}, want: core.MediaTV},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := titleMedia(tc.title); got != tc.want {
				t.Fatalf("titleMedia = %q, want %q", got, tc.want)
			}
		})
	}
}
