package vela

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/frontend/frontendtest"
	"github.com/braedonsaunders/ether/internal/frontend/kit/ids"
	"github.com/braedonsaunders/ether/internal/identity"
	"github.com/braedonsaunders/ether/internal/store"
)

func TestVelaMediaClassesTitleMediaChildrenPagingAndSyntheticSeries(t *testing.T) {
	ctx := context.Background()
	db := store.OpenTest(t)
	if _, err := db.Exec(`INSERT INTO auth_users(id,plex_id,created_at,updated_at) VALUES(1,123,0,0)`); err != nil {
		t.Fatal(err)
	}
	grants, err := identity.NewGrants(db, castMembers{}, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	bearer, err := grants.Issue(ctx, 1, nil, "vela", core.Device{ID: "tv"})
	if err != nil {
		t.Fatal(err)
	}

	artistKey := core.PluginKey(101)
	albumKey := core.PluginKey(102)
	trackKeys := []core.RatingKey{
		core.PluginKey(103),
		core.PluginKey(104),
		core.PluginKey(105),
		core.PluginKey(106),
		core.PluginKey(107),
	}
	book1Key := core.PluginKey(201)
	book15Key := core.PluginKey(202)
	book2Key := core.PluginKey(203)
	authorPersonID := core.PluginPersonID("Frank Herbert")
	artistPersonID := core.PluginPersonID("Miles Davis")

	albumTitle := core.Title{
		Key:        albumKey,
		Type:       core.TypeSeason,
		ShowKey:    artistKey,
		Season:     0,
		Episode:    0,
		Title:      "Kind of Blue",
		Summary:    "Recorded in 1959.",
		Year:       1959,
		Genres:     []string{"Jazz"},
		ChildCount: len(trackKeys),
		MediaClass: core.MediaClassMusic,
		MediaRole:  core.MediaRoleAlbum,
		MediaParents: []core.TitleParent{
			{Key: artistKey, Title: "Miles Davis", Role: core.MediaRoleArtist},
		},
		MediaCredits: []core.MediaCredit{
			{Name: "Miles Davis", Role: "artist", TMDB: artistPersonID},
		},
		MediaFacts: []core.TitleFact{
			{Label: "Label", Value: "Columbia"},
		},
	}

	tracks := make([]core.Title, len(trackKeys))
	for idx, k := range trackKeys {
		tracks[idx] = core.Title{
			Key:        k,
			Type:       core.TypeEpisode,
			ShowKey:    artistKey,
			ItemIndex:  idx + 1,
			Disc:       1,
			Title:      []string{"So What", "Freddie Freeloader", "Blue in Green", "All Blues", "Flamenco Sketches"}[idx],
			Runtime:    time.Duration(idx+3) * time.Minute,
			MediaClass: core.MediaClassMusic,
			MediaRole:  core.MediaRoleTrack,
			MediaParents: []core.TitleParent{
				{Key: albumKey, Title: "Kind of Blue", Role: core.MediaRoleAlbum},
				{Key: artistKey, Title: "Miles Davis", Role: core.MediaRoleArtist},
			},
			MediaCredits: []core.MediaCredit{
				{Name: "Miles Davis", Role: "artist", TMDB: artistPersonID},
			},
		}
	}

	books := []core.Title{
		{
			Key:        book2Key,
			Type:       core.TypeMovie,
			ItemIndex:  2,
			Title:      "Dune Messiah",
			SortTitle:  "dune messiah",
			Genres:     []string{"Sci-Fi"},
			MediaClass: core.MediaClassBook,
			MediaRole:  core.MediaRoleBook,
			MediaParents: []core.TitleParent{
				{FacetID: "facet:series:Dune", Title: "Dune", Role: core.MediaRoleSeries, Index: 2},
			},
			MediaCredits: []core.MediaCredit{
				{Name: "Frank Herbert", Role: "author", TMDB: authorPersonID},
			},
			MediaReading: &core.TitleReading{Format: "epub", Pages: 256},
			MediaFacts:   []core.TitleFact{{Label: "Publisher", Value: "Putnam"}},
		},
		{
			Key:          book15Key,
			Type:         core.TypeMovie,
			ItemIndex:    1,
			IndexDisplay: "1.5",
			Title:        "Dune Interlude",
			SortTitle:    "dune interlude",
			Genres:       []string{"Sci-Fi"},
			MediaClass:   core.MediaClassBook,
			MediaRole:    core.MediaRoleBook,
			MediaParents: []core.TitleParent{
				{FacetID: "facet:series:Dune", Title: "Dune", Role: core.MediaRoleSeries, Index: 1, IndexDisplay: "1.5"},
			},
			MediaCredits: []core.MediaCredit{
				{Name: "Frank Herbert", Role: "author", TMDB: authorPersonID},
			},
			MediaReading: &core.TitleReading{Format: "epub", Pages: 80},
		},
		{
			Key:        book1Key,
			Type:       core.TypeMovie,
			ItemIndex:  1,
			Title:      "Dune",
			SortTitle:  "dune",
			Genres:     []string{"Sci-Fi", "Classic"},
			MediaClass: core.MediaClassBook,
			MediaRole:  core.MediaRoleBook,
			MediaParents: []core.TitleParent{
				{FacetID: "facet:series:Dune", Title: "Dune", Role: core.MediaRoleSeries, Index: 1},
			},
			MediaCredits: []core.MediaCredit{
				{Name: "Frank Herbert", Role: "author", TMDB: authorPersonID},
			},
			MediaReading: &core.TitleReading{Format: "epub", Pages: 412},
			MediaFacts:   []core.TitleFact{{Label: "Publisher", Value: "Chilton Books"}},
		},
	}

	videoSeasonKey := core.SeasonKey(55, 2)
	videoSeason := core.Title{
		Key:     videoSeasonKey,
		Type:    core.TypeSeason,
		ShowKey: core.ShowKey(55),
		Season:  2,
		Episode: 0,
		Title:   "Season 2",
	}

	views := []frontend.View{
		{ID: "1", Name: "Movies", Type: core.TypeMovie},
		{ID: "70", LibraryID: 70, Name: "Music", Type: core.TypeShow, PluginID: "community.music", KindKey: "music", Media: core.MediaClassMusic, Card: "square"},
		{
			ID: "71", LibraryID: 71, Name: "Books", Type: core.TypeMovie, PluginID: "community.books", KindKey: "book", Media: core.MediaClassBook, Card: "poster",
			Facets: []frontend.ViewFacet{{ID: "series", Label: "Series", Type: "string", Multi: true}},
			Tabs: []frontend.ViewTab{
				{ID: "library", Label: "Books", Kind: "library"},
				{ID: "series", Label: "Series", Kind: "collections", Facet: "series", Order: "name"},
			},
		},
	}

	canPlayFalse := false
	exp := frontendtest.Experience{
		ViewsFunc: func(context.Context, *core.Viewer) ([]frontend.View, error) { return views, nil },
		ItemFunc: func(_ context.Context, req frontend.ItemRequest) (frontend.ItemDetail, error) {
			switch req.Key {
			case albumKey:
				return frontend.ItemDetail{Title: albumTitle, CanPlay: &canPlayFalse}, nil
			case book15Key:
				return frontend.ItemDetail{Title: books[1]}, nil
			case videoSeasonKey:
				return frontend.ItemDetail{Title: videoSeason}, nil
			default:
				return frontend.ItemDetail{}, core.ErrNotFound
			}
		},
		ChildrenFunc: func(_ context.Context, _ *core.Viewer, _ core.Device, k core.RatingKey, q frontend.ItemQuery) (core.Page, error) {
			if k != albumKey {
				return core.Page{Items: []core.Title{}}, nil
			}
			start := min(q.Offset, len(tracks))
			end := len(tracks)
			if q.Limit > 0 && start+q.Limit < end {
				end = start + q.Limit
			}
			return core.Page{Items: tracks[start:end], Total: len(tracks)}, nil
		},
		LibraryItemsFunc: func(_ context.Context, _ *core.Viewer, _ core.Device, viewID string, q frontend.ItemQuery) (core.Page, error) {
			if viewID == "71" && reflect.DeepEqual(q.Filter.Facets["series"], []string{"Dune"}) {
				return core.Page{Items: books, Total: len(books)}, nil
			}
			return core.Page{Items: []core.Title{}}, nil
		},
	}

	seal, err := encryption(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	inst := &instance{
		ctx:     ctx,
		db:      db,
		seal:    seal,
		replays: make(map[string]*replay),
		mux:     http.NewServeMux(),
		host: frontend.Host{
			Identity:       identityFake{},
			Experience:     exp,
			ServerIdentity: frontend.ServerIdentity{MachineID: "srv-1", FriendlyName: "Ether", Version: "0.3.17"},
		},
	}
	inst.routes()

	doGet := func(path string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("X-Vela-Device-Id", "tv")
		rr := httptest.NewRecorder()
		inst.serve(rr, req)
		var body map[string]any
		if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
			t.Fatalf("decode %s (status %d): %v", path, rr.Code, err)
		}
		return rr.Code, body
	}

	// 1. Capabilities advertises features.mediaClasses with all 6 classes.
	code, caps := doGet("/vela/v0/capabilities")
	if code != 200 {
		t.Fatalf("capabilities status=%d body=%v", code, caps)
	}
	features := caps["features"].(map[string]any)
	mc, ok := features["mediaClasses"].(map[string]any)
	if !ok || mc["available"] != true {
		t.Fatalf("features.mediaClasses = %v", features["mediaClasses"])
	}
	wantClasses := []any{"video", "music", "audiobook", "book", "podcast", "photo"}
	if !reflect.DeepEqual(mc["classes"], wantClasses) {
		t.Fatalf("mediaClasses.classes = %v, want %v", mc["classes"], wantClasses)
	}

	// 2. GET /libraries emits media class on plugin libraries and omits it on video built-ins.
	code, libsResp := doGet("/vela/v0/libraries")
	if code != 200 {
		t.Fatalf("libraries status=%d body=%v", code, libsResp)
	}
	libsByID := map[string]map[string]any{}
	for _, raw := range libsResp["items"].([]any) {
		m := raw.(map[string]any)
		libsByID[m["id"].(string)] = m
	}
	if _, has := libsByID["1"]["media"]; has {
		t.Fatalf("built-in video library should omit media, got %v", libsByID["1"]["media"])
	}
	if libsByID["70"]["media"] != "music" || libsByID["71"]["media"] != "book" {
		t.Fatalf("plugin library media classes = music:%v book:%v", libsByID["70"]["media"], libsByID["71"]["media"])
	}

	// 3. Album detail emits Title.media and NEVER emits showId/seasonNumber/episodeNumber ("Season 0 Episode 0").
	code, albumDetail := doGet("/vela/v0/titles/" + ids.RatingKeyGUID(albumKey))
	if code != 200 {
		t.Fatalf("album detail status=%d body=%v", code, albumDetail)
	}
	albumWire := albumDetail["title"].(map[string]any)
	for _, forbidden := range []string{"showId", "seasonNumber", "episodeNumber"} {
		if _, has := albumWire[forbidden]; has {
			t.Fatalf("music album leaked video field %q: %v", forbidden, albumWire)
		}
	}
	albumMedia := albumWire["media"].(map[string]any)
	if albumMedia["class"] != "music" || albumMedia["role"] != "album" || albumMedia["childCount"] != float64(5) {
		t.Fatalf("unexpected album media: %v", albumMedia)
	}
	parents := albumMedia["parents"].([]any)
	if len(parents) != 1 || parents[0].(map[string]any)["id"] != ids.RatingKeyGUID(artistKey) || parents[0].(map[string]any)["role"] != "artist" {
		t.Fatalf("unexpected album parents: %v", parents)
	}
	credits := albumMedia["credits"].([]any)
	if len(credits) != 1 || credits[0].(map[string]any)["role"] != "artist" || credits[0].(map[string]any)["personId"] == "" {
		t.Fatalf("unexpected album credits: %v", credits)
	}
	facts := albumMedia["facts"].([]any)
	if len(facts) != 1 || facts[0].(map[string]any)["label"] != "Label" || facts[0].(map[string]any)["value"] != "Columbia" {
		t.Fatalf("unexpected album facts: %v", facts)
	}

	// 4. Video season emits seasonNumber but NEVER episodeNumber: 0.
	code, seasonDetail := doGet("/vela/v0/titles/" + ids.RatingKeyGUID(videoSeasonKey))
	if code != 200 {
		t.Fatalf("season detail status=%d body=%v", code, seasonDetail)
	}
	seasonWire := seasonDetail["title"].(map[string]any)
	if seasonWire["seasonNumber"] != float64(2) {
		t.Fatalf("seasonNumber = %v, want 2", seasonWire["seasonNumber"])
	}
	if _, hasEp := seasonWire["episodeNumber"]; hasEp {
		t.Fatalf("video season should not emit episodeNumber: %v", seasonWire)
	}

	// 5. GET /titles/{albumId}/children cursor pagination: 5 tracks at limit=2 -> 3 pages, terminating cleanly.
	code, p1 := doGet("/vela/v0/titles/" + ids.RatingKeyGUID(albumKey) + "/children?limit=2")
	if code != 200 || p1["continuation"] != "more" || len(p1["items"].([]any)) != 2 {
		t.Fatalf("children page 1 = %d %v", code, p1)
	}
	c1, _ := p1["nextCursor"].(string)
	if c1 == "" {
		t.Fatalf("children page 1 missing nextCursor: %v", p1)
	}
	tr0 := p1["items"].([]any)[0].(map[string]any)
	tr0Media := tr0["media"].(map[string]any)
	if tr0Media["class"] != "music" || tr0Media["role"] != "track" || tr0Media["index"] != float64(1) || tr0Media["disc"] != float64(1) {
		t.Fatalf("unexpected track media: %v", tr0Media)
	}
	tr0Parents := tr0Media["parents"].([]any)
	if len(tr0Parents) != 2 || tr0Parents[0].(map[string]any)["role"] != "album" || tr0Parents[1].(map[string]any)["role"] != "artist" {
		t.Fatalf("track parents must be nearest-first [album, artist], got %v", tr0Parents)
	}

	code, p2 := doGet("/vela/v0/titles/" + ids.RatingKeyGUID(albumKey) + "/children?limit=2&cursor=" + url.QueryEscape(c1))
	if code != 200 || p2["continuation"] != "more" || len(p2["items"].([]any)) != 2 {
		t.Fatalf("children page 2 = %d %v", code, p2)
	}
	c2, _ := p2["nextCursor"].(string)
	code, p3 := doGet("/vela/v0/titles/" + ids.RatingKeyGUID(albumKey) + "/children?limit=2&cursor=" + url.QueryEscape(c2))
	if code != 200 || p3["continuation"] != "exhausted" || len(p3["items"].([]any)) != 1 {
		t.Fatalf("children page 3 = %d %v", code, p3)
	}
	if _, hasNext := p3["nextCursor"]; hasNext {
		t.Fatalf("exhausted children page should not include nextCursor: %v", p3)
	}
	// Exact-multiple limit (limit=5 for 5 tracks) MUST return "exhausted", NEVER "unknown".
	code, pExact := doGet("/vela/v0/titles/" + ids.RatingKeyGUID(albumKey) + "/children?limit=5")
	if code != 200 || pExact["continuation"] != "exhausted" || len(pExact["items"].([]any)) != 5 {
		t.Fatalf("exact-limit children page = %d %v", code, pExact)
	}
	// Cursor with different limit returns 409.
	if badCode, _ := doGet("/vela/v0/titles/" + ids.RatingKeyGUID(albumKey) + "/children?limit=3&cursor=" + url.QueryEscape(c1)); badCode != 409 {
		t.Fatalf("mismatched cursor limit status=%d, want 409", badCode)
	}

	// 6. Synthetic facet-backed series parent on 1-level book + /titles/facet:series:Dune + /children.
	code, bookDetail := doGet("/vela/v0/titles/" + ids.RatingKeyGUID(book15Key))
	if code != 200 {
		t.Fatalf("book detail status=%d body=%v", code, bookDetail)
	}
	bookMedia := bookDetail["title"].(map[string]any)["media"].(map[string]any)
	if bookMedia["class"] != "book" || bookMedia["role"] != "book" || bookMedia["index"] != float64(1) || bookMedia["indexDisplay"] != "1.5" {
		t.Fatalf("unexpected book media: %v", bookMedia)
	}
	reading := bookMedia["reading"].(map[string]any)
	if reading["format"] != "epub" || reading["pages"] != float64(80) {
		t.Fatalf("unexpected book reading: %v", reading)
	}
	bParents := bookMedia["parents"].([]any)
	if len(bParents) != 1 || bParents[0].(map[string]any)["id"] != "facet:series:Dune" || bParents[0].(map[string]any)["role"] != "series" || bParents[0].(map[string]any)["indexDisplay"] != "1.5" {
		t.Fatalf("unexpected book series parent: %v", bParents)
	}

	// Opening the synthetic series ID directly returns SeriesPage metadata.
	code, seriesDetail := doGet("/vela/v0/titles/" + url.PathEscape("facet:series:Dune"))
	if code != 200 {
		t.Fatalf("synthetic series detail status=%d body=%v", code, seriesDetail)
	}
	seriesTitle := seriesDetail["title"].(map[string]any)
	seriesMedia := seriesTitle["media"].(map[string]any)
	if seriesTitle["id"] != "facet:series:Dune" || seriesTitle["name"] != "Dune" || seriesMedia["class"] != "book" || seriesMedia["role"] != "series" || seriesMedia["childCount"] != float64(3) {
		t.Fatalf("unexpected synthetic series detail: %v", seriesDetail)
	}

	// Fetching synthetic series children returns books ordered 1, 1.5, 2 with paging.
	code, sc1 := doGet("/vela/v0/titles/" + url.PathEscape("facet:series:Dune") + "/children?limit=2")
	if code != 200 || sc1["continuation"] != "more" {
		t.Fatalf("series children page 1 = %d %v", code, sc1)
	}
	scItems1 := sc1["items"].([]any)
	if scItems1[0].(map[string]any)["name"] != "Dune" || scItems1[1].(map[string]any)["name"] != "Dune Interlude" {
		t.Fatalf("series children page 1 order = %v", scItems1)
	}
	code, sc2 := doGet("/vela/v0/titles/" + url.PathEscape("facet:series:Dune") + "/children?limit=2&cursor=" + url.QueryEscape(sc1["nextCursor"].(string)))
	if code != 200 || sc2["continuation"] != "exhausted" {
		t.Fatalf("series children page 2 = %d %v", code, sc2)
	}
	scItems2 := sc2["items"].([]any)
	if len(scItems2) != 1 || scItems2[0].(map[string]any)["name"] != "Dune Messiah" {
		t.Fatalf("series children page 2 = %v", scItems2)
	}
}
