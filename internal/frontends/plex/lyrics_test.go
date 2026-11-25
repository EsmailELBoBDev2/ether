package plex

import (
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/frontends/plex/model"
)

// Plexamp reads simple LRC only: one [mm:ss.xx] stamp per line.
func TestLyricsTextIsSimpleLRC(t *testing.T) {
	got := lyricsText(frontend.Lyrics{Lines: []frontend.LyricLine{
		{Start: 1500 * time.Millisecond, Text: "Hello"},
		{Start: 61*time.Second + 230*time.Millisecond, Text: "Later"},
	}})
	if want := "[00:01.50]Hello\n[01:01.23]Later\n"; got != want {
		t.Fatalf("lrc = %q, want %q", got, want)
	}
	if got := lyricsText(frontend.Lyrics{Plain: "One\r\nTwo\n"}); got != "One\nTwo\n" {
		t.Fatalf("plain = %q", got)
	}
	if got := lyricsText(frontend.Lyrics{}); got != "" {
		t.Fatalf("empty = %q", got)
	}
}

// The lyric stream rides on a Part that lists its streams, keyed by the
// track, and its id never collides with an issued media stream id.
func TestAddLyricStream(t *testing.T) {
	key := core.PluginKey(42)
	media := []model.Media{
		{Part: []model.Part{{Stream: []model.Stream{{ID: 1387133930, StreamType: 2}}}}},
		{Part: []model.Part{{}}}, // a list row: no streams listed, nothing added
	}
	addLyricStream(media, key)
	streams := media[0].Part[0].Stream
	if len(streams) != 2 {
		t.Fatalf("streams = %+v", streams)
	}
	lyric := streams[1]
	if lyric.StreamType != 4 || lyric.Codec != "lrc" || lyric.Timed != "1" || lyric.ID != int64(key) || lyric.Key == "" {
		t.Fatalf("lyric stream = %+v", lyric)
	}
	if len(media[1].Part[0].Stream) != 0 {
		t.Fatal("a part without streams got a lyric stream")
	}
	if !isLyricStreamID(lyric.ID) || isLyricStreamID(1387133930) {
		t.Fatal("lyric and media stream ids are not told apart")
	}
}
