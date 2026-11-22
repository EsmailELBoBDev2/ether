package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// LiveEvent state values (A2 live event contract).
const (
	LiveEventStatePre       = "pre"
	LiveEventStateIn        = "in"
	LiveEventStatePost      = "post"
	LiveEventStateDelayed   = "delayed"
	LiveEventStatePostponed = "postponed"
	LiveEventStateCanceled  = "canceled"
)

// ValidLiveEventState reports whether s is a defined LiveEventStatus.State value.
func ValidLiveEventState(s string) bool {
	switch s {
	case LiveEventStatePre, LiveEventStateIn, LiveEventStatePost,
		LiveEventStateDelayed, LiveEventStatePostponed, LiveEventStateCanceled:
		return true
	default:
		return false
	}
}

// LiveEvent broadcast kind values.
const (
	LiveEventBroadcastTV     = "tv"
	LiveEventBroadcastStream = "stream"
	LiveEventBroadcastRadio  = "radio"
)

// LiveEvent play emphasis values.
const (
	LiveEventPlayEmphasisScore  = "score"
	LiveEventPlayEmphasisKey    = "key"
	LiveEventPlayEmphasisNormal = "normal"
)

// LiveEvent watch target kind values (A5).
const (
	LiveEventWatchChannel = "channel"
	LiveEventWatchAddon   = "addon"
	LiveEventWatchPlugin  = "plugin"
)

// LiveEvent alert kind values emitted on the Vela live stream (A7).
const (
	LiveEventAlertStarted = "started"
	LiveEventAlertClose   = "close"
	LiveEventAlertFinal   = "final"
)

// NamespaceLiveEventID constructs Ether's globally unique event ID:
// "pluginID:feedID:eventID". Plugins always use their own feed-scoped ID;
// Ether namespaces it at the hub boundary.
func NamespaceLiveEventID(pluginID, feedID, eventID string) string {
	return pluginID + ":" + feedID + ":" + eventID
}

// ParseLiveEventID splits a namespaced event ID ("pluginID:feedID:eventID")
// into its plugin ID, feed ID, and plugin-scoped event ID.
func ParseLiveEventID(namespaced string) (pluginID, feedID, eventID string, ok bool) {
	p, rest, ok1 := strings.Cut(namespaced, ":")
	if !ok1 || p == "" || strings.ContainsAny(p, "/ \t\r\n") {
		return "", "", "", false
	}
	f, id, ok2 := strings.Cut(rest, ":")
	if !ok2 || f == "" || id == "" || strings.ContainsAny(f, "/ \t\r\n") {
		return "", "", "", false
	}
	return p, f, id, true
}

// NamespaceLiveEntityKey constructs Ether's globally unique entity key:
// "pluginID:feedID:entityKind:entityID".
func NamespaceLiveEntityKey(pluginID, feedID, entityKind, entityID string) string {
	return pluginID + ":" + feedID + ":" + entityKind + ":" + entityID
}

// ParseLiveEntityKey splits a namespaced entity key
// ("pluginID:feedID:entityKind:entityID") into its four components.
func ParseLiveEntityKey(key string) (pluginID, feedID, entityKind, entityID string, ok bool) {
	p, rest1, ok1 := strings.Cut(key, ":")
	if !ok1 || p == "" || strings.ContainsAny(p, "/ \t\r\n") {
		return "", "", "", "", false
	}
	f, rest2, ok2 := strings.Cut(rest1, ":")
	if !ok2 || f == "" || strings.ContainsAny(f, "/ \t\r\n") {
		return "", "", "", "", false
	}
	kind, id, ok3 := strings.Cut(rest2, ":")
	if !ok3 || kind == "" || id == "" || strings.ContainsAny(kind, "/ \t\r\n") {
		return "", "", "", "", false
	}
	return p, f, kind, id, true
}

// LiveEventLeague identifies the competition or circuit an event belongs to.
type LiveEventLeague struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Abbr string `json:"abbr,omitempty"`
	Logo string `json:"logo,omitempty"`
}

// LiveEventStatus describes the current lifecycle and clock state of a live event.
type LiveEventStatus struct {
	State  string `json:"state"`            // pre|in|post|delayed|postponed|canceled
	Detail string `json:"detail,omitempty"` // human-readable period/clock summary
	Period int    `json:"period,omitempty"`
	Clock  string `json:"clock,omitempty"`
	Close  bool   `json:"close,omitempty"` // plugin-determined close-contest indicator
}

// LiveEventCompetitor describes one participant in a head-to-head or multi-side event.
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

// LiveEventRankRow describes one leaderboard row for N-competitor events
// (such as motorsport or tournament leaderboards).
type LiveEventRankRow struct {
	Pos      int    `json:"pos"`
	Name     string `json:"name"`
	Abbr     string `json:"abbr,omitempty"`
	Detail   string `json:"detail,omitempty"`
	EntityID string `json:"entityId,omitempty"`
}

// LiveEventProbability holds an optional live win or outcome probability (0..1).
type LiveEventProbability struct {
	Label        string  `json:"label,omitempty"`
	Value        float64 `json:"value"` // 0..1
	CompetitorID string  `json:"competitorId,omitempty"`
}

// LiveEventBroadcast describes one network, stream, or radio carriage for an event.
type LiveEventBroadcast struct {
	Name   string `json:"name"`
	Kind   string `json:"kind,omitempty"` // tv|stream|radio
	Region string `json:"region,omitempty"`
}

// LiveEventMatchHints supplies normalised terms and broadcast callsigns used by
// Ether to match a live event against Live TV guide programmes and channels.
type LiveEventMatchHints struct {
	Terms     []string `json:"terms,omitempty"`
	Callsigns []string `json:"callsigns,omitempty"`
}

// LiveEventWatchTarget links a live event to a playable channel, add-on, or
// plugin-provided stream target. Ether attaches this; plugins do not emit it.
type LiveEventWatchTarget struct {
	Kind       string  `json:"kind"` // channel|addon|plugin
	ChannelID  string  `json:"channelId,omitempty"`
	Name       string  `json:"name"`
	Number     string  `json:"number,omitempty"`
	Logo       string  `json:"logo,omitempty"`
	Live       bool    `json:"live,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	URL        string  `json:"url,omitempty"`
}

// LiveEvent is Ether's JSON-stable, generic live event model suitable for
// head-to-head matches, tournaments, motorsport, and esports.
type LiveEvent struct {
	ID          string                 `json:"id"`
	Feed        string                 `json:"feed"`
	Category    string                 `json:"category,omitempty"`
	League      LiveEventLeague        `json:"league"`
	Title       string                 `json:"title,omitempty"`
	Start       string                 `json:"start"` // RFC3339
	Status      LiveEventStatus        `json:"status"`
	Competitors []LiveEventCompetitor  `json:"competitors,omitempty"`
	Ranking     []LiveEventRankRow     `json:"ranking,omitempty"`
	Situation   map[string]string      `json:"situation,omitempty"`
	Probability *LiveEventProbability  `json:"probability,omitempty"`
	Venue       string                 `json:"venue,omitempty"`
	Broadcasts  []LiveEventBroadcast   `json:"broadcasts,omitempty"`
	MatchHints  *LiveEventMatchHints   `json:"matchHints,omitempty"`
	Watch       []LiveEventWatchTarget `json:"watch,omitempty"`
	Spoiler     bool                   `json:"spoiler,omitempty"`   // set by Ether, never by plugins
	UpdatedAt   string                 `json:"updatedAt,omitempty"` // RFC3339
}

// Clone returns a deep copy of e so callers may apply viewer-specific filters
// without mutating shared cached state.
func (e LiveEvent) Clone() LiveEvent {
	out := e
	if e.Competitors != nil {
		out.Competitors = append([]LiveEventCompetitor(nil), e.Competitors...)
	}
	if e.Ranking != nil {
		out.Ranking = append([]LiveEventRankRow(nil), e.Ranking...)
	}
	if e.Situation != nil {
		out.Situation = make(map[string]string, len(e.Situation))
		for k, v := range e.Situation {
			out.Situation[k] = v
		}
	}
	if e.Probability != nil {
		p := *e.Probability
		out.Probability = &p
	}
	if e.Broadcasts != nil {
		out.Broadcasts = append([]LiveEventBroadcast(nil), e.Broadcasts...)
	}
	if e.MatchHints != nil {
		h := &LiveEventMatchHints{}
		if e.MatchHints.Terms != nil {
			h.Terms = append([]string(nil), e.MatchHints.Terms...)
		}
		if e.MatchHints.Callsigns != nil {
			h.Callsigns = append([]string(nil), e.MatchHints.Callsigns...)
		}
		out.MatchHints = h
	}
	if e.Watch != nil {
		out.Watch = append([]LiveEventWatchTarget(nil), e.Watch...)
	}
	return out
}

// LiveEventTable is a generic tabular dataset used for box scores, line scores,
// and league standings.
type LiveEventTable struct {
	Title   string     `json:"title"`
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
}

// Clone returns a deep copy of t.
func (t LiveEventTable) Clone() LiveEventTable {
	out := LiveEventTable{Title: t.Title}
	if t.Columns != nil {
		out.Columns = append([]string(nil), t.Columns...)
	}
	if t.Rows != nil {
		out.Rows = make([][]string, len(t.Rows))
		for i, row := range t.Rows {
			if row != nil {
				out.Rows[i] = append([]string(nil), row...)
			}
		}
	}
	return out
}

// LiveEventPlay is one entry in an event's play-by-play or race timeline.
type LiveEventPlay struct {
	Clock        string `json:"clock,omitempty"`
	Period       int    `json:"period,omitempty"`
	Text         string `json:"text"`
	Emphasis     string `json:"emphasis,omitempty"` // score|key|normal
	CompetitorID string `json:"competitorId,omitempty"`
}

// LiveEventProbabilityPoint is one sample along an event's probability series.
// T is a timestamp or axis label (such as RFC3339 or game clock); numeric JSON
// inputs are normalised to strings on unmarshal.
type LiveEventProbabilityPoint struct {
	T     string  `json:"t"`
	Value float64 `json:"value"` // 0..1
}

// UnmarshalJSON accepts both string and numeric JSON values for t so wire
// output from Ether is always a canonical string.
func (p *LiveEventProbabilityPoint) UnmarshalJSON(data []byte) error {
	var raw struct {
		T     json.RawMessage `json:"t"`
		Value float64         `json:"value"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	p.Value = raw.Value
	trimmed := bytes.TrimSpace(raw.T)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		p.T = ""
		return nil
	}
	if trimmed[0] == '"' {
		return json.Unmarshal(trimmed, &p.T)
	}
	var num float64
	if err := json.Unmarshal(trimmed, &num); err != nil {
		return fmt.Errorf("probability point t must be a string or number: %w", err)
	}
	p.T = strconv.FormatFloat(num, 'f', -1, 64)
	return nil
}

// LiveEventLink is an external reference link attached to an event detail view.
type LiveEventLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// LiveEventDetail extends LiveEvent with detail-view tables, play-by-play,
// probability series, and links.
type LiveEventDetail struct {
	LiveEvent
	Tables            []LiveEventTable            `json:"tables,omitempty"`
	Plays             []LiveEventPlay             `json:"plays,omitempty"`
	ProbabilitySeries []LiveEventProbabilityPoint `json:"probabilitySeries,omitempty"`
	Links             []LiveEventLink             `json:"links,omitempty"`
}

// Clone returns a deep copy of d.
func (d LiveEventDetail) Clone() LiveEventDetail {
	out := LiveEventDetail{
		LiveEvent: d.LiveEvent.Clone(),
	}
	if d.Tables != nil {
		out.Tables = make([]LiveEventTable, len(d.Tables))
		for i, tbl := range d.Tables {
			out.Tables[i] = tbl.Clone()
		}
	}
	if d.Plays != nil {
		out.Plays = append([]LiveEventPlay(nil), d.Plays...)
	}
	if d.ProbabilitySeries != nil {
		out.ProbabilitySeries = append([]LiveEventProbabilityPoint(nil), d.ProbabilitySeries...)
	}
	if d.Links != nil {
		out.Links = append([]LiveEventLink(nil), d.Links...)
	}
	return out
}

// LiveEventsResponse is returned by GET /ether/v1/live/{feed}/events.
type LiveEventsResponse struct {
	Events      []LiveEvent `json:"events"`
	NextPollSec int         `json:"nextPollSec,omitempty"`
}

// LiveEventEntity describes a followable entity (such as a team, driver, or
// player) returned by GET /ether/v1/live/{feed}/entities.
type LiveEventEntity struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Abbr   string `json:"abbr,omitempty"`
	Logo   string `json:"logo,omitempty"`
	League string `json:"league,omitempty"`
	Color  string `json:"color,omitempty"`
}

// LiveEventEntitiesResponse is returned by GET /ether/v1/live/{feed}/entities.
type LiveEventEntitiesResponse struct {
	Entities []LiveEventEntity `json:"entities"`
}

// LiveEventStandingsResponse is returned by GET /ether/v1/live/{feed}/standings.
type LiveEventStandingsResponse struct {
	Tables []LiveEventTable `json:"tables"`
}

// LiveEventAlert is an in-app alert emitted on /vela/v0/live/stream?alerts=1
// as `event: alert`.
type LiveEventAlert struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"` // started|close|final
	EventID   string `json:"eventId"`
	EntityKey string `json:"entityKey"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	At        string `json:"at"` // RFC3339
}
