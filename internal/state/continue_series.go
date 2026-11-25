package state

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

const continueSeriesIndex = `CREATE INDEX state_history_continue_series ON state_history(user_id,completed,rating_key,viewed_at DESC,id DESC);`

func init() {
	store.Register("state", 11, continueSeriesIndex)
}

// Rank only this viewer's completed episodes using the covering viewer-first
// index. Limit before loading snapshots, then fetch each selected row by its
// primary key rather than scanning history a second time. Both databases use
// integer division for series identity and support these window/CTE clauses.
var continueSeriesQuery = fmt.Sprintf(`WITH input(uid,lo,hi,page_size) AS (VALUES (CAST(? AS BIGINT),CAST(? AS BIGINT),CAST(? AS BIGINT),CAST(? AS BIGINT))),
 episodes AS (
 SELECT id,viewed_at,2000000000000000+(rating_key-4000000000000000)/10000000 AS show_key
 FROM state_history WHERE user_id=(SELECT uid FROM input) AND completed=1
 AND rating_key>=(SELECT lo FROM input) AND rating_key<(SELECT hi FROM input)
 UNION ALL
 SELECT h.id,h.viewed_at,a.show_key FROM state_history h JOIN catalog_allocated_child a ON a.key=h.rating_key AND a.kind=4
 WHERE h.user_id=(SELECT uid FROM input) AND h.completed=1 AND h.rating_key>%d AND h.rating_key<%d
 ), ranked AS (
 SELECT id,viewed_at,ROW_NUMBER() OVER (PARTITION BY show_key ORDER BY viewed_at DESC,id DESC) AS position FROM episodes
 ), selected AS (
 SELECT id FROM ranked WHERE position=1 ORDER BY viewed_at DESC,id DESC LIMIT (SELECT page_size FROM input)
 ) SELECT h.id,h.user_id,h.rating_key,h.viewed_at,h.completed,h.view_type,h.device_id,h.snapshot
 FROM selected JOIN state_history h ON h.id=selected.id
 ORDER BY h.viewed_at DESC,h.id DESC`, core.AllocatedEpisodeBase, core.AllocatedEpisodeBase+core.AllocatedKeyMaxSeq)

// ContinueSeriesHistory returns the latest completed episode per series,
// newest first, without an age cutoff. Reduce before limiting so repeated
// episodes and movies cannot evict another series from the candidate list.
func (s *State) ContinueSeriesHistory(ctx context.Context, uid int64, limit int) ([]core.HistoryEntry, error) {
	if uid <= 0 {
		return nil, nil
	}
	// Capture the pending writes before reading history: an entry that
	// drains during this read is already represented by the durable state.
	var pending map[core.RatingKey]pendingWrite
	if s.pending != nil {
		pending = s.pending.snapshot(uid, s.pending.userKeys(uid))
	}
	limit = min(max(limit, 1), 200)
	rows, err := s.db.QueryContext(ctx, continueSeriesQuery, uid,
		int64(core.EpisodeKey(0, 0, 0)), int64(core.EpisodeKey(0, 0, 0))+1000000000000000, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.HistoryEntry
	for rows.Next() {
		h, err := scanHistory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	var keys []core.RatingKey
	for key, write := range pending {
		if key.Kind() == core.KindEpisode && (write.kind == "scrobble" || (write.kind == "progress" && write.progress.state != "progress")) {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return out, nil
	}
	statuses, err := s.GetMany(ctx, uid, keys)
	if err != nil {
		return nil, err
	}
	for _, key := range keys {
		write := pending[key]
		ws := statuses[key]
		progress := write.progress
		completes := write.kind == "scrobble" || progress.completes()
		if !completes && !strings.HasPrefix(progress.state, "explicit-") && ws.Duration > 0 {
			// Timelines can omit duration and reuse the last reported value.
			completes = float64(progress.offset) >= WatchedThreshold*float64(ws.Duration)
		}
		if completes && ws.ViewCount > 0 && ws.ViewOffset == 0 && ws.LastViewedAt.Equal(progress.at) {
			out = append(out, core.HistoryEntry{UserID: uid, Key: key, ViewedAt: progress.at, Completed: true})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].ViewedAt.Equal(out[j].ViewedAt) {
			return out[i].ViewedAt.After(out[j].ViewedAt)
		}
		if out[i].ID == 0 && out[j].ID == 0 {
			return pending[out[i].Key].seq > pending[out[j].Key].seq
		}
		if out[i].ID == 0 || out[j].ID == 0 {
			return out[i].ID == 0
		}
		return out[i].ID > out[j].ID
	})
	keys = keys[:0]
	for _, entry := range out {
		keys = append(keys, entry.Key)
	}
	parents, err := s.allocatedAncestry(ctx, keys)
	if err != nil {
		return nil, err
	}
	seen := make(map[core.RatingKey]bool, len(out))
	distinct := out[:0]
	for _, entry := range out {
		show := parents[entry.Key]
		if show == 0 {
			if entry.Key.Allocated() {
				show = entry.Key
			} else {
				show = core.ShowKey(entry.Key.TMDB())
			}
		}
		if seen[show] {
			continue
		}
		seen[show] = true
		distinct = append(distinct, entry)
		if len(distinct) == limit {
			break
		}
	}
	return distinct, nil
}
