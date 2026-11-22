package app

import (
	"context"
	"errors"
	"log/slog"

	"github.com/braedonsaunders/ether/internal/cluster"
	"github.com/braedonsaunders/ether/internal/clusterbus"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/liveevents"
	"github.com/braedonsaunders/ether/internal/plugins"
)

// LiveEventsHub returns Ether's live events hub (nil if plugins are not wired).
// W256 (screens) and W257 (watch targets, overlays, alerts) integrate through
// this handle.
func (a *App) LiveEventsHub() *liveevents.Hub {
	if a == nil {
		return nil
	}
	return a.liveEvents
}

// buildLiveEvents constructs the live events hub bound to the plugin host and
// (when clustered) the cluster runtime. Safe to call multiple times.
func (a *App) buildLiveEvents() {
	if a == nil || a.liveEvents != nil || a.pluginsRT == nil || a.pluginsRT.mgr == nil {
		return
	}
	var cl cluster.Cluster
	var cbus clusterbus.Bus
	if a.clustered() {
		cl = a.cluster
		cbus = a.clusterNode()
	}
	var log *slog.Logger
	if a.log != nil {
		log = a.log.With("component", "liveevents")
	}
	hub := liveevents.NewHub(liveevents.Options{
		Source:     liveevents.NewManagerSource(a.pluginsRT.mgr),
		DB:         a.db,
		Cluster:    cl,
		ClusterBus: cbus,
		Log:        log,
	})
	a.pluginsRT.mgr.OnSync(func(ctx context.Context, _ plugins.Record) {
		hub.SyncFeeds(ctx)
	})
	a.pluginsRT.mgr.OnRemove(func(ctx context.Context, _ string) {
		hub.SyncFeeds(ctx)
	})
	a.liveEvents = hub
	if a.pluginsRT.screens != nil {
		a.pluginsRT.screens.SetHub(liveEventsScreenHub{hub: hub})
		reg := a.pluginsRT.screens.Registry()
		hub.SetScreenFeedResolver(func(ctx context.Context, viewer *core.Viewer, screenID string) ([]liveevents.FeedRef, error) {
			if reg == nil || viewer == nil {
				return nil, nil
			}
			sc, ok, err := reg.FindScreen(ctx, viewer, "vela", screenID)
			if err != nil || !ok {
				return nil, err
			}
			seen := map[string]bool{}
			var refs []liveevents.FeedRef
			addFeed := func(feed string) {
				if feed == "" || seen[feed] {
					return
				}
				seen[feed] = true
				refs = append(refs, liveevents.FeedRef{PluginID: sc.PluginID, FeedID: feed})
			}
			addFeed(sc.DefaultFeed)
			for _, sec := range sc.Sections {
				if sec.Feed != "" {
					addFeed(sec.Feed)
				} else if sec.Query != nil && sec.Query.Feed != "" {
					addFeed(sec.Query.Feed)
				}
			}
			return refs, nil
		})
	}
	a.closers = append(a.closers, hub.Close)
}

// runLiveEvents runs the hub's cluster leadership and bus lifecycle until ctx
// is canceled. Idle cost is ~0 when no clients are subscribed.
func (a *App) runLiveEvents(ctx context.Context) {
	if a == nil || a.liveEvents == nil {
		return
	}
	a.liveEvents.Start(ctx)
}

type surfaceLiveEvents struct {
	app *App
}

func (s surfaceLiveEvents) hub() *liveevents.Hub {
	if s.app == nil {
		return nil
	}
	return s.app.liveEvents
}

func (s surfaceLiveEvents) Available(ctx context.Context) bool {
	hub := s.hub()
	if hub == nil {
		return false
	}
	return hub.HasLiveFeeds(ctx)
}

func (s surfaceLiveEvents) Events(ctx context.Context, viewer *frontend.Viewer, ids []string) ([]frontend.LiveEvent, error) {
	hub := s.hub()
	if hub == nil {
		return nil, frontend.ErrNotAvailable
	}
	events, err := hub.GetEvents(ctx, viewer, ids)
	if err != nil {
		return nil, mapLiveEventError(err)
	}
	return events, nil
}

func (s surfaceLiveEvents) Detail(ctx context.Context, viewer *frontend.Viewer, id string) (*frontend.LiveEventDetail, error) {
	hub := s.hub()
	if hub == nil {
		return nil, frontend.ErrNotAvailable
	}
	detail, err := hub.GetDetail(ctx, viewer, id)
	if err != nil {
		return nil, mapLiveEventError(err)
	}
	return detail, nil
}

func (s surfaceLiveEvents) Subscribe(ctx context.Context, viewer *frontend.Viewer, req frontend.LiveEventStreamRequest) (frontend.LiveEventStream, error) {
	hub := s.hub()
	if hub == nil {
		return nil, frontend.ErrNotAvailable
	}
	sub, err := hub.Subscribe(ctx, liveevents.SubscribeRequest{
		EventIDs:       req.EventIDs,
		Screens:        req.Screens,
		Viewer:         viewer,
		FilterSpoilers: true,
		Alerts:         req.Alerts,
	})
	if err != nil {
		return nil, mapLiveEventError(err)
	}
	initial, err := hub.SubscriptionSnapshot(ctx, sub)
	if err != nil {
		sub.Close()
		return nil, mapLiveEventError(err)
	}
	return &liveEventStreamAdapter{sub: sub, initial: initial}, nil
}

func (s surfaceLiveEvents) Reveal(ctx context.Context, viewer *frontend.Viewer, id string) error {
	hub := s.hub()
	if hub == nil {
		return frontend.ErrNotAvailable
	}
	_, err := hub.RevealEvent(ctx, viewer, id)
	return mapLiveEventError(err)
}

func (s surfaceLiveEvents) SpoilerFree(ctx context.Context, viewer *frontend.Viewer) (bool, error) {
	hub := s.hub()
	if hub == nil {
		return false, frontend.ErrNotAvailable
	}
	if viewer == nil || viewer.ID <= 0 {
		return false, frontend.ErrUnauthorized
	}
	sf, err := hub.SpoilerFree(ctx, viewer.ID)
	if err != nil {
		return false, mapLiveEventError(err)
	}
	return sf, nil
}

func (s surfaceLiveEvents) SetSpoilerFree(ctx context.Context, viewer *frontend.Viewer, enabled bool) error {
	hub := s.hub()
	if hub == nil {
		return frontend.ErrNotAvailable
	}
	if viewer == nil || viewer.ID <= 0 {
		return frontend.ErrUnauthorized
	}
	return mapLiveEventError(hub.SetSpoilerFree(ctx, viewer.ID, enabled))
}

type liveEventStreamAdapter struct {
	sub     *liveevents.Subscription
	initial []frontend.LiveEvent
}

func (a *liveEventStreamAdapter) Initial() []frontend.LiveEvent          { return a.initial }
func (a *liveEventStreamAdapter) Updates() <-chan []frontend.LiveEvent   { return a.sub.Updates() }
func (a *liveEventStreamAdapter) Alerts() <-chan frontend.LiveEventAlert { return a.sub.Alerts() }
func (a *liveEventStreamAdapter) Close()                                 { a.sub.Close() }

func mapLiveEventError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, liveevents.ErrNotFound):
		return frontend.ErrNotFound
	case errors.Is(err, liveevents.ErrInvalidRequest):
		return frontend.ErrInvalidPreferences
	case errors.Is(err, liveevents.ErrRateLimited):
		return frontend.ErrRateLimited
	case errors.Is(err, liveevents.ErrTooManySubscribers):
		return frontend.ErrReceiptCapacity
	case errors.Is(err, liveevents.ErrUnavailable):
		return frontend.ErrSourceUnavailable
	case errors.Is(err, core.ErrUnauthorized):
		return frontend.ErrUnauthorized
	default:
		return err
	}
}
