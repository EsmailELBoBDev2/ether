package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/livetv/dvr"
	"github.com/braedonsaunders/ether/internal/plugins"
	"github.com/braedonsaunders/ether/internal/plugins/adapters"
)

// Finished Live TV recordings appear as two Ether libraries, Recorded TV
// (show, season, episode) and Recorded Movies, in every app. They are host
// kinds: Ether pushes the items and opens the files itself.
const (
	dvrLibraryPlugin = "ether.dvr"
	dvrTVKind        = "recorded-tv"
	dvrMoviesKind    = "recorded-movies"
)

func dvrKinds() []plugins.MediaKindMeta {
	tv := plugins.MediaKindMeta{
		Key:     dvrTVKind,
		Label:   "Recorded TV",
		Media:   "video",
		Library: plugins.KindLibraryDecl{Name: "Recorded TV", DateDisplay: "date"},
		Levels: []plugins.KindLevel{
			{Key: "show", Label: "Show", Role: "show"},
			{Key: "season", Label: "Season"},
			{Key: "episode", Label: "Episode", Role: "episode", Playable: true},
		},
		Present: core.Presentations{
			Plex:         &core.PlexPresentation{Base: "show"},
			MediaBrowser: &core.MediaBrowserPresentation{CollectionType: "tvshows", ItemTypes: map[string]string{"show": "Series", "season": "Season", "episode": "Episode"}},
		},
		Browse: plugins.KindBrowseDecl{Sorts: []string{"title", "added", "released", "index"}, Search: true},
		Sync:   plugins.KindSyncDecl{Mode: plugins.KindSyncPush},
	}
	movies := plugins.MediaKindMeta{
		Key:     dvrMoviesKind,
		Label:   "Recorded Movies",
		Media:   "video",
		Library: plugins.KindLibraryDecl{Name: "Recorded Movies"},
		Levels:  []plugins.KindLevel{{Key: "movie", Label: "Movie", Role: "item", Playable: true}},
		Present: core.Presentations{
			Plex:         &core.PlexPresentation{Base: "movie"},
			MediaBrowser: &core.MediaBrowserPresentation{CollectionType: "movies", ItemTypes: map[string]string{"movie": "Movie"}},
		},
		Browse: plugins.KindBrowseDecl{Sorts: []string{"title", "added", "released", "year"}, Search: true},
		Sync:   plugins.KindSyncDecl{Mode: plugins.KindSyncPush},
	}
	return []plugins.MediaKindMeta{tv, movies}
}

// dvrHints is what an item carries to find its file again.
type dvrHints struct {
	Path        string  `json:"path"`
	DurationSec float64 `json:"durationSec,omitempty"`
}

// dvrLibrary keeps the recordings libraries in step with the DVR store.
type dvrLibrary struct {
	a     *App
	store *dvr.Store

	mu      sync.Mutex
	running bool
	again   bool
}

// buildDVRLibrary registers the recordings libraries once plugin kinds
// exist, fills them, and resyncs whenever a recording finishes or is
// deleted.
func (a *App) buildDVRLibrary(ctx context.Context) {
	if a.livetv == nil || a.pluginsRT == nil || a.pluginsRT.kindCatalog == nil || a.pluginsRT.pluginitems == nil {
		return
	}
	root := func() string { return a.liveTVDVRSettings().Dir }
	for _, k := range dvrKinds() {
		_, err := a.pluginsRT.kindCatalog.RegisterHostKind(ctx, adapters.HostKind{PluginID: dvrLibraryPlugin, Kind: k, Root: root, Resolve: dvrResolve})
		if err != nil {
			a.log.Warn("recordings library unavailable", "kind", k.Key, "error", err)
		}
	}
	l := &dvrLibrary{a: a, store: dvr.NewStore(a.db, nil)}
	a.livetv.SetDVRRecordingsChanged(l.changed)
	l.changed()
}

// changed resyncs in the background; changes during a sync run it again.
func (l *dvrLibrary) changed() {
	l.mu.Lock()
	if l.running {
		l.again = true
		l.mu.Unlock()
		return
	}
	l.running = true
	l.mu.Unlock()
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			if err := l.sync(ctx); err != nil {
				l.a.log.Warn("recordings library sync failed", "error", err)
			}
			cancel()
			l.mu.Lock()
			if !l.again {
				l.running = false
				l.mu.Unlock()
				return
			}
			l.again = false
			l.mu.Unlock()
		}
	}()
}

func (l *dvrLibrary) sync(ctx context.Context) error {
	recs, err := l.store.Recordings(ctx, 0)
	if err != nil {
		return err
	}
	tv, movies := dvrLibraryItems(recs)
	items := l.a.pluginsRT.pluginitems
	for kind, want := range map[string][]core.KindItem{dvrTVKind: tv, dvrMoviesKind: movies} {
		if _, err := items.GetKind(ctx, dvrLibraryPlugin, kind); err != nil {
			continue // the owner deleted this library
		}
		if len(want) > 0 {
			if _, err := items.UpsertItems(ctx, dvrLibraryPlugin, kind, want); err != nil {
				return fmt.Errorf("%s: %w", kind, err)
			}
		}
		keep := make(map[string]bool, len(want))
		for _, it := range want {
			keep[it.ID] = true
		}
		var stale []string
		cursor := ""
		for {
			page, err := items.KindItems(ctx, dvrLibraryPlugin, kind, cursor, 500)
			if err != nil {
				return err
			}
			for _, it := range page.Items {
				if !keep[it.ID] {
					stale = append(stale, it.ID)
				}
			}
			if page.NextCursor == "" || page.NextCursor == cursor {
				break
			}
			cursor = page.NextCursor
		}
		if len(stale) > 0 {
			if err := items.DeleteItems(ctx, dvrLibraryPlugin, kind, stale); err != nil {
				return err
			}
		}
	}
	return nil
}

func dvrHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

func dvrArt(role, url string) []core.KindArtRef {
	if strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") {
		return []core.KindArtRef{{Role: role, URL: url}}
	}
	return nil
}

// dvrLibraryItems maps finished recordings to library items: parents first,
// then their children, as the item store requires.
func dvrLibraryItems(recs []dvr.Recording) (tv, movies []core.KindItem) {
	var shows, seasons, episodes []core.KindItem
	seenShow, seenSeason := map[string]bool{}, map[string]bool{}
	for _, r := range recs {
		if r.Status != dvr.JobCompleted && r.Status != dvr.JobPartial {
			continue
		}
		p := r.Programme
		hints, _ := json.Marshal(dvrHints{Path: r.Path, DurationSec: float64(r.DurationMS) / 1000})
		released := p.Start.Unix()
		if p.IsMovie() {
			title := p.Title
			if title == "" {
				title = "Recording"
			}
			movies = append(movies, core.KindItem{ID: "rec-" + r.ID, Level: "movie", Title: title, Summary: p.Summary, Year: p.Year(), Released: released, Playable: true, Art: dvrArt("poster", p.Thumb), Hints: hints})
			continue
		}
		show := p.Show()
		if show == "" {
			show = "Recording"
		}
		showID := "show-" + dvrHash(dvr.NormTitle(show))
		if !seenShow[showID] {
			seenShow[showID] = true
			shows = append(shows, core.KindItem{ID: showID, Level: "show", Title: show, Art: dvrArt("poster", p.Thumb)})
		}
		season := p.Season
		if season <= 0 {
			// Unnumbered airings (news, sport) are grouped by year.
			season = p.Start.Local().Year()
		}
		seasonID := fmt.Sprintf("%s-s%d", showID, season)
		if !seenSeason[seasonID] {
			seenSeason[seasonID] = true
			seasons = append(seasons, core.KindItem{ID: seasonID, Level: "season", ParentID: showID, Index: season, Title: fmt.Sprintf("Season %d", season)})
		}
		title := p.EpisodeTitle
		if title == "" {
			title = show + " " + p.Start.Local().Format("2006-01-02 15:04")
		}
		episodes = append(episodes, core.KindItem{ID: "rec-" + r.ID, Level: "episode", ParentID: seasonID, Index: p.Episode, Title: title, Summary: p.Summary, Year: p.Start.Local().Year(), Released: released, Playable: true, Art: dvrArt("thumb", p.Thumb), Hints: hints})
	}
	tv = append(append(shows, seasons...), episodes...)
	return tv, movies
}

// dvrResolve opens a recording's file. The host kind confines it to the
// recordings folder.
func dvrResolve(_ context.Context, item core.KindItem) ([]core.PlayableSource, error) {
	var h dvrHints
	if err := json.Unmarshal(item.Hints, &h); err != nil || h.Path == "" {
		return nil, core.ErrSourceUnavailable
	}
	info, err := os.Stat(h.Path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, core.ErrSourceUnavailable
	}
	return []core.PlayableSource{{ID: "recording", Label: "Recording", Pattern: "file", Path: h.Path, MIME: "video/mp2t", Size: info.Size(), DurationSec: h.DurationSec}}, nil
}
