package app

import (
	"context"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
)

// Bounds for app-requested title refreshes ("Refresh metadata" in Plex,
// Jellyfin and Emby apps, "Update Metadata" in Infuse). Each request
// refreshes one title from TMDB. An app that asks again for the same title
// within the cooldown, or while refreshes are already running at the cap,
// is answered without new work.
const (
	titleRefreshTimeout     = 30 * time.Second
	titleRefreshCooldown    = 10 * time.Minute
	titleRefreshConcurrency = 2
)

type titleRefresher struct {
	refresh func(context.Context, core.RatingKey) error
	now     func() time.Time

	mu      sync.Mutex
	running int
	recent  map[core.RatingKey]time.Time
}

func newTitleRefresher(refresh func(context.Context, core.RatingKey) error) *titleRefresher {
	return &titleRefresher{refresh: refresh, now: time.Now, recent: map[core.RatingKey]time.Time{}}
}

// refreshTarget is the catalogue title an app's refresh names: movies and
// shows themselves, a season or episode its show. Other kinds have no
// catalogue details to refresh.
func refreshTarget(key core.RatingKey) (core.RatingKey, bool) {
	if key.IsPluginKey() {
		return 0, false
	}
	switch key.Kind() {
	case core.KindMovie, core.KindShow:
		return key, true
	case core.KindSeason, core.KindEpisode:
		return core.ShowKey(key.TMDB()), true
	}
	return 0, false
}

func (t *titleRefresher) RefreshTitleMetadata(_ context.Context, viewer *core.Viewer, key core.RatingKey) error {
	if t == nil || t.refresh == nil || viewer == nil || !viewer.IsOwner {
		return nil
	}
	target, ok := refreshTarget(key)
	if !ok {
		return nil
	}
	now := t.now()
	t.mu.Lock()
	for k, at := range t.recent {
		if now.Sub(at) >= titleRefreshCooldown {
			delete(t.recent, k)
		}
	}
	if _, seen := t.recent[target]; seen || t.running >= titleRefreshConcurrency {
		t.mu.Unlock()
		return nil
	}
	t.recent[target] = now
	t.running++
	t.mu.Unlock()
	go func() {
		defer func() {
			t.mu.Lock()
			t.running--
			t.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), titleRefreshTimeout)
		defer cancel()
		_ = t.refresh(ctx, target)
	}()
	return nil
}

var _ frontend.TitleRefresher = (*titleRefresher)(nil)

// appTitleRefresher refreshes and persists one title's catalogue details and
// drops its cached row so the next read serves them.
func (a *App) appTitleRefresher() *titleRefresher {
	a.titleRefreshOnce.Do(func() {
		a.titleRefresh = newTitleRefresher(func(ctx context.Context, key core.RatingKey) error {
			if a.catalog == nil {
				return nil
			}
			if _, err := a.catalog.RefreshTitle(ctx, key, true); err != nil {
				if a.log != nil {
					a.log.Debug("app title refresh failed", "key", key, "err", err)
				}
				return err
			}
			_, err := a.catalog.RefreshStoredTitle(ctx, key)
			return err
		})
	})
	return a.titleRefresh
}
