package dvr

import (
	"sort"
	"strings"
	"time"
)

// Reasons a job is skipped without the user asking. Only these are revived
// when the rule matches the airing again; a user's cancel stays.
const (
	ReasonCancelled    = "cancelled"
	ReasonRuleDisabled = "rule disabled"
	ReasonNotInGuide   = "no longer in the guide"
	ReasonRuleChanged  = "no longer matches the rule"
)

func revivable(j Job) bool {
	return j.Status == JobSkipped && (j.Reason == ReasonRuleDisabled || j.Reason == ReasonNotInGuide || j.Reason == ReasonRuleChanged)
}

// Matches reports whether an airing belongs to the rule. Weekday and time
// slot are read in loc, the server's wall clock.
func (r Rule) Matches(p Programme, loc *time.Location) bool {
	if r.ChannelID != "" && p.ChannelID != r.ChannelID {
		return false
	}
	if loc == nil {
		loc = time.Local
	}
	local := p.Start.In(loc)
	if r.Weekdays != 0 && r.Weekdays&(1<<uint(local.Weekday())) == 0 {
		return false
	}
	if r.SlotMinute >= 0 {
		d := local.Hour()*60 + local.Minute() - r.SlotMinute
		if d < 0 {
			d = -d
		}
		if d > 12*60 {
			d = 24*60 - d
		}
		if d > 30 {
			return false
		}
	}
	switch r.Kind {
	case RuleSeries, "":
		if r.ShowGUID != "" && p.ShowGUID != "" {
			return r.ShowGUID == p.ShowGUID
		}
		return NormTitle(r.Title) != "" && NormTitle(p.Show()) == NormTitle(r.Title)
	case RuleMovie:
		return p.Episode == 0 && NormTitle(p.Title) == NormTitle(r.Title)
	case RuleKeyword:
		kw := NormTitle(r.Keyword)
		hay := NormTitle(p.Title + " " + p.EpisodeTitle + " " + p.Summary)
		return kw != "" && strings.Contains(" "+hay+" ", " "+kw+" ")
	}
	return false
}

// Plan is what one expansion changes.
type Plan struct {
	Insert []Job
	Update []Job
}

func (p Plan) Empty() bool { return len(p.Insert) == 0 && len(p.Update) == 0 }

// Expand matches rules against one user's guide and returns the jobs to
// create and the jobs to change. jobs are that user's stored jobs (rule
// jobs of any status, recent ones at least; pending one-offs); recorded is
// the set of episode keys the user already has. It is pure so it can be
// tested without a guide or a database.
//
//   - Each episode is scheduled once, at its earliest airing that has not
//     started (or the one in progress when no later airing is known).
//   - A pending job follows its airing when the guide moves it, by episode
//     key, or for poorly described airings by channel, title and time
//     within DriftWindow.
//   - A pending rule job whose airing left a guide that still covers that
//     time is skipped; a guide with no data for the slot changes nothing.
//   - One-offs follow drift but are never skipped by the guide.
func Expand(now time.Time, loc *time.Location, rules []Rule, guide []Programme, jobs []Job, recorded map[string]bool) Plan {
	var plan Plan
	horizon := now.Add(Horizon)
	byChannel := map[string][]Programme{}
	for _, p := range guide {
		if !p.End.After(p.Start) {
			continue
		}
		byChannel[p.ChannelID] = append(byChannel[p.ChannelID], p)
	}
	for _, list := range byChannel {
		sort.Slice(list, func(i, j int) bool { return list[i].Start.Before(list[j].Start) })
	}
	covered := func(channel string, start, end time.Time) bool {
		for _, p := range byChannel[channel] {
			if p.Start.Before(end) && p.End.After(start) {
				return true
			}
		}
		return false
	}
	byRule := map[string][]int{}
	for i, j := range jobs {
		if j.RuleID != "" {
			byRule[j.RuleID] = append(byRule[j.RuleID], i)
		}
	}
	changed := map[int]bool{}
	update := func(i int) { changed[i] = true }

	for _, rule := range rules {
		own := byRule[rule.ID]
		if !rule.Enabled {
			for _, i := range own {
				if jobs[i].Status.Pending() && jobs[i].Programme.Start.After(now) {
					jobs[i].Status, jobs[i].Reason = JobSkipped, ReasonRuleDisabled
					update(i)
				}
			}
			continue
		}
		chosen := map[string]Programme{}
		var keys []string
		for _, list := range byChannel {
			for _, p := range list {
				if !p.End.After(now) || !p.Start.Before(horizon) || !rule.Matches(p, loc) {
					continue
				}
				key := EpisodeKey(p)
				prev, seen := chosen[key]
				switch {
				case !seen:
					keys = append(keys, key)
					chosen[key] = p
				case prev.Start.Before(now) && !p.Start.Before(now):
					// A full later airing beats one already in progress.
					chosen[key] = p
				case !p.Start.Before(now) && p.Start.Before(prev.Start):
					chosen[key] = p
				}
			}
		}
		sort.Strings(keys)
		claimed := map[int]bool{}
		for _, key := range keys {
			p := chosen[key]
			if rule.NewOnly && !p.Premiere {
				continue
			}
			at := -1
			for _, i := range own {
				if jobs[i].MatchKey == key && !claimed[i] && (at < 0 || jobs[at].Status.Final() && !jobs[i].Status.Final()) {
					at = i
				}
			}
			if at < 0 && isAiringKey(key) {
				at = driftMatch(jobs, own, claimed, p)
			}
			if at >= 0 {
				claimed[at] = true
				j := &jobs[at]
				switch {
				case revivable(*j) && j.Programme.Start.After(now):
					j.Status, j.Reason = JobScheduled, ""
					follow(j, key, p)
					update(at)
					continue
				case j.Status.Pending():
					if follow(j, key, p) {
						update(at)
					}
					continue
				case j.Status == JobFailed && p.Start.After(j.Programme.Start):
					// A failed episode is tried again at a later airing.
				default:
					continue
				}
			}
			if !rule.RecordAgain && !isAiringKey(key) && recorded[key] {
				continue
			}
			plan.Insert = append(plan.Insert, Job{UserID: rule.UserID, RuleID: rule.ID, MatchKey: key, Programme: p, PrePad: rule.PrePad, PostPad: rule.PostPad, Priority: rule.Priority, Status: JobScheduled})
		}
		for _, i := range own {
			j := &jobs[i]
			if claimed[i] || !j.Status.Pending() || !j.Programme.Start.After(now) || j.Programme.Start.After(horizon) {
				continue
			}
			switch {
			case !rule.Matches(j.Programme, loc):
				j.Status, j.Reason = JobSkipped, ReasonRuleChanged
				update(i)
			case covered(j.Programme.ChannelID, j.Programme.Start, j.Programme.End):
				j.Status, j.Reason = JobSkipped, ReasonNotInGuide
				update(i)
			}
		}
	}

	for i := range jobs {
		j := &jobs[i]
		if j.RuleID != "" || !j.Status.Pending() || !j.Programme.Start.After(now) {
			continue
		}
		p, ok := oneOffMatch(byChannel[j.Programme.ChannelID], *j)
		if !ok {
			continue
		}
		key := j.MatchKey
		if isAiringKey(key) {
			key = AiringKey(p)
		}
		if follow(j, key, p) {
			update(i)
		}
	}

	idx := make([]int, 0, len(changed))
	for i := range changed {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for _, i := range idx {
		plan.Update = append(plan.Update, jobs[i])
	}
	return plan
}

// follow moves a pending job onto p. It reports whether anything changed.
func follow(j *Job, key string, p Programme) bool {
	same := j.MatchKey == key && j.Programme.ChannelID == p.ChannelID && j.Programme.Start.Equal(p.Start) && j.Programme.End.Equal(p.End) && j.Programme.Title == p.Title && j.Programme.EpisodeTitle == p.EpisodeTitle
	if same {
		return false
	}
	j.MatchKey, j.Programme = key, p
	return true
}

// driftMatch finds a pending job for a poorly described airing that the
// guide moved: same channel and title, start within DriftWindow.
func driftMatch(jobs []Job, own []int, claimed map[int]bool, p Programme) int {
	at, best := -1, DriftWindow+1
	for _, i := range own {
		j := jobs[i]
		if claimed[i] || !isAiringKey(j.MatchKey) || !(j.Status.Pending() || revivable(j)) || j.Programme.ChannelID != p.ChannelID || NormTitle(j.Programme.Show()) != NormTitle(p.Show()) {
			continue
		}
		if d := absDuration(j.Programme.Start.Sub(p.Start)); d <= DriftWindow && d < best {
			at, best = i, d
		}
	}
	return at
}

// oneOffMatch finds where a one-off's airing is now: the same episode on
// its channel, or the same title nearest its old start within DriftWindow.
func oneOffMatch(list []Programme, j Job) (Programme, bool) {
	var out Programme
	best := DriftWindow + 1
	for _, p := range list {
		d := absDuration(p.Start.Sub(j.Programme.Start))
		if d > DriftWindow {
			continue
		}
		sameEpisode := !isAiringKey(j.MatchKey) && EpisodeKey(p) == j.MatchKey
		sameTitle := NormTitle(p.Show()) == NormTitle(j.Programme.Show())
		if (sameEpisode || sameTitle) && d < best {
			out, best = p, d
		}
	}
	return out, best <= DriftWindow
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
