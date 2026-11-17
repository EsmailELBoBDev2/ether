package livetv

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestM3UPlaylistHeaderGuideDiscoveryAndOwnerOverride(t *testing.T) {
	ctx := context.Background()

	var leakedAuth atomic.Bool
	epgOne := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leakedAuth.Store(true)
		}
		_, _ = w.Write([]byte(xmltvFixture(4, "news.us=News Network")))
	}))
	defer epgOne.Close()

	epgTwo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leakedAuth.Store(true)
		}
		_, _ = w.Write([]byte(xmltvFixture(4, "sport.uk=Sport One")))
	}))
	defer epgTwo.Close()

	ownerEPG := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(xmltvFixture(6, "news.us=News Network")))
	}))
	defer ownerEPG.Close()

	playlistBody := `#EXTM3U x-tvg-url="` + epgOne.URL + `/epg1.xml, ` + epgTwo.URL + `/epg2.xml" url-tvg="` + epgOne.URL + `/epg1.xml"` + "\n" +
		`#EXTINF:-1 tvg-id="news.us",News Network` + "\nhttps://streams.invalid/news.ts\n" +
		`#EXTINF:-1 tvg-id="sport.uk",Sport One` + "\nhttps://streams.invalid/sport.ts\n"

	playlistSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "owner" || p != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(playlistBody))
	}))
	defer playlistSrv.Close()

	s := testService(t, Options{
		HTTPClient: playlistSrv.Client(),
		Now:        func() time.Time { return guideTestNow },
	})

	got, err := s.ImportSource(ctx, Source{
		ID:          "m3u-epg",
		Name:        "Playlist with guide",
		PlaylistURL: playlistSrv.URL + "/list.m3u",
		Auth:        &SourceAuth{Type: "basic", Username: "owner", Password: "secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if leakedAuth.Load() {
		t.Fatal("playlist basic-auth credentials leaked to cross-origin x-tvg-url host")
	}
	if !got.PlaylistGuide || !got.HasGuide || got.GuideURL != "" || got.GuideChannelCount != 2 || got.ProgrammeCount != 8 {
		t.Fatalf("expected discovered playlist guide (2 guide channels, 8 programmes), got %+v", got)
	}
	listed := sourceByID(t, s, "m3u-epg")
	if !listed.PlaylistGuide || !listed.HasGuide || listed.GuideURL != "" {
		t.Fatalf("listed source missing playlistGuide flag: %+v", listed)
	}

	// Owner-set GuideURL wins and clears PlaylistGuide.
	overridden, err := s.ImportSource(ctx, Source{
		ID:          "m3u-epg",
		Name:        "Playlist with guide",
		PlaylistURL: playlistSrv.URL + "/list.m3u",
		GuideURL:    ownerEPG.URL + "/owner.xml",
		Auth:        &SourceAuth{Type: "basic", Username: "owner", Password: "secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if overridden.PlaylistGuide || overridden.GuideURL != ownerEPG.URL+"/owner.xml" || overridden.GuideChannelCount != 1 || overridden.ProgrammeCount != 6 {
		t.Fatalf("owner GuideURL was overridden by playlist header: %+v", overridden)
	}

	// Owner-set inline Guide also wins over the playlist's #EXTM3U header.
	inline, err := s.ImportSource(ctx, Source{
		ID:       "m3u-inline",
		Name:     "Inline guide override",
		Playlist: playlistBody,
		Guide:    xmltvFixture(2, "sport.uk=Sport One"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if inline.PlaylistGuide || !inline.HasGuide || inline.GuideChannelCount != 1 || inline.ProgrammeCount != 2 {
		t.Fatalf("owner inline Guide was overridden by playlist header: %+v", inline)
	}
}
