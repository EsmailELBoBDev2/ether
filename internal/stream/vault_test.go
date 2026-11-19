package stream

import (
	"bytes"
	"io"
	"testing"

	"github.com/braedonsaunders/ether/internal/nzb"
	"github.com/braedonsaunders/ether/internal/vault"
)

func TestVaultEpisodeReplayAndStaleExtent(t *testing.T) {
	f := newFakeFetcher()
	a, b := mkvBytes(1, 20000), mkvBytes(2, 40000)
	xml := post(t, f, 2048, postFile{name: "Show.S01E01.mkv", data: a}, postFile{name: "Show.S01E02.mkv", data: b})
	learned := make(chan vault.Layout, 1)
	engine := newTestEngine(t, f, nil, Options{OnLayout: func(_ string, l vault.Layout) { learned <- l }})
	s := openSource(t, engine, "show", xml)
	defer s.Close()
	l := <-learned
	doc, err := nzb.Parse(xml)
	if err != nil {
		t.Fatal(err)
	}
	doc.Meta = map[string][]string{nzb.MetaEpisode: {(nzb.EpisodeHint{Season: 1, Episode: 1}).String()}}
	for _, stale := range []bool{false, true} {
		if stale {
			for i := range l.Members {
				m := &l.Members[i]
				if m.Episode == 1 {
					m.Extents[0].Offset++
					m.Extents[0].Size--
					m.Size--
				}
			}
		}
		e := newTestEngine(t, f, nil, Options{})
		src, err := e.OpenRecord(t.Context(), "show-ep1", &vault.Record{NZB: doc, Layout: &l})
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(io.NewSectionReader(src, 0, src.Size()))
		if err != nil || !bytes.Equal(got, a) {
			t.Fatalf("stale %t: wrong episode or extent: %v", stale, err)
		}
		if src.(*source).plan.restored == stale {
			t.Fatalf("stale %t: unexpected planner", stale)
		}
		src.Close()
	}
}
