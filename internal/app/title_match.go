package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/releaseparse"
	"github.com/braedonsaunders/ether/internal/store"
)

func init() {
	store.RegisterAfter("title_match", "state", "favorites", "collections", "watchlist_episodes", "availability_gate", "playback")
	store.Register("title_match", 1, `
CREATE INDEX state_history_title_match ON state_history(rating_key);
CREATE INDEX state_playlist_items_title_match ON state_playlist_items(rating_key);
CREATE INDEX favorites_title_match ON favorites_items(kind,subject_id);
CREATE INDEX collections_members_title_match ON collections_members(key);
CREATE INDEX watchlist_episodes_title_match ON watchlist_episodes(item_key);
CREATE INDEX availability_gate_title_match ON availability_gate(rating_key);
CREATE INDEX playback_resume_title_match ON playback_resume(rating_key);
`)
}

var _ frontend.TitleMatcher = (*App)(nil)

func (a *App) SearchTitleMatches(ctx context.Context, typ core.MediaType, query string, year int) ([]core.Title, error) {
	if a.catalog == nil {
		return nil, core.ErrNotAvailable
	}
	return a.catalog.SearchTitleMatches(ctx, typ, query, year)
}

func (a *App) RefreshCatalogTitle(ctx context.Context, key core.RatingKey) error {
	if a.catalog == nil {
		return core.ErrNotAvailable
	}
	if _, err := a.catalog.StoredTitle(ctx, key); err != nil {
		return err
	}
	return a.appTitleRefresher().run(ctx, key)
}

func (a *App) ApplyTitleMatch(ctx context.Context, from core.RatingKey, id int64) (core.RatingKey, error) {
	if a.catalog == nil || id <= 0 || id >= 100_000_000 {
		return 0, core.ErrNotFound
	}
	var to core.RatingKey
	switch from.Kind() {
	case core.KindMovie:
		to = core.MovieKey(id)
	case core.KindShow:
		to = core.ShowKey(id)
	default:
		return 0, core.ErrNotFound
	}
	old, err := a.catalog.StoredTitle(ctx, from)
	if err != nil {
		return 0, err
	}
	// Validate the candidate before publishing anything or moving bindings.
	next, err := a.catalog.RefreshTitle(ctx, to, false)
	if err != nil {
		return 0, err
	}
	if next.Adult {
		return 0, core.ErrNotFound
	}
	if err := a.appTitleRefresher().run(ctx, to); err != nil {
		return 0, err
	}
	if from == to {
		return to, nil
	}
	if err := a.moveTitleBindings(ctx, old, next); err != nil {
		return 0, err
	}
	a.catalog.TitleBindingsChanged(old.Key, next.Key)
	if a.availability != nil {
		a.availability.InvalidateTitleMatches()
	}
	if a.resolver != nil {
		a.resolver.InvalidateDetailVariants()
	}
	return to, nil
}

type matchRange struct {
	first, last, delta int64
}

func matchRanges(from, to core.RatingKey) []matchRange {
	ranges := []matchRange{{int64(from), int64(from), int64(to - from)}}
	if from.Kind() == core.KindShow {
		f, t := from.TMDB(), to.TMDB()
		ranges = append(ranges,
			matchRange{int64(core.SeasonKey(f, 0)), int64(core.SeasonKey(f, 9999)), int64(core.SeasonKey(t, 0) - core.SeasonKey(f, 0))},
			matchRange{int64(core.EpisodeKey(f, 0, 0)), int64(core.EpisodeKey(f, 999, 9999)), int64(core.EpisodeKey(t, 0, 0) - core.EpisodeKey(f, 0, 0))})
	}
	return ranges
}

func (a *App) moveTitleBindings(ctx context.Context, old, next *core.Title) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	dialect := store.DialectOf(a.db)
	for _, key := range []core.RatingKey{min(old.Key, next.Key), max(old.Key, next.Key)} {
		if err := store.LockKey(ctx, dialect, tx, "title-match:"+key.String()); err != nil {
			return err
		}
	}

	ranges := matchRanges(old.Key, next.Key)
	var changedPins []core.RatingKey
	changedCollections, err := moveCollectionMatches(ctx, tx, old.Key, next.Key)
	if err != nil {
		return err
	}
	for _, r := range ranges {
		if err := moveWatchMatch(ctx, tx, r); err != nil {
			return err
		}
		if err := moveFavoriteMatch(ctx, tx, r); err != nil {
			return err
		}
		if a.availability != nil {
			if err := a.availability.RebindTitleReleases(ctx, tx, r.first, r.last, next); err != nil {
				return err
			}
		}
		if a.resolver != nil {
			keys, err := a.resolver.RebindTitlePins(ctx, tx, r.first, r.last, r.delta, next.Title)
			if err != nil {
				return err
			}
			changedPins = append(changedPins, keys...)
		}
		// Destination rows win duplicate bindings. Rows with independent
		// identities, such as playlist entries, keep their own positions.
		for _, binding := range []struct {
			table, key string
			unique     []string
		}{
			{"state_history", "rating_key", []string{"user_id", "viewed_at"}},
			{"state_playlist_items", "rating_key", nil},
			{"playback_resume", "rating_key", []string{"user_id"}},
			{"collections_members", "key", nil},
			{"availability_releases", "rating_key", []string{"id", "source_id"}},
			{"availability_gate", "rating_key", []string{"policy"}},
			{"catalog_release_evidence", "rating_key", []string{"release_id", "source_id"}},
			{"catalog_release_audio_langs", "rating_key", []string{"release_id", "source_id", "lang"}},
			{"catalog_owner_includes", "key", []string{}},
			{"catalog_owner_tags", "key", []string{"tag"}},
		} {
			if err := moveMatchRows(ctx, tx, r, binding.table, binding.key, binding.unique); err != nil {
				return err
			}
		}
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO watchlist_items(user_id,item_key,source,title,added,checked,available,notified,warmed)
		SELECT user_id,?,source,?,added,checked,available,notified,warmed FROM watchlist_items WHERE item_key=?
		ON CONFLICT(user_id,item_key,source) DO NOTHING`, int64(next.Key), next.Title, int64(old.Key)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM watchlist_items WHERE item_key=?`, int64(old.Key)); err != nil {
		return err
	}
	if old.Type == core.TypeShow {
		if _, err := tx.ExecContext(ctx, `INSERT INTO watchlist_episodes(user_id,item_key,episode_key,at)
			SELECT user_id,?,episode_key+?,at FROM watchlist_episodes WHERE item_key=?
			ON CONFLICT(user_id,item_key,episode_key) DO NOTHING`, int64(next.Key), ranges[2].delta, int64(old.Key)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM watchlist_episodes WHERE item_key=?`, int64(old.Key)); err != nil {
			return err
		}
	}
	// Vault content keys and pack offsets are independent of title identity.
	// Movie and TV TMDB IDs overlap, so constrain by the vault's numbering.
	const episodic = "(season<>0 OR episode<>0 OR EXISTS (SELECT 1 FROM vault_members m WHERE m.key=vault_releases.key AND m.episode>0))"
	numbering := "NOT " + episodic
	if old.Type == core.TypeShow {
		numbering = episodic
	}
	if _, err := tx.ExecContext(ctx, `UPDATE vault_releases SET tmdb=?, title_key=?, year=?, owner_match=1 WHERE tmdb=? AND `+numbering,
		next.IDs.TMDB, releaseparse.NormalizeTitle(next.Title), next.Year, old.IDs.TMDB); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE catalog_titles SET browsable=1 WHERE key=?
		AND EXISTS (SELECT 1 FROM catalog_owner_includes WHERE key=?)`, int64(next.Key), int64(next.Key)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if a.resolver != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		a.resolver.TitlePinsRebound(ctx, changedPins)
	}
	if a.collections != nil {
		a.collections.TitleMatchesChanged(changedCollections)
	}
	return nil
}

func moveMatchRows(ctx context.Context, tx *sql.Tx, r matchRange, table, key string, unique []string) error {
	where := "old." + key + " BETWEEN ? AND ?"
	if unique != nil {
		match := "dest." + key + "=old." + key + "+?"
		for _, column := range unique {
			match += " AND dest." + column + "=old." + column
		}
		if table == "state_history" {
			match += " AND dest.imported=1 AND old.imported=1"
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM "+table+" AS old WHERE "+where+" AND EXISTS (SELECT 1 FROM "+table+" AS dest WHERE "+match+")", r.first, r.last, r.delta)
		if err != nil {
			return fmt.Errorf("title match: %s: %w", table, err)
		}
	}
	_, err := tx.ExecContext(ctx, "UPDATE "+table+" AS old SET "+key+"="+key+"+? WHERE "+where, r.delta, r.first, r.last)
	if err != nil {
		return fmt.Errorf("title match: %s: %w", table, err)
	}
	return nil
}

func moveWatchMatch(ctx context.Context, tx *sql.Tx, r matchRange) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO state_watch(user_id,rating_key,view_offset_ns,duration_ns,view_count,counted,last_viewed_at,user_rating,updated_at,version,watch_at,rating_at)
		SELECT user_id,rating_key+?,view_offset_ns,duration_ns,view_count,counted,last_viewed_at,user_rating,?,version+1,watch_at,rating_at
		FROM state_watch WHERE rating_key BETWEEN ? AND ?
		ON CONFLICT(user_id,rating_key) DO UPDATE SET
		view_offset_ns=CASE WHEN excluded.watch_at>state_watch.watch_at OR excluded.last_viewed_at>state_watch.last_viewed_at THEN excluded.view_offset_ns ELSE state_watch.view_offset_ns END,
		duration_ns=CASE WHEN excluded.watch_at>state_watch.watch_at OR excluded.last_viewed_at>state_watch.last_viewed_at THEN excluded.duration_ns ELSE state_watch.duration_ns END,
		counted=CASE WHEN excluded.watch_at>state_watch.watch_at OR excluded.last_viewed_at>state_watch.last_viewed_at THEN excluded.counted ELSE state_watch.counted END,
		view_count=CASE WHEN excluded.view_count>state_watch.view_count THEN excluded.view_count ELSE state_watch.view_count END,
		last_viewed_at=CASE WHEN excluded.last_viewed_at>state_watch.last_viewed_at THEN excluded.last_viewed_at ELSE state_watch.last_viewed_at END,
		user_rating=CASE WHEN excluded.rating_at>state_watch.rating_at OR state_watch.rating_at=0 AND state_watch.user_rating=0 THEN excluded.user_rating ELSE state_watch.user_rating END,
		rating_at=CASE WHEN excluded.rating_at>state_watch.rating_at THEN excluded.rating_at ELSE state_watch.rating_at END,
		watch_at=CASE WHEN excluded.watch_at>state_watch.watch_at THEN excluded.watch_at ELSE state_watch.watch_at END,
		updated_at=excluded.updated_at,version=state_watch.version+1`, r.delta, time.Now().UnixNano(), r.first, r.last)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM state_watch WHERE rating_key BETWEEN ? AND ?`, r.first, r.last)
	return err
}

func moveFavoriteMatch(ctx context.Context, tx *sql.Tx, r matchRange) error {
	first, last := core.RatingKey(r.first).String(), core.RatingKey(r.last).String()
	_, err := tx.ExecContext(ctx, `INSERT INTO favorites_items(viewer_id,kind,subject_id,added_at,sort_order,role)
		SELECT viewer_id,kind,CAST(CAST(subject_id AS BIGINT)+? AS TEXT),added_at,sort_order,role FROM favorites_items
		WHERE kind='title' AND subject_id BETWEEN ? AND ?
		ON CONFLICT(viewer_id,kind,subject_id) DO NOTHING`, r.delta, first, last)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM favorites_items WHERE kind='title' AND subject_id BETWEEN ? AND ?`, first, last)
	return err
}

func moveCollectionMatches(ctx context.Context, tx *sql.Tx, from, to core.RatingKey) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,spec_json FROM collections_specs WHERE id IN
		(SELECT spec_id FROM collections_members WHERE key=?
		UNION SELECT spec_id FROM collections_title_refs WHERE title_key=?)`, int64(from), from.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type specRow struct {
		id  int64
		raw string
	}
	var affected []specRow
	for rows.Next() {
		var row specRow
		if err := rows.Scan(&row.id, &row.raw); err != nil {
			return nil, err
		}
		affected = append(affected, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	ids := make([]int64, 0, len(affected))
	for _, row := range affected {
		var spec core.CollectionSpec
		if err := json.Unmarshal([]byte(row.raw), &spec); err != nil {
			return nil, err
		}
		raw := row.raw
		dirty := false
		for i := range spec.Sources {
			source := &spec.Sources[i]
			if source.Kind != "titles" || !slices.Contains(source.Keys, from.String()) {
				continue
			}
			for j, key := range source.Keys {
				if key == from.String() {
					source.Keys[j] = to.String()
				}
			}
			var seen bool
			source.Keys = slices.DeleteFunc(source.Keys, func(key string) bool {
				if key != to.String() {
					return false
				}
				duplicate := seen
				seen = true
				return duplicate
			})
			dirty = true
		}
		if dirty {
			encoded, err := json.Marshal(spec)
			if err != nil {
				return nil, err
			}
			raw = string(encoded)
		}
		res, err := tx.ExecContext(ctx, `UPDATE collections_specs SET spec_json=?,build_hash='',changed_at=?
			WHERE id=? AND spec_json=?`, raw, time.Now().Unix(), row.id, row.raw)
		if err != nil {
			return nil, err
		}
		if updated, err := res.RowsAffected(); err != nil || updated != 1 {
			return nil, core.ErrNotAvailable
		}
		// Preserve the chosen entry's position unless the destination was
		// already in the collection.
		if _, err := tx.ExecContext(ctx, `DELETE FROM collections_members WHERE spec_id=? AND key=?
			AND EXISTS (SELECT 1 FROM collections_members WHERE spec_id=? AND key=?)`, row.id, int64(from), row.id, int64(to)); err != nil {
			return nil, err
		}
		ids = append(ids, row.id)
	}
	return ids, nil
}
