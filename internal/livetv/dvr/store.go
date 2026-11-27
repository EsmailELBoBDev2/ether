package dvr

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

// The DVR has its own migration component so its versions never collide
// with the Live TV service's, which other branches also extend.
const component = "livetv_dvr"

func init() {
	store.Register(component, 1, `CREATE TABLE livetv_dvr_rules(id TEXT PRIMARY KEY, user_id BIGINT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, priority INTEGER NOT NULL DEFAULT 0, data TEXT NOT NULL, created_ms BIGINT NOT NULL, updated_ms BIGINT NOT NULL);
CREATE INDEX livetv_dvr_rules_user ON livetv_dvr_rules(user_id);
CREATE TABLE livetv_dvr_jobs(id TEXT PRIMARY KEY, user_id BIGINT NOT NULL, rule_id TEXT NOT NULL DEFAULT '', match_key TEXT NOT NULL, channel_id TEXT NOT NULL, start_ms BIGINT NOT NULL, end_ms BIGINT NOT NULL, status TEXT NOT NULL, data TEXT NOT NULL, created_ms BIGINT NOT NULL, updated_ms BIGINT NOT NULL);
CREATE INDEX livetv_dvr_jobs_due ON livetv_dvr_jobs(status, start_ms);
CREATE INDEX livetv_dvr_jobs_rule ON livetv_dvr_jobs(rule_id, match_key);
CREATE INDEX livetv_dvr_jobs_user ON livetv_dvr_jobs(user_id, start_ms);
CREATE TABLE livetv_dvr_recordings(id TEXT PRIMARY KEY, user_id BIGINT NOT NULL, job_id TEXT NOT NULL, rule_id TEXT NOT NULL DEFAULT '', match_key TEXT NOT NULL, path TEXT NOT NULL, status TEXT NOT NULL, size_bytes BIGINT NOT NULL DEFAULT 0, duration_ms BIGINT NOT NULL DEFAULT 0, protect INTEGER NOT NULL DEFAULT 0, data TEXT NOT NULL, created_ms BIGINT NOT NULL, updated_ms BIGINT NOT NULL);
CREATE INDEX livetv_dvr_recordings_user ON livetv_dvr_recordings(user_id, match_key);
CREATE INDEX livetv_dvr_recordings_rule ON livetv_dvr_recordings(rule_id, created_ms);
CREATE TABLE livetv_dvr_leases(job_id TEXT PRIMARY KEY, holder TEXT NOT NULL, until_ms BIGINT NOT NULL);`)
	store.RegisterViewerTable(component, "livetv_dvr_rules", "user_id", store.ViewerMergeMove)
	store.RegisterViewerTable(component, "livetv_dvr_jobs", "user_id", store.ViewerMergeMove)
	store.RegisterViewerTable(component, "livetv_dvr_recordings", "user_id", store.ViewerMergeMove)
}

// ErrRunning is returned when a change needs a job that is recording to
// stop first. The recorder owns that transition.
var ErrRunning = errors.New("dvr: job is recording")

// Store keeps rules, jobs and recordings.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

func NewStore(db *sql.DB, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, now: now}
}

// SaveRule creates the rule when it has no ID, or replaces it. The stored
// rule (with defaults, ID and times) is written back to r.
func (s *Store) SaveRule(ctx context.Context, r *Rule) error {
	if r.UserID == 0 {
		return fmt.Errorf("a rule needs an owner")
	}
	if err := r.Normalize(); err != nil {
		return err
	}
	now := s.now().UTC()
	r.Updated = now
	if r.ID == "" {
		r.ID = newID()
		r.Created = now
		data, err := json.Marshal(r)
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO livetv_dvr_rules(id,user_id,enabled,priority,data,created_ms,updated_ms) VALUES(?,?,?,?,?,?,?)`, r.ID, r.UserID, boolInt(r.Enabled), r.Priority, string(data), now.UnixMilli(), now.UnixMilli())
		return err
	}
	old, err := s.Rule(ctx, r.ID)
	if err != nil {
		return err
	}
	if old.UserID != r.UserID {
		return core.ErrNotFound
	}
	r.Created = old.Created
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE livetv_dvr_rules SET enabled=?,priority=?,data=?,updated_ms=? WHERE id=?`, boolInt(r.Enabled), r.Priority, string(data), now.UnixMilli(), r.ID)
	return err
}

func (s *Store) Rule(ctx context.Context, id string) (Rule, error) {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM livetv_dvr_rules WHERE id=?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return Rule{}, core.ErrNotFound
	}
	if err != nil {
		return Rule{}, err
	}
	var r Rule
	if err := json.Unmarshal([]byte(data), &r); err != nil {
		return Rule{}, err
	}
	return r, nil
}

// Rules lists one user's rules, or everyone's for userID 0, highest
// priority first.
func (s *Store) Rules(ctx context.Context, userID int64) ([]Rule, error) {
	q := `SELECT data FROM livetv_dvr_rules`
	var args []any
	if userID != 0 {
		q += ` WHERE user_id=?`
		args = append(args, userID)
	}
	q += ` ORDER BY priority DESC, created_ms, id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rule{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var r Rule
		if err := json.Unmarshal([]byte(data), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteRule removes a rule and cancels its jobs that have not started.
// Jobs already recording finish; finished jobs and recordings stay.
func (s *Store) DeleteRule(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM livetv_dvr_rules WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return core.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM livetv_dvr_jobs WHERE rule_id=? AND status IN (?,?,?)`, id, JobScheduled, JobConflict, JobSkipped); err != nil {
		return err
	}
	return tx.Commit()
}

// JobQuery filters jobs. Zero fields match everything. From and Until
// select jobs whose airing overlaps the span.
type JobQuery struct {
	UserID      int64
	RuleID      string
	ChannelID   string
	Statuses    []JobStatus
	From, Until time.Time
	Limit       int
}

func (s *Store) Jobs(ctx context.Context, q JobQuery) ([]Job, error) {
	var where []string
	var args []any
	if q.UserID != 0 {
		where, args = append(where, "user_id=?"), append(args, q.UserID)
	}
	if q.RuleID != "" {
		where, args = append(where, "rule_id=?"), append(args, q.RuleID)
	}
	if q.ChannelID != "" {
		where, args = append(where, "channel_id=?"), append(args, q.ChannelID)
	}
	if len(q.Statuses) > 0 {
		marks := make([]string, len(q.Statuses))
		for i, st := range q.Statuses {
			marks[i] = "?"
			args = append(args, string(st))
		}
		where = append(where, "status IN ("+strings.Join(marks, ",")+")")
	}
	if !q.From.IsZero() {
		where, args = append(where, "end_ms>?"), append(args, q.From.UnixMilli())
	}
	if !q.Until.IsZero() {
		where, args = append(where, "start_ms<?"), append(args, q.Until.UnixMilli())
	}
	query := `SELECT data FROM livetv_dvr_jobs`
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, " AND ")
	}
	query += ` ORDER BY start_ms, id`
	if q.Limit > 0 {
		query += fmt.Sprintf(` LIMIT %d`, q.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var j Job
		if err := json.Unmarshal([]byte(data), &j); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (s *Store) Job(ctx context.Context, id string) (Job, error) {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM livetv_dvr_jobs WHERE id=?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, core.ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	var j Job
	if err := json.Unmarshal([]byte(data), &j); err != nil {
		return Job{}, err
	}
	return j, nil
}

// InsertJob stores a new job. ID and times are filled in.
func (s *Store) InsertJob(ctx context.Context, j *Job) error {
	return insertJob(ctx, s.db, s.now(), j)
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertJob(ctx context.Context, db execer, now time.Time, j *Job) error {
	if j.UserID == 0 || j.Programme.ChannelID == "" || !j.Programme.End.After(j.Programme.Start) {
		return fmt.Errorf("a job needs an owner, a channel and an airing")
	}
	if j.ID == "" {
		j.ID = newID()
	}
	if j.Status == "" {
		j.Status = JobScheduled
	}
	if j.MatchKey == "" {
		j.MatchKey = EpisodeKey(j.Programme)
	}
	now = now.UTC()
	j.Created, j.Updated = now, now
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO livetv_dvr_jobs(id,user_id,rule_id,match_key,channel_id,start_ms,end_ms,status,data,created_ms,updated_ms) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		j.ID, j.UserID, j.RuleID, j.MatchKey, j.Programme.ChannelID, j.Programme.Start.UnixMilli(), j.Programme.End.UnixMilli(), string(j.Status), string(data), now.UnixMilli(), now.UnixMilli())
	return err
}

// UpdateJob replaces a stored job.
func (s *Store) UpdateJob(ctx context.Context, j *Job) error {
	return updateJob(ctx, s.db, s.now(), j)
}

func updateJob(ctx context.Context, db execer, now time.Time, j *Job) error {
	j.Updated = now.UTC()
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	res, err := db.ExecContext(ctx, `UPDATE livetv_dvr_jobs SET rule_id=?,match_key=?,channel_id=?,start_ms=?,end_ms=?,status=?,data=?,updated_ms=? WHERE id=?`,
		j.RuleID, j.MatchKey, j.Programme.ChannelID, j.Programme.Start.UnixMilli(), j.Programme.End.UnixMilli(), string(j.Status), string(data), j.Updated.UnixMilli(), j.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return core.ErrNotFound
	}
	return nil
}

// ScheduleOnce records one airing for a user. Asking twice for the same
// airing returns the job already scheduled.
func (s *Store) ScheduleOnce(ctx context.Context, userID int64, p Programme, prePad, postPad time.Duration, priority int) (Job, error) {
	var err error
	if prePad, err = pad(prePad); err != nil {
		return Job{}, err
	}
	if postPad, err = pad(postPad); err != nil {
		return Job{}, err
	}
	if !p.End.After(s.now()) {
		return Job{}, fmt.Errorf("the programme has ended")
	}
	existing, err := s.Jobs(ctx, JobQuery{UserID: userID, ChannelID: p.ChannelID, From: p.Start, Until: p.End})
	if err != nil {
		return Job{}, err
	}
	for _, j := range existing {
		if j.Programme.Start.Equal(p.Start) && !j.Status.Final() {
			return j, nil
		}
	}
	j := Job{UserID: userID, Programme: p, MatchKey: EpisodeKey(p), PrePad: prePad, PostPad: postPad, Priority: priority}
	if err := s.InsertJob(ctx, &j); err != nil {
		return Job{}, err
	}
	return j, nil
}

// CancelJob stops a job from running. A one-off is cancelled; a rule's
// airing is skipped so the rule does not schedule it again. A job that is
// recording returns ErrRunning; finished jobs are left as they are.
func (s *Store) CancelJob(ctx context.Context, id string) (Job, error) {
	j, err := s.Job(ctx, id)
	if err != nil {
		return Job{}, err
	}
	switch {
	case j.Status == JobRecording:
		return j, ErrRunning
	case !j.Status.Pending():
		return j, nil
	case j.RuleID == "":
		j.Status = JobCancelled
	default:
		j.Status = JobSkipped
	}
	j.Reason = "cancelled"
	return j, s.UpdateJob(ctx, &j)
}

// RecordedKeys is the set of episode keys a user has complete recordings
// of, for rules that skip episodes already recorded.
func (s *Store) RecordedKeys(ctx context.Context, userID int64) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT match_key FROM livetv_dvr_recordings WHERE user_id=? AND status=?`, userID, string(JobCompleted))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		out[key] = true
	}
	return out, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
