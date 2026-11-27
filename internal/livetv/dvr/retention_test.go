package dvr

import (
	"testing"
	"time"
)

func TestRetention(t *testing.T) {
	day := 24 * time.Hour
	rec := func(id, rule string, age time.Duration) Recording {
		return Recording{ID: id, RuleID: rule, Status: JobCompleted, SizeBytes: 10, Created: t0.Add(-age)}
	}
	recs := []Recording{
		rec("latest-new", "latest", 1*day), rec("latest-mid", "latest", 2*day), rec("latest-old", "latest", 3*day),
		rec("days-new", "days", 1*day), rec("days-old", "days", 10*day),
		rec("all", "all", 30*day),
		rec("watched", "watched", 5*day), rec("unwatched", "watched", 6*day),
		rec("once-old", "", 20*day), rec("once-new", "", 1*day),
		{ID: "running", Status: JobRecording, Created: t0.Add(-40 * day)},
		{ID: "protected", Protect: true, Status: JobCompleted, Created: t0.Add(-50 * day)},
	}
	rules := map[string]Rule{
		"latest":  {ID: "latest", Keep: Keep{Mode: KeepLatest, N: 2}},
		"days":    {ID: "days", Keep: Keep{Mode: KeepDays, N: 7}},
		"all":     {ID: "all", Keep: Keep{Mode: KeepAll}},
		"watched": {ID: "watched", Keep: Keep{Mode: KeepUntilWatched}},
	}
	watched := func(r Recording) (bool, time.Time) { return r.ID == "watched", t0.Add(-2 * day) }
	expired, evictable := Retention(recs, rules, watched, t0)
	ids := func(rs []Recording) []string {
		out := []string{}
		for _, r := range rs {
			out = append(out, r.ID)
		}
		return out
	}
	if got := ids(expired); len(got) != 3 || got[0] != "days-old" || got[1] != "watched" || got[2] != "latest-old" {
		t.Fatalf("expired = %v", got)
	}
	// Unknown watch state counts as unwatched; oldest first after that.
	if got := ids(evictable); len(got) != 6 || got[0] != "once-old" || got[1] != "unwatched" || got[2] != "latest-mid" {
		t.Fatalf("evictable = %v", got)
	}
	for _, id := range ids(evictable) {
		if id == "all" || id == "running" || id == "protected" {
			t.Fatalf("%s may be evicted", id)
		}
	}
	if UsedBytes(recs) != 100 {
		t.Fatalf("used = %d", UsedBytes(recs))
	}
}
