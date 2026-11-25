package plex

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/frontends/plex/model"
)

// lyricsProvider names Ether as the lyric stream's source.
const lyricsProvider = "tv.ether.lyrics"

// A lyric stream's id is its track's rating key. Plugin keys are far above
// the ids issued to media streams, so /library/streams/{id} tells the two
// apart without a binding: the lyrics are read when the app asks.
func lyricStreamID(key frontend.RatingKey) int64 { return int64(key) }

func isLyricStreamID(id int64) bool { return frontend.RatingKey(id).IsPluginKey() }

// lyricsLikely is the cheap hint: what Ether already knows about the track's
// lyrics, never a call to the plugin.
func (s *Server) lyricsLikely(ctx context.Context, t *frontend.Title) bool {
	if t == nil || !t.Key.IsPluginKey() {
		return false
	}
	hint, ok := s.exp().(frontend.TitleLyricsHint)
	return ok && hint.TitleLyricsLikely(ctx, *t)
}

// addLyricStream lists a lyric stream (streamType 4, as PMS writes a
// LyricFind or sidecar .lrc track) on each Part that already lists its
// streams, so Plex apps offer lyrics for the track.
func addLyricStream(media []model.Media, key frontend.RatingKey) {
	for m := range media {
		for p := range media[m].Part {
			part := &media[m].Part[p]
			if len(part.Stream) == 0 {
				continue
			}
			part.Stream = append(part.Stream, model.Stream{
				ID:           lyricStreamID(key),
				StreamType:   4,
				Codec:        "lrc",
				Format:       "lrc",
				Key:          fmt.Sprintf("/library/streams/%d", lyricStreamID(key)),
				Timed:        "1",
				Provider:     lyricsProvider,
				DisplayTitle: "Lyrics",
			})
		}
	}
}

// serveLyricStream answers /library/streams/{id} for a lyric stream: the
// track's lyrics as LRC (synced lines with [mm:ss.xx] stamps), or plain text
// when the source has no timing.
func (s *Server) serveLyricStream(w http.ResponseWriter, r *http.Request, id int64) {
	pr := FromContext(r.Context())
	if pr == nil || pr.User == nil {
		writeHTTPError(w, http.StatusUnauthorized)
		return
	}
	source, ok := s.exp().(frontend.TitleLyrics)
	if !ok {
		writeHTTPError(w, http.StatusNotFound)
		return
	}
	lyrics, err := source.TitleLyrics(r.Context(), pr.User, frontend.RatingKey(id))
	if errors.Is(err, frontend.ErrNotFound) {
		writeHTTPError(w, http.StatusNotFound)
		return
	}
	if err != nil {
		writeHTTPError(w, http.StatusServiceUnavailable)
		return
	}
	body := lyricsText(lyrics)
	if body == "" {
		writeHTTPError(w, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write([]byte(body))
}

// lyricsText renders lyrics as simple LRC, the only form Plexamp reads:
// one [mm:ss.xx] stamp per line, no word timing. Plain lyrics keep their
// lines unstamped.
func lyricsText(l frontend.Lyrics) string {
	var b strings.Builder
	if len(l.Lines) > 0 {
		for _, line := range l.Lines {
			cs := line.Start.Milliseconds() / 10
			fmt.Fprintf(&b, "[%02d:%02d.%02d]%s\n", cs/6000, (cs/100)%60, cs%100, line.Text)
		}
		return b.String()
	}
	plain := strings.TrimSpace(strings.ReplaceAll(l.Plain, "\r\n", "\n"))
	if plain != "" {
		return plain + "\n"
	}
	if l.Instrumental {
		return "♪\n"
	}
	return ""
}
