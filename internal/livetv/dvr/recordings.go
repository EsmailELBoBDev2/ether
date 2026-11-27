package dvr

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// Recording is a file a job produced. Status is JobRecording while the job
// runs, then JobCompleted or JobPartial.
type Recording struct {
	ID         string    `json:"id"`
	UserID     int64     `json:"userId"`
	JobID      string    `json:"jobId"`
	RuleID     string    `json:"ruleId,omitempty"`
	MatchKey   string    `json:"matchKey"`
	Path       string    `json:"path"`
	Status     JobStatus `json:"status"`
	SizeBytes  int64     `json:"sizeBytes"`
	DurationMS int64     `json:"durationMs"`
	Protect    bool      `json:"protect,omitempty"`
	Programme  Programme `json:"programme"`
	// Gaps counts the times the stream dropped and was tuned again.
	Gaps    int       `json:"gaps,omitempty"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// SaveRecording inserts or replaces a recording.
func (s *Store) SaveRecording(ctx context.Context, r *Recording) error {
	now := s.now().UTC()
	if r.ID == "" {
		r.ID = newID()
	}
	if r.Created.IsZero() {
		r.Created = now
	}
	r.Updated = now
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO livetv_dvr_recordings(id,user_id,job_id,rule_id,match_key,path,status,size_bytes,duration_ms,protect,data,created_ms,updated_ms) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET path=excluded.path,status=excluded.status,size_bytes=excluded.size_bytes,duration_ms=excluded.duration_ms,protect=excluded.protect,data=excluded.data,updated_ms=excluded.updated_ms`,
		r.ID, r.UserID, r.JobID, r.RuleID, r.MatchKey, r.Path, string(r.Status), r.SizeBytes, r.DurationMS, boolInt(r.Protect), string(data), r.Created.UnixMilli(), now.UnixMilli())
	return err
}

func (s *Store) Recording(ctx context.Context, id string) (Recording, error) {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM livetv_dvr_recordings WHERE id=?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return Recording{}, core.ErrNotFound
	}
	if err != nil {
		return Recording{}, err
	}
	var r Recording
	return r, json.Unmarshal([]byte(data), &r)
}

// Recordings lists a user's recordings (all users for 0), newest first.
func (s *Store) Recordings(ctx context.Context, userID int64) ([]Recording, error) {
	q := `SELECT data FROM livetv_dvr_recordings`
	var args []any
	if userID != 0 {
		q += ` WHERE user_id=?`
		args = append(args, userID)
	}
	q += ` ORDER BY created_ms DESC, id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Recording{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var r Recording
		if err := json.Unmarshal([]byte(data), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteRecording removes the row. The caller deletes the file.
func (s *Store) DeleteRecording(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM livetv_dvr_recordings WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return core.ErrNotFound
	}
	return nil
}

// AcquireLease claims a job for one node until the lease runs out, or
// extends a lease the holder already has. Nodes sharing a database never
// record the same job twice.
func (s *Store) AcquireLease(ctx context.Context, jobID, holder string, until time.Time) (bool, error) {
	now := s.now().UnixMilli()
	res, err := s.db.ExecContext(ctx, `INSERT INTO livetv_dvr_leases(job_id,holder,until_ms) VALUES(?,?,?)
ON CONFLICT(job_id) DO UPDATE SET holder=excluded.holder,until_ms=excluded.until_ms WHERE livetv_dvr_leases.holder=excluded.holder OR livetv_dvr_leases.until_ms<=?`, jobID, holder, until.UnixMilli(), now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *Store) ReleaseLease(ctx context.Context, jobID, holder string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM livetv_dvr_leases WHERE job_id=? AND holder=?`, jobID, holder)
	return err
}
