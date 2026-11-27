package dvr

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

type fakeGuide map[int64][]Programme

func (g fakeGuide) DVRGuide(_ context.Context, userID int64, from, until time.Time) ([]Programme, error) {
	if ps, ok := g[userID]; ok {
		return ps, nil
	}
	return nil, errors.New("no guide")
}

func TestStoreRulesAndPlanner(t *testing.T) {
	ctx := context.Background()
	st := NewStore(store.OpenTest(t), func() time.Time { return t0 })
	rule := Rule{UserID: 7, Title: "The Show", SlotMinute: -1, Enabled: true, PostPad: 5 * time.Minute}
	if err := st.SaveRule(ctx, &rule); err != nil {
		t.Fatal(err)
	}
	if rule.ID == "" || rule.Keep.Mode != KeepUntilSpace || !rule.Created.Equal(t0) {
		t.Fatalf("saved = %+v", rule)
	}
	rule.Priority = 3
	if err := st.SaveRule(ctx, &rule); err != nil {
		t.Fatal(err)
	}
	other := rule
	other.UserID = 8
	if err := st.SaveRule(ctx, &other); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("another user's rule saved: %v", err)
	}
	rules, err := st.Rules(ctx, 7)
	if err != nil || len(rules) != 1 || rules[0].Priority != 3 {
		t.Fatalf("rules = %+v, %v", rules, err)
	}

	ep1 := episode(airing("a", "The Show", 2*time.Hour, time.Hour), 1, 1)
	ep2 := episode(airing("a", "The Show", 26*time.Hour, time.Hour), 1, 2)
	planner := &Planner{Store: st, Guide: fakeGuide{7: {ep1, ep2}}, Location: time.UTC, Now: func() time.Time { return t0 }}
	plan, err := planner.Run(ctx)
	if err != nil || len(plan.Insert) != 2 {
		t.Fatalf("first plan = %+v, %v", plan, err)
	}
	// A second run changes nothing.
	if plan, err = planner.Run(ctx); err != nil || !plan.Empty() {
		t.Fatalf("second plan = %+v, %v", plan, err)
	}
	jobs, err := st.Jobs(ctx, JobQuery{UserID: 7, Statuses: []JobStatus{JobScheduled}})
	if err != nil || len(jobs) != 2 || jobs[0].PostPad != 5*time.Minute || jobs[0].Priority != 3 {
		t.Fatalf("jobs = %+v, %v", jobs, err)
	}

	// Cancelling a rule airing skips it for good.
	if j, err := st.CancelJob(ctx, jobs[0].ID); err != nil || j.Status != JobSkipped {
		t.Fatalf("cancel = %+v, %v", j, err)
	}
	if plan, err = planner.Run(ctx); err != nil || !plan.Empty() {
		t.Fatalf("cancelled airing rescheduled: %+v, %v", plan, err)
	}

	// A recording job is not cancelled here, and the planner leaves it.
	running := jobs[1]
	running.Status = JobRecording
	if err := st.UpdateJob(ctx, &running); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CancelJob(ctx, running.ID); !errors.Is(err, ErrRunning) {
		t.Fatalf("cancel running = %v", err)
	}

	// Deleting the rule removes its pending and skipped jobs only.
	if err := st.DeleteRule(ctx, rule.ID); err != nil {
		t.Fatal(err)
	}
	left, err := st.Jobs(ctx, JobQuery{UserID: 7})
	if err != nil || len(left) != 1 || left[0].ID != running.ID {
		t.Fatalf("after delete = %+v, %v", left, err)
	}
}

func TestStoreScheduleOnceAndGuideErrors(t *testing.T) {
	ctx := context.Background()
	st := NewStore(store.OpenTest(t), func() time.Time { return t0 })
	p := airing("a", "Match", 3*time.Hour, 2*time.Hour)
	j, err := st.ScheduleOnce(ctx, 9, p, time.Minute, time.Minute, 10)
	if err != nil || j.ID == "" || j.RuleID != "" || j.Status != JobScheduled {
		t.Fatalf("once = %+v, %v", j, err)
	}
	again, err := st.ScheduleOnce(ctx, 9, p, 0, 0, 0)
	if err != nil || again.ID != j.ID {
		t.Fatalf("second schedule made a new job: %+v, %v", again, err)
	}
	if _, err := st.ScheduleOnce(ctx, 9, airing("a", "Old", -3*time.Hour, time.Hour), 0, 0, 0); err == nil {
		t.Fatal("scheduled an ended programme")
	}
	// The user's guide is unavailable: nothing changes and the error is
	// reported.
	planner := &Planner{Store: st, Guide: fakeGuide{}, Location: time.UTC, Now: func() time.Time { return t0 }}
	if plan, err := planner.Run(ctx); err == nil || !plan.Empty() {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
	if c, err := st.CancelJob(ctx, j.ID); err != nil || c.Status != JobCancelled {
		t.Fatalf("cancel = %+v, %v", c, err)
	}
	if _, err := st.Job(ctx, "missing"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing job: %v", err)
	}
}

func TestStoreRecordingsAndLeases(t *testing.T) {
	ctx := context.Background()
	clock := t0
	st := NewStore(store.OpenTest(t), func() time.Time { return clock })
	rec := Recording{UserID: 3, JobID: "j", MatchKey: "ep:x:1:1", Path: "/r/x.ts", Status: JobRecording}
	if err := st.SaveRecording(ctx, &rec); err != nil {
		t.Fatal(err)
	}
	if keys, err := st.RecordedKeys(ctx, 3); err != nil || keys["ep:x:1:1"] {
		t.Fatalf("an unfinished recording counted as recorded: %v %v", keys, err)
	}
	rec.Status, rec.SizeBytes = JobCompleted, 42
	if err := st.SaveRecording(ctx, &rec); err != nil {
		t.Fatal(err)
	}
	if keys, err := st.RecordedKeys(ctx, 3); err != nil || !keys["ep:x:1:1"] {
		t.Fatalf("recorded keys = %v %v", keys, err)
	}
	if got, err := st.Recording(ctx, rec.ID); err != nil || got.SizeBytes != 42 {
		t.Fatalf("recording = %+v %v", got, err)
	}

	if ok, err := st.AcquireLease(ctx, "job", "a", clock.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("first lease: %v %v", ok, err)
	}
	if ok, _ := st.AcquireLease(ctx, "job", "b", clock.Add(time.Minute)); ok {
		t.Fatal("a held lease was taken")
	}
	if ok, _ := st.AcquireLease(ctx, "job", "a", clock.Add(2*time.Minute)); !ok {
		t.Fatal("the holder could not renew")
	}
	clock = clock.Add(3 * time.Minute)
	if ok, _ := st.AcquireLease(ctx, "job", "b", clock.Add(time.Minute)); !ok {
		t.Fatal("an expired lease was not taken over")
	}
	if err := st.DeleteRecording(ctx, rec.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteRecording(ctx, rec.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}
