package livetv

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// applyXMLTVEpisode copies the episode facts an XMLTV programme carries:
// sub-title, episode-num (xmltv_ns, onscreen and URI systems), <new> and
// date. These are what the export in handler.go writes, so an Ether guide
// round-trips, and what recording rules need to tell episodes apart.
func applyXMLTVEpisode(item *Item, p xmlProgramme) {
	if len(p.SubTitle) > 0 {
		if sub := strings.TrimSpace(p.SubTitle[0].Value); sub != "" && !strings.EqualFold(sub, strings.TrimSpace(item.Title)) {
			item.EpisodeTitle = sub
		}
	}
	for _, n := range p.EpisodeNum {
		system := strings.ToLower(strings.TrimSpace(n.System))
		value := strings.TrimSpace(n.Value)
		if value == "" {
			continue
		}
		switch {
		case system == "xmltv_ns":
			if season, episode, ok := parseXMLTVNS(value); ok && item.Episode == 0 {
				item.Season, item.Episode = season, episode
			}
		case strings.Contains(value, "://"):
			// Ether's export writes a catalogue GUID with its scheme as the
			// system. Only that exact shape is taken as a GUID.
			if scheme, _, ok := strings.Cut(value, "://"); ok && strings.EqualFold(scheme, system) && item.GUID == "" {
				item.GUID = value
			}
		default:
			if season, episode, ok := parseOnscreenEpisode(value); ok && item.Episode == 0 {
				item.Season, item.Episode = season, episode
			}
		}
	}
	if p.New != nil {
		item.Premiere = true
	}
	if ms, ok := parseXMLTVDate(p.Date); ok {
		item.ReleasedMS = ms
	}
}

// parseXMLTVNS reads "season.episode.part" with zero-based numbers and
// optional "/total" suffixes. A missing season is season 0 (specials are
// not distinguished in XMLTV), so only the episode is required.
func parseXMLTVNS(v string) (season, episode int, ok bool) {
	parts := strings.Split(strings.ReplaceAll(v, " ", ""), ".")
	if len(parts) < 2 {
		return 0, 0, false
	}
	num := func(s string) (int, bool) {
		s, _, _ = strings.Cut(s, "/")
		if s == "" {
			return 0, false
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 || n > 9999 {
			return 0, false
		}
		return n + 1, true
	}
	e, ok := num(parts[1])
	if !ok {
		return 0, 0, false
	}
	s, _ := num(parts[0])
	return s, e, true
}

var onscreenEpisode = regexp.MustCompile(`(?i)\bS(\d{1,4})\s*[-.]?\s*E(\d{1,4})\b`)

// parseOnscreenEpisode reads the common onscreen forms "S01E02" and
// "S1 E2". Other free text is ignored.
func parseOnscreenEpisode(v string) (season, episode int, ok bool) {
	m := onscreenEpisode.FindStringSubmatch(v)
	if m == nil {
		return 0, 0, false
	}
	season, _ = strconv.Atoi(m[1])
	episode, _ = strconv.Atoi(m[2])
	return season, episode, episode > 0
}

// parseXMLTVDate reads the leading YYYY, YYYYMM or YYYYMMDD of an XMLTV
// date. Later fields (time, zone) are not needed for a release date.
func parseXMLTVDate(v string) (int64, bool) {
	v = strings.TrimSpace(v)
	digits := 0
	for digits < len(v) && digits < 8 && v[digits] >= '0' && v[digits] <= '9' {
		digits++
	}
	var layout string
	switch {
	case digits >= 8:
		layout, v = "20060102", v[:8]
	case digits >= 6:
		layout, v = "200601", v[:6]
	case digits >= 4:
		layout, v = "2006", v[:4]
	default:
		return 0, false
	}
	t, err := time.Parse(layout, v)
	if err != nil || t.Year() < 1880 {
		return 0, false
	}
	return t.UnixMilli(), true
}
