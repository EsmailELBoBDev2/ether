package core

import "time"

// Temporary W255 / W257 contract types for parallel worker W256 (screens +
// entity favourites). The coordinator removes/dedupes this file once W255
// (internal/core/liveevent*.go) and W257 merge.
//
// Shapes match spec sections A1, A2, and A5 verbatim.

const (
	LiveEventStatePre       = "pre"
	LiveEventStateIn        = "in"
	LiveEventStatePost      = "post"
	LiveEventStateDelayed   = "delayed"
	LiveEventStatePostponed = "postponed"
	LiveEventStateCanceled  = "canceled"
)

// LiveEventLeague identifies the competition or circuit an event belongs to.
type LiveEventLeague struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Abbr string `json:"abbr,omitempty"`
	Logo string `json:"logo,omitempty"`
}

// LiveEventStatus describes the current lifecycle and clock state of an event.
type LiveEventStatus struct {
	State  string `json:"state"` // pre|in|post|delayed|postponed|canceled
	Detail string `json:"detail,omitempty"`
	Period int    `json:"period,omitempty"`
	Clock  string `json:"clock,omitempty"`
	Close  bool   `json:"close,omitempty"`
}

// LiveEventCompetitor is one side of a head-to-head or multi-side event.
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

// LiveEventRanking is one ordered row in an N-competitor event (e.g. racing).
type LiveEventRanking struct {
	Pos      int    `json:"pos"`
	Name     string `json:"name"`
	Abbr     string `json:"abbr,omitempty"`
	Detail   string `json:"detail,omitempty"`
	EntityID string `json:"entityId,omitempty"`
}

// LiveEventProbability holds an optional live probability indicator.
type LiveEventProbability struct {
	Label        string  `json:"label,omitempty"`
	Value        float64 `json:"value"` // 0..1
	CompetitorID string  `json:"competitorId,omitempty"`
}

// LiveEventBroadcast describes one known broadcast outlet for an event.
type LiveEventBroadcast struct {
	Name   string `json:"name"`
	Kind   string `json:"kind,omitempty"` // tv|stream|radio
	Region string `json:"region,omitempty"`
}

// LiveEventMatchHints assists guide/stream matching (spec A5).
type LiveEventMatchHints struct {
	Terms     []string `json:"terms,omitempty"`
	Callsigns []string `json:"callsigns,omitempty"`
}

// LiveEvent is Ether's generic, JSON-stable live event model (spec A2).
type LiveEvent struct {
	ID          string                `json:"id"`
	Feed        string                `json:"feed"`
	Category    string                `json:"category,omitempty"`
	League      LiveEventLeague       `json:"league"`
	Title       string                `json:"title,omitempty"`
	Start       time.Time             `json:"start"`
	Status      LiveEventStatus       `json:"status"`
	Competitors []LiveEventCompetitor `json:"competitors,omitempty"`
	Ranking     []LiveEventRanking    `json:"ranking,omitempty"`
	Situation   map[string]string     `json:"situation,omitempty"`
	Probability *LiveEventProbability `json:"probability,omitempty"`
	Venue       string                `json:"venue,omitempty"`
	Broadcasts  []LiveEventBroadcast  `json:"broadcasts,omitempty"`
	MatchHints  *LiveEventMatchHints  `json:"matchHints,omitempty"`
	Spoiler     bool                  `json:"spoiler,omitempty"`
	UpdatedAt   time.Time             `json:"updatedAt"`
}

// LiveEventTable is a generic tabular dataset used for standings and box scores.
type LiveEventTable struct {
	Title   string     `json:"title"`
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
}

// LiveEntity is one followable entity exposed by a live feed (spec A1/A4).
type LiveEntity struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Abbr   string `json:"abbr,omitempty"`
	Logo   string `json:"logo,omitempty"`
	League string `json:"league,omitempty"`
	Color  string `json:"color,omitempty"`
}

// WatchTarget is one matched place a viewer can watch a live event (spec A5).
type WatchTarget struct {
	Kind       string  `json:"kind"` // channel|addon|plugin
	ChannelID  string  `json:"channelId,omitempty"`
	Name       string  `json:"name"`
	Number     string  `json:"number,omitempty"`
	Logo       string  `json:"logo,omitempty"`
	Live       bool    `json:"live"`
	Confidence float64 `json:"confidence"`
}
