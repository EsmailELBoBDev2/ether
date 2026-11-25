package catalog

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

type showOrder struct {
	title core.Title
	order core.EpisodeOrder
}

type resolvingEpisodeDefault struct{}

func (c *Catalog) readShowOrders(ctx context.Context, keys []core.RatingKey) (map[core.RatingKey]showOrder, error) {
	out := make(map[core.RatingKey]showOrder, len(keys))
	keys = uniqueRatingKeys(keys)
	for part := range store.Chunks(keys, 200) {
		args := make([]any, len(part))
		for i, k := range part {
			args[i] = k
		}
		rows, err := c.db.QueryContext(ctx, `SELECT `+prefixedCols("t")+`,COALESCE(o.provider,''),COALESCE(o.order_kind,'')
 FROM catalog_titles t LEFT JOIN catalog_episode_order o ON o.show_key=t.key WHERE t.type='show' AND t.key IN (`+placeholders(len(args))+`)`, args...)
		if err != nil {
			return nil, err
		}
		var items []showOrder
		for rows.Next() {
			var override core.EpisodeOrder
			st, err := scanTitle(extraScan{rows, []any{&override.Source, &override.Kind}})
			if err != nil {
				rows.Close()
				return nil, err
			}
			items = append(items, showOrder{title: st.t, order: override})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if item.order.Source == "" {
				cfg, err := c.episodeLibraryDefault(ctx, &item.title)
				if err != nil {
					return nil, err
				}
				item.order = cfg.LibraryDefault
			}
			out[item.title.Key] = item
		}
	}
	return out, nil
}

func uniqueRatingKeys(keys []core.RatingKey) []core.RatingKey {
	seen := make(map[core.RatingKey]bool, len(keys))
	out := make([]core.RatingKey, 0, len(keys))
	for _, k := range keys {
		if k != 0 && !seen[k] {
			out = append(out, k)
			seen[k] = true
		}
	}
	return out
}

// ProjectEpisodeTitles projects a bounded batch of already loaded titles from
// local data. A child absent from the selected order retains its own identity
// for history and direct playback. Enumeration uses the selected order's rows.
func (c *Catalog) ProjectEpisodeTitles(ctx context.Context, titles []core.Title) error {
	if ctx.Value(resolvingEpisodeDefault{}) != nil {
		return nil
	}
	var shows []core.RatingKey
	for _, t := range titles {
		if t.Type == core.TypeShow {
			shows = append(shows, t.Key)
		}
		if (t.Type == core.TypeEpisode || t.Type == core.TypeSeason) && t.ShowKey != 0 {
			shows = append(shows, t.ShowKey)
		}
	}
	if len(shows) == 0 {
		return nil
	}
	selected, err := c.readShowOrders(ctx, shows)
	if err != nil {
		return err
	}
	incoming := map[core.RatingKey][]int{}
	var alternates []core.RatingKey
	for i := range titles {
		t := &titles[i]
		if t.Type == core.TypeShow {
			choice, ok := selected[t.Key]
			if ok {
				t.EpisodeOrder = choice.order
			} else {
				t.EpisodeOrder = tmdbAired
			}
			continue
		}
		if t.Type != core.TypeSeason && t.Type != core.TypeEpisode {
			continue
		}
		choice, ok := selected[t.ShowKey]
		t.EpisodeOrder = tmdbAired
		if ok {
			t.EpisodeOrder = choice.order
			t.ShowIDs = mergeSourceIDs(choice.title.IDs, t.ShowIDs)
		}
		if t.Type == core.TypeSeason {
			t.ParentKey = t.ShowKey
		} else {
			t.EpisodeAirDate = t.Released
			if !t.Key.Allocated() {
				t.ParentKey = t.Key.Parent()
			}
		}
		if ok && providerLayout(&choice.title, choice.order) {
			if len(incoming[t.Key]) == 0 {
				alternates = append(alternates, t.Key)
			}
			incoming[t.Key] = append(incoming[t.Key], i)
		}
	}
	for part := range store.Chunks(alternates, 200) {
		args := make([]any, 0, len(part)*3)
		values := ""
		for _, k := range part {
			t := &titles[incoming[k][0]]
			if values != "" {
				values += ","
			}
			values += "(CAST(? AS BIGINT),CAST(? AS TEXT),CAST(? AS TEXT))"
			args = append(args, k, t.EpisodeOrder.Source, t.EpisodeOrder.Kind)
		}
		rows, err := c.db.QueryContext(ctx, `WITH chosen(key,provider,order_kind) AS (VALUES `+values+`)
 SELECT e.episode_key,e.season,e.episode,e.absolute,e.air_date,s.season_key,s.title,s.summary,s.poster,s.episode_count,0 AS is_season,e.title,e.summary,e.still,e.runtime_min,s.provider_id
 FROM chosen x JOIN catalog_order_episodes e ON e.episode_key=x.key AND e.provider=x.provider AND e.order_kind=x.order_kind
 JOIN catalog_order_seasons s ON s.show_key=e.show_key AND s.provider=e.provider AND s.order_kind=e.order_kind AND s.season=e.season
 UNION ALL
 SELECT s.season_key,s.season,0,0,s.air_date,s.season_key,s.title,s.summary,s.poster,s.episode_count,1,'','','',0,s.provider_id
 FROM chosen x JOIN catalog_order_seasons s ON s.season_key=x.key AND s.provider=x.provider AND s.order_kind=x.order_kind`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var e EpisodeCoordinate
			var date, name, summary, poster, epTitle, epSummary, still, seasonID string
			var count, isSeason, runtime int
			if err := rows.Scan(&e.Key, &e.Season, &e.Episode, &e.Absolute, &date, &e.ParentKey, &name, &summary, &poster, &count, &isSeason, &epTitle, &epSummary, &still, &runtime, &seasonID); err != nil {
				rows.Close()
				return err
			}
			e.AirDate = metadataDate(date)
			for _, i := range incoming[e.Key] {
				t := &titles[i]
				if isSeason != 0 {
					t.Season, t.SeasonTitle, t.Title, t.Summary, t.PosterPath = e.Season, name, name, summary, poster
					t.ChildCount, t.LeafCount, t.Released = count, count, e.AirDate
					id, _ := strconv.ParseInt(seasonID, 10, 64)
					switch t.EpisodeOrder.Source {
					case core.MetadataTMDB:
						if id > 0 {
							t.IDs.TMDB = id
						}
					case core.MetadataTVDB:
						if id > 0 {
							t.IDs.TVDB = id
						}
					}
				} else {
					applyEpisodeCoordinate(t, e)
					t.SeasonTitle = name
					if epTitle != "" {
						t.Title = epTitle
					}
					t.Summary, t.StillPath = epSummary, still
					if runtime > 0 {
						t.Runtime = time.Duration(runtime) * time.Minute
					}
				}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func applyEpisodeCoordinate(t *core.Title, e EpisodeCoordinate) {
	t.Season, t.Episode, t.AbsoluteEpisode = e.Season, e.Episode, e.Absolute
	t.ParentKey, t.EpisodeAirDate = e.ParentKey, e.AirDate
}

func (c *Catalog) projectSingle(ctx context.Context, t *core.Title) error {
	one := []core.Title{*t}
	if err := c.ProjectEpisodeTitles(ctx, one); err != nil {
		return err
	}
	*t = one[0]
	return nil
}

func (c *Catalog) orderForParent(ctx context.Context, key core.RatingKey) (*core.Title, *core.Title, core.EpisodeOrder, error) {
	if key.Kind() != core.KindShow && key.Kind() != core.KindSeason && key.Kind() != core.KindEpisode {
		return nil, nil, core.EpisodeOrder{}, nil
	}
	var parent *core.Title
	showKey := key
	if key.Kind() != core.KindShow {
		row, err := c.loadByKey(ctx, key)
		if err == nil {
			parent, showKey = &row.t, row.t.ShowKey
		} else if !errors.Is(err, core.ErrNotFound) {
			return nil, nil, core.EpisodeOrder{}, err
		} else if !key.Allocated() {
			showKey = core.ShowKey(key.TMDB())
		}
	}
	show, err := c.StoredTitle(ctx, showKey)
	if errors.Is(err, core.ErrNotFound) {
		return parent, nil, tmdbAired, nil
	}
	if err != nil {
		return nil, nil, core.EpisodeOrder{}, err
	}
	effective, err := c.effectiveEpisodeOrder(ctx, show)
	return parent, show, effective.Order, err
}

func (c *Catalog) orderSeasonByKey(ctx context.Context, show core.RatingKey, order core.EpisodeOrder, key core.RatingKey) (orderSeason, error) {
	var season orderSeason
	err := c.db.QueryRowContext(ctx, `SELECT season,season_key,title,summary,poster,air_date,episode_count,fetched_at,provider_id
 FROM catalog_order_seasons WHERE show_key=? AND provider=? AND order_kind=? AND season_key=? LIMIT 1`, show, order.Source, order.Kind, key).
		Scan(&season.Season, &season.key, &season.Title, &season.Summary, &season.PosterPath, &season.AirDate, &season.EpisodeCount, &season.fetched, &season.Ref.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return season, core.ErrNotFound
	}
	return season, err
}

func (c *Catalog) orderedEpisodePage(ctx context.Context, show *core.Title, order core.EpisodeOrder, season, offset, limit int, scheduled bool) (core.Page, error) {
	out := core.Page{Items: []core.Title{}}
	date := c.now().UTC().Format(time.DateOnly)
	cmp := "<="
	if scheduled {
		cmp = ">"
	}
	var filter string
	args := []any{show.Key, order.Source, order.Kind}
	if season >= 0 {
		filter = " AND e.season=?"
		args = append(args, season)
	}
	q := ` FROM catalog_order_episodes e JOIN catalog_titles t ON t.key=e.episode_key
 WHERE e.show_key=? AND e.provider=? AND e.order_kind=?` + filter + ` AND t.adult=0 AND e.air_date<>'' AND e.air_date` + cmp + `?`
	args = append(args, date)
	if err := c.db.QueryRowContext(ctx, `SELECT count(*)`+q, args...).Scan(&out.Total); err != nil {
		return out, err
	}
	if offset >= out.Total {
		return out, nil
	}
	args = append(args, limit, offset)
	items, err := c.scanTitleKeys(ctx, `SELECT `+prefixedCols("t")+q+` ORDER BY e.season,e.episode LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return out, err
	}
	if err := c.hydrateSlice(ctx, items); err != nil {
		return out, err
	}
	if err := c.ProjectEpisodeTitles(ctx, items); err != nil {
		return out, err
	}
	out.Items = items
	return out, nil
}

func selectedEpisodeReleased(t *core.Title, now time.Time) bool {
	if t.Type == core.TypeEpisode && !t.EpisodeAirDate.IsZero() {
		return !t.EpisodeAirDate.After(now)
	}
	return t.IsReleased(now)
}
