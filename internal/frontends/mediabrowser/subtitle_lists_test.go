package mediabrowser

import (
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/frontend"
)

// Jellyfin Web posts "SubtitleStreamIndex":"-1" from its hidden subtitle
// menu when the detail page it showed listed no subtitle track. Only a reply
// that listed subtitles makes a later "-1" the viewer's choice; Ether's own
// later probe does not.
func TestSubtitleListShown(t *testing.T) {
	var s subtitleListShown
	v := &frontend.Viewer{ID: 7}
	key := frontend.RatingKey(42)
	now := time.Now()
	if s.shown(v, key, now) {
		t.Fatal("nothing sent yet, but shown")
	}
	unprobed := []any{map[string]any{"MediaStreams": []any{
		map[string]any{"Type": "Video"}, map[string]any{"Type": "Audio"},
	}}}
	s.note(v, key, unprobed, now)
	if s.shown(v, key, now) {
		t.Fatal("a source without subtitles counted as a list")
	}
	probed := []any{map[string]any{"MediaStreams": []map[string]any{
		{"Type": "Video"}, {"Type": "Audio"}, {"Type": "Subtitle", "Language": "eng"},
	}}}
	s.note(v, key, probed, now)
	if !s.shown(v, key, now) {
		t.Fatal("a source listing subtitles was not recorded")
	}
	if s.shown(&frontend.Viewer{ID: 8}, key, now) {
		t.Fatal("another viewer inherited the list")
	}
	if s.shown(v, key, now.Add(subtitleListTTL+time.Minute)) {
		t.Fatal("a stale list still counted")
	}
	// The detail page re-rendered before the probe: back to unlisted.
	s.note(v, key, unprobed, now)
	if s.shown(v, key, now) {
		t.Fatal("the latest reply listed no subtitles")
	}
}
