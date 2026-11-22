package core

import (
	"slices"
	"time"
)

// Watch target kinds returned by A5 "Where to watch".
const (
	WatchTargetKindChannel = "channel"
	WatchTargetKindAddon   = "addon"
	WatchTargetKindPlugin  = "plugin"
)

// Alert kinds raised by A7 for favourite entities.
const (
	LiveEventAlertStarted = "started"
	LiveEventAlertClose   = "close"
	LiveEventAlertFinal   = "final"
)

// WatchTarget describes one playable destination where a viewer can watch a
// live event (a Live TV channel, a Stremio event channel addon, or a plugin
// stream link).
type WatchTarget struct {
	Kind       string  `json:"kind"`
	ChannelID  string  `json:"channelId,omitempty"`
	Name       string  `json:"name"`
	Number     string  `json:"number,omitempty"`
	Logo       string  `json:"logo,omitempty"`
	URL        string  `json:"url,omitempty"`
	Live       bool    `json:"live"`
	Confidence float64 `json:"confidence"`
}

// LiveEventRef links a playing live channel programme back to its live event
// for the Vela playback snapshot and session SSE (A6).
type LiveEventRef struct {
	ID   string `json:"id"`
	Feed string `json:"feed"`
}

// LiveEventAlert is the in-app and push alert payload emitted when a viewer's
// favourited entity hits a notable event transition (A7).
type LiveEventAlert struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"` // started|close|final
	EventID   string    `json:"eventId"`
	EntityKey string    `json:"entityKey"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	At        time.Time `json:"at"`
}

// LiveTickerItem is the compact live event summary returned by
// GET /vela/v0/live/ticker (A6).
type LiveTickerItem struct {
	ID          string                `json:"id"`
	Feed        string                `json:"feed"`
	Category    string                `json:"category,omitempty"`
	League      LiveEventLeague       `json:"league"`
	Title       string                `json:"title,omitempty"`
	Start       time.Time             `json:"start"`
	Status      LiveEventStatus       `json:"status"`
	Competitors []LiveEventCompetitor `json:"competitors,omitempty"`
	Ranking     []LiveEventRank       `json:"ranking,omitempty"`
	Spoiler     bool                  `json:"spoiler,omitempty"`
	UpdatedAt   time.Time             `json:"updatedAt"`
}

// TickerItemFromEvent projects a LiveEvent into its compact ticker form.
func TickerItemFromEvent(ev LiveEvent) LiveTickerItem {
	item := LiveTickerItem{
		ID:        ev.ID,
		Feed:      ev.Feed,
		Category:  ev.Category,
		League:    ev.League,
		Title:     ev.Title,
		Start:     ev.Start,
		Status:    ev.Status,
		Spoiler:   ev.Spoiler,
		UpdatedAt: ev.UpdatedAt,
	}
	if len(ev.Competitors) > 0 {
		item.Competitors = slices.Clone(ev.Competitors)
	}
	if len(ev.Ranking) > 0 {
		item.Ranking = slices.Clone(ev.Ranking)
	}
	return item
}
