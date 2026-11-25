package mediabrowser

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
)

// Jellyfin Web treats a line with a Start property as synced and converts
// play time to ticks (ms × 10,000), so synced lines carry Start in ticks and
// plain lines carry no Start key at all.
func TestJellyfinLyricDTO(t *testing.T) {
	synced := jellyfinLyricDTO(frontend.Lyrics{Lines: []frontend.LyricLine{
		{Start: 1500 * time.Millisecond, Text: "Hello world", Words: []core.LyricWord{
			{Start: 1500 * time.Millisecond, End: 2 * time.Second, Text: "Hello"},
			{Start: 2 * time.Second, Text: "world"},
		}},
		{Start: 3 * time.Second, Text: "Second"},
	}})
	b, _ := json.Marshal(synced)
	var got struct {
		Metadata struct{ IsSynced bool }
		Lyrics   []map[string]any
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Metadata.IsSynced || len(got.Lyrics) != 2 {
		t.Fatalf("synced dto = %s", b)
	}
	if got.Lyrics[0]["Start"].(float64) != 15_000_000 || got.Lyrics[1]["Start"].(float64) != 30_000_000 {
		t.Fatalf("start ticks = %v, %v", got.Lyrics[0]["Start"], got.Lyrics[1]["Start"])
	}
	cues := got.Lyrics[0]["Cues"].([]any)
	first, second := cues[0].(map[string]any), cues[1].(map[string]any)
	if first["Position"].(float64) != 0 || first["EndPosition"].(float64) != 5 || first["End"].(float64) != 20_000_000 {
		t.Fatalf("first cue = %v", first)
	}
	if second["Position"].(float64) != 6 || second["EndPosition"].(float64) != 11 || second["End"] != nil {
		t.Fatalf("second cue = %v", second)
	}
	if _, ok := got.Lyrics[1]["Cues"]; ok {
		t.Fatal("a line without word timing got cues")
	}

	plain := jellyfinLyricDTO(frontend.Lyrics{Plain: "One\r\nTwo\n\n"})
	b, _ = json.Marshal(plain)
	got.Lyrics = nil
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Metadata.IsSynced || len(got.Lyrics) != 2 || got.Lyrics[0]["Text"] != "One" || got.Lyrics[1]["Text"] != "Two" {
		t.Fatalf("plain dto = %s", b)
	}
	if _, ok := got.Lyrics[0]["Start"]; ok {
		t.Fatal("a plain line carries Start, so Jellyfin Web would treat it as synced")
	}

	inst := jellyfinLyricDTO(frontend.Lyrics{Instrumental: true})
	if lines := inst["Lyrics"].([]map[string]any); len(lines) != 1 {
		t.Fatalf("instrumental dto = %v", inst)
	}
}
