package catalog

import (
	"context"
	"sort"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

type EpisodeCoordinate struct {
	Key, ParentKey            core.RatingKey
	IDs                       core.IDs
	Season, Episode, Absolute int
	AirDate                   time.Time
}

type EpisodeLayout struct {
	Order    core.EpisodeOrder
	Seasons  map[int]core.RatingKey
	Episodes []EpisodeCoordinate
	Complete map[int]bool
}

func providerLayout(show *core.Title, order core.EpisodeOrder) bool {
	return order != tmdbAired || show.Key.Allocated()
}

func (c *Catalog) CachedEpisodeLayout(ctx context.Context, show core.RatingKey) (EpisodeLayout, bool, error) {
	t, err := c.StoredTitle(ctx, show)
	if err != nil {
		return EpisodeLayout{}, false, err
	}
	effective, err := c.effectiveEpisodeOrder(ctx, t)
	if err != nil {
		return EpisodeLayout{}, false, err
	}
	return c.readEpisodeLayout(ctx, t, effective.Order)
}

func (c *Catalog) EpisodeLayout(ctx context.Context, show core.RatingKey) (EpisodeLayout, error) {
	t, err := c.StoredTitle(ctx, show)
	if err != nil {
		return EpisodeLayout{}, err
	}
	effective, err := c.effectiveEpisodeOrder(ctx, t)
	if err != nil {
		return EpisodeLayout{}, err
	}
	if providerLayout(t, effective.Order) {
		if _, err := c.ensureOrderManifest(ctx, t, effective.Order); err != nil {
			return EpisodeLayout{}, err
		}
	}
	layout, _, err := c.readEpisodeLayout(ctx, t, effective.Order)
	return layout, err
}

func (c *Catalog) readEpisodeLayout(ctx context.Context, show *core.Title, order core.EpisodeOrder) (EpisodeLayout, bool, error) {
	out := EpisodeLayout{Order: order, Seasons: map[int]core.RatingKey{}, Complete: map[int]bool{}}
	if !providerLayout(show, order) {
		legacy, err := c.ReleaseEpisodeLayout(ctx, show.Key)
		if err != nil {
			return out, false, err
		}
		rows, err := c.db.QueryContext(ctx, `SELECT key,tmdb_id,tvdb,imdb,season,episode,released_ts FROM catalog_titles
 WHERE show_key=? AND type='episode' ORDER BY season,episode,key`, show.Key)
		if err != nil {
			return out, false, err
		}
		defer rows.Close()
		for rows.Next() {
			var ep EpisodeCoordinate
			var stamp int64
			if err := rows.Scan(&ep.Key, &ep.IDs.TMDB, &ep.IDs.TVDB, &ep.IDs.IMDB, &ep.Season, &ep.Episode, &stamp); err != nil {
				return out, false, err
			}
			if stamp != 0 {
				ep.AirDate = time.Unix(stamp, 0).UTC()
			}
			ep.ParentKey = core.SeasonKey(show.IDs.TMDB, ep.Season)
			ep.Absolute = legacy.Numbers[core.EpisodeNumber{Season: ep.Season, Episode: ep.Episode}]
			out.Seasons[ep.Season] = ep.ParentKey
			out.Episodes = append(out.Episodes, ep)
		}
		for n := range legacy.Seasons {
			out.Seasons[n] = core.SeasonKey(show.IDs.TMDB, n)
		}
		out.Complete = legacy.Complete
		return out, len(out.Seasons) > 0, rows.Err()
	}
	seasons, stamp, err := c.orderManifest(ctx, show.Key, order)
	if err != nil || stamp == 0 {
		return out, false, err
	}
	for _, season := range seasons {
		out.Seasons[season.Season] = season.key
		out.Complete[season.Season] = season.fetched != 0
	}
	rows, err := c.db.QueryContext(ctx, `SELECT e.episode_key,e.season,e.episode,e.absolute,e.air_date,t.tmdb_id,t.tvdb,t.imdb
 FROM catalog_order_episodes e JOIN catalog_titles t ON t.key=e.episode_key
 WHERE e.show_key=? AND e.provider=? AND e.order_kind=? ORDER BY e.season,e.episode`, show.Key, order.Source, order.Kind)
	if err != nil {
		return out, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var ep EpisodeCoordinate
		var date string
		if err := rows.Scan(&ep.Key, &ep.Season, &ep.Episode, &ep.Absolute, &date, &ep.IDs.TMDB, &ep.IDs.TVDB, &ep.IDs.IMDB); err != nil {
			return out, false, err
		}
		ep.AirDate = metadataDate(date)
		ep.ParentKey = out.Seasons[ep.Season]
		out.Episodes = append(out.Episodes, ep)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, false, err
	}
	if order.Source != core.MetadataTMDB && show.IDs.TMDB > 0 && !show.Key.Allocated() {
		if err := c.addCanonicalSpecials(ctx, show, order, &out); err != nil {
			return out, false, err
		}
	}
	return out, true, nil
}

func (c *Catalog) addCanonicalSpecials(ctx context.Context, show *core.Title, order core.EpisodeOrder, out *EpisodeLayout) error {
	rows, err := c.db.QueryContext(ctx, `SELECT t.key,t.episode,t.tmdb_id,t.tvdb,t.imdb,t.released_ts FROM catalog_titles t
 WHERE t.show_key=? AND t.type='episode' AND t.season=0
 AND t.key>=? AND t.key<?
 AND NOT EXISTS (SELECT 1 FROM catalog_order_episodes e WHERE e.show_key=t.show_key AND e.provider=? AND e.order_kind=?
 AND (e.episode_key=t.key OR (e.season=0 AND e.episode=t.episode)))`, show.Key,
		core.EpisodeKey(show.IDs.TMDB, 0, 0), core.EpisodeKey(show.IDs.TMDB, 1, 0), order.Source, order.Kind)
	if err != nil {
		return err
	}
	defer rows.Close()
	parent := out.Seasons[0]
	if parent == 0 {
		parent = core.SeasonKey(show.IDs.TMDB, 0)
	}
	for rows.Next() {
		ep := EpisodeCoordinate{ParentKey: parent}
		var date int64
		if err := rows.Scan(&ep.Key, &ep.Episode, &ep.IDs.TMDB, &ep.IDs.TVDB, &ep.IDs.IMDB, &date); err != nil {
			return err
		}
		if date != 0 {
			ep.AirDate = time.Unix(date, 0).UTC()
		}
		out.Seasons[0] = parent
		out.Episodes = append(out.Episodes, ep)
	}
	sort.Slice(out.Episodes, func(i, j int) bool {
		a, b := out.Episodes[i], out.Episodes[j]
		if a.Season != b.Season {
			return a.Season < b.Season
		}
		return a.Episode < b.Episode
	})
	return rows.Err()
}

func (l EpisodeLayout) EpisodeAt(season, episode int) (EpisodeCoordinate, bool) {
	i := sort.Search(len(l.Episodes), func(i int) bool {
		e := l.Episodes[i]
		return e.Season > season || e.Season == season && e.Episode >= episode
	})
	if i < len(l.Episodes) && l.Episodes[i].Season == season && l.Episodes[i].Episode == episode {
		return l.Episodes[i], true
	}
	return EpisodeCoordinate{}, false
}

// EpisodeOn returns only an unambiguous match on the provider's calendar date.
func (l EpisodeLayout) EpisodeOn(date time.Time) (EpisodeCoordinate, bool) {
	var found EpisodeCoordinate
	if date.IsZero() {
		return found, false
	}
	day := date.Format(time.DateOnly)
	for _, ep := range l.Episodes {
		if !ep.AirDate.IsZero() && ep.AirDate.Format(time.DateOnly) == day {
			if found.Key != 0 {
				return EpisodeCoordinate{}, false
			}
			found = ep
		}
	}
	return found, found.Key != 0
}

type EpisodeGap struct {
	Order             core.EpisodeOrder
	Count, Difference int
	Known             bool
}

func (c *Catalog) EpisodeGap(ctx context.Context, show core.RatingKey) ([]EpisodeGap, error) {
	current, err := c.EffectiveEpisodeOrder(ctx, show)
	if err != nil {
		return nil, err
	}
	orders := []core.EpisodeOrder{current.Order}
	for _, source := range c.metadataSources.Configured() {
		if !source.Capabilities().Shows || !validSourceOrder(source, core.OrderAired) {
			continue
		}
		order := core.EpisodeOrder{Source: source.ID(), Kind: core.OrderAired}
		if order != current.Order {
			orders = append(orders, order)
		}
	}
	out := make([]EpisodeGap, 0, len(orders))
	for _, order := range orders {
		item := EpisodeGap{Order: order}
		seasons, stamp, err := c.orderManifest(ctx, show, order)
		if err != nil {
			return nil, err
		}
		if stamp != 0 {
			item.Known = true
			for _, season := range seasons {
				if season.EpisodeCount < 0 {
					item.Known = false
					break
				}
				item.Count += season.EpisodeCount
			}
		} else if order == tmdbAired && !show.Allocated() {
			var n int
			err = c.db.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(child_count),0) FROM catalog_titles WHERE show_key=? AND type='season'`, show).Scan(&n, &item.Count)
			if err != nil {
				return nil, err
			}
			item.Known = n > 0
		}
		if !item.Known {
			item.Count = 0
		}
		if len(out) > 0 && item.Known && out[0].Known {
			item.Difference = item.Count - out[0].Count
		}
		out = append(out, item)
	}
	return out, nil
}

// EpisodeMatchRanges maps known children by external identity, then by their
// effective coordinates. Each identity is assigned at most once.
func (c *Catalog) EpisodeMatchRanges(ctx context.Context, from, to core.RatingKey) ([]core.TitleKeyRange, error) {
	out := []core.TitleKeyRange{{First: int64(from), Last: int64(from), Delta: int64(to - from)}}
	if from == to || from.Kind() != core.KindShow || to.Kind() != core.KindShow {
		return out, nil
	}
	old, _, err := c.CachedEpisodeLayout(ctx, from)
	if err != nil {
		return nil, err
	}
	next, _, err := c.CachedEpisodeLayout(ctx, to)
	if err != nil {
		return nil, err
	}
	appendPair := func(a, b core.RatingKey) {
		if a != 0 && b != 0 && a != b {
			out = append(out, core.TitleKeyRange{First: int64(a), Last: int64(a), Delta: int64(b - a)})
		}
	}
	for n, key := range old.Seasons {
		appendPair(key, next.Seasons[n])
	}
	used := map[core.RatingKey]bool{}
	mapped := map[core.RatingKey]bool{}
	facts := map[episodeMatchFact][]EpisodeCoordinate{}
	for _, ep := range next.Episodes {
		for _, fact := range episodeFacts(ep.IDs, "", "") {
			facts[fact] = append(facts[fact], ep)
		}
	}
	for _, ep := range old.Episodes {
		for _, fact := range episodeFacts(ep.IDs, "", "") {
			matches := facts[fact]
			if len(matches) != 1 || used[matches[0].Key] || !compatibleEpisodeIDs(ep.IDs, matches[0].IDs) {
				continue
			}
			appendPair(ep.Key, matches[0].Key)
			mapped[ep.Key], used[matches[0].Key] = true, true
			break
		}
	}
	for _, ep := range old.Episodes {
		if mapped[ep.Key] {
			continue
		}
		if target, ok := next.EpisodeAt(ep.Season, ep.Episode); ok && !used[target.Key] {
			appendPair(ep.Key, target.Key)
			used[target.Key] = true
		}
	}
	sort.Slice(out[1:], func(i, j int) bool { return out[i+1].First < out[j+1].First })
	return out, nil
}

// HydrateEpisodeLayout loads the selected layout on explicit use, including
// the destination tree of a user-requested match correction.
func (c *Catalog) HydrateEpisodeLayout(ctx context.Context, show core.RatingKey) (EpisodeLayout, error) {
	t, err := c.StoredTitle(ctx, show)
	if err != nil {
		return EpisodeLayout{}, err
	}
	effective, err := c.effectiveEpisodeOrder(ctx, t)
	if err != nil {
		return EpisodeLayout{}, err
	}
	if t.Key.Allocated() && t.IDs.TMDB == 0 && t.IDs.TVDB > 0 && effective.Order == tmdbAired {
		effective.Order = core.EpisodeOrder{Source: core.MetadataTVDB, Kind: core.OrderAired}
	}

	if !providerLayout(t, effective.Order) {
		seasons, err := c.seasonsOfShow(ctx, t.IDs.TMDB)
		if err != nil {
			return EpisodeLayout{}, err
		}
		if err := c.loadSeasons(ctx, t.IDs.TMDB, seasons); err != nil {
			return EpisodeLayout{}, err
		}
	} else {
		seasons, err := c.ensureOrderManifest(ctx, t, effective.Order)
		if err != nil {
			return EpisodeLayout{}, err
		}
		for _, season := range seasons {
			if err := c.ensureOrderSeason(ctx, t, effective.Order, season); err != nil {
				return EpisodeLayout{}, err
			}
		}
	}
	layout, _, err := c.readEpisodeLayout(ctx, t, effective.Order)
	return layout, err
}
