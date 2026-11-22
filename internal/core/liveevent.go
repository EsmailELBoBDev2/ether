package core

import (
	"maps"
	"slices"
	"time"
)

// Note: A2 live event contract types (W255 owns this file; defined here against
// the shared sports-0319 spec so W257 compiles before W255 merges and can be
// deduped cleanly at merge).

// Live event lifecycle states.
const (
	LiveEventStatePre       = "pre"
	LiveEventStateIn        = "in"
	LiveEventStatePost      = "post"
	LiveEventStateDelayed   = "delayed"
	LiveEventStatePostponed = "postponed"
	LiveEventStateCanceled  = "canceled"
)

// LiveEvent is Ether's generic, JSON-stable representation of a live event
// provided by a plugin feed (e.g., team contests, multi-competitor races, or
// other scheduled live occurrences).
type LiveEvent struct {
	ID          string                `json:"id"`
	Feed        string                `json:"feed"`
	Category    string                `json:"category,omitempty"`
	League      LiveEventLeague       `json:"league"`
	Title       string                `json:"title,omitempty"`
	Start       time.Time             `json:"start"`
	Status      LiveEventStatus       `json:"status"`
	Competitors []LiveEventCompetitor `json:"competitors,omitempty"`
	Ranking     []LiveEventRank       `json:"ranking,omitempty"`
	Situation   map[string]string     `json:"situation,omitempty"`
	Probability *LiveEventProbability `json:"probability,omitempty"`
	Venue       string                `json:"venue,omitempty"`
	Broadcasts  []LiveEventBroadcast  `json:"broadcasts,omitempty"`
	MatchHints  LiveEventMatchHints   `json:"matchHints,omitempty"`
	Spoiler     bool                  `json:"spoiler,omitempty"`
	UpdatedAt   time.Time             `json:"updatedAt"`
}

// LiveEventLeague identifies the competition or circuit grouping an event.
type LiveEventLeague struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Abbr string `json:"abbr,omitempty"`
	Logo string `json:"logo,omitempty"`
}

// LiveEventStatus describes the current progression of a live event.
type LiveEventStatus struct {
	State  string `json:"state"` // pre|in|post|delayed|postponed|canceled
	Detail string `json:"detail,omitempty"`
	Period int    `json:"period,omitempty"`
	Clock  string `json:"clock,omitempty"`
	Close  bool   `json:"close,omitempty"`
}

// LiveEventCompetitor represents one side in a head-to-head or multi-side event.
type LiveEventCompetitor struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Short    string `json:"short,omitempty"`
	Abbr     string `json:"abbr,omitempty"`
	Logo     string `json:"logo,omitempty"`
	Color    string `json:"color,omitempty"`
	AltColor string `json:"altColor,omitempty"`
	Score    string `json:"score,omitempty"`
	Record   string `json:"record,omitempty"`
	Rank     int    `json:"rank,omitempty"`
	Home     bool   `json:"home,omitempty"`
	Winner   bool   `json:"winner,omitempty"`
	EntityID string `json:"entityId,omitempty"`
}

// LiveEventRank represents a leaderboard row for N-competitor events.
type LiveEventRank struct {
	Pos      int    `json:"pos"`
	Name     string `json:"name"`
	Abbr     string `json:"abbr,omitempty"`
	Detail   string `json:"detail,omitempty"`
	EntityID string `json:"entityId,omitempty"`
}

// LiveEventProbability holds an optional win/outcome probability snapshot.
type LiveEventProbability struct {
	Label        string  `json:"label,omitempty"`
	Value        float64 `json:"value"`
	CompetitorID string  `json:"competitorId,omitempty"`
}

// LiveEventBroadcast describes a broadcast channel or stream carrying the event.
type LiveEventBroadcast struct {
	Name   string `json:"name"`
	Kind   string `json:"kind,omitempty"` // tv|stream|radio
	Region string `json:"region,omitempty"`
	URL    string `json:"url,omitempty"`
}

// LiveEventMatchHints guides Live TV guide matching (A5).
type LiveEventMatchHints struct {
	Terms     []string `json:"terms,omitempty"`
	Callsigns []string `json:"callsigns,omitempty"`
}

// LiveEventTable is a generic tabular block (box score, linescore, standings).
type LiveEventTable struct {
	Title   string     `json:"title"`
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
}

// LiveEventPlay is a single chronological event in a live event play-by-play.
type LiveEventPlay struct {
	Clock        string `json:"clock,omitempty"`
	Period       int    `json:"period,omitempty"`
	Text         string `json:"text"`
	Emphasis     string `json:"emphasis,omitempty"` // score|key|normal
	CompetitorID string `json:"competitorId,omitempty"`
}

// LiveEventProbPoint is a single point along a probability curve.
type LiveEventProbPoint struct {
	T     float64 `json:"t"`
	Value float64 `json:"value"`
}

// LiveEventLink is a plugin-provided link associated with an event.
type LiveEventLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// LiveEventDetail extends LiveEvent with detail-only tables, plays, series and links.
type LiveEventDetail struct {
	LiveEvent
	Tables            []LiveEventTable     `json:"tables,omitempty"`
	Plays             []LiveEventPlay      `json:"plays,omitempty"`
	ProbabilitySeries []LiveEventProbPoint `json:"probabilitySeries,omitempty"`
	Links             []LiveEventLink      `json:"links,omitempty"`
}

// StripLiveEventSpoilers returns a copy of ev with scores, outcomes, win
// probability, and live situational cues removed for a spoiler-free viewer.
func StripLiveEventSpoilers(ev LiveEvent) LiveEvent {
	out := ev
	out.Spoiler = true
	out.Probability = nil
	out.Situation = nil
	out.Status.Close = false
	if len(ev.Competitors) > 0 {
		out.Competitors = slices.Clone(ev.Competitors)
		for i := range out.Competitors {
			out.Competitors[i].Score = ""
			out.Competitors[i].Winner = false
		}
	}
	if len(ev.Ranking) > 0 {
		out.Ranking = slices.Clone(ev.Ranking)
		for i := range out.Ranking {
			out.Ranking[i].Pos = 0
			out.Ranking[i].Detail = ""
		}
	}
	if len(ev.Broadcasts) > 0 {
		out.Broadcasts = slices.Clone(ev.Broadcasts)
	}
	if len(ev.MatchHints.Terms) > 0 {
		out.MatchHints.Terms = slices.Clone(ev.MatchHints.Terms)
	}
	if len(ev.MatchHints.Callsigns) > 0 {
		out.MatchHints.Callsigns = slices.Clone(ev.MatchHints.Callsigns)
	}
	return out
}

// CloneLiveEvent returns a deep copy of ev safe for concurrent consumers.
func CloneLiveEvent(ev LiveEvent) LiveEvent {
	out := ev
	if ev.Competitors != nil {
		out.Competitors = slices.Clone(ev.Competitors)
	}
	if ev.Ranking != nil {
		out.Ranking = slices.Clone(ev.Ranking)
	}
	if ev.Situation != nil {
		out.Situation = maps.Clone(ev.Situation)
	}
	if ev.Probability != nil {
		p := *ev.Probability
		out.Probability = &p
	}
	if ev.Broadcasts != nil {
		out.Broadcasts = slices.Clone(ev.Broadcasts)
	}
	if ev.MatchHints.Terms != nil {
		out.MatchHints.Terms = slices.Clone(ev.MatchHints.Terms)
	}
	if ev.MatchHints.Callsigns != nil {
		out.MatchHints.Callsigns = slices.Clone(ev.MatchHints.Callsigns)
	}
	return out
}
