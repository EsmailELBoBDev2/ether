package mediabrowser

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/ids"
)

// ticksPerMillisecond converts milliseconds to .NET ticks (100 ns), the unit
// Jellyfin's lyric timings use.
const ticksPerMillisecond = 10_000

// audioLyrics serves Jellyfin's GET /Audio/{itemId}/Lyrics (10.9+) from the
// title's lyrics source, as a LyricDto: Metadata plus Lyrics lines. A synced
// line carries Start (ticks) and, with word timing, Cues; an unsynced line
// carries Text only, because Jellyfin Web treats any line with a Start
// property as synced.
func (i *instance) audioLyrics(w http.ResponseWriter, r *http.Request) {
	v, _, err := i.viewer(r)
	if err != nil || v == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	source, ok := i.host.Experience.(frontend.TitleLyrics)
	if !ok {
		http.NotFound(w, r)
		return
	}
	key, err := ids.ParseRatingKeyGUID(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	lyrics, err := source.TitleLyrics(r.Context(), v, key)
	if errors.Is(err, frontend.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "Unavailable", http.StatusServiceUnavailable)
		return
	}
	jsonReply(w, jellyfinLyricDTO(lyrics))
}

func jellyfinLyricDTO(l frontend.Lyrics) map[string]any {
	lines := make([]map[string]any, 0, len(l.Lines))
	synced := len(l.Lines) > 0
	for _, line := range l.Lines {
		wire := map[string]any{"Text": line.Text, "Start": line.Start.Milliseconds() * ticksPerMillisecond}
		if cues := jellyfinLyricCues(line); len(cues) > 0 {
			wire["Cues"] = cues
		}
		lines = append(lines, wire)
	}
	if !synced {
		for _, text := range strings.Split(strings.ReplaceAll(l.Plain, "\r\n", "\n"), "\n") {
			lines = append(lines, map[string]any{"Text": text})
		}
		for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]["Text"].(string)) == "" {
			lines = lines[:len(lines)-1]
		}
	}
	if len(lines) == 0 && l.Instrumental {
		lines = append(lines, map[string]any{"Text": "♪"})
	}
	return map[string]any{"Metadata": map[string]any{"IsSynced": synced}, "Lyrics": lines}
}

// jellyfinLyricCues maps word timing to Jellyfin cues: each word's character
// range in the line's text and its start (and end) in ticks.
func jellyfinLyricCues(line frontend.LyricLine) []map[string]any {
	if len(line.Words) == 0 {
		return nil
	}
	cues := make([]map[string]any, 0, len(line.Words))
	pos := 0
	for _, word := range line.Words {
		text := word.Text
		at := strings.Index(line.Text[min(pos, len(line.Text)):], text)
		if text == "" || at < 0 {
			return nil
		}
		start := utf8.RuneCountInString(line.Text[:pos+at])
		end := start + utf8.RuneCountInString(text)
		pos += at + len(text)
		cue := map[string]any{"Position": start, "EndPosition": end, "Start": word.Start.Milliseconds() * ticksPerMillisecond}
		if word.End > word.Start {
			cue["End"] = word.End.Milliseconds() * ticksPerMillisecond
		}
		cues = append(cues, cue)
	}
	return cues
}

// lyricsLikely is the cheap HasLyrics hint for an audio item: what Ether
// already knows, never a call to the plugin.
func (i *instance) lyricsLikely(t frontend.Title) bool {
	if t.MBItemType != "Audio" || !t.Key.IsPluginKey() {
		return false
	}
	hint, ok := i.host.Experience.(frontend.TitleLyricsHint)
	return ok && hint.TitleLyricsLikely(context.Background(), t)
}
