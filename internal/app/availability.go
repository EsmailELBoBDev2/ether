package app

import (
	"context"
	"github.com/braedonsaunders/ether/internal/availability"
	"github.com/braedonsaunders/ether/internal/core"
	"time"
)

func (a *App) buildAvailability() error {
	s, err := availability.New(availability.Deps{Clustered: a.clustered(), DB: a.db, Config: a.cfg.Read, ConfigGeneration: a.cfg.Generation, Feeds: a.indexers, Catalog: a.catalog, Validator: a.resolver,
		CacheCheck: a.debrid.CheckCached, PackFiles: a.debrid.CachedPackMatcher, Search: func(ctx context.Context, req core.SearchRequest) ([]core.Release, error) {
			if a.indexers == nil {
				return nil, nil
			}
			return a.indexers.Search(ctx, req)
		}, Watchlist: func(ctx context.Context) (map[core.RatingKey]bool, error) {
			out := map[core.RatingKey]bool{}
			follow := func(k, show core.RatingKey) {
				out[k] = true
				if show == 0 && !k.Allocated() {
					switch k.Kind() {
					case core.KindSeason:
						show = k.Parent()
					case core.KindEpisode:
						show = k.Parent().Parent()
					}
				}
				if show != 0 {
					out[show] = true
				}
			}
			rows, err := a.db.QueryContext(ctx, `SELECT DISTINCT w.item_key, COALESCE(t.show_key,0) FROM watchlist_items w LEFT JOIN catalog_titles t ON t.key=w.item_key`)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			for rows.Next() {
				var key, show core.RatingKey
				if err := rows.Scan(&key, &show); err != nil {
					return nil, err
				}
				follow(key, show)
			}
			if err := rows.Err(); err != nil {
				return nil, err
			}
			if a.state != nil {
				recent, err := a.state.RecentlyViewedKeys(ctx, time.Now().Add(-30*24*time.Hour), 200)
				if err != nil {
					return nil, err
				}
				var children []core.RatingKey
				for _, k := range recent {
					follow(k, 0)
					if k.Allocated() && (k.Kind() == core.KindSeason || k.Kind() == core.KindEpisode) {
						children = append(children, k)
					}
				}
				titles, err := a.catalog.CachedTitles(ctx, children)
				if err != nil {
					return nil, err
				}
				for _, title := range titles {
					follow(title.Key, title.ShowKey)
				}
			}
			return out, nil
		}, Active: func() bool { return len(a.playback.Sessions()) > 0 },
		Seal: func(raw string) (string, error) { return a.cfg.Seal("availability-release", []byte(raw)) },
		Open: func(raw string) (string, error) {
			b, e := a.cfg.Unseal("availability-release", raw)
			return string(b), e
		},
		Logger: a.log.With("component", "availability"),
		// Section and home loads read the last evidence answer; judging
		// retained feed releases never runs inside a request (#303).
		Async: true,
		// Background requests to one indexer stay at least this far apart.
		Pace: 3 * time.Second,
	})
	a.availability = s
	a.availabilityLive.Store(s)
	if err == nil {
		a.closers = append(a.closers, s.Close)
	}
	return err
}
func (a *App) discoveredReleases(ctx context.Context, t *core.Title) []core.Release {
	if a.availability == nil {
		return nil
	}
	return a.availability.Candidates(ctx, t)
}
func (a *App) observeRelease(ctx context.Context, t *core.Title, r core.Release) {
	if a.availability != nil {
		a.availability.Observe(ctx, t, r)
	}
}

func (a *App) invalidateRelease(ctx context.Context, id string) {
	if a.availability != nil {
		a.availability.Invalidate(ctx, id)
	}
}

func (a *App) observeSearch(ctx context.Context, t *core.Title, rs []core.Release) {
	if a.availability != nil {
		a.availability.RecordSearch(ctx, t, rs)
	}
}

func (a *App) revealTitleSeason(key core.RatingKey) {
	if a.availability != nil {
		a.availability.RevealTitleSeason(key)
	}
}
