package plex

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/experience"
	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/sessions"
	"github.com/braedonsaunders/ether/internal/transcode"
)

type audioPluginStore struct {
	lib      core.Library
	items    map[core.RatingKey]core.Title
	children map[core.RatingKey][]core.Title
	all      []core.Title
}

func (s *audioPluginStore) Libraries(context.Context) ([]core.Library, error) {
	return []core.Library{
		{ID: core.LibraryMovies, Name: "Movies", Source: core.LibraryBuiltin, MediaType: core.TypeMovie},
		{ID: core.LibraryShows, Name: "TV Shows", Source: core.LibraryBuiltin, MediaType: core.TypeShow},
		s.lib,
	}, nil
}

func (s *audioPluginStore) Library(_ context.Context, id core.LibraryID) (core.Library, error) {
	switch id {
	case core.LibraryMovies:
		return core.Library{ID: core.LibraryMovies, Name: "Movies", Source: core.LibraryBuiltin, MediaType: core.TypeMovie}, nil
	case core.LibraryShows:
		return core.Library{ID: core.LibraryShows, Name: "TV Shows", Source: core.LibraryBuiltin, MediaType: core.TypeShow}, nil
	case s.lib.ID:
		return s.lib, nil
	}
	return core.Library{}, core.ErrNotFound
}

func (s *audioPluginStore) IsPluginLibraryActive(context.Context, core.Library) bool { return true }
func (s *audioPluginStore) RootMediaType(context.Context, core.Library) core.MediaType {
	return core.TypeShow
}
func (s *audioPluginStore) Count(context.Context, core.Library) (int, error) {
	return len(s.all), nil
}

func (s *audioPluginStore) Browse(_ context.Context, lib core.Library, q frontend.ItemQuery, _ string, _ int) (experience.PluginBrowsePage, error) {
	if lib.ID != s.lib.ID {
		return experience.PluginBrowsePage{}, nil
	}
	var out []core.Title
	wantTracks := slices.Contains(q.Types, core.MediaType(plexBaseTrack))
	for _, it := range s.all {
		if wantTracks {
			if it.KindLevel == "track" {
				out = append(out, it)
			}
			continue
		}
		if it.KindLevel == "artist" {
			out = append(out, it)
		}
	}
	return experience.PluginBrowsePage{Items: out, Total: len(out), TotalExact: true}, nil
}

func (s *audioPluginStore) Item(_ context.Context, key core.RatingKey) (experience.PluginItemView, error) {
	t, ok := s.items[key]
	if !ok {
		return experience.PluginItemView{}, core.ErrNotFound
	}
	return experience.PluginItemView{
		Title:    t,
		Library:  s.lib,
		Playable: t.MediaClass != core.MediaClassBook && (t.Type == core.TypeEpisode || t.Type == core.TypeMovie),
	}, nil
}

func (s *audioPluginStore) ItemsMany(ctx context.Context, keys []core.RatingKey) (map[core.RatingKey]experience.PluginItemView, error) {
	out := make(map[core.RatingKey]experience.PluginItemView, len(keys))
	for _, k := range keys {
		if piv, err := s.Item(ctx, k); err == nil {
			out[k] = piv
		}
	}
	return out, nil
}

func (s *audioPluginStore) Children(_ context.Context, parent core.RatingKey, _ bool, _ string, _ int) (experience.PluginBrowsePage, error) {
	kids := append([]core.Title(nil), s.children[parent]...)
	return experience.PluginBrowsePage{Items: kids, Total: len(kids), TotalExact: true}, nil
}

func (s *audioPluginStore) Ancestors(context.Context, core.RatingKey) ([]core.Title, core.Library, error) {
	return nil, s.lib, nil
}
func (s *audioPluginStore) Facets(context.Context, core.Library, string) ([]core.FacetValue, error) {
	return nil, nil
}
func (s *audioPluginStore) Search(context.Context, core.Library, string, frontend.ItemQuery, int) ([]core.Title, error) {
	return nil, nil
}
func (s *audioPluginStore) MemberOf(context.Context, core.Library, core.RatingKey) (bool, error) {
	return false, nil
}

func TestPluginAudioPlayQueueDecisionTimelineAndSessions(t *testing.T) {
	const libID = core.LibraryID(7)
	artistKey := core.PluginKey(701)
	albumKey := core.PluginKey(702)
	track1Key := core.PluginKey(703)
	track2Key := core.PluginKey(704)
	emptyAlbumKey := core.PluginKey(705)
	bookKey := core.PluginKey(706)

	artist := core.Title{
		Key: artistKey, Type: core.TypeShow, Title: "Artist One",
		ChildCount: 2, LeafCount: 2, KindLevel: "artist", LibraryID: libID,
	}
	album := core.Title{
		Key: albumKey, Type: core.TypeSeason, Title: "Album One",
		ParentKey: artistKey, ShowKey: artistKey, ShowTitle: "Artist One",
		ChildCount: 2, KindLevel: "album", LibraryID: libID, Year: 2009,
	}
	emptyAlbum := core.Title{
		Key: emptyAlbumKey, Type: core.TypeSeason, Title: "Empty Album",
		ParentKey: artistKey, ShowKey: artistKey, ShowTitle: "Artist One",
		ChildCount: 0, KindLevel: "album", LibraryID: libID,
	}
	track1 := core.Title{
		Key: track1Key, Type: core.TypeEpisode, Title: "Track One",
		Episode: 1, Disc: 1, ParentKey: albumKey, ParentTitle: "Album One",
		ShowKey: artistKey, ShowTitle: "Artist One",
		MediaClass: core.MediaClassMusic, KindLevel: "track", LibraryID: libID,
		Runtime: 210 * time.Second, Year: 2009,
	}
	track2 := core.Title{
		Key: track2Key, Type: core.TypeEpisode, Title: "Track Two",
		Episode: 2, Disc: 1, ParentKey: albumKey, ParentTitle: "Album One",
		ShowKey: artistKey, ShowTitle: "Artist One",
		MediaClass: core.MediaClassMusic, KindLevel: "track", LibraryID: libID,
		Runtime: 240 * time.Second, Year: 2009,
	}
	book := core.Title{
		Key: bookKey, Type: core.TypeMovie, Title: "Unplayable Book",
		MediaClass: core.MediaClassBook, KindLevel: "book", LibraryID: libID,
		Runtime: 100 * time.Minute,
	}

	store := &audioPluginStore{
		lib: core.Library{
			ID:        libID,
			Name:      "Music",
			Source:    core.LibraryPlugin,
			PluginID:  "ether.music",
			KindKey:   "artists",
			MediaType: core.TypeShow,
			Present:   core.Presentations{Plex: &core.PlexPresentation{Base: plexBaseArtist}},
		},
		items: map[core.RatingKey]core.Title{
			artistKey:     artist,
			albumKey:      album,
			emptyAlbumKey: emptyAlbum,
			track1Key:     track1,
			track2Key:     track2,
			bookKey:       book,
		},
		children: map[core.RatingKey][]core.Title{
			artistKey: {album, emptyAlbum},
			albumKey:  {track1, track2},
		},
		all: []core.Title{artist, track1, track2},
	}

	audioInfo := core.MediaInfo{
		Container:   "mp3",
		Duration:    210 * time.Second,
		BitrateKbps: 320,
		Streams: []core.StreamInfo{
			{Index: 0, Type: "audio", Codec: "mp3", Channels: 2},
		},
	}
	pb := newFakePlayback(audioInfo)
	tc := newFakeTranscoder(core.Decision{Kind: core.DecisionDirectPlay, AudioCopy: true, Container: "mp3"})
	kindSrc := func(_ context.Context, _ *core.Viewer, key core.RatingKey) (*core.KindInput, error) {
		switch key {
		case track1Key:
			cp := track1
			return &core.KindInput{Title: &cp, SourceID: "track-1"}, nil
		case track2Key:
			cp := track2
			return &core.KindInput{Title: &cp, SourceID: "track-2"}, nil
		}
		return nil, core.ErrNotFound
	}
	rt := sessions.NewLocalRuntime(sessions.LocalRuntimeDeps{
		Playback:   pb,
		Transcoder: tc,
		KindSource: kindSrc,
	})

	f := newFixture(func(d *testServerDeps) {
		d.Playback = pb
		d.Transcoder = tc
		d.Deps.Host.Sessions = rt
	})
	defer f.srv.Close()

	active := func(_ context.Context, id int64) (*core.Viewer, error) {
		if id <= 0 {
			return nil, core.ErrUnauthorized
		}
		return &core.Viewer{ID: id}, nil
	}
	events := sessions.NewEventBus(nil, active)
	t.Cleanup(events.Close)
	broker, err := sessions.NewBroker(sessions.BrokerDeps{
		SurfacePolicy: func(protocol core.Protocol) core.SessionPolicy {
			if protocol == "plex" {
				return SessionPolicy()
			}
			return core.SessionPolicy{}
		},
		Lookup: func(ctx context.Context, _ *core.Viewer, _ core.Device, key core.RatingKey) (*core.Title, error) {
			if t, ok := store.items[key]; ok {
				cp := t
				return &cp, nil
			}
			return f.catalog.Get(ctx, key)
		},
		KindSource:   kindSrc,
		Playback:     pb,
		Transcoder:   tc,
		Progress:     discardProgress{},
		ActiveViewer: active,
		CanPlay:      func(*core.Viewer, bool, time.Time) bool { return true },
		BitrateLimit: func(*core.Viewer) int { return 0 },
		OnTimeline:   func(*core.Viewer, core.RatingKey, time.Duration, time.Duration, string) {},
		Events:       events,
		Now:          func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	t.Cleanup(func() { broker.Close(context.Background()) })

	expSvc := experience.NewDirectService(experience.ServiceDeps{
		Active: func(_ context.Context, id int64) (*core.User, error) {
			if id == ownerUser.ID {
				u := ownerUser
				return &u, nil
			}
			return nil, core.ErrUnauthorized
		},
		Catalog:         f.catalog,
		State:           f.state,
		Playlists:       f.pls,
		PluginLibraries: store,
		Protocol:        "plex",
		LibraryPresentation: func(p core.Presentations, mt core.MediaType) (core.MediaType, string, bool) {
			if p.Plex != nil && p.Plex.Base != "" {
				return mt, p.Plex.Base, true
			}
			if mt == core.TypeShow {
				return mt, plexBaseShow, true
			}
			return mt, plexBaseMovie, true
		},
		BuiltinNative: func(mt core.MediaType) string {
			if mt == core.TypeShow {
				return plexBaseShow
			}
			return plexBaseMovie
		},
		Visibility: experience.Visibility{
			SectionFor: func(mt core.MediaType) (int, bool) {
				if mt == core.TypeShow || mt == core.TypeSeason || mt == core.TypeEpisode {
					return 2, true
				}
				return 1, true
			},
		},
	})
	f.srv.bindHost(frontend.Host{
		Experience: expSvc,
		Sessions:   sessions.BindSurface(broker, "plex"),
	})

	machineID := f.srv.identity().MachineIdentifier
	headers := map[string]string{
		"Accept":                    "application/json",
		"X-Plex-Client-Identifier":  "plex-web-audio",
		"X-Plex-Product":            "Plex Web",
		"X-Plex-Session-Identifier": "pq-audio-sess",
	}

	// 1. POST /playQueues?type=audio on album children?excludeAllLeaves=1 starting on track2.
	albumURI := "server://" + machineID + "/com.plexapp.plugins.library/library/metadata/" + albumKey.String() + "/children?excludeAllLeaves=1"
	rec := f.request(t, http.MethodPost,
		"/playQueues?type=audio&uri="+url.QueryEscape(albumURI)+"&key="+url.QueryEscape("/library/metadata/"+track2Key.String())+"&shuffle=0&repeat=0&continuous=1",
		headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /playQueues (album): status = %d body = %s", rec.Code, rec.Body.String())
	}
	mc := decodeJSON(t, rec)
	if got := mc["size"]; got != float64(2) {
		t.Fatalf("size = %v, want 2 (body=%s)", got, rec.Body.String())
	}
	if got := mc["playQueueTotalCount"]; got != float64(2) {
		t.Fatalf("playQueueTotalCount = %v, want 2", got)
	}
	if got := mc["playQueueSelectedItemOffset"]; got != float64(1) {
		t.Fatalf("playQueueSelectedItemOffset = %v, want 1", got)
	}
	if got := mc["playQueueSelectedMetadataItemID"]; got != track2Key.String() {
		t.Fatalf("playQueueSelectedMetadataItemID = %v, want %s", got, track2Key)
	}
	srcURI, _ := mc["playQueueSourceURI"].(string)
	if !strings.Contains(srcURI, "/directory/") || strings.Contains(srcURI, "/item/") {
		t.Fatalf("playQueueSourceURI = %q, want /directory/ URI", srcURI)
	}
	rows, _ := mc["Metadata"].([]any)
	if len(rows) != 2 {
		t.Fatalf("Metadata len = %d, want 2", len(rows))
	}
	for i, raw := range rows {
		row := raw.(map[string]any)
		if row["type"] != plexBaseTrack {
			t.Errorf("row %d type = %v, want %s", i, row["type"], plexBaseTrack)
		}
		if row["parentRatingKey"] != albumKey.String() {
			t.Errorf("row %d parentRatingKey = %v, want %s", i, row["parentRatingKey"], albumKey)
		}
		if row["parentTitle"] != "Album One" {
			t.Errorf("row %d parentTitle = %v, want Album One", i, row["parentTitle"])
		}
		if row["grandparentRatingKey"] != artistKey.String() {
			t.Errorf("row %d grandparentRatingKey = %v, want %s", i, row["grandparentRatingKey"], artistKey)
		}
		if row["grandparentTitle"] != "Artist One" {
			t.Errorf("row %d grandparentTitle = %v, want Artist One", i, row["grandparentTitle"])
		}
		media, _ := row["Media"].([]any)
		if len(media) == 0 {
			t.Fatalf("row %d missing Media", i)
		}
		parts, _ := media[0].(map[string]any)["Part"].([]any)
		if len(parts) == 0 {
			t.Fatalf("row %d missing Part", i)
		}
	}

	// 2. Artist children expansion yields [track1, track2] and omits emptyAlbum.
	artistURI := "server://" + machineID + "/com.plexapp.plugins.library/library/metadata/" + artistKey.String() + "/children"
	recArtist := f.request(t, http.MethodPost, "/playQueues?type=audio&uri="+url.QueryEscape(artistURI), headers)
	if recArtist.Code != http.StatusOK {
		t.Fatalf("POST /playQueues (artist): status = %d body = %s", recArtist.Code, recArtist.Body.String())
	}
	mcArtist := decodeJSON(t, recArtist)
	if got := mcArtist["size"]; got != float64(2) {
		t.Fatalf("artist playQueue size = %v, want 2 (body=%s)", got, recArtist.Body.String())
	}
	for _, raw := range mcArtist["Metadata"].([]any) {
		row := raw.(map[string]any)
		if row["ratingKey"] == emptyAlbumKey.String() {
			t.Fatalf("artist playQueue included empty album %s", emptyAlbumKey)
		}
		if row["ratingKey"] != track1Key.String() && row["ratingKey"] != track2Key.String() {
			t.Fatalf("unexpected item in artist playQueue: %v", row["ratingKey"])
		}
	}

	// 3. Section all?type=10 expansion returns tracks in section 7.
	secURI := "server://" + machineID + "/com.plexapp.plugins.library/library/sections/7/all?type=10"
	recSec := f.request(t, http.MethodPost, "/playQueues?type=audio&uri="+url.QueryEscape(secURI), headers)
	if recSec.Code != http.StatusOK {
		t.Fatalf("POST /playQueues (section): status = %d body = %s", recSec.Code, recSec.Body.String())
	}
	mcSec := decodeJSON(t, recSec)
	if got := mcSec["size"]; got != float64(2) {
		t.Fatalf("section playQueue size = %v, want 2 (body=%s)", got, recSec.Body.String())
	}

	// 3b. Artist allLeaves renders viewGroup=track, title2=All Tracks, and 2 track rows.
	allLeavesRec := f.request(t, http.MethodGet, "/library/metadata/"+artistKey.String()+"/allLeaves", headers)
	if allLeavesRec.Code != http.StatusOK {
		t.Fatalf("GET /library/metadata/%s/allLeaves status = %d body = %s", artistKey, allLeavesRec.Code, allLeavesRec.Body.String())
	}
	allLeavesMC := decodeJSON(t, allLeavesRec)
	if allLeavesMC["viewGroup"] != plexBaseTrack {
		t.Errorf("allLeaves viewGroup = %v, want %s", allLeavesMC["viewGroup"], plexBaseTrack)
	}
	if allLeavesMC["title2"] != "All Tracks" {
		t.Errorf("allLeaves title2 = %v, want All Tracks", allLeavesMC["title2"])
	}
	if leavesRows, _ := allLeavesMC["Metadata"].([]any); len(leavesRows) != 2 {
		t.Fatalf("allLeaves Metadata len = %d, want 2 (body=%s)", len(leavesRows), allLeavesRec.Body.String())
	}

	// 4. Book item is rejected from playQueue creation.
	bookURI := "server://" + machineID + "/com.plexapp.plugins.library/library/metadata/" + bookKey.String()
	recBookPQ := f.request(t, http.MethodPost, "/playQueues?type=audio&uri="+url.QueryEscape(bookURI), headers)
	if recBookPQ.Code == http.StatusOK {
		t.Fatalf("POST /playQueues on Book unexpectedly returned 200: %s", recBookPQ.Body.String())
	}

	// 5. /music/:/transcode/universal/decision succeeds for track1 and returns 404 without panic for bookKey.
	decRec := f.get(t, "/music/:/transcode/universal/decision?path="+url.QueryEscape("/library/metadata/"+track1Key.String())+"&protocol=http&musicBitrate=192&session=music-dec-1",
		"Accept", "application/json",
		"X-Plex-Client-Identifier", "plex-web-audio",
		"X-Plex-Product", "Plex Web",
		"X-Plex-Session-Identifier", "pq-audio-sess",
	)
	if decRec.Code != http.StatusOK {
		t.Fatalf("/music/:/transcode/universal/decision (track1) status = %d body = %s", decRec.Code, decRec.Body.String())
	}
	decMC := decodeJSON(t, decRec)
	decMeta, _ := decMC["Metadata"].([]any)
	if len(decMeta) != 1 {
		t.Fatalf("decision Metadata len = %d, want 1", len(decMeta))
	}
	decItem := decMeta[0].(map[string]any)
	if decItem["type"] != plexBaseTrack {
		t.Errorf("decision type = %v, want %s", decItem["type"], plexBaseTrack)
	}
	if decMedia, _ := decItem["Media"].([]any); len(decMedia) == 0 {
		t.Fatalf("decision Media empty: %v", decItem)
	}

	bookDecRec := f.get(t, "/music/:/transcode/universal/decision?path="+url.QueryEscape("/library/metadata/"+bookKey.String())+"&protocol=http&musicBitrate=192",
		"Accept", "application/json",
		"X-Plex-Client-Identifier", "plex-web-audio",
	)
	if bookDecRec.Code != http.StatusNotFound {
		t.Fatalf("/music/:/transcode/universal/decision (book) status = %d, want 404: %s", bookDecRec.Code, bookDecRec.Body.String())
	}

	// 6. Timeline playing report + /status/sessions for plugin track.
	tlRec := f.get(t, "/:/timeline?ratingKey="+track1Key.String()+"&state=playing&time=5000&duration=210000",
		"Accept", "application/json",
		"X-Plex-Client-Identifier", "plex-web-audio",
		"X-Plex-Product", "Plex Web",
		"X-Plex-Session-Identifier", "pq-audio-sess",
	)
	if tlRec.Code != http.StatusOK {
		t.Fatalf("/:/timeline status = %d body = %s", tlRec.Code, tlRec.Body.String())
	}
	sessRec := f.get(t, "/status/sessions", "Accept", "application/json")
	if sessRec.Code != http.StatusOK {
		t.Fatalf("/status/sessions status = %d body = %s", sessRec.Code, sessRec.Body.String())
	}
	sessMC := decodeJSON(t, sessRec)
	sessMeta, _ := sessMC["Metadata"].([]any)
	if len(sessMeta) == 0 {
		t.Fatalf("/status/sessions returned 0 Metadata rows: %s", sessRec.Body.String())
	}
	sessItem := sessMeta[0].(map[string]any)
	if sessItem["ratingKey"] != track1Key.String() || sessItem["type"] != plexBaseTrack {
		t.Fatalf("/status/sessions item = %+v, want ratingKey=%s type=%s", sessItem, track1Key, plexBaseTrack)
	}

	// 7. transcodeSessionID extracts session IDs for all four transcode prefixes.
	for _, tc := range []struct {
		path string
		want string
	}{
		{"/video/:/transcode/universal/session/sess-v/base/index.m3u8", "sess-v"},
		{"/music/:/transcode/universal/session/sess-m/base/index.m3u8", "sess-m"},
		{"/audio/:/transcode/universal/session/sess-a/base/index.m3u8", "sess-a"},
		{"/subtitles/:/transcode/universal/session/sess-s/base/0.vtt", "sess-s"},
	} {
		if got := transcodeSessionID(tc.path); got != tc.want {
			t.Errorf("transcodeSessionID(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

type decideTranscoder struct {
	*fakeTranscoder
}

func (d *decideTranscoder) Decide(info core.MediaInfo, caps core.ClientCaps, opts core.DecideOptions) core.Decision {
	return transcode.Decide(info, caps, opts, transcode.Policy{})
}

func TestPlexampAudioDecisionAndDirectStart(t *testing.T) {
	const libID = core.LibraryID(7)
	artistKey := core.PluginKey(801)
	albumKey := core.PluginKey(802)
	track1Key := core.PluginKey(803)
	track2Key := core.PluginKey(804)

	artist := core.Title{
		Key: artistKey, Type: core.TypeShow, Title: "Artist One",
		ChildCount: 1, LeafCount: 2, KindLevel: "artist", LibraryID: libID,
	}
	album := core.Title{
		Key: albumKey, Type: core.TypeSeason, Title: "Album One",
		ParentKey: artistKey, ShowKey: artistKey, ShowTitle: "Artist One",
		ChildCount: 2, KindLevel: "album", LibraryID: libID, Year: 2024,
	}
	track1 := core.Title{
		Key: track1Key, Type: core.TypeEpisode, Title: "Track One",
		Episode: 1, Disc: 1, ParentKey: albumKey, ParentTitle: "Album One",
		ShowKey: artistKey, ShowTitle: "Artist One",
		MediaClass: core.MediaClassMusic, KindLevel: "track", LibraryID: libID,
		Runtime: 210 * time.Second, Year: 2024,
	}
	track2 := core.Title{
		Key: track2Key, Type: core.TypeEpisode, Title: "Track Two",
		Episode: 2, Disc: 1, ParentKey: albumKey, ParentTitle: "Album One",
		ShowKey: artistKey, ShowTitle: "Artist One",
		MediaClass: core.MediaClassMusic, KindLevel: "track", LibraryID: libID,
		Runtime: 240 * time.Second, Year: 2024,
	}

	store := &audioPluginStore{
		lib: core.Library{
			ID:        libID,
			Name:      "Music",
			Source:    core.LibraryPlugin,
			PluginID:  "ether.music",
			KindKey:   "artists",
			MediaType: core.TypeShow,
			Present:   core.Presentations{Plex: &core.PlexPresentation{Base: plexBaseArtist}},
		},
		items: map[core.RatingKey]core.Title{
			artistKey: artist,
			albumKey:  album,
			track1Key: track1,
			track2Key: track2,
		},
		children: map[core.RatingKey][]core.Title{
			artistKey: {album},
			albumKey:  {track1, track2},
		},
		all: []core.Title{artist, track1, track2},
	}

	flacInfo := core.MediaInfo{
		Container:   "flac",
		Duration:    210 * time.Second,
		BitrateKbps: 1000,
		Streams: []core.StreamInfo{
			{Index: 0, Type: "audio", Codec: "flac", Channels: 2, BitrateKbps: 1000},
		},
	}
	pb := newFakePlayback(flacInfo)
	tc := &decideTranscoder{fakeTranscoder: newFakeTranscoder(core.Decision{})}
	kindSrc := func(_ context.Context, _ *core.Viewer, key core.RatingKey) (*core.KindInput, error) {
		switch key {
		case track1Key:
			cp := track1
			return &core.KindInput{Title: &cp, SourceID: "track-1"}, nil
		case track2Key:
			cp := track2
			return &core.KindInput{Title: &cp, SourceID: "track-2"}, nil
		}
		return nil, core.ErrNotFound
	}
	rt := sessions.NewLocalRuntime(sessions.LocalRuntimeDeps{
		Playback:   pb,
		Transcoder: tc,
		KindSource: kindSrc,
	})

	f := newFixture(func(d *testServerDeps) {
		d.Playback = pb
		d.Transcoder = tc
		d.Deps.Host.Sessions = rt
	})
	defer f.srv.Close()

	active := func(_ context.Context, id int64) (*core.Viewer, error) {
		if id <= 0 {
			return nil, core.ErrUnauthorized
		}
		return &core.Viewer{ID: id}, nil
	}
	events := sessions.NewEventBus(nil, active)
	t.Cleanup(events.Close)
	broker, err := sessions.NewBroker(sessions.BrokerDeps{
		SurfacePolicy: func(protocol core.Protocol) core.SessionPolicy {
			if protocol == "plex" {
				return SessionPolicy()
			}
			return core.SessionPolicy{}
		},
		Lookup: func(ctx context.Context, _ *core.Viewer, _ core.Device, key core.RatingKey) (*core.Title, error) {
			if t, ok := store.items[key]; ok {
				cp := t
				return &cp, nil
			}
			return f.catalog.Get(ctx, key)
		},
		KindSource:   kindSrc,
		Playback:     pb,
		Transcoder:   tc,
		Progress:     discardProgress{},
		ActiveViewer: active,
		CanPlay:      func(*core.Viewer, bool, time.Time) bool { return true },
		BitrateLimit: func(*core.Viewer) int { return 0 },
		OnTimeline:   func(*core.Viewer, core.RatingKey, time.Duration, time.Duration, string) {},
		Events:       events,
		Now:          func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	t.Cleanup(func() { broker.Close(context.Background()) })

	expSvc := experience.NewDirectService(experience.ServiceDeps{
		Active: func(_ context.Context, id int64) (*core.User, error) {
			if id == ownerUser.ID {
				u := ownerUser
				return &u, nil
			}
			return nil, core.ErrUnauthorized
		},
		Catalog:         f.catalog,
		State:           f.state,
		Playlists:       f.pls,
		PluginLibraries: store,
		Protocol:        "plex",
		LibraryPresentation: func(p core.Presentations, mt core.MediaType) (core.MediaType, string, bool) {
			if p.Plex != nil && p.Plex.Base != "" {
				return mt, p.Plex.Base, true
			}
			if mt == core.TypeShow {
				return mt, plexBaseShow, true
			}
			return mt, plexBaseMovie, true
		},
		BuiltinNative: func(mt core.MediaType) string {
			if mt == core.TypeShow {
				return plexBaseShow
			}
			return plexBaseMovie
		},
		Visibility: experience.Visibility{
			SectionFor: func(mt core.MediaType) (int, bool) {
				if mt == core.TypeShow || mt == core.TypeSeason || mt == core.TypeEpisode {
					return 2, true
				}
				return 1, true
			},
		},
	})
	f.srv.bindHost(frontend.Host{
		Experience: expSvc,
		Sessions:   sessions.BindSurface(broker, "plex"),
	})

	plexampHdrs := map[string]string{
		"Accept":                      "application/json",
		"X-Plex-Client-Identifier":    "plexamp-android",
		"X-Plex-Product":              "Plexamp",
		"X-Plex-Client-Profile-Name":  "Plexamp",
		"X-Plex-Client-Profile-Extra": "add-transcode-target(type=musicProfile&context=streaming&protocol=http&container=opus&audioCodec=opus)",
		"X-Plex-Session-Identifier":   "plexamp-sess",
	}

	// 1. HEAD /audio/:/transcode/universal/decision with directPlay=0 returns 200 OK.
	headRec := f.request(t, http.MethodHead,
		"/audio/:/transcode/universal/decision?path="+url.QueryEscape("/library/metadata/"+track1Key.String())+"&directPlay=0&musicBitrate=192&session=pa-1",
		plexampHdrs)
	if headRec.Code != http.StatusOK {
		t.Fatalf("HEAD /audio/:/transcode/universal/decision status = %d", headRec.Code)
	}

	// 2. GET /audio/:/transcode/universal/decision with directPlay=0 returns Conversion OK + opus target.
	getDecRec := f.request(t, http.MethodGet,
		"/audio/:/transcode/universal/decision?path="+url.QueryEscape("/library/metadata/"+track1Key.String())+"&directPlay=0&musicBitrate=192&session=pa-1",
		plexampHdrs)
	if getDecRec.Code != http.StatusOK {
		t.Fatalf("GET /audio/:/transcode/universal/decision status = %d body = %s", getDecRec.Code, getDecRec.Body.String())
	}
	decMC := decodeJSON(t, getDecRec)
	if got := decMC["generalDecisionCode"]; got != float64(codeConversionOK) {
		t.Fatalf("generalDecisionCode = %v, want %d (body=%s)", got, codeConversionOK, getDecRec.Body.String())
	}
	decMeta, _ := decMC["Metadata"].([]any)
	if len(decMeta) != 1 {
		t.Fatalf("Metadata len = %d, want 1", len(decMeta))
	}
	decMedia, _ := decMeta[0].(map[string]any)["Media"].([]any)
	if len(decMedia) == 0 {
		t.Fatalf("decision missing Media: %v", decMeta[0])
	}
	m0 := decMedia[0].(map[string]any)
	if c, _ := m0["container"].(string); c != "opus" && c != "ogg" {
		t.Errorf("Media[0].container = %q, want opus or ogg", c)
	}
	if ac, _ := m0["audioCodec"].(string); ac != "opus" {
		t.Errorf("Media[0].audioCodec = %q, want opus", ac)
	}

	// 3. GET /audio/:/transcode/universal/start after decision starts an HTTP opus audio session.
	startRec := f.request(t, http.MethodGet,
		"/audio/:/transcode/universal/start?path="+url.QueryEscape("/library/metadata/"+track1Key.String())+"&directPlay=0&musicBitrate=192&session=pa-1",
		plexampHdrs)
	if startRec.Code != http.StatusOK {
		t.Fatalf("GET /audio/:/transcode/universal/start status = %d body = %s", startRec.Code, startRec.Body.String())
	}
	tc.mu.Lock()
	sess1 := tc.sessions["pa-1"]
	tc.mu.Unlock()
	if sess1 == nil {
		t.Fatal("expected transcode session pa-1 to start")
	}
	if got := sess1.req.Decision.Container; got != "opus" && got != "ogg" {
		t.Errorf("pa-1 Decision.Container = %q, want opus or ogg", got)
	}
	if sess1.req.Decision.AudioCodec != "opus" {
		t.Errorf("pa-1 Decision.AudioCodec = %q, want opus", sess1.req.Decision.AudioCodec)
	}
	if sess1.req.Decision.VideoCopy {
		t.Errorf("pa-1 Decision.VideoCopy = true, want false")
	}

	// 4. Direct unarmed GET /audio/:/transcode/universal/start without prior /decision succeeds (non-mpegts).
	directRec := f.request(t, http.MethodGet,
		"/audio/:/transcode/universal/start?path="+url.QueryEscape("/library/metadata/"+track2Key.String())+"&musicBitrate=192&session=pa-direct",
		plexampHdrs)
	if directRec.Code != http.StatusOK {
		t.Fatalf("direct unarmed /audio/:/transcode/universal/start status = %d body = %s", directRec.Code, directRec.Body.String())
	}
	tc.mu.Lock()
	sessDirect := tc.sessions["pa-direct"]
	tc.mu.Unlock()
	if sessDirect == nil {
		t.Fatal("expected transcode session pa-direct to start")
	}
	if sessDirect.req.Decision.Container == "mpegts" {
		t.Errorf("pa-direct Decision.Container = mpegts, want audio container")
	}
	if sessDirect.req.Decision.VideoCopy {
		t.Errorf("pa-direct Decision.VideoCopy = true, want false")
	}

	// 5. Video start without prior /decision STILL returns 400 Bad Request.
	vidRec := f.request(t, http.MethodGet,
		"/video/:/transcode/universal/start?path="+url.QueryEscape("/library/metadata/"+fightClub.Key.String())+"&session=vid-unarmed",
		plexampHdrs)
	if vidRec.Code != http.StatusBadRequest {
		t.Fatalf("unarmed /video/:/transcode/universal/start status = %d, want 400", vidRec.Code)
	}

	// 6. Explicit URL audioCodec=mp3 overrides musicProfile container=opus&audioCodec=opus without mixing.
	mp3DecRec := f.request(t, http.MethodGet,
		"/audio/:/transcode/universal/decision?path="+url.QueryEscape("/library/metadata/"+track1Key.String())+"&directPlay=0&audioCodec=mp3&musicBitrate=192&session=pa-mp3",
		plexampHdrs)
	if mp3DecRec.Code != http.StatusOK {
		t.Fatalf("GET /audio/:/transcode/universal/decision (audioCodec=mp3) status = %d body = %s", mp3DecRec.Code, mp3DecRec.Body.String())
	}
	mp3MC := decodeJSON(t, mp3DecRec)
	if got := mp3MC["generalDecisionCode"]; got != float64(codeConversionOK) {
		t.Fatalf("pa-mp3 generalDecisionCode = %v, want %d (body=%s)", got, codeConversionOK, mp3DecRec.Body.String())
	}
	mp3Meta, _ := mp3MC["Metadata"].([]any)
	if len(mp3Meta) != 1 {
		t.Fatalf("pa-mp3 Metadata len = %d, want 1", len(mp3Meta))
	}
	mp3Media, _ := mp3Meta[0].(map[string]any)["Media"].([]any)
	if len(mp3Media) == 0 {
		t.Fatalf("pa-mp3 decision missing Media: %v", mp3Meta[0])
	}
	mp3M0 := mp3Media[0].(map[string]any)
	if c, _ := mp3M0["container"].(string); c != "mp3" {
		t.Errorf("pa-mp3 Media[0].container = %q, want mp3", c)
	}
	if ac, _ := mp3M0["audioCodec"].(string); ac != "mp3" {
		t.Errorf("pa-mp3 Media[0].audioCodec = %q, want mp3", ac)
	}
}
