package liveevents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/livetv"
)

const (
	defaultWatchCacheCap   = 512
	defaultReverseCacheCap = 512
)

// GuideSource provides bounded time-indexed Live TV guide access for A5 and A6.
// Production wires NewLiveTVGuideSource(livetvService); tests can supply fakes.
type GuideSource interface {
	GuideRevision(ctx context.Context, viewer *core.User) (string, error)
	WatchCandidates(ctx context.Context, viewer *core.User, from, until time.Time, callsigns []string) ([]GuideCandidate, string, error)
	CurrentProgramme(ctx context.Context, viewer *core.User, channelID string, at time.Time) (*GuideCandidate, string, error)
}

// ActiveEventsSource supplies the current set of live / near-live events (and
// a monotonically changing or content-based revision) for A6 reverse matching.
type ActiveEventsSource interface {
	ActiveEvents(ctx context.Context, viewer *core.User, at time.Time) (events []core.LiveEvent, revision string, err error)
}

// LiveTVGuideSource adapts *livetv.Service to GuideSource.
type LiveTVGuideSource struct {
	svc *livetv.Service
}

// NewLiveTVGuideSource wraps a Live TV service for bounded guide matching.
func NewLiveTVGuideSource(svc *livetv.Service) *LiveTVGuideSource {
	if svc == nil {
		return nil
	}
	return &LiveTVGuideSource{svc: svc}
}

func (g *LiveTVGuideSource) GuideRevision(ctx context.Context, viewer *core.User) (string, error) {
	if g == nil || g.svc == nil {
		return "none", nil
	}
	return g.svc.GuideRevision(ctx, viewer)
}

func (g *LiveTVGuideSource) WatchCandidates(ctx context.Context, viewer *core.User, from, until time.Time, callsigns []string) ([]GuideCandidate, string, error) {
	if g == nil || g.svc == nil {
		return nil, "none", nil
	}
	entries, rev, err := g.svc.WatchCandidates(ctx, viewer, from, until, callsigns)
	if err != nil {
		return nil, "", err
	}
	out := make([]GuideCandidate, 0, len(entries))
	for _, e := range entries {
		out = append(out, guideCandidateFromLiveTV(e))
	}
	return out, rev, nil
}

func (g *LiveTVGuideSource) CurrentProgramme(ctx context.Context, viewer *core.User, channelID string, at time.Time) (*GuideCandidate, string, error) {
	if g == nil || g.svc == nil {
		return nil, "", nil
	}
	entry, key, err := g.svc.CurrentProgrammeForWatch(ctx, viewer, channelID, at)
	if err != nil || entry == nil {
		return nil, key, err
	}
	c := guideCandidateFromLiveTV(*entry)
	return &c, key, nil
}

func guideCandidateFromLiveTV(e livetv.WatchGuideEntry) GuideCandidate {
	return GuideCandidate{
		ChannelID:     e.ChannelID,
		ChannelName:   e.ChannelName,
		ChannelNumber: e.ChannelNumber,
		ChannelLogo:   e.ChannelLogo,
		TvgName:       e.TvgName,
		GuideID:       e.GuideID,
		IsEventAddon:  e.IsEventAddon,
		ChannelOnly:   e.ChannelOnly,
		Unhealthy:     e.Unhealthy,
		Title:         e.Programme.Item.Title,
		EpisodeTitle:  e.Programme.Item.EpisodeTitle,
		Summary:       e.Programme.Item.Summary,
		Genres:        slices.Clone(e.Programme.Item.Genres),
		Start:         e.Programme.Start,
		End:           e.Programme.End,
		Live:          e.Live,
	}
}

// WatchMatcher implements A5 ("Where to watch") and A6 (reverse channel->event
// linking), caching forward results per event + guide revision and reverse
// results per programme + events revision within bounded memory.
type WatchMatcher struct {
	guide            GuideSource
	activeEvents     ActiveEventsSource
	favoriteEntities func(ctx context.Context, viewer *core.User) ([]string, error)
	now              func() time.Time

	mu           sync.Mutex
	watchCap     int
	watchCache   map[string][]core.WatchTarget
	watchOrder   []string
	reverseCap   int
	reverseCache map[string]*core.LiveEventRef
	reverseOrder []string
}

// WatchMatcherOptions configures a WatchMatcher.
type WatchMatcherOptions struct {
	Guide            GuideSource
	ActiveEvents     ActiveEventsSource
	FavoriteEntities func(ctx context.Context, viewer *core.User) ([]string, error)
	Now              func() time.Time
	CacheCap         int
}

// NewWatchMatcher constructs a bounded, cached A5/A6 matcher.
func NewWatchMatcher(opts WatchMatcherOptions) *WatchMatcher {
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	capVal := opts.CacheCap
	if capVal <= 0 {
		capVal = defaultWatchCacheCap
	}
	return &WatchMatcher{
		guide:            opts.Guide,
		activeEvents:     opts.ActiveEvents,
		favoriteEntities: opts.FavoriteEntities,
		now:              nowFn,
		watchCap:         capVal,
		watchCache:       make(map[string][]core.WatchTarget),
		reverseCap:       capVal,
		reverseCache:     make(map[string]*core.LiveEventRef),
	}
}

const (
	watchBatchMaxSpan         = 36 * time.Hour
	watchBatchPrimaryCapGuard = 1800
)

// eventWatchWindow returns the guide overlap window for ev.
// Multi-day live tournaments (such as PGA or F1) have ev.Start days earlier,
// so in-progress/delayed events outside [start-30m, start+4h] anchor around now.
func eventWatchWindow(ev core.LiveEvent, now time.Time) (time.Time, time.Time, bool) {
	before := time.Duration(WatchWindowBefore) * time.Second
	after := time.Duration(WatchWindowAfter) * time.Second
	start := parseEventTime(ev.Start)
	if ev.Status.State == core.LiveEventStateIn || ev.Status.State == core.LiveEventStateDelayed {
		if start.IsZero() || now.After(start.Add(after)) || now.Before(start.Add(-before)) {
			return now.Add(-before), now.Add(after), true
		}
	}
	if start.IsZero() {
		return time.Time{}, time.Time{}, false
	}
	return start.Add(-before), start.Add(after), true
}

// WatchTargets resolves playable watch targets for ev (spec A5).
// External reference links (detail.Links) are intentionally excluded; only
// Live TV channels, event add-ons, and playable plugin streams are returned.
// Results are cached per event fingerprint and viewer guide revision.
func (m *WatchMatcher) WatchTargets(ctx context.Context, ev core.LiveEvent, viewer *core.User) ([]core.WatchTarget, error) {
	if m == nil {
		return nil, nil
	}
	guideRev := "none"
	if m.guide != nil {
		rev, err := m.guide.GuideRevision(ctx, viewer)
		if err != nil {
			return nil, err
		}
		guideRev = rev
	}

	cacheKey := watchCacheKey(ev, guideRev)
	if cached, ok := m.getWatchCached(cacheKey); ok {
		return cached, nil
	}

	var candidates []GuideCandidate
	if m.guide != nil && viewer != nil {
		if winFrom, winUntil, ok := eventWatchWindow(ev, m.now()); ok {
			callsigns := collectEventCallsigns(ev)
			cands, rev, err := m.guide.WatchCandidates(ctx, viewer, winFrom, winUntil, callsigns)
			if err != nil {
				return nil, err
			}
			candidates = cands
			if rev != "" && rev != guideRev {
				cacheKey = watchCacheKey(ev, rev)
			}
		}
	}

	targets := matchEventToCandidates(ev, candidates)
	m.putWatchCached(cacheKey, targets)
	return cloneWatchTargets(targets), nil
}

// WatchTargetsBatch resolves playable watch targets for many events with one
// GuideRevision check and a single bounded guide query for cache-missed events
// falling within a compact time span.
func (m *WatchMatcher) WatchTargetsBatch(ctx context.Context, events []core.LiveEvent, viewer *core.User) (map[string][]core.WatchTarget, error) {
	out := make(map[string][]core.WatchTarget, len(events))
	if m == nil || len(events) == 0 {
		return out, nil
	}

	guideRev := "none"
	if m.guide != nil {
		rev, err := m.guide.GuideRevision(ctx, viewer)
		if err != nil {
			return nil, err
		}
		guideRev = rev
	}

	type pendingEvent struct {
		ev       core.LiveEvent
		cacheKey string
		winFrom  time.Time
		winUntil time.Time
		hasWin   bool
	}

	now := m.now()
	seenID := make(map[string]struct{}, len(events))
	var missed []pendingEvent
	for _, ev := range events {
		if _, dup := seenID[ev.ID]; dup {
			continue
		}
		seenID[ev.ID] = struct{}{}
		ck := watchCacheKey(ev, guideRev)
		if cached, ok := m.getWatchCached(ck); ok {
			out[ev.ID] = cached
			continue
		}
		wf, wu, ok := eventWatchWindow(ev, now)
		missed = append(missed, pendingEvent{
			ev:       ev,
			cacheKey: ck,
			winFrom:  wf,
			winUntil: wu,
			hasWin:   ok,
		})
	}
	if len(missed) == 0 {
		return out, nil
	}

	if m.guide == nil || viewer == nil {
		for _, p := range missed {
			targets := matchEventToCandidates(p.ev, nil)
			m.putWatchCached(p.cacheKey, targets)
			out[p.ev.ID] = cloneWatchTargets(targets)
		}
		return out, nil
	}

	// Single bounded union lookup when missed events cluster within watchBatchMaxSpan.
	var minFrom, maxUntil time.Time
	var unionCallsigns []string
	seenCS := make(map[string]struct{})
	hasWindowed := false
	for _, p := range missed {
		if !p.hasWin {
			continue
		}
		if !hasWindowed {
			minFrom, maxUntil = p.winFrom, p.winUntil
			hasWindowed = true
		} else {
			if p.winFrom.Before(minFrom) {
				minFrom = p.winFrom
			}
			if p.winUntil.After(maxUntil) {
				maxUntil = p.winUntil
			}
		}
		for _, cs := range collectEventCallsigns(p.ev) {
			low := strings.ToLower(strings.TrimSpace(cs))
			if low == "" {
				continue
			}
			if _, ok := seenCS[low]; !ok {
				seenCS[low] = struct{}{}
				unionCallsigns = append(unionCallsigns, cs)
			}
		}
	}

	var batchCands []GuideCandidate
	batchRev := guideRev
	batchUsable := false
	if hasWindowed && maxUntil.Sub(minFrom) <= watchBatchMaxSpan {
		cands, rev, err := m.guide.WatchCandidates(ctx, viewer, minFrom, maxUntil, unionCallsigns)
		if err != nil {
			return nil, err
		}
		if rev != "" {
			batchRev = rev
		}
		// Only reuse the shared slice when it did not approach the primary
		// programme cap (ensures huge EPGs never drop any event's window).
		progCount := 0
		for _, c := range cands {
			if !c.ChannelOnly {
				progCount++
			}
		}
		if progCount < watchBatchPrimaryCapGuard {
			batchCands = cands
			batchUsable = true
		}
	}

	for _, p := range missed {
		var cands []GuideCandidate
		rev := batchRev
		if p.hasWin {
			if batchUsable {
				cands = filterCandidatesForWindow(batchCands, p.winFrom, p.winUntil)
			} else {
				var err error
				cands, rev, err = m.guide.WatchCandidates(ctx, viewer, p.winFrom, p.winUntil, collectEventCallsigns(p.ev))
				if err != nil {
					return nil, err
				}
			}
		}
		ck := p.cacheKey
		if rev != "" && rev != guideRev {
			ck = watchCacheKey(p.ev, rev)
		}
		targets := matchEventToCandidates(p.ev, cands)
		m.putWatchCached(ck, targets)
		out[p.ev.ID] = cloneWatchTargets(targets)
	}
	return out, nil
}

func filterCandidatesForWindow(all []GuideCandidate, from, until time.Time) []GuideCandidate {
	if len(all) == 0 {
		return nil
	}
	out := make([]GuideCandidate, 0, len(all))
	for _, c := range all {
		if c.ChannelOnly {
			out = append(out, c)
			continue
		}
		if c.Start.IsZero() && c.End.IsZero() {
			// Fake/test candidates without timestamps pass through.
			out = append(out, c)
			continue
		}
		if c.Start.Before(until) && c.End.After(from) {
			out = append(out, c)
		}
	}
	return out
}

// LiveEventForChannel resolves the live event matching the programme currently
// airing on channelID for viewer (A6 reverse of A5), cached per programme.
// Returns (programmeKey, ref, err); ref is nil when no event matches.
func (m *WatchMatcher) LiveEventForChannel(ctx context.Context, viewer *core.User, channelID string) (string, *core.LiveEventRef, error) {
	if m == nil || m.guide == nil || m.activeEvents == nil || viewer == nil || channelID == "" {
		return "", nil, nil
	}
	now := m.now()
	cand, progKey, err := m.guide.CurrentProgramme(ctx, viewer, channelID, now)
	if err != nil || cand == nil || progKey == "" || cand.Unhealthy {
		return progKey, nil, err
	}

	events, eventsRev, err := m.activeEvents.ActiveEvents(ctx, viewer, now)
	if err != nil {
		return progKey, nil, err
	}
	if len(events) == 0 {
		return progKey, nil, nil
	}

	revKey := progKey + "|" + eventsRev
	var favKeys []string
	if cand.ChannelOnly && m.favoriteEntities != nil {
		keys, ferr := m.favoriteEntities(ctx, viewer)
		if ferr == nil && len(keys) > 0 {
			favKeys = slices.Clone(keys)
			slices.Sort(favKeys)
			h := sha256.New()
			for _, k := range favKeys {
				_, _ = io.WriteString(h, k)
				_, _ = io.WriteString(h, "\x00")
			}
			revKey += "|fav:" + hex.EncodeToString(h.Sum(nil)[:8])
		}
	}
	if ref, ok := m.getReverseCached(revKey); ok {
		return progKey, ref, nil
	}

	var ref *core.LiveEventRef
	if cand.ChannelOnly {
		ref = matchChannelOnlyToEvents(*cand, events, favKeys)
	} else {
		ref = matchProgrammeToEvents(*cand, events)
	}
	m.putReverseCached(revKey, ref)
	return progKey, cloneLiveEventRef(ref), nil
}

func (m *WatchMatcher) getWatchCached(key string) ([]core.WatchTarget, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.watchCache[key]
	if !ok {
		return nil, false
	}
	return cloneWatchTargets(v), true
}

func (m *WatchMatcher) putWatchCached(key string, targets []core.WatchTarget) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.watchCache[key]; !exists {
		if len(m.watchOrder) >= m.watchCap {
			evict := m.watchOrder[0]
			m.watchOrder = m.watchOrder[1:]
			delete(m.watchCache, evict)
		}
		m.watchOrder = append(m.watchOrder, key)
	}
	m.watchCache[key] = cloneWatchTargets(targets)
}

func cloneWatchTargets(in []core.WatchTarget) []core.WatchTarget {
	if in == nil {
		return nil
	}
	out := make([]core.WatchTarget, len(in))
	for i, t := range in {
		if t.Available != nil {
			av := *t.Available
			t.Available = &av
		}
		out[i] = t
	}
	return out
}

func (m *WatchMatcher) getReverseCached(key string) (*core.LiveEventRef, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.reverseCache[key]
	if !ok {
		return nil, false
	}
	return cloneLiveEventRef(v), true
}

func (m *WatchMatcher) putReverseCached(key string, ref *core.LiveEventRef) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.reverseCache[key]; !exists {
		if len(m.reverseOrder) >= m.reverseCap {
			evict := m.reverseOrder[0]
			m.reverseOrder = m.reverseOrder[1:]
			delete(m.reverseCache, evict)
		}
		m.reverseOrder = append(m.reverseOrder, key)
	}
	m.reverseCache[key] = cloneLiveEventRef(ref)
}

func cloneLiveEventRef(ref *core.LiveEventRef) *core.LiveEventRef {
	if ref == nil {
		return nil
	}
	cp := *ref
	return &cp
}

func parseEventTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t
	}
	return time.Time{}
}

func collectEventCallsigns(ev core.LiveEvent) []string {
	var out []string
	for _, b := range ev.Broadcasts {
		if b.Kind != "radio" && b.Name != "" {
			out = append(out, b.Name)
		}
	}
	if ev.MatchHints != nil {
		out = append(out, ev.MatchHints.Callsigns...)
	}
	return out
}

func watchCacheKey(ev core.LiveEvent, guideRev string) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "id=%s|rev=%s|start=%s|state=%s|title=%s|cat=%s|lg=%s:%s",
		ev.ID, guideRev, ev.Start, ev.Status.State, ev.Title, ev.Category, ev.League.Name, ev.League.Abbr)
	for _, c := range ev.Competitors {
		_, _ = fmt.Fprintf(h, "|c=%s:%s:%s", c.Name, c.Short, c.Abbr)
	}
	for _, b := range ev.Broadcasts {
		_, _ = fmt.Fprintf(h, "|b=%s:%s:%s", b.Name, b.Kind, b.URL)
	}
	if ev.MatchHints != nil {
		for _, t := range ev.MatchHints.Terms {
			_, _ = fmt.Fprintf(h, "|t=%s", t)
		}
		for _, cs := range ev.MatchHints.Callsigns {
			_, _ = fmt.Fprintf(h, "|cs=%s", cs)
		}
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}
