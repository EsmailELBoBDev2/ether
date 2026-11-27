package livetv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/braedonsaunders/ether/internal/livetv/dvr"
)

// dvrRelPath names a recording inside the recordings folder, in the
// layout media servers and people both read:
//
//	TV/<Show>/Season 01/<Show> - S01E02 - <Episode> (2026-11-27 2100).ts
//	TV/<Show>/<Show> - 2026-11-27 2100 - <Episode>.ts
//	Movies/<Title> (<Year>)/<Title> (<Year>).ts
func dvrRelPath(p dvr.Programme) string {
	stamp := p.Start.Local().Format("2006-01-02 1504")
	if p.IsMovie() {
		title := dvrSafeName(p.Title, "Movie")
		if y := p.Year(); y > 0 {
			title = fmt.Sprintf("%s (%d)", title, y)
		}
		return filepath.Join("Movies", title, title+".ts")
	}
	show := dvrSafeName(p.Show(), "Recording")
	episode := dvrSafeName(p.EpisodeTitle, "")
	if p.Episode > 0 {
		name := fmt.Sprintf("%s - S%02dE%02d", show, p.Season, p.Episode)
		if episode != "" {
			name += " - " + episode
		}
		return filepath.Join("TV", show, fmt.Sprintf("Season %02d", p.Season), name+" ("+stamp+").ts")
	}
	name := show + " - " + stamp
	if episode != "" && !strings.EqualFold(episode, show) {
		name += " - " + episode
	}
	return filepath.Join("TV", show, name+".ts")
}

// dvrSafeName keeps a title usable as one path element on Linux, macOS,
// Windows shares and NAS file systems.
func dvrSafeName(s, fallback string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f || strings.ContainsRune(`/\:*?"<>|`, r):
			space = true
		case unicode.IsSpace(r):
			space = true
		default:
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), ". ")
	const limit = 120
	if len(out) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(out[cut]) {
			cut--
		}
		out = strings.TrimRight(out[:cut], ". ")
	}
	if out == "" {
		return fallback
	}
	return out
}

// dvrUniquePath returns path, or path with a counter when a file (or its
// partial) is already there.
func dvrUniquePath(path string) string {
	exists := func(p string) bool {
		_, err := os.Lstat(p)
		_, perr := os.Lstat(p + dvrPartialSuffix)
		return err == nil || perr == nil
	}
	if !exists(path) {
		return path
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for i := 2; i < 1000; i++ {
		p := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if !exists(p) {
			return p
		}
	}
	return base + " (" + randomID()[:8] + ")" + ext
}

// dvrInside reports whether path is inside dir, so a stored path can never
// make the recorder or the cleaner touch other files.
func dvrInside(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." && !filepath.IsAbs(rel)
}
