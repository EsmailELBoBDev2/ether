package catalog

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

func init() {
	store.RegisterAfter("catalog_episode_release", "catalog")
	store.Register("catalog_episode_release", 1, `CREATE TABLE catalog_episode_release(key BIGINT PRIMARY KEY, earliest_ts BIGINT NOT NULL);`)
}

// RecordPlayableEpisode remembers actual playable evidence independently of
// metadata refreshes. released_ts is the earliest known effective release date;
// the evidence survives a later linear air date returned by the metadata source.
func (c *Catalog) RecordPlayableEpisode(ctx context.Context, key core.RatingKey, at time.Time) error {
	if key.Kind() != core.KindEpisode || at.IsZero() {
		return nil
	}
	return c.RecordPlayableEpisodes(ctx, []core.RatingKey{key}, at)
}

// RecordPlayableEpisodes records playable evidence for a batch of episodes (and
// their parent season containers) in a single transaction using bounded
// set-based SQL statements instead of one transaction per episode.
func (c *Catalog) RecordPlayableEpisodes(ctx context.Context, keys []core.RatingKey, at time.Time) error {
	if len(keys) == 0 || at.IsZero() {
		return nil
	}
	seen := make(map[core.RatingKey]bool, len(keys)*2)
	targets := make([]core.RatingKey, 0, len(keys)*2)
	addTarget := func(k core.RatingKey) {
		if k > 0 && !seen[k] {
			seen[k] = true
			targets = append(targets, k)
		}
	}
	for _, key := range keys {
		if key.Kind() != core.KindEpisode {
			continue
		}
		// A playable premiere also releases its season navigation container.
		addTarget(key)
		addTarget(core.SeasonKey(key.TMDB(), key.Season()))
	}
	if len(targets) == 0 {
		return nil
	}

	atTS := at.Unix()
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	const batch = 500
	for chunk := range store.Chunks(targets, batch) {
		var sb strings.Builder
		sb.Grow(64 + len(chunk)*6)
		sb.WriteString(`INSERT INTO catalog_episode_release(key,earliest_ts) VALUES `)
		insertArgs := make([]any, 0, len(chunk)*2)
		updateArgs := make([]any, len(chunk))
		for i, k := range chunk {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString("(?,?)")
			insertArgs = append(insertArgs, int64(k), atTS)
			updateArgs[i] = int64(k)
		}
		sb.WriteString(` ON CONFLICT(key) DO UPDATE SET earliest_ts=CASE WHEN excluded.earliest_ts<catalog_episode_release.earliest_ts THEN excluded.earliest_ts ELSE catalog_episode_release.earliest_ts END`)
		if _, err := tx.ExecContext(ctx, sb.String(), insertArgs...); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE catalog_titles SET released_ts=(SELECT earliest_ts FROM catalog_episode_release e WHERE e.key=catalog_titles.key),browsable=1 WHERE key IN (`+placeholders(len(chunk))+`) AND EXISTS(SELECT 1 FROM catalog_episode_release e WHERE e.key=catalog_titles.key AND (catalog_titles.released_ts=0 OR e.earliest_ts<catalog_titles.released_ts))`, updateArgs...); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	c.writeGen.Add(1)
	return nil
}

func applyEpisodeRelease(ctx context.Context, tx *sql.Tx, key core.RatingKey) error {
	_, err := tx.ExecContext(ctx, `UPDATE catalog_titles SET released_ts=(SELECT earliest_ts FROM catalog_episode_release WHERE key=?),browsable=1 WHERE key=? AND EXISTS(SELECT 1 FROM catalog_episode_release e WHERE e.key=catalog_titles.key AND (catalog_titles.released_ts=0 OR e.earliest_ts<catalog_titles.released_ts))`, int64(key), int64(key))
	return err
}
