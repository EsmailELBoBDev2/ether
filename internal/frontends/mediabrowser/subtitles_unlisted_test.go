package mediabrowser

import (
	"testing"

	"github.com/braedonsaunders/ether/internal/frontend"
)

// Jellyfin Web posts "SubtitleStreamIndex":"-1" from its hidden subtitle
// menu when the detail page listed no subtitle track. Only a probed Auto
// source with subtitle tracks makes that -1 a choice.
func TestSubtitlesUnlisted(t *testing.T) {
	probed := frontend.ReleaseVariant{Info: frontend.MediaInfo{Streams: []frontend.StreamInfo{
		{Type: "video"}, {Type: "audio", Language: "kor"}, {Type: "subtitle", Language: "eng", Codec: "subrip"},
	}}}
	for _, tc := range []struct {
		name     string
		variants []frontend.ReleaseVariant
		want     bool
	}{
		{"no variants: keep the client's value", nil, false},
		{"estimated auto source", []frontend.ReleaseVariant{{Info: frontend.MediaInfo{Estimated: true}}}, true},
		{"probed without subtitles", []frontend.ReleaseVariant{{Info: frontend.MediaInfo{Streams: []frontend.StreamInfo{{Type: "audio"}}}}}, true},
		{"probed with subtitles", []frontend.ReleaseVariant{probed}, false},
	} {
		if got := subtitlesUnlisted(tc.variants); got != tc.want {
			t.Errorf("%s: subtitlesUnlisted = %v, want %v", tc.name, got, tc.want)
		}
	}
}
