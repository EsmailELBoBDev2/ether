package experience

import (
	"context"
	"strconv"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
)

type spyMusicPluginLibraries struct {
	fakePluginLibraries
	browseCalls []frontend.ItemQuery
	browsePage  PluginBrowsePage
	personMatch PluginPersonMatch
}

func (s *spyMusicPluginLibraries) Browse(_ context.Context, _ core.Library, q frontend.ItemQuery, _ string, _ int) (PluginBrowsePage, error) {
	s.browseCalls = append(s.browseCalls, q)
	return s.browsePage, nil
}

func (s *spyMusicPluginLibraries) SearchPeople(context.Context, core.Library, string, frontend.ItemQuery, int, int) ([]PluginPersonMatch, error) {
	return nil, nil
}

func (s *spyMusicPluginLibraries) PersonByID(_ context.Context, _ core.Library, personID int64, _ int) (PluginPersonMatch, bool, error) {
	if s.personMatch.PersonID == personID {
		return s.personMatch, true, nil
	}
	return PluginPersonMatch{}, false, nil
}

func (s *spyMusicPluginLibraries) FindPersonID(_ context.Context, _ core.Library, match func(int64, string) bool) (int64, bool, error) {
	if s.personMatch.PersonID != 0 && match(s.personMatch.PersonID, s.personMatch.Name) {
		return s.personMatch.PersonID, true, nil
	}
	return 0, false, nil
}

func (s *spyMusicPluginLibraries) Item(_ context.Context, key core.RatingKey) (PluginItemView, error) {
	for _, t := range s.browsePage.Items {
		if t.Key == key && len(s.libs) > 0 {
			return PluginItemView{Library: s.libs[0], Title: t}, nil
		}
	}
	return PluginItemView{}, core.ErrNotFound
}

// TestPluginBrowseSQLFastPathForMediaBrowserItemTypes proves MediaBrowser
// item-type queries (IncludeItemTypes=MusicAlbum/Audio, ExcludeItemTypes=...)
// stay on the bounded SQL Browse fast path instead of scanning the library.
func TestPluginBrowseSQLFastPathForMediaBrowserItemTypes(t *testing.T) {
	ctx := context.Background()
	viewer := &core.User{ID: 1, AllowedLibraries: []core.LibraryID{9}}
	musicLib := core.Library{
		ID:       9,
		Key:      "plugin:music:albums",
		Name:     "Music",
		Source:   core.LibraryPlugin,
		PluginID: "music.plugin",
		KindKey:  "albums",
		Present:  mediaBrowserPresentation("music", map[string]string{"album": "MusicAlbum"}),
	}
	album := core.Title{
		Key:           core.PluginKey(1),
		Type:          core.TypeMovie,
		MBItemType:    "MusicAlbum",
		Title:         "First Album",
		PluginAddedAt: 1700000200,
	}
	pl := &spyMusicPluginLibraries{
		fakePluginLibraries: fakePluginLibraries{libs: []core.Library{musicLib}},
		browsePage:          PluginBrowsePage{Items: []core.Title{album}, Total: 1},
	}
	svc := &Service{d: ServiceDeps{
		Active:          func(context.Context, int64) (*core.User, error) { return viewer, nil },
		PluginLibraries: pl,
		Protocol:        "jellyfin",
		LibraryPresentation: func(p core.Presentations, mt core.MediaType) (core.MediaType, string, bool) {
			if p.MediaBrowser != nil {
				return mt, p.MediaBrowser.CollectionType, true
			}
			return mt, "", false
		},
	}}

	page, err := svc.LibraryItems(ctx, viewer, core.Device{}, "9", frontend.ItemQuery{
		UnservedTypes: []string{"musicalbum"},
		Recursive:     true,
		Limit:         10,
	})
	if err != nil {
		t.Fatalf("LibraryItems MusicAlbum: %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Title != "First Album" {
		t.Fatalf("unexpected page: %+v", page)
	}
	if len(pl.browseCalls) != 1 || len(pl.browseCalls[0].UnservedTypes) != 1 || pl.browseCalls[0].UnservedTypes[0] != "musicalbum" {
		t.Fatalf("expected bounded SQL Browse with UnservedTypes=[musicalbum], got %+v", pl.browseCalls)
	}
	// Cursor prefix must distinguish UnservedTypes and Watched state so
	// MusicAlbum vs Audio vs played filters never collide.
	tTrue := true
	pAlbum := pluginCursorPrefix("lib:9", frontend.ItemQuery{UnservedTypes: []string{"musicalbum"}})
	pAudio := pluginCursorPrefix("lib:9", frontend.ItemQuery{UnservedTypes: []string{"audio"}})
	pWatched := pluginCursorPrefix("lib:9", frontend.ItemQuery{UnservedTypes: []string{"musicalbum"}, Watched: &tTrue})
	if pAlbum == pAudio || pAlbum == pWatched {
		t.Fatalf("cursor prefixes collided: album=%s audio=%s watched=%s", pAlbum, pAudio, pWatched)
	}
}

// TestLatestAddedAndResolveFacetForPluginMusicLibrary proves LatestAdded serves
// published plugin libraries via SQL SortAdded desc and ResolveFacet resolves
// synthetic plugin person IDs even when scoped to a plugin view.
func TestLatestAddedAndResolveFacetForPluginMusicLibrary(t *testing.T) {
	ctx := context.Background()
	viewer := &core.User{ID: 1, AllowedLibraries: []core.LibraryID{9}}
	artistPID := core.PluginPersonID("Bracket")
	artistGUID := personFacetID(artistPID)
	album := core.Title{
		Key:           core.PluginKey(2),
		Type:          core.TypeMovie,
		MBItemType:    "MusicAlbum",
		Title:         "Latest Record",
		PluginAddedAt: 1700000500,
		Cast:          []core.Person{{TMDB: artistPID, Name: "Bracket"}},
	}
	musicLib := core.Library{
		ID:       9,
		Key:      "plugin:music:albums",
		Name:     "Music",
		Source:   core.LibraryPlugin,
		PluginID: "music.plugin",
		KindKey:  "albums",
		Present:  mediaBrowserPresentation("music", map[string]string{"album": "MusicAlbum"}),
	}
	pl := &spyMusicPluginLibraries{
		fakePluginLibraries: fakePluginLibraries{libs: []core.Library{musicLib}},
		browsePage:          PluginBrowsePage{Items: []core.Title{album}, Total: 1},
		personMatch:         PluginPersonMatch{PersonID: artistPID, Name: "Bracket", Titles: []core.Title{album}},
	}
	svc := &Service{d: ServiceDeps{
		Active:          func(context.Context, int64) (*core.User, error) { return viewer, nil },
		PluginLibraries: pl,
		Protocol:        "jellyfin",
		LibraryPresentation: func(p core.Presentations, mt core.MediaType) (core.MediaType, string, bool) {
			if p.MediaBrowser != nil {
				return mt, p.MediaBrowser.CollectionType, true
			}
			return mt, "", false
		},
	}}

	latest, err := svc.LatestAdded(ctx, viewer, core.Device{}, "9", 10)
	if err != nil {
		t.Fatalf("LatestAdded plugin library: %v", err)
	}
	if len(latest) != 1 || latest[0].Title.Key != album.Key || latest[0].AddedAt == nil || latest[0].AddedAt.Unix() != 1700000500 {
		t.Fatalf("unexpected latest items: %+v", latest)
	}
	if len(pl.browseCalls) != 1 || pl.browseCalls[0].Sort != core.SortAdded || !pl.browseCalls[0].Desc || !pl.browseCalls[0].NoTotal {
		t.Fatalf("expected O(limit) SortAdded desc query, got %+v", pl.browseCalls)
	}

	// ResolveFacet scoped to plugin view must resolve plugin person IDs via index.
	fv, err := svc.ResolveFacet(ctx, viewer, core.Device{}, "9", "person", artistGUID)
	if err != nil {
		t.Fatalf("ResolveFacet plugin person with plugin viewID: %v", err)
	}
	if fv.Key != strconv.FormatInt(artistPID, 10) || fv.Title != "Bracket" {
		t.Fatalf("unexpected resolved facet: %+v", fv)
	}

	// pluginPerson() for a music-only plugin artist marks KnownFor = "MusicArtist".
	detail, err := svc.pluginPerson(ctx, viewer, artistPID)
	if err != nil {
		t.Fatalf("pluginPerson(music artist): %v", err)
	}
	if detail.KnownFor != "MusicArtist" {
		t.Fatalf("expected KnownFor=MusicArtist, got %+v", detail)
	}
}
