package vela

import (
	"context"
	"errors"
	"net/http"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
)

func (i *instance) lyricsAvailable() bool {
	source, ok := i.host.Experience.(frontend.TitleLyrics)
	return ok && source.LyricsAvailable(context.Background())
}

// titleLyrics serves GET /vela/v0/titles/{id}/lyrics: timed lines
// ({startMs, text}) when the source has them, else plain text; 404 when the
// title has none.
func (i *instance) titleLyrics(w http.ResponseWriter, r *http.Request) {
	v, _, err := i.viewer(r)
	if err != nil {
		fail(w, err, r)
		return
	}
	source, ok := i.host.Experience.(frontend.TitleLyrics)
	if !ok {
		i.unavailable(w, r)
		return
	}
	k, err := key(r)
	if err != nil {
		problem(w, 404, "not-found", "not_found")
		return
	}
	lyrics, err := source.TitleLyrics(r.Context(), v, k)
	if errors.Is(err, core.ErrNotFound) {
		problem(w, 404, "not-found", "not_found")
		return
	}
	if err != nil {
		fail(w, err, r)
		return
	}
	lines := make([]object, len(lyrics.Lines))
	for n, line := range lyrics.Lines {
		lines[n] = object{"startMs": line.Start.Milliseconds(), "text": line.Text}
	}
	out := object{"lines": lines, "instrumental": lyrics.Instrumental}
	if lyrics.Plain != "" {
		out["plain"] = lyrics.Plain
	}
	if lyrics.Source != "" {
		out["source"] = lyrics.Source
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	send(w, 200, out)
}
