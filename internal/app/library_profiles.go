package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/lru"
)

// libraryProfileCacheEntries bounds the per-title library→profile answer.
// A title is asked for on every resolve, so the cache keeps the request
// path free of repeated filter SQL; the libraries generation invalidates it
// on any owner edit.
const libraryProfileCacheEntries = 8192

// libraryProfiles answers resolver.LibraryProfiles: which release profiles
// a title ranks with, from the library it belongs to. A catalogue or
// collection library that contains the title and chose a profile wins over
// the built-in library; the lowest library id breaks a tie between them,
// independent of entry point. Membership is one indexed read per such
// library (no walk), and the answer is cached until local generations move
// or the short TTL expires.
type libraryProfiles struct {
	app *App

	mu         sync.Mutex
	gen        uint64
	catalogGen uint64
	configGen  uint64
	at         time.Time
	cache      *lru.Cache[core.RatingKey, core.LibraryQuality]
}

func newLibraryProfiles(a *App) *libraryProfiles {
	return &libraryProfiles{app: a, cache: lru.New[core.RatingKey, core.LibraryQuality](lru.Options[core.RatingKey, core.LibraryQuality]{MaxEntries: libraryProfileCacheEntries})}
}

// LibraryQualityForTitle returns the profiles the title's most specific
// library chose; empty names mean the server's Default ones. It never
// blocks a request on more than one indexed read per library, and caches
// the result.
func (l *libraryProfiles) LibraryQualityForTitle(ctx context.Context, t *core.Title) core.LibraryQuality {
	if l == nil || l.app == nil || l.app.libraries == nil || t == nil {
		return core.LibraryQuality{}
	}
	key := libraryProfileKey(t)
	if lq, ok := l.cached(key); ok {
		return lq
	}
	lq := l.resolve(ctx, t)
	l.store(key, lq)
	return lq
}

func (l *libraryProfiles) cached(key core.RatingKey) (core.LibraryQuality, bool) {
	gen := l.app.libraries.Generation()
	l.mu.Lock()
	defer l.mu.Unlock()
	catalogGen := l.app.catalog.WriteGeneration()
	configGen := l.app.cfg.Generation()
	if l.gen != gen || l.configGen != configGen || l.catalogGen != catalogGen || time.Since(l.at) > 30*time.Second {
		l.cache.Clear()
		l.gen = gen
		l.catalogGen = catalogGen
		l.configGen = configGen
		l.at = time.Now()
	}
	return l.cache.Get(key)
}

func (l *libraryProfiles) store(key core.RatingKey, lq core.LibraryQuality) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cache.Add(key, lq)
}

// libraryQuality is the profile choice a library made, keeping only names
// a configured profile has.
func libraryQuality(lib core.Library, profiles []core.QualityProfile) core.LibraryQuality {
	var lq core.LibraryQuality
	if _, ok := core.FindProfile(profiles, lib.QualityProfile); ok {
		lq.Home = lib.QualityProfile
	}
	if _, ok := core.FindProfile(profiles, lib.AwayQualityProfile); ok {
		lq.Away = lib.AwayQualityProfile
	}
	return lq
}

// resolve picks the winning library and returns its profile choice.
func (l *libraryProfiles) resolve(ctx context.Context, t *core.Title) core.LibraryQuality {
	media := titleMedia(t)
	if media == core.MediaAny {
		return core.LibraryQuality{}
	}
	libs, err := l.app.libraries.List(ctx)
	if err != nil {
		return core.LibraryQuality{}
	}
	// Catalogue and collection libraries are more specific than the
	// built-in library. Among several matches the lowest id wins; the list
	// is in sort/id order, but such a library may sort anywhere, so track
	// the minimum.
	var winner core.Library
	var winnerQ core.LibraryQuality
	found := false
	profiles := l.app.cfg.Read().Quality.Profiles
	for _, lib := range libs {
		if core.IsBuiltinLibrary(lib.ID) || (lib.Source != core.LibraryCatalog && lib.Source != core.LibraryCollection) {
			continue
		}
		lq := libraryQuality(lib, profiles)
		if lq == (core.LibraryQuality{}) {
			continue
		}
		if lib.MediaType != "" && string(lib.MediaType) != media {
			continue
		}
		if lib.Source == core.LibraryCollection && lib.CollectionID <= 0 {
			continue
		}
		if found && lib.ID > winner.ID {
			continue
		}
		membership := libraryScopeFilter(lib, core.Filter{})
		if l.app.catalog == nil {
			continue
		}
		ok, err := l.app.catalog.TitleInFilter(ctx, membership, libraryProfileKey(t))
		if err != nil || !ok {
			continue
		}
		winner, winnerQ, found = lib, lq, true
	}
	if found {
		return winnerQ
	}
	// No catalogue or collection library chose one: the built-in library.
	canonical := core.LibraryMovies
	if media == string(core.TypeShow) {
		canonical = core.LibraryShows
	}
	if lib, err := l.app.libraries.Get(ctx, canonical); err == nil {
		return libraryQuality(lib, profiles)
	}
	return core.LibraryQuality{}
}

// titleMedia classifies a title as a movie or a show for library matching.
func titleMedia(t *core.Title) string {
	switch t.Type {
	case core.TypeMovie:
		return core.MediaMovie
	case core.TypeShow, core.TypeSeason, core.TypeEpisode:
		return string(core.TypeShow)
	}
	switch t.Key.Kind() {
	case core.KindMovie:
		return core.MediaMovie
	case core.KindShow, core.KindSeason, core.KindEpisode:
		return string(core.TypeShow)
	}
	return core.MediaAny
}

// Seasons and episodes inherit membership and the cached profile of their show.
func libraryProfileKey(t *core.Title) core.RatingKey {
	if (t.Type == core.TypeSeason || t.Type == core.TypeEpisode) && t.ShowKey != 0 {
		return t.ShowKey
	}
	switch t.Key.Kind() {
	case core.KindSeason, core.KindEpisode:
		return core.ShowKey(t.Key.TMDB())
	}
	return t.Key
}

// PolicyKey binds durable feed evidence to library profile assignments and filters.
func (l *libraryProfiles) PolicyKey(ctx context.Context) string {
	libs, err := l.app.libraries.List(ctx)
	if err != nil {
		return "unavailable"
	}
	b, _ := json.Marshal(libs)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

func (a *App) playbackProfile(caps core.ClientCaps, user *core.User, key core.RatingKey) core.QualityProfile {
	ctx := context.Background()
	// Plugin items open their own ByteSource; video release filters have no meaning.
	if key.IsPluginKey() {
		return core.QualityProfile{}
	}
	// Encoded keys identify movie/show and the parent show without hydrating
	// metadata or calling a catalogue provider on the playback path.
	return a.resolver.ProfileForTitle(ctx, caps, user, &core.Title{Key: key})
}
