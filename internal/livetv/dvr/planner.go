package dvr

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// Guide supplies the airings one user may record: the channels they can
// see, from the Live TV guide.
type Guide interface {
	DVRGuide(ctx context.Context, userID int64, from, until time.Time) ([]Programme, error)
}

// Planner expands every user's rules into jobs. The Live TV service runs it
// after a guide refresh, after a rule changes and on a timer.
type Planner struct {
	Store    *Store
	Guide    Guide
	Location *time.Location
	Now      func() time.Time
}

// lookback keeps recent jobs in an expansion so an episode just recorded,
// failed or skipped is not scheduled again at a nearby airing.
const lookback = 48 * time.Hour

// Run expands all rules. A user whose guide cannot be read is left as it
// is; the error is returned after the other users are planned.
func (p *Planner) Run(ctx context.Context) (Plan, error) {
	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}
	rules, err := p.Store.Rules(ctx, 0)
	if err != nil {
		return Plan{}, err
	}
	byUser := map[int64][]Rule{}
	for _, r := range rules {
		byUser[r.UserID] = append(byUser[r.UserID], r)
	}
	pending, err := p.Store.pendingOneOffUsers(ctx, now)
	if err != nil {
		return Plan{}, err
	}
	for _, u := range pending {
		if _, ok := byUser[u]; !ok {
			byUser[u] = nil
		}
	}
	users := make([]int64, 0, len(byUser))
	for u := range byUser {
		users = append(users, u)
	}
	sort.Slice(users, func(i, j int) bool { return users[i] < users[j] })
	var total Plan
	var firstErr error
	for _, u := range users {
		plan, err := p.runUser(ctx, now, u, byUser[u])
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("dvr: plan user %d: %w", u, err)
			}
			continue
		}
		total.Insert = append(total.Insert, plan.Insert...)
		total.Update = append(total.Update, plan.Update...)
	}
	return total, firstErr
}

func (p *Planner) runUser(ctx context.Context, now time.Time, userID int64, rules []Rule) (Plan, error) {
	guide, err := p.Guide.DVRGuide(ctx, userID, now.Add(-12*time.Hour), now.Add(Horizon))
	if err != nil {
		return Plan{}, err
	}
	jobs, err := p.Store.Jobs(ctx, JobQuery{UserID: userID, From: now.Add(-lookback)})
	if err != nil {
		return Plan{}, err
	}
	recorded, err := p.Store.RecordedKeys(ctx, userID)
	if err != nil {
		return Plan{}, err
	}
	plan := Expand(now, p.Location, rules, guide, jobs, recorded)
	if plan.Empty() {
		return plan, nil
	}
	return plan, p.Store.apply(ctx, now, &plan)
}

// apply writes one plan in a transaction. Updates only touch jobs that are
// still pending (or skipped by the planner), so a job the recorder started
// meanwhile is left alone.
func (s *Store) apply(ctx context.Context, now time.Time, plan *Plan) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i := range plan.Insert {
		if err := insertJob(ctx, tx, now, &plan.Insert[i]); err != nil {
			return err
		}
	}
	kept := plan.Update[:0]
	for i := range plan.Update {
		j := &plan.Update[i]
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM livetv_dvr_jobs WHERE id=?`, j.ID).Scan(&status); err != nil {
			return err
		}
		if st := JobStatus(status); !st.Pending() && st != JobSkipped {
			continue
		}
		if err := updateJob(ctx, tx, now, j); err != nil {
			return err
		}
		kept = append(kept, *j)
	}
	plan.Update = kept
	return tx.Commit()
}

func (s *Store) pendingOneOffUsers(ctx context.Context, now time.Time) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT user_id FROM livetv_dvr_jobs WHERE rule_id='' AND status IN (?,?) AND end_ms>?`, string(JobScheduled), string(JobConflict), now.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var u int64
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
