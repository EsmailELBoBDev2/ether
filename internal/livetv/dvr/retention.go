package dvr

import (
	"sort"
	"time"
)

// WatchedFunc reports whether a recording was watched, and when. Nil means
// watch state is unknown, so "until watched" recordings are kept.
type WatchedFunc func(Recording) (bool, time.Time)

// watchedGrace keeps a watched recording a day so a viewer can go back.
const watchedGrace = 24 * time.Hour

// Retention splits recordings into those their rule's keep policy has
// expired and, in eviction order, those that may go when space is needed.
// Recordings in progress, protected ones and those whose rule keeps all
// are never returned. A one-off (no rule) is kept until space is needed.
func Retention(recs []Recording, rules map[string]Rule, watched WatchedFunc, now time.Time) (expired, evictable []Recording) {
	byRule := map[string][]Recording{}
	for _, r := range recs {
		if r.Status == JobRecording || r.Protect {
			continue
		}
		byRule[r.RuleID] = append(byRule[r.RuleID], r)
	}
	type candidate struct {
		rec     Recording
		watched bool
	}
	var spare []candidate
	for ruleID, list := range byRule {
		keep := Keep{Mode: KeepUntilSpace}
		if rule, ok := rules[ruleID]; ok && ruleID != "" {
			keep = rule.Keep
		}
		// Newest first, for "keep the latest N".
		sort.Slice(list, func(i, j int) bool { return list[i].Created.After(list[j].Created) })
		for i, r := range list {
			seen, at := false, time.Time{}
			if watched != nil {
				seen, at = watched(r)
			}
			switch keep.Mode {
			case KeepAll:
				continue
			case KeepLatest:
				if i >= keep.N {
					expired = append(expired, r)
					continue
				}
			case KeepDays:
				if now.Sub(r.Created) > time.Duration(keep.N)*24*time.Hour {
					expired = append(expired, r)
					continue
				}
			case KeepUntilWatched:
				if seen && now.Sub(at) > watchedGrace {
					expired = append(expired, r)
					continue
				}
			}
			spare = append(spare, candidate{rec: r, watched: seen})
		}
	}
	// Watched recordings go before unwatched ones, oldest first.
	sort.Slice(spare, func(i, j int) bool {
		if spare[i].watched != spare[j].watched {
			return spare[i].watched
		}
		if !spare[i].rec.Created.Equal(spare[j].rec.Created) {
			return spare[i].rec.Created.Before(spare[j].rec.Created)
		}
		return spare[i].rec.ID < spare[j].rec.ID
	})
	sort.Slice(expired, func(i, j int) bool { return expired[i].Created.Before(expired[j].Created) })
	for _, c := range spare {
		evictable = append(evictable, c.rec)
	}
	return expired, evictable
}

// UsedBytes is the size of every stored recording.
func UsedBytes(recs []Recording) int64 {
	var n int64
	for _, r := range recs {
		n += r.SizeBytes
	}
	return n
}
