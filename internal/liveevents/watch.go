package liveevents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
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
		Title:         e.Programme.Item.Title,
		EpisodeTitle:  e.Programme.Item.EpisodeTitle,
		Summary:       e.Programme.Item.Summary,
		Genres:        slices.Clone(e.Programme.Item.Genres),
		Live:          e.Live,
	}
}

// WatchMatcher implements A5 ("Where to watch") and A6 (reverse channel->event
// linking), caching forward results per event + guide revision and reverse
// results per programme + events revision within bounded memory.
type WatchMatcher struct {
	guide        GuideSource
	activeEvents ActiveEventsSource
	now          func() time.Time

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
	Guide        GuideSource
	ActiveEvents ActiveEventsSource
	Now          func() time.Time
	CacheCap     int
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
		guide:        opts.Guide,
		activeEvents: opts.ActiveEvents,
		now:          nowFn,
		watchCap:     capVal,
		watchCache:   make(map[string][]core.WatchTarget),
		reverseCap:   capVal,
		reverseCache: make(map[string]*core.LiveEventRef),
	}
}

// WatchTargets resolves where viewer can watch ev (A5).
// Results are cached per event fingerprint and viewer guide revision.
func (m *WatchMatcher) WatchTargets(ctx context.Context, ev core.LiveEvent, viewer *core.User, pluginLinks ...core.LiveEventLink) ([]core.WatchTarget, error) {
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

	cacheKey := watchCacheKey(ev, pluginLinks, guideRev)
	if cached, ok := m.getWatchCached(cacheKey); ok {
		return cached, nil
	}

	var candidates []GuideCandidate
	if m.guide != nil && viewer != nil && !ev.Start.IsZero() {
		winFrom := ev.Start.Add(-time.Duration(WatchWindowBefore) * time.Second)
		winUntil := ev.Start.Add(time.Duration(WatchWindowAfter) * time.Second)
		callsigns := collectEventCallsigns(ev)
		cands, rev, err := m.guide.WatchCandidates(ctx, viewer, winFrom, winUntil, callsigns)
		if err != nil {
			return nil, err
		}
		candidates = cands
		if rev != "" && rev != guideRev {
			cacheKey = watchCacheKey(ev, pluginLinks, rev)
		}
	}

	targets := matchEventToCandidates(ev, candidates, pluginLinks)
	m.putWatchCached(cacheKey, targets)
	return slices.Clone(targets), nil
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
	if err != nil || cand == nil || progKey == "" {
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
	if ref, ok := m.getReverseCached(revKey); ok {
		return progKey, ref, nil
	}

	ref := matchProgrammeToEvents(*cand, events)
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
	return slices.Clone(v), true
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
	m.watchCache[key] = slices.Clone(targets)
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

func collectEventCallsigns(ev core.LiveEvent) []string {
	var out []string
	for _, b := range ev.Broadcasts {
		if b.Kind != "radio" && b.Name != "" {
			out = append(out, b.Name)
		}
	}
	out = append(out, ev.MatchHints.Callsigns...)
	return out
}

func watchCacheKey(ev core.LiveEvent, links []core.LiveEventLink, guideRev string) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "id=%s|rev=%s|start=%d|state=%s|title=%s|cat=%s|lg=%s:%s",
		ev.ID, guideRev, ev.Start.Unix(), ev.Status.State, ev.Title, ev.Category, ev.League.Name, ev.League.Abbr)
	for _, c := range ev.Competitors {
		_, _ = fmt.Fprintf(h, "|c=%s:%s:%s", c.Name, c.Short, c.Abbr)
	}
	for _, b := range ev.Broadcasts {
		_, _ = fmt.Fprintf(h, "|b=%s:%s:%s", b.Name, b.Kind, b.URL)
	}
	for _, t := range ev.MatchHints.Terms {
		_, _ = fmt.Fprintf(h, "|t=%s", t)
	}
	for _, cs := range ev.MatchHints.Callsigns {
		_, _ = fmt.Fprintf(h, "|cs=%s", cs)
	}
	for _, l := range links {
		_, _ = fmt.Fprintf(h, "|l=%s:%s", l.Label, l.URL)
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}
