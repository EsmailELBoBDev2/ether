package livetv

import (
	"time"
)

// liveHealthEvery spaces the frame clock's periodic health line for a live
// provider output. One line per channel every half minute names everything a
// visible glitch depends on: the reserve behind the decoder, the provider's
// cadence, the output's lead over the wall clock and the published edge.
const liveHealthEvery = 30 * time.Second

// logHealth records the output's live state. reason is "periodic", "stall"
// (the decoder stopped delivering pictures) or "resume" (it delivered again).
// It runs on the frame clock and takes no lock the clock does not already
// allow: the planner reads an atomic health snapshot.
func (b *broadcast) logHealth(reason string, now, scheduled time.Time) {
	b.healthAt = now
	attrs := []any{"reason", reason, "lead", b.lead.Round(time.Millisecond)}
	if !scheduled.IsZero() {
		attrs = append(attrs, "clockLate", time.Since(scheduled.Add(-b.lead)).Round(time.Millisecond))
	}
	if b.pacer != nil {
		attrs = append(attrs, "playRate", b.pacer.lastRate)
	}
	if b.current != nil {
		attrs = append(attrs, "decodedAhead", len(b.current.frames))
		if !b.current.lastFrameAt.IsZero() {
			attrs = append(attrs, "sincePicture", now.Sub(b.current.lastFrameAt).Round(time.Millisecond))
		}
	} else {
		attrs = append(attrs, "input", "none")
	}
	if !b.stallSince.IsZero() && reason == "resume" {
		attrs = append(attrs, "stalledFor", now.Sub(b.stallSince).Round(time.Millisecond))
	}
	if b.holdMode != "" {
		attrs = append(attrs, "hold", b.holdMode, "holdFor", now.Sub(b.holdSince).Round(time.Millisecond))
	}
	if source, ok := b.planner.(interface{ healthAttrs() []any }); ok {
		attrs = append(attrs, source.healthAttrs()...)
	}
	b.mu.Lock()
	for id, r := range b.renditions {
		attrs = append(attrs, "rendition", id, "queued", r.queue.pending())
		if index := r.encoder.Segments(); index != nil {
			if latest, ok := index.Latest(); ok {
				attrs = append(attrs, "publishedLead", time.Until(latest.Start.Add(latest.Duration)).Round(time.Millisecond))
			}
		}
	}
	b.mu.Unlock()
	b.logger.Info("live steady health", attrs...)
}

// healthAttrs describes the current input's provider transport for
// broadcast.logHealth.
func (p *providerBroadcastPlanner) healthAttrs() []any {
	p.mu.Lock()
	a := p.attempt
	if a == nil {
		a = p.stalledAttempt
	}
	p.mu.Unlock()
	if a == nil || a.link == nil {
		return []any{"source", "none"}
	}
	h, ok := p.s.inputHealth(a.link.ss)
	if !ok {
		return []any{"source", a.src.ID, "transport", "unknown"}
	}
	return []any{"source", a.src.ID, "transport", h.Kind,
		"reserve", h.ReserveDepth.Round(time.Millisecond), "reserveTarget", h.Target,
		"dataAge", h.DataAge.Round(time.Millisecond), "stallAfter", h.StallAfter.Round(time.Millisecond),
		"liveSocket", h.LiveSocket, "providerStalled", h.Stalled, "failed", h.Failed,
		"reconnects", h.Reconnects, "kbps", h.Bitrate / 1000}
}

func (q *broadcastFrameQueue) pending() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.frames) - q.head
}
