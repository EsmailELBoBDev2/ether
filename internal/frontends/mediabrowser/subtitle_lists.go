package mediabrowser

import (
	"strconv"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/frontend"
)

// subtitleListShown remembers, per viewer and title, whether the last item
// detail or PlaybackInfo source Ether sent listed subtitle tracks. Jellyfin
// Web posts its subtitle menu's value with PlaybackInfo; on a release that
// was not probed when the detail page rendered, that menu is hidden and
// holds "-1". Ether may probe the release in the background before the
// viewer presses Play, so its own media facts cannot tell whether the client
// ever saw a list: only what was sent can.
type subtitleListShown struct {
	mu sync.Mutex
	m  map[string]subtitleListEntry
}

type subtitleListEntry struct {
	listed bool
	at     time.Time
}

const (
	subtitleListTTL = 12 * time.Hour
	subtitleListCap = 4096
)

func subtitleListKey(v *frontend.Viewer, key frontend.RatingKey) string {
	return strconv.FormatInt(v.ID, 10) + ":" + strconv.FormatInt(int64(key), 10)
}

// note records what a reply listed. sources is the reply's MediaSources.
func (s *subtitleListShown) note(v *frontend.Viewer, key frontend.RatingKey, sources any, now time.Time) {
	if s == nil || v == nil || key == 0 {
		return
	}
	listed := sourcesListSubtitles(sources)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil || len(s.m) >= subtitleListCap {
		s.m = map[string]subtitleListEntry{}
	}
	s.m[subtitleListKey(v, key)] = subtitleListEntry{listed: listed, at: now}
}

// shown reports whether the viewer was recently sent a subtitle list for key.
func (s *subtitleListShown) shown(v *frontend.Viewer, key frontend.RatingKey, now time.Time) bool {
	if s == nil || v == nil || key == 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[subtitleListKey(v, key)]
	return ok && e.listed && now.Sub(e.at) <= subtitleListTTL
}

func sourcesListSubtitles(sources any) bool {
	var list []map[string]any
	switch v := sources.(type) {
	case []any:
		for _, s := range v {
			if m, ok := s.(map[string]any); ok {
				list = append(list, m)
			}
		}
	case []map[string]any:
		list = v
	case map[string]any:
		list = []map[string]any{v}
	}
	for _, src := range list {
		switch streams := src["MediaStreams"].(type) {
		case []any:
			for _, st := range streams {
				if m, ok := st.(map[string]any); ok && m["Type"] == "Subtitle" {
					return true
				}
			}
		case []map[string]any:
			for _, m := range streams {
				if m["Type"] == "Subtitle" {
					return true
				}
			}
		}
	}
	return false
}
