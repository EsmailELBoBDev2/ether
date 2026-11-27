package dvr

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

func airing(channel, title string, at time.Duration, length time.Duration) Programme {
	start := t0.Add(at)
	return Programme{ID: channel + "-" + start.Format("1504"), ChannelID: channel, Start: start, End: start.Add(length), Title: title}
}

func episode(p Programme, season, ep int) Programme {
	p.Season, p.Episode = season, ep
	return p
}

func seriesRule(title string) Rule {
	r := Rule{ID: "r1", UserID: 1, Kind: RuleSeries, Title: title, SlotMinute: -1, Enabled: true, PrePad: DefaultPrePad, PostPad: DefaultPostPad}
	if err := r.Normalize(); err != nil {
		panic(err)
	}
	return r
}

func TestExpandSchedulesEachEpisodeOnceAtEarliestAiring(t *testing.T) {
	guide := []Programme{
		episode(airing("a", "The Show", 2*time.Hour, time.Hour), 1, 1),
		episode(airing("b", "the show!", 30*time.Hour, time.Hour), 1, 1), // repeat elsewhere
		episode(airing("a", "The Show", 26*time.Hour, time.Hour), 1, 2),
		airing("a", "Other", 4*time.Hour, time.Hour),
	}
	plan := Expand(t0, time.UTC, []Rule{seriesRule("The Show")}, guide, nil, nil)
	if len(plan.Insert) != 2 || len(plan.Update) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
	got := map[string]time.Time{}
	for _, j := range plan.Insert {
		got[j.MatchKey] = j.Programme.Start
		if j.RuleID != "r1" || j.UserID != 1 || j.PrePad != DefaultPrePad || j.Status != JobScheduled {
			t.Fatalf("job = %+v", j)
		}
	}
	if !got["ep:the show:1:1"].Equal(t0.Add(2*time.Hour)) || !got["ep:the show:1:2"].Equal(t0.Add(26*time.Hour)) {
		t.Fatalf("keys = %v", got)
	}
}

func TestExpandSkipsRecordedAndHonoursNewOnly(t *testing.T) {
	old := episode(airing("a", "Show", time.Hour, time.Hour), 2, 3)
	fresh := episode(airing("a", "Show", 3*time.Hour, time.Hour), 2, 4)
	fresh.Premiere = true
	rule := seriesRule("Show")
	plan := Expand(t0, time.UTC, []Rule{rule}, []Programme{old, fresh}, nil, map[string]bool{EpisodeKey(old): true})
	if len(plan.Insert) != 1 || plan.Insert[0].MatchKey != EpisodeKey(fresh) {
		t.Fatalf("recorded episode scheduled: %+v", plan.Insert)
	}
	rule.RecordAgain = true
	rule.NewOnly = true
	plan = Expand(t0, time.UTC, []Rule{rule}, []Programme{old, fresh}, nil, map[string]bool{EpisodeKey(old): true})
	if len(plan.Insert) != 1 || plan.Insert[0].MatchKey != EpisodeKey(fresh) {
		t.Fatalf("new-only: %+v", plan.Insert)
	}
}

func TestExpandFollowsDriftAndSkipsRemovedAirings(t *testing.T) {
	rule := seriesRule("News")
	// No episode facts: airing keys.
	was := airing("a", "News", 5*time.Hour, 30*time.Minute)
	gone := airing("a", "News", 29*time.Hour, 30*time.Minute)
	jobs := []Job{
		{ID: "j1", UserID: 1, RuleID: "r1", MatchKey: AiringKey(was), Programme: was, Status: JobScheduled},
		{ID: "j2", UserID: 1, RuleID: "r1", MatchKey: AiringKey(gone), Programme: gone, Status: JobScheduled},
	}
	moved := airing("a", "News", 5*time.Hour+15*time.Minute, 30*time.Minute)
	filler := airing("a", "Film", 28*time.Hour, 3*time.Hour) // covers the removed slot
	plan := Expand(t0, time.UTC, []Rule{rule}, []Programme{moved, filler}, jobs, nil)
	if len(plan.Insert) != 0 || len(plan.Update) != 2 {
		t.Fatalf("plan = %+v", plan)
	}
	if u := plan.Update[0]; u.ID != "j1" || !u.Programme.Start.Equal(moved.Start) || u.MatchKey != AiringKey(moved) || u.Status != JobScheduled {
		t.Fatalf("drift = %+v", u)
	}
	if u := plan.Update[1]; u.ID != "j2" || u.Status != JobSkipped || u.Reason != ReasonNotInGuide {
		t.Fatalf("removed = %+v", u)
	}
}

func TestExpandLeavesJobsAloneWithoutGuideData(t *testing.T) {
	rule := seriesRule("News")
	at := airing("a", "News", 5*time.Hour, 30*time.Minute)
	jobs := []Job{{ID: "j1", UserID: 1, RuleID: "r1", MatchKey: AiringKey(at), Programme: at, Status: JobScheduled}}
	if plan := Expand(t0, time.UTC, []Rule{rule}, nil, jobs, nil); !plan.Empty() {
		t.Fatalf("an empty guide changed jobs: %+v", plan)
	}
}

func TestExpandDisableThenEnableRevivesJobs(t *testing.T) {
	rule := seriesRule("Show")
	p := episode(airing("a", "Show", 5*time.Hour, time.Hour), 1, 1)
	jobs := []Job{{ID: "j1", UserID: 1, RuleID: "r1", MatchKey: EpisodeKey(p), Programme: p, Status: JobScheduled}}
	rule.Enabled = false
	plan := Expand(t0, time.UTC, []Rule{rule}, []Programme{p}, jobs, nil)
	if len(plan.Update) != 1 || plan.Update[0].Status != JobSkipped || plan.Update[0].Reason != ReasonRuleDisabled {
		t.Fatalf("disable = %+v", plan)
	}
	rule.Enabled = true
	plan = Expand(t0, time.UTC, []Rule{rule}, []Programme{p}, plan.Update, nil)
	if len(plan.Insert) != 0 || len(plan.Update) != 1 || plan.Update[0].Status != JobScheduled {
		t.Fatalf("enable = %+v", plan)
	}
	// A user's cancel is not revived.
	cancelled := plan.Update
	cancelled[0].Status, cancelled[0].Reason = JobSkipped, ReasonCancelled
	if plan = Expand(t0, time.UTC, []Rule{rule}, []Programme{p}, cancelled, nil); !plan.Empty() {
		t.Fatalf("cancel revived: %+v", plan)
	}
}

func TestExpandRetriesFailedEpisodeAtLaterAiring(t *testing.T) {
	rule := seriesRule("Show")
	first := episode(airing("a", "Show", -3*time.Hour, time.Hour), 1, 1)
	again := episode(airing("a", "Show", 20*time.Hour, time.Hour), 1, 1)
	jobs := []Job{{ID: "j1", UserID: 1, RuleID: "r1", MatchKey: EpisodeKey(first), Programme: first, Status: JobFailed}}
	plan := Expand(t0, time.UTC, []Rule{rule}, []Programme{first, again}, jobs, nil)
	if len(plan.Insert) != 1 || !plan.Insert[0].Programme.Start.Equal(again.Start) {
		t.Fatalf("plan = %+v", plan)
	}
	jobs[0].Status = JobCompleted
	if plan = Expand(t0, time.UTC, []Rule{rule}, []Programme{first, again}, jobs, nil); !plan.Empty() {
		t.Fatalf("completed episode scheduled again: %+v", plan)
	}
}

func TestExpandOneOffFollowsItsAiring(t *testing.T) {
	was := airing("a", "Match", 3*time.Hour, 2*time.Hour)
	now := airing("a", "Match", 3*time.Hour+10*time.Minute, 2*time.Hour)
	jobs := []Job{{ID: "o1", UserID: 1, MatchKey: AiringKey(was), Programme: was, Status: JobScheduled}}
	plan := Expand(t0, time.UTC, nil, []Programme{now}, jobs, nil)
	if len(plan.Update) != 1 || !plan.Update[0].Programme.Start.Equal(now.Start) {
		t.Fatalf("plan = %+v", plan)
	}
	// Gone from the guide: a one-off still records its slot.
	other := airing("a", "Something else", 2*time.Hour, 4*time.Hour)
	if plan = Expand(t0, time.UTC, nil, []Programme{other}, jobs, nil); !plan.Empty() {
		t.Fatalf("one-off changed: %+v", plan)
	}
}

func TestRuleMatches(t *testing.T) {
	p := airing("a", "Doctor Who (2023)", 8*time.Hour, time.Hour) // Friday 20:00 UTC
	p.Summary = "The Doctor meets a robot."
	for _, c := range []struct {
		name string
		rule Rule
		want bool
	}{
		{"title folds", Rule{Kind: RuleSeries, Title: "doctor who 2023", SlotMinute: -1}, true},
		{"other channel", Rule{Kind: RuleSeries, Title: "Doctor Who (2023)", ChannelID: "b", SlotMinute: -1}, false},
		{"friday", Rule{Kind: RuleSeries, Title: "Doctor Who (2023)", Weekdays: 1 << 5, SlotMinute: -1}, true},
		{"monday", Rule{Kind: RuleSeries, Title: "Doctor Who (2023)", Weekdays: 1 << 1, SlotMinute: -1}, false},
		{"slot near", Rule{Kind: RuleSeries, Title: "Doctor Who (2023)", SlotMinute: 20*60 + 25}, true},
		{"slot far", Rule{Kind: RuleSeries, Title: "Doctor Who (2023)", SlotMinute: 21 * 60}, false},
		{"keyword word", Rule{Kind: RuleKeyword, Keyword: "robot", SlotMinute: -1}, true},
		{"keyword part", Rule{Kind: RuleKeyword, Keyword: "rob", SlotMinute: -1}, false},
		{"guid wins", Rule{Kind: RuleSeries, Title: "Doctor Who (2023)", ShowGUID: "x", SlotMinute: -1}, true},
	} {
		if got := c.rule.Matches(p, time.UTC); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
	p.ShowGUID = "y"
	if (Rule{Kind: RuleSeries, Title: "Doctor Who (2023)", ShowGUID: "x", SlotMinute: -1}).Matches(p, time.UTC) {
		t.Errorf("different show GUID matched")
	}
}

func TestRuleNormalize(t *testing.T) {
	r := Rule{Title: "  X  ", SlotMinute: -1}
	if err := r.Normalize(); err != nil || r.Kind != RuleSeries || r.Title != "X" || r.Keep.Mode != KeepUntilSpace {
		t.Fatalf("normalize = %+v, %v", r, err)
	}
	for _, bad := range []Rule{
		{Title: "", SlotMinute: -1},
		{Kind: RuleKeyword, SlotMinute: -1},
		{Title: "x", SlotMinute: 24 * 60},
		{Title: "x", SlotMinute: -1, PrePad: -time.Second},
		{Title: "x", SlotMinute: -1, Keep: Keep{Mode: KeepLatest}},
		{Title: "x", SlotMinute: -1, Kind: "weird"},
	} {
		if err := bad.Normalize(); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}
