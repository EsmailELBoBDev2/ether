package state

import (
	"context"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// completedBetweenQuery is one row per key the viewer completed in the
// window, latest completion first. It reads only this viewer's completed rows
// on state_history_continue_series (user_id, completed, rating_key, viewed_at),
// covering, so started-only rows and later rows never take a share of a
// fixed page (#799) and nothing in it grows with the library.
const completedBetweenQuery = `SELECT rating_key, MAX(viewed_at) AS latest FROM state_history
 WHERE user_id=? AND completed=1 AND viewed_at>=? AND viewed_at<?
 GROUP BY rating_key ORDER BY latest DESC, rating_key`

// CompletedBetween lists the distinct keys userID completed in [from, to),
// most recently completed first. Each entry carries the key and its latest
// completion in the window.
func (s *State) CompletedBetween(ctx context.Context, userID int64, from, to time.Time) ([]core.HistoryEntry, error) {
	if userID <= 0 || !to.After(from) {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, completedBetweenQuery, userID, from.UnixNano(), to.UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.HistoryEntry
	for rows.Next() {
		var key, at int64
		if err := rows.Scan(&key, &at); err != nil {
			return nil, err
		}
		out = append(out, core.HistoryEntry{UserID: userID, Key: core.RatingKey(key), ViewedAt: time.Unix(0, at).UTC(), Completed: true})
	}
	return out, rows.Err()
}
