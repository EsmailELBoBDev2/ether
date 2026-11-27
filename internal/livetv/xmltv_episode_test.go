package livetv

import (
	"strings"
	"testing"
	"time"
)

func TestParseXMLTVKeepsEpisodeFacts(t *testing.T) {
	guide := `<?xml version="1.0"?><tv>
<channel id="c"><display-name>C</display-name></channel>
<programme channel="c" start="20261127200000 +0000" stop="20261127210000 +0000">
  <title>Show</title><sub-title>Pilot</sub-title>
  <episode-num system="xmltv_ns">1.4/10.0/1</episode-num>
  <episode-num system="plex">plex://episode/abc</episode-num>
  <new/><date>20261120</date>
</programme>
<programme channel="c" start="20261127210000 +0000" stop="20261127220000 +0000">
  <title>Other</title><sub-title>Other</sub-title>
  <episode-num system="onscreen">S03E07</episode-num>
  <episode-num system="dd_progid">EP123.0004</episode-num>
  <date>1999</date>
</programme>
<programme channel="c" start="20261127220000 +0000" stop="20261127230000 +0000">
  <title>Bare</title><episode-num system="xmltv_ns">..</episode-num>
</programme>
</tv>`
	g, _, err := parseXMLTV(strings.NewReader(guide), guideFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Programmes) != 3 {
		t.Fatalf("got %d programmes", len(g.Programmes))
	}
	first := g.Programmes[0].Item
	if first.EpisodeTitle != "Pilot" || first.Season != 2 || first.Episode != 5 || !first.Premiere || first.GUID != "plex://episode/abc" {
		t.Fatalf("first = %+v", first)
	}
	if got := time.UnixMilli(first.ReleasedMS).UTC().Format("2006-01-02"); got != "2026-11-20" {
		t.Fatalf("release = %s", got)
	}
	second := g.Programmes[1].Item
	if second.EpisodeTitle != "" || second.Season != 3 || second.Episode != 7 || second.Premiere || second.GUID != "" {
		t.Fatalf("second = %+v", second)
	}
	if got := time.UnixMilli(second.ReleasedMS).UTC().Year(); got != 1999 {
		t.Fatalf("year = %d", got)
	}
	bare := g.Programmes[2].Item
	if bare.Season != 0 || bare.Episode != 0 || bare.ReleasedMS != 0 {
		t.Fatalf("bare = %+v", bare)
	}
}

func TestParseXMLTVNS(t *testing.T) {
	for _, c := range []struct {
		in   string
		s, e int
		ok   bool
	}{
		{"0.0.", 1, 1, true}, {".3.", 0, 4, true}, {"2 . 9 / 12 .", 3, 10, true}, {"1..", 0, 0, false}, {"x.y.", 0, 0, false}, {"5", 0, 0, false},
	} {
		s, e, ok := parseXMLTVNS(c.in)
		if s != c.s || e != c.e || ok != c.ok {
			t.Errorf("%q = %d,%d,%v want %d,%d,%v", c.in, s, e, ok, c.s, c.e, c.ok)
		}
	}
}
