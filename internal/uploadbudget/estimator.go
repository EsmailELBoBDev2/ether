package uploadbudget

import (
	"math"
	"sort"
	"sync"
	"time"
)

const (
	// Minimum size and duration for a single transfer to count as real link
	// throughput rather than socket-buffer absorption or header timing noise.
	minObservationBytes = 64 << 10
	minObservationDur   = 100 * time.Millisecond

	// A transfer at least this large taking at least this long exceeds typical
	// TCP send-buffer burst absorption and reflects real bottleneck pacing.
	sustainedBurstBytes = 512 << 10
	sustainedBurstDur   = 250 * time.Millisecond

	// Non-trivial TCP kernel send queue indicates active network backpressure.
	backpressureBacklogBytes = 32 << 10

	// Minimum passive evidence required before Ether enforces an automatic
	// upload budget. Below this, Ether assumes no limit (existing behaviour).
	minEvidenceSamples    = 4
	minEvidenceSpan       = 10 * time.Second
	minStarvedSamples     = 2
	minStarvedSpan        = 5 * time.Second
	maxEstimatorSamples   = 64
	estimatorSampleMaxAge = 24 * time.Hour

	// Conservative scaling and safety floor for learned upload capacity.
	conservativeFactorPct = 95
	minEstimatedKbps      = 1500
)

// Observation captures one passive throughput measurement from an active
// remote Ether stream. LAN streams must never be passed to the estimator.
type Observation struct {
	At                time.Time
	Bytes             int64
	Duration          time.Duration
	SegDuration       time.Duration
	StreamRateKbps    int
	AggregateKbps     int
	MediaBitrateKbps  int
	TCPBacklogBytes   int
	Starved           bool
	ConcurrentStreams int
}

type sample struct {
	at                time.Time
	aggregateKbps     int
	starved           bool
	backpressured     bool
	concurrentStreams int
}

// Estimator learns a conservative rolling estimate of the server's usable
// internet upload capacity strictly from real remote streams (never via
// third-party speed tests). Until sufficient evidence exists, EstimateKbps
// returns 0 so Ether keeps its existing unlimited default.
type Estimator struct {
	mu      sync.Mutex
	now     func() time.Time
	samples []sample
}

// NewEstimator creates a passive upload capacity estimator.
func NewEstimator(now func() time.Time) *Estimator {
	if now == nil {
		now = time.Now
	}
	return &Estimator{now: now}
}

// Observe records one remote delivery observation. Non-evidential or noisy
// samples are ignored cleanly.
func (e *Estimator) Observe(obs Observation) bool {
	if e == nil || obs.Bytes < minObservationBytes || obs.Duration < minObservationDur {
		return false
	}
	at := obs.At
	if at.IsZero() {
		at = e.now()
	}

	streamRate := obs.StreamRateKbps
	if streamRate <= 0 {
		streamRate = int(math.Round(float64(obs.Bytes*8) / obs.Duration.Seconds() / 1000))
	}
	agg := obs.AggregateKbps
	if agg < streamRate {
		agg = streamRate
	}
	if agg <= 0 {
		return false
	}

	// Determine whether this transfer carried real bottleneck evidence:
	// 1. Explicit client buffer starvation, or a segment taking >= 70% of its
	//    media duration (or slower than 1.4x media bitrate).
	// 2. Non-trivial TCP kernel send backlog.
	// 3. Sustained large transfer exceeding socket burst buffers.
	starved := obs.Starved
	if !starved && obs.SegDuration > 0 && obs.Duration >= obs.SegDuration*7/10 {
		starved = true
	}
	if !starved && obs.MediaBitrateKbps > 0 && streamRate*100 <= obs.MediaBitrateKbps*140 {
		starved = true
	}
	backpressured := starved ||
		obs.TCPBacklogBytes >= backpressureBacklogBytes ||
		(obs.Bytes >= sustainedBurstBytes && obs.Duration >= sustainedBurstDur)
	if !backpressured {
		return false
	}

	concurrent := obs.ConcurrentStreams
	if concurrent < 1 {
		concurrent = 1
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.pruneLocked(at)

	// If multiple concurrent streams are repeatedly starved at a lower rate,
	// retire stale higher observations older than 10 minutes so an ISP slowdown
	// or link change adapts downward without reacting to a single slow client.
	e.samples = append(e.samples, sample{
		at:                at,
		aggregateKbps:     agg,
		starved:           starved,
		backpressured:     backpressured,
		concurrentStreams: concurrent,
	})
	if len(e.samples) > maxEstimatorSamples {
		copy(e.samples, e.samples[len(e.samples)-maxEstimatorSamples:])
		e.samples = e.samples[:maxEstimatorSamples]
	}
	e.adaptDownIfMultiStreamSaturatedLocked(at)
	return true
}

func (e *Estimator) pruneLocked(now time.Time) {
	cutoff := now.Add(-estimatorSampleMaxAge)
	kept := e.samples[:0]
	for _, s := range e.samples {
		if !s.at.Before(cutoff) {
			kept = append(kept, s)
		}
	}
	e.samples = kept
}

func (e *Estimator) adaptDownIfMultiStreamSaturatedLocked(now time.Time) {
	recentWindow := now.Add(-5 * time.Minute)
	recentCount := 0
	recentMax := 0
	for _, s := range e.samples {
		if !s.at.Before(recentWindow) && s.concurrentStreams >= 2 && s.starved {
			recentCount++
			if s.aggregateKbps > recentMax {
				recentMax = s.aggregateKbps
			}
		}
	}
	if recentCount < 3 || recentMax <= 0 {
		return
	}
	staleCutoff := now.Add(-10 * time.Minute)
	kept := e.samples[:0]
	for _, s := range e.samples {
		if s.at.Before(staleCutoff) && s.aggregateKbps > recentMax*120/100 {
			continue
		}
		kept = append(kept, s)
	}
	e.samples = kept
}

// Estimate returns the conservative rolling upload capacity estimate in kbps
// (0 when insufficient evidence exists) and the count of active samples.
func (e *Estimator) Estimate() (kbps int, samples int) {
	if e == nil {
		return 0, 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	e.pruneLocked(now)
	n := len(e.samples)
	if n == 0 {
		return 0, 0
	}

	var starvedCount int
	var firstAll, lastAll time.Time
	var firstStarved, lastStarved time.Time
	for _, s := range e.samples {
		if firstAll.IsZero() || s.at.Before(firstAll) {
			firstAll = s.at
		}
		if s.at.After(lastAll) {
			lastAll = s.at
		}
		if s.starved || s.concurrentStreams >= 2 {
			starvedCount++
			if firstStarved.IsZero() || s.at.Before(firstStarved) {
				firstStarved = s.at
			}
			if s.at.After(lastStarved) {
				lastStarved = s.at
			}
		}
	}

	enoughGeneral := n >= minEvidenceSamples && lastAll.Sub(firstAll) >= minEvidenceSpan
	enoughStarved := starvedCount >= minStarvedSamples && lastStarved.Sub(firstStarved) >= minStarvedSpan
	if !enoughGeneral && !enoughStarved {
		return 0, n
	}

	// Weighted 75th percentile of aggregate throughput. Using the upper
	// quartile avoids letting a single viewer on slow mobile/hotel Wi-Fi drag
	// down the server's estimated uplink, while multi-stream saturation and
	// conservative 95% scaling keep the budget safely below saturation.
	type weighted struct {
		kbps   int
		weight float64
	}
	items := make([]weighted, 0, n)
	totalW := 0.0
	for _, s := range e.samples {
		ageHours := now.Sub(s.at).Hours()
		if ageHours < 0 {
			ageHours = 0
		}
		// Exponential time decay with 6-hour half-life.
		w := math.Pow(0.5, ageHours/6.0)
		if s.starved {
			w *= 1.5
		}
		if s.concurrentStreams >= 2 {
			w *= 1.5
		}
		items = append(items, weighted{kbps: s.aggregateKbps, weight: w})
		totalW += w
	}
	sort.Slice(items, func(i, j int) bool { return items[i].kbps < items[j].kbps })

	target := totalW * 0.75
	acc := 0.0
	p75 := items[len(items)-1].kbps
	for _, it := range items {
		acc += it.weight
		if acc >= target {
			p75 = it.kbps
			break
		}
	}

	est := p75 * conservativeFactorPct / 100
	if est < minEstimatedKbps {
		est = minEstimatedKbps
	}
	// Round to nearest 100 kbps for clean display and stability.
	est = ((est + 50) / 100) * 100
	return est, n
}
