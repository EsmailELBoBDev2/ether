package app

import "github.com/braedonsaunders/ether/internal/events"

const eventFlowsBusDropped = "flows.bus.dropped"

// initEventBus constructs the shared domain event registry and non-blocking bus
// once cluster identity is known so all services and specialist roles can emit.
func (a *App) initEventBus() {
	nodeID := ""
	if a.cluster != nil {
		nodeID = a.cluster.Self().ID
	}
	a.eventRegistry = events.NewBuiltinRegistry()
	a.eventBus = events.NewBus(events.BusOptions{
		NodeID:   nodeID,
		Registry: a.eventRegistry,
		Logger:   a.log.With("component", "events"),
		OnDrop: func(subscriber string, ev events.Envelope) {
			if ev.Type == eventFlowsBusDropped || a.eventBus == nil {
				return
			}
			var droppedTotal uint64
			var capacity int
			for _, st := range a.eventBus.Stats().Subscribers {
				if st.Name == subscriber {
					droppedTotal = st.Dropped
					capacity = st.Capacity
					break
				}
			}
			if droppedTotal != 1 && droppedTotal%100 != 0 {
				return
			}
			_ = a.eventBus.PublishTyped(eventFlowsBusDropped, map[string]any{
				"subscriber":   subscriber,
				"eventType":    ev.Type,
				"droppedTotal": int(droppedTotal),
				"capacity":     capacity,
			})
		},
	})
	a.closers = append(a.closers, func() { a.eventBus.Close() })
}

// EventBus returns the application's non-blocking in-process event bus.
func (a *App) EventBus() *events.Bus {
	if a == nil {
		return nil
	}
	return a.eventBus
}

// EventRegistry returns the application's built-in + plugin event schema registry.
func (a *App) EventRegistry() *events.Registry {
	if a == nil {
		return nil
	}
	return a.eventRegistry
}

// publishEvent marshals payload and publishes a typed event non-blockingly.
func (a *App) publishEvent(eventType string, payload any, opts ...events.EnvelopeOption) {
	if a == nil || a.eventBus == nil {
		return
	}
	if err := a.eventBus.PublishTyped(eventType, payload, opts...); err != nil && a.log != nil {
		a.log.Debug("events: publish failed", "event", eventType, "err", err)
	}
}
