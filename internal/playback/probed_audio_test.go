package playback

import (
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestAudioProbedAcceptable(t *testing.T) {
	m := &Manager{}
	s := &session{
		title: &core.Title{Language: "en"},
		user:  core.User{AudioLanguages: []string{"en"}},
	}
	profile := core.QualityProfile{Language: "en"}

	if m.probedAudioAcceptable(s, profile, core.MediaInfo{
		Streams: []core.StreamInfo{{Type: "audio", Language: "fr"}},
	}) {
		t.Fatal("expected false when orig matches wanted but probed audio is only fr")
	}

	if !m.probedAudioAcceptable(s, profile, core.MediaInfo{
		Streams: []core.StreamInfo{
			{Type: "audio", Language: "fr"},
			{Type: "audio", Language: "en"},
		},
	}) {
		t.Fatal("expected true when probed audio includes en")
	}

	if !m.probedAudioAcceptable(s, profile, core.MediaInfo{
		Streams: []core.StreamInfo{{Type: "audio", Language: "und"}},
	}) {
		t.Fatal("expected true when probed audio is untagged und")
	}
}
