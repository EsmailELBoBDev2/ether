package screens

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/favorites"
	"github.com/braedonsaunders/ether/internal/plugins"
)

const defaultHeroCompanions = 3

// ServiceOptions configures a screen resolver Service.
type ServiceOptions struct {
	Registry  *Registry
	Hub       LiveFeedHub
	Watch     WatchTargetResolver
	Favorites FavoriteEntities
	Now       func() time.Time
}

// Service resolves plugin-declared screens and live entity searches on demand.
// It performs no background polling and memoizes repeated hub and watch-target
// lookups within a single Resolve call.
type Service struct {
	registry  *Registry
	favorites FavoriteEntities
	now       func() time.Time

	mu    sync.RWMutex
	hub   LiveFeedHub
	watch WatchTargetResolver
}

// NewService constructs a screen resolver service.
func NewService(opts ServiceOptions) *Service {
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	reg := opts.Registry
	if reg == nil {
		reg = NewRegistry(RegistryOptions{})
	}
	return &Service{
		registry:  reg,
		favorites: opts.Favorites,
		now:       nowFn,
		hub:       opts.Hub,
		watch:     opts.Watch,
	}
}

// Registry returns the underlying screen registry for lifecycle hooks.
func (s *Service) Registry() *Registry { return s.registry }

// Generation returns the registry's navigation generation for bootstrap cache invalidation.
func (s *Service) Generation() uint64 {
	if s == nil || s.registry == nil {
		return 0
	}
	return s.registry.Generation()
}

// SetHub swaps the live event hub implementation (used when W255's liveevents.Hub attaches).
func (s *Service) SetHub(h LiveFeedHub) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.hub = h
	s.mu.Unlock()
}

// SetWatchTargets swaps the watch-target matcher (used when W257's matcher attaches).
func (s *Service) SetWatchTargets(w WatchTargetResolver) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.watch = w
	s.mu.Unlock()
}

func (s *Service) deps() (LiveFeedHub, WatchTargetResolver) {
	s.mu.RLock()
	h, w := s.hub, s.watch
	s.mu.RUnlock()
	return h, w
}

// Navigation lists top-level plugin screens published to frontendID and
// permitted for viewer, matching bootstrap.navigation = [{kind:"screen", id, title, icon?}].
func (s *Service) Navigation(ctx context.Context, viewer *core.User, frontendID string) ([]NavigationItem, error) {
	if s == nil || s.registry == nil || viewer == nil {
		return nil, nil
	}
	declared, err := s.registry.ListScreens(ctx, viewer, frontendID)
	if err != nil {
		return nil, err
	}
	out := make([]NavigationItem, len(declared))
	for i, sc := range declared {
		out[i] = NavigationItem{
			Kind:     "screen",
			ID:       sc.ID,
			Title:    sc.Title,
			Icon:     sc.Icon,
			PluginID: sc.PluginID,
		}
	}
	return out, nil
}

// Resolve builds the generic resolved screen payload for GET /vela/v0/screens/{id}.
func (s *Service) Resolve(ctx context.Context, viewer *core.User, req ResolveRequest) (ResolvedScreen, error) {
	if s == nil || s.registry == nil || viewer == nil {
		return ResolvedScreen{}, core.ErrNotFound
	}
	sc, ok, err := s.registry.FindScreen(ctx, viewer, req.FrontendID, req.ScreenID)
	if err != nil {
		return ResolvedScreen{}, err
	}
	if !ok {
		return ResolvedScreen{}, core.ErrNotFound
	}

	now := s.now().UTC()
	selectedDay := startOfDayUTC(now)
	if strings.TrimSpace(req.Date) != "" {
		parsed, perr := time.Parse("2006-01-02", strings.TrimSpace(req.Date))
		if perr != nil {
			return ResolvedScreen{}, ErrInvalidDate
		}
		selectedDay = parsed.UTC()
	}

	favSet, err := s.loadFavorites(ctx, viewer)
	if err != nil {
		return ResolvedScreen{}, err
	}

	hub, watch := s.deps()
	rc := &resolveContext{
		ctx:        ctx,
		viewer:     viewer,
		pluginID:   sc.PluginID,
		hub:        hub,
		watch:      watch,
		favorites:  favSet,
		now:        now,
		day:        selectedDay,
		reqLeague:  strings.TrimSpace(req.League),
		eventCache: map[eventCacheKey][]core.LiveEvent{},
		watchCache: map[string][]core.WatchTarget{},
	}

	sections := make([]ResolvedSection, 0, len(sc.Sections))
	usedIDs := map[string]bool{}
	for idx, sec := range sc.Sections {
		secID := sec.ID
		if secID == "" {
			if !usedIDs[sec.Type] {
				secID = sec.Type
			} else {
				secID = fmt.Sprintf("%s-%d", sec.Type, idx)
			}
		}
		usedIDs[secID] = true

		feed := sectionFeedHint(sec, sc.DefaultFeed)
		resolved, rerr := rc.resolveSection(secID, sec, feed, sc.EntityKind)
		if rerr != nil {
			return ResolvedScreen{}, rerr
		}
		sections = append(sections, resolved)
	}

	return ResolvedScreen{
		ID:       sc.ID,
		PluginID: sc.PluginID,
		Title:    sc.Title,
		Icon:     sc.Icon,
		Feed:     sc.DefaultFeed,
		League:   rc.reqLeague,
		Date:     selectedDay.Format("2006-01-02"),
		Sections: sections,
	}, nil
}

// QueryEntities searches entities in a live feed and annotates each LiveEntity
// with its canonical favourite key and current viewer favourite state (spec A4).
func (s *Service) QueryEntities(ctx context.Context, viewer *core.User, frontendID, feedRef, kind, league, q string) (EntitySearchResult, error) {
	if s == nil || s.registry == nil || viewer == nil {
		return EntitySearchResult{}, ErrFeedNotFound
	}
	pluginID, feedID, defaultKind, ok := s.registry.ResolveFeed(ctx, viewer, frontendID, feedRef)
	if !ok {
		return EntitySearchResult{}, ErrFeedNotFound
	}
	if strings.TrimSpace(kind) == "" {
		kind = defaultKind
	}
	favSet, err := s.loadFavorites(ctx, viewer)
	if err != nil {
		return EntitySearchResult{}, err
	}
	hub, _ := s.deps()
	var raw []core.LiveEntity
	if hub != nil {
		raw, err = hub.Entities(ctx, viewer, EntityQuery{
			PluginID: pluginID,
			Feed:     feedID,
			Kind:     kind,
			League:   strings.TrimSpace(league),
			Query:    strings.TrimSpace(q),
		})
		if err != nil && !errors.Is(err, core.ErrNotFound) && !errors.Is(err, core.ErrNotAvailable) {
			return EntitySearchResult{}, err
		}
	}
	out := annotateEntities(raw, pluginID, feedID, kind, favSet)
	return EntitySearchResult{
		Feed:       feedID,
		EntityKind: kind,
		Entities:   out,
	}, nil
}

func (s *Service) loadFavorites(ctx context.Context, viewer *core.User) (*favoriteSet, error) {
	fs := newFavoriteSet()
	if s.favorites == nil || viewer == nil {
		return fs, nil
	}
	keys, err := s.favorites.ListEntityKeys(ctx, viewer)
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		fs.add(k)
	}
	return fs, nil
}

type eventCacheKey struct {
	feed   string
	from   int64
	to     int64
	league string
}

type resolveContext struct {
	ctx        context.Context
	viewer     *core.User
	pluginID   string
	hub        LiveFeedHub
	watch      WatchTargetResolver
	favorites  *favoriteSet
	now        time.Time
	day        time.Time
	reqLeague  string
	eventCache map[eventCacheKey][]core.LiveEvent
	watchCache map[string][]core.WatchTarget
}

func (rc *resolveContext) fetchEvents(feed string, from, to time.Time, league string) ([]core.LiveEvent, error) {
	if rc.hub == nil || feed == "" {
		return nil, nil
	}
	key := eventCacheKey{
		feed:   feed,
		from:   from.UTC().Unix(),
		to:     to.UTC().Unix(),
		league: strings.ToLower(strings.TrimSpace(league)),
	}
	if cached, ok := rc.eventCache[key]; ok {
		return cached, nil
	}
	events, err := rc.hub.Events(rc.ctx, rc.viewer, EventQuery{
		PluginID: rc.pluginID,
		Feed:     feed,
		From:     from.UTC(),
		To:       to.UTC(),
		League:   strings.TrimSpace(league),
	})
	if err != nil {
		return nil, err
	}
	rc.eventCache[key] = events
	return events, nil
}

func (rc *resolveContext) resolveEvent(ev core.LiveEvent, feed, entityKind string) ResolvedEvent {
	var targets []core.WatchTarget
	if rc.watch != nil {
		if cached, ok := rc.watchCache[ev.ID]; ok {
			targets = cached
		} else {
			wt, err := rc.watch.WatchTargets(rc.ctx, rc.viewer, ev)
			if err == nil && len(wt) > 0 {
				targets = wt
			} else {
				targets = []core.WatchTarget{}
			}
			rc.watchCache[ev.ID] = targets
		}
	}
	if targets == nil {
		targets = []core.WatchTarget{}
	}
	evFeed := ev.Feed
	if evFeed == "" {
		evFeed = feed
	}
	fav := rc.favorites.matchesEvent(rc.pluginID, evFeed, entityKind, ev)
	return ResolvedEvent{
		LiveEvent: ev,
		Watch:     targets,
		Favorite:  fav,
	}
}

func (rc *resolveContext) resolveSection(secID string, sec plugins.ScreenSection, feed, defaultEntityKind string) (ResolvedSection, error) {
	entKind := sec.EntityKind
	if entKind == "" {
		entKind = defaultEntityKind
	}
	if entKind == "" {
		entKind = "team"
	}
	league := rc.effectiveLeague(sec)

	out := ResolvedSection{
		ID:     secID,
		Type:   sec.Type,
		Title:  sec.Title,
		Feed:   feed,
		League: league,
	}

	switch sec.Type {
	case "hero":
		return rc.resolveHero(out, sec, feed, entKind, league)
	case "events":
		return rc.resolveEvents(out, sec, feed, entKind, league)
	case "table":
		return rc.resolveTable(out, feed, league)
	case "schedule":
		return rc.resolveSchedule(out, sec, feed, entKind, league)
	case "leagues":
		return rc.resolveLeagues(out, sec, feed)
	case "entities":
		out.EntityKind = entKind
		return rc.resolveEntities(out, feed, entKind, league)
	default:
		return out, nil
	}
}

func (rc *resolveContext) effectiveLeague(sec plugins.ScreenSection) string {
	if rc.reqLeague != "" {
		return rc.reqLeague
	}
	if sec.Query != nil && strings.TrimSpace(sec.Query.League) != "" {
		return strings.TrimSpace(sec.Query.League)
	}
	return strings.TrimSpace(sec.League)
}

func (rc *resolveContext) resolveHero(out ResolvedSection, sec plugins.ScreenSection, feed, entityKind, league string) (ResolvedSection, error) {
	window := "today"
	if sec.Query != nil && sec.Query.Window != "" {
		window = sec.Query.Window
	}
	from, to := windowRange(rc.now, window, nil)
	raw, err := rc.fetchEvents(feed, from, to, league)
	if err != nil {
		return out, err
	}
	filtered := rc.filterEvents(raw, feed, entityKind, sec.Query, league, from, to, window)
	resolved := make([]ResolvedEvent, 0, len(filtered))
	for _, ev := range filtered {
		resolved = append(resolved, rc.resolveEvent(ev, feed, entityKind))
	}
	sort.SliceStable(resolved, func(i, j int) bool {
		return heroEventBefore(resolved[i], resolved[j])
	})

	if len(resolved) > 0 {
		hero := resolved[0]
		out.Event = &hero
		companions := defaultHeroCompanions
		if sec.Limit > 0 {
			companions = sec.Limit - 1
		}
		if companions > 0 && len(resolved) > 1 {
			end := min(1+companions, len(resolved))
			out.Also = append([]ResolvedEvent(nil), resolved[1:end]...)
		}
	}
	return out, nil
}

func (rc *resolveContext) resolveEvents(out ResolvedSection, sec plugins.ScreenSection, feed, entityKind, league string) (ResolvedSection, error) {
	window := "today"
	var states []string
	if sec.Query != nil {
		qCopy := *sec.Query
		qCopy.State = slices.Clone(sec.Query.State)
		if league != "" {
			qCopy.League = league
		}
		out.Query = &qCopy
		if sec.Query.Window != "" {
			window = sec.Query.Window
		}
		states = sec.Query.State
	}
	from, to := windowRange(rc.now, window, states)
	raw, err := rc.fetchEvents(feed, from, to, league)
	if err != nil {
		return out, err
	}
	filtered := rc.filterEvents(raw, feed, entityKind, sec.Query, league, from, to, window)
	resolved := make([]ResolvedEvent, 0, len(filtered))
	for _, ev := range filtered {
		resolved = append(resolved, rc.resolveEvent(ev, feed, entityKind))
	}
	recentOrder := window == "recent" || (len(states) == 1 && states[0] == core.LiveEventStatePost)
	sort.SliceStable(resolved, func(i, j int) bool {
		if recentOrder {
			if !resolved[i].Start.Equal(resolved[j].Start) {
				return resolved[i].Start.After(resolved[j].Start)
			}
			return resolved[i].ID < resolved[j].ID
		}
		return rowEventBefore(resolved[i], resolved[j])
	})
	if sec.Limit > 0 && len(resolved) > sec.Limit {
		resolved = resolved[:sec.Limit]
	}
	out.Events = resolved
	return out, nil
}

func (rc *resolveContext) resolveTable(out ResolvedSection, feed, league string) (ResolvedSection, error) {
	out.Tables = []core.LiveEventTable{}
	if rc.hub == nil || feed == "" {
		return out, nil
	}
	tables, err := rc.hub.Standings(rc.ctx, rc.viewer, rc.pluginID, feed, league)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) || errors.Is(err, core.ErrNotAvailable) {
			return out, nil
		}
		return out, err
	}
	if tables != nil {
		out.Tables = tables
	}
	return out, nil
}

func (rc *resolveContext) resolveSchedule(out ResolvedSection, sec plugins.ScreenSection, feed, entityKind, league string) (ResolvedSection, error) {
	from := rc.day
	to := rc.day.Add(24 * time.Hour)
	out.Date = from.Format("2006-01-02")
	out.Previous = from.AddDate(0, 0, -1).Format("2006-01-02")
	out.Next = from.AddDate(0, 0, 1).Format("2006-01-02")

	raw, err := rc.fetchEvents(feed, from, to, league)
	if err != nil {
		return out, err
	}
	filtered := rc.filterEvents(raw, feed, entityKind, sec.Query, league, from, to, "")
	resolved := make([]ResolvedEvent, 0, len(filtered))
	for _, ev := range filtered {
		resolved = append(resolved, rc.resolveEvent(ev, feed, entityKind))
	}
	sort.SliceStable(resolved, func(i, j int) bool {
		if !resolved[i].Start.Equal(resolved[j].Start) {
			return resolved[i].Start.Before(resolved[j].Start)
		}
		return resolved[i].ID < resolved[j].ID
	})
	if sec.Limit > 0 && len(resolved) > sec.Limit {
		resolved = resolved[:sec.Limit]
	}
	out.Events = resolved
	out.Days = scheduleDayStrip(rc.now, rc.day, len(resolved))
	return out, nil
}

func scheduleDayStrip(now, selected time.Time, selectedCount int) []ScheduleDay {
	today := startOfDayUTC(now)
	selDate := selected.Format("2006-01-02")
	days := make([]ScheduleDay, 0, 7)
	for offset := -2; offset <= 4; offset++ {
		d := selected.AddDate(0, 0, offset)
		dateStr := d.Format("2006-01-02")
		dayDiff := int(d.Sub(today) / (24 * time.Hour))
		var label string
		switch dayDiff {
		case -1:
			label = "Yesterday"
		case 0:
			label = "Today"
		case 1:
			label = "Tomorrow"
		default:
			label = d.Format("Mon Jan 2")
		}
		entry := ScheduleDay{Date: dateStr, Label: label}
		if dateStr == selDate && selectedCount > 0 {
			entry.Count = selectedCount
		}
		days = append(days, entry)
	}
	return days
}

func (rc *resolveContext) resolveLeagues(out ResolvedSection, sec plugins.ScreenSection, feed string) (ResolvedSection, error) {
	// Fetch unfiltered events so choosing one league chip never hides the other chips.
	from, to := windowRange(rc.now, "today", nil)
	raw, err := rc.fetchEvents(feed, from, to, "")
	if err != nil {
		return out, err
	}
	discovered := map[string]core.LiveEventLeague{}
	order := []string{}
	for _, ev := range raw {
		id := strings.TrimSpace(ev.League.ID)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if _, ok := discovered[key]; !ok {
			discovered[key] = ev.League
			order = append(order, key)
		}
	}

	activeKey := strings.ToLower(rc.reqLeague)
	chips := make([]core.LiveEventLeague, 0, len(sec.Leagues)+len(order))
	seen := map[string]bool{}
	selected := ""
	for _, lg := range sec.Leagues {
		key := strings.ToLower(strings.TrimSpace(lg.ID))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		if activeKey != "" && key == activeKey {
			selected = lg.ID
		}
		chips = append(chips, core.LiveEventLeague{
			ID:   lg.ID,
			Name: lg.Name,
			Abbr: lg.Abbr,
			Logo: lg.Logo,
		})
	}
	if len(sec.Leagues) == 0 {
		for _, key := range order {
			if seen[key] {
				continue
			}
			seen[key] = true
			lg := discovered[key]
			if activeKey != "" && key == activeKey {
				selected = lg.ID
			}
			chips = append(chips, lg)
		}
	}
	if selected == "" && rc.reqLeague != "" {
		selected = rc.reqLeague
	}
	out.Leagues = chips
	out.Selected = selected
	return out, nil
}

func (rc *resolveContext) resolveEntities(out ResolvedSection, feed, entityKind, league string) (ResolvedSection, error) {
	out.Entities = []ResolvedEntity{}
	if rc.hub == nil || feed == "" {
		return out, nil
	}
	raw, err := rc.hub.Entities(rc.ctx, rc.viewer, EntityQuery{
		PluginID: rc.pluginID,
		Feed:     feed,
		Kind:     entityKind,
		League:   league,
	})
	if err != nil {
		if errors.Is(err, core.ErrNotFound) || errors.Is(err, core.ErrNotAvailable) {
			return out, nil
		}
		return out, err
	}
	out.Entities = annotateEntities(raw, rc.pluginID, feed, entityKind, rc.favorites)
	return out, nil
}

func (rc *resolveContext) filterEvents(raw []core.LiveEvent, feed, entityKind string, q *plugins.ScreenEventQuery, league string, from, to time.Time, window string) []core.LiveEvent {
	out := make([]core.LiveEvent, 0, len(raw))
	for _, ev := range raw {
		if league != "" && !strings.EqualFold(ev.League.ID, league) {
			continue
		}
		if q != nil {
			if len(q.State) > 0 && !slices.Contains(q.State, ev.Status.State) {
				continue
			}
			if q.Category != "" && !strings.EqualFold(ev.Category, q.Category) {
				continue
			}
			if q.Favorites {
				evFeed := ev.Feed
				if evFeed == "" {
					evFeed = feed
				}
				if !rc.favorites.matchesEvent(rc.pluginID, evFeed, entityKind, ev) {
					continue
				}
			}
		}
		// Live in-progress events stay visible in today/week views even if their
		// start time fell just before midnight UTC.
		liveKeep := ev.Status.State == core.LiveEventStateIn && (window == "today" || window == "week")
		if !liveKeep && !ev.Start.IsZero() {
			if !from.IsZero() && ev.Start.Before(from) {
				continue
			}
			if !to.IsZero() && !ev.Start.Before(to) {
				continue
			}
		}
		out = append(out, ev)
	}
	return out
}

func windowRange(now time.Time, window string, states []string) (time.Time, time.Time) {
	day := startOfDayUTC(now)
	switch window {
	case "week":
		includePast := len(states) == 0 || slices.Contains(states, core.LiveEventStatePost)
		if includePast {
			return day.AddDate(0, 0, -1), day.AddDate(0, 0, 7)
		}
		return day, day.AddDate(0, 0, 7)
	case "recent":
		return now.UTC().Add(-72 * time.Hour), now.UTC().Add(time.Minute)
	default: // "today"
		return day, day.Add(24 * time.Hour)
	}
}

func startOfDayUTC(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// heroEventBefore ranks candidate events for the hero section:
// favorite live > close live (unless spoiler-hidden) > ranked/other live >
// favorite upcoming > other upcoming > favorite final > other final > other.
func heroEventBefore(a, b ResolvedEvent) bool {
	pa, pb := heroTier(a), heroTier(b)
	if pa != pb {
		return pa < pb
	}
	ra, rb := bestCompetitorRank(a.LiveEvent), bestCompetitorRank(b.LiveEvent)
	if ra != rb {
		return ra < rb
	}
	if a.Status.State == core.LiveEventStatePost && b.Status.State == core.LiveEventStatePost {
		if !a.Start.Equal(b.Start) {
			return a.Start.After(b.Start)
		}
	} else if !a.Start.Equal(b.Start) {
		return a.Start.Before(b.Start)
	}
	return a.ID < b.ID
}

func heroTier(ev ResolvedEvent) int {
	switch ev.Status.State {
	case core.LiveEventStateIn:
		if ev.Favorite {
			return 0
		}
		// Never leak close-game status through hero ranking when spoilers are hidden.
		if !ev.Spoiler && ev.Status.Close {
			return 1
		}
		return 2
	case core.LiveEventStatePre:
		if ev.Favorite {
			return 3
		}
		return 4
	case core.LiveEventStatePost:
		if ev.Favorite {
			return 5
		}
		return 6
	default:
		if ev.Favorite {
			return 7
		}
		return 8
	}
}

func rowEventBefore(a, b ResolvedEvent) bool {
	sa, sb := stateOrder(a.Status.State), stateOrder(b.Status.State)
	if sa != sb {
		return sa < sb
	}
	if a.Favorite != b.Favorite {
		return a.Favorite
	}
	if a.Status.State == core.LiveEventStateIn && !a.Spoiler && !b.Spoiler && a.Status.Close != b.Status.Close {
		return a.Status.Close
	}
	if a.Status.State == core.LiveEventStatePost {
		if !a.Start.Equal(b.Start) {
			return a.Start.After(b.Start)
		}
	} else if !a.Start.Equal(b.Start) {
		return a.Start.Before(b.Start)
	}
	return a.ID < b.ID
}

func stateOrder(state string) int {
	switch state {
	case core.LiveEventStateIn:
		return 0
	case core.LiveEventStatePre:
		return 1
	case core.LiveEventStateDelayed:
		return 2
	case core.LiveEventStatePost:
		return 3
	case core.LiveEventStatePostponed:
		return 4
	case core.LiveEventStateCanceled:
		return 5
	default:
		return 6
	}
}

func bestCompetitorRank(ev core.LiveEvent) int {
	best := 1 << 30
	for _, c := range ev.Competitors {
		if c.Rank > 0 && c.Rank < best {
			best = c.Rank
		}
	}
	return best
}

func annotateEntities(raw []core.LiveEntity, pluginID, feedID, fallbackKind string, favSet *favoriteSet) []ResolvedEntity {
	out := make([]ResolvedEntity, 0, len(raw))
	for _, e := range raw {
		kind := strings.TrimSpace(e.Kind)
		if kind == "" {
			kind = fallbackKind
		}
		if kind == "" {
			kind = "team"
		}
		key := favorites.FormatEntityKey(pluginID, feedID, kind, e.ID)
		out = append(out, ResolvedEntity{
			Key:      key,
			ID:       e.ID,
			Kind:     kind,
			Name:     e.Name,
			Abbr:     e.Abbr,
			Logo:     e.Logo,
			League:   e.League,
			Color:    e.Color,
			Favorite: favSet.hasKey(key),
		})
	}
	return out
}

type favoriteSet struct {
	full   map[string]bool
	byFeed map[string]map[string]bool // "pluginID:feed" -> { "kind:id", "id" }
}

func newFavoriteSet() *favoriteSet {
	return &favoriteSet{
		full:   map[string]bool{},
		byFeed: map[string]map[string]bool{},
	}
}

func (f *favoriteSet) add(rawKey string) {
	if f == nil {
		return
	}
	parsed, ok := favorites.ParseEntityKey(rawKey)
	if !ok {
		return
	}
	f.full[rawKey] = true
	scope := parsed.PluginID + ":" + parsed.Feed
	m := f.byFeed[scope]
	if m == nil {
		m = map[string]bool{}
		f.byFeed[scope] = m
	}
	m[parsed.Kind+":"+parsed.ID] = true
	m[parsed.ID] = true
}

func (f *favoriteSet) hasKey(rawKey string) bool {
	return f != nil && f.full[rawKey]
}

func (f *favoriteSet) matchesEvent(pluginID, feed, entityKind string, ev core.LiveEvent) bool {
	if f == nil || len(f.full) == 0 {
		return false
	}
	matchEntityID := func(raw string) bool {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return false
		}
		if f.full[raw] {
			return true
		}
		scope := pluginID + ":" + feed
		m := f.byFeed[scope]
		if len(m) == 0 {
			return false
		}
		if entityKind != "" && m[entityKind+":"+raw] {
			return true
		}
		return m[raw]
	}
	for _, c := range ev.Competitors {
		if matchEntityID(c.EntityID) {
			return true
		}
	}
	for _, r := range ev.Ranking {
		if matchEntityID(r.EntityID) {
			return true
		}
	}
	return false
}
