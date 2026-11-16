package plex

// Each owner group is advertised as its own Plex Live TV source: a separate
// MediaProvider (tv.plex.providers.epg.xmltv:<n>) and DVR, mirroring how PMS
// presents multiple DVRs/EPG providers, so clients can switch sources. The
// number derives from the group id (never its name), so identity survives
// renames and restarts.

import (
	"context"
	"hash/fnv"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/go-chi/chi/v5"
)

type liveProviderInfo struct {
	num     int
	groupID string // "" is the all-channels provider
	title   string
}

func (p liveProviderInfo) identifier() string {
	return "tv.plex.providers.epg.xmltv:" + strconv.Itoa(p.num)
}
func (p liveProviderInfo) path() string { return "/" + p.identifier() }

var mainLiveProvider = liveProviderInfo{num: 1, title: "Live TV"}

func liveGroupProviderNum(groupID string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte("ether-livetv-group:" + groupID))
	return 1000 + int(h.Sum32()%900_000_000)
}

type liveProvidersEntry struct {
	lineup time.Time
	infos  []liveProviderInfo
}
type liveProvidersCache struct {
	mu       sync.Mutex
	byViewer map[int64]liveProvidersEntry
}

// liveProviders lists the main provider plus one per visible owner group,
// cached per viewer against the lineup epoch (group saves and channel
// changes both bump it), so provider requests never rescan the lineup.
func (s *Server) liveProviders(ctx context.Context, u *frontend.User) []liveProviderInfo {
	infos := []liveProviderInfo{mainLiveProvider}
	g, ok := s.svc().liveTV.(frontend.LiveTVGroups)
	if !ok || u == nil {
		return infos
	}
	lineup := s.svc().liveTV.LineupChangedFor(u)
	c := &s.liveProviderList
	c.mu.Lock()
	if e, hit := c.byViewer[u.ID]; hit && e.lineup.Equal(lineup) {
		c.mu.Unlock()
		return e.infos
	}
	c.mu.Unlock()
	groups, err := g.Groups(ctx, u)
	if err != nil {
		s.log.Warn("live group providers unavailable", "error", err)
		return infos
	}
	// Numbers are assigned on an id-sorted copy (deterministic collision
	// resolution: same group set, same numbers) but providers are listed in
	// owner order.
	sorted := slices.Clone(groups)
	slices.SortFunc(sorted, func(a, b frontend.LiveGroup) int { return strings.Compare(a.ID, b.ID) })
	numByID := make(map[string]int, len(sorted))
	used := map[int]bool{1: true}
	for _, gd := range sorted {
		n := liveGroupProviderNum(gd.ID)
		for used[n] {
			n++
		}
		used[n] = true
		numByID[gd.ID] = n
	}
	for _, gd := range groups {
		infos = append(infos, liveProviderInfo{num: numByID[gd.ID], groupID: gd.ID, title: gd.Name})
	}
	c.mu.Lock()
	if c.byViewer == nil {
		c.byViewer = map[int64]liveProvidersEntry{}
	}
	if len(c.byViewer) >= 32 {
		clear(c.byViewer)
	}
	c.byViewer[u.ID] = liveProvidersEntry{lineup: lineup, infos: infos}
	c.mu.Unlock()
	return infos
}

func (s *Server) resolveLiveProviderNum(ctx context.Context, u *frontend.User, num int) (liveProviderInfo, bool) {
	if num == 1 {
		// The main provider always exists; resolving it must not load the
		// group list (the tune path is query-budgeted per channel).
		return mainLiveProvider, true
	}
	for _, p := range s.liveProviders(ctx, u) {
		if p.num == num {
			return p, true
		}
	}
	return liveProviderInfo{}, false
}

// liveScope carries one request's provider. Group channel membership is
// resolved once per request through GroupChannels and reused by handlers.
type liveScope struct {
	info     liveProviderInfo
	resolved bool
	channels []frontend.LiveTVChannel
	set      map[string]bool
	err      error
}

type liveProviderNumKey struct{}
type liveScopeKey struct{}

func liveScopeFrom(r *http.Request) *liveScope {
	if sc, ok := r.Context().Value(liveScopeKey{}).(*liveScope); ok && sc != nil {
		return sc
	}
	return &liveScope{info: mainLiveProvider}
}

func (s *Server) liveScopeChannels(r *http.Request, sc *liveScope) ([]frontend.LiveTVChannel, map[string]bool, error) {
	if sc.info.groupID == "" {
		return nil, nil, nil
	}
	if !sc.resolved {
		sc.resolved = true
		g, ok := s.svc().liveTV.(frontend.LiveTVGroups)
		if !ok {
			sc.err = frontend.ErrNotFound
		} else {
			sc.channels, sc.err = g.GroupChannels(r.Context(), FromContext(r.Context()).User, sc.info.groupID)
		}
		if sc.err == nil {
			sc.set = make(map[string]bool, len(sc.channels))
			for _, c := range sc.channels {
				sc.set[c.ID] = true
			}
		}
	}
	return sc.channels, sc.set, sc.err
}

// rewriteLiveGroupProvider maps /tv.plex.providers.epg.xmltv:<n>/... (n>1)
// onto the main provider routes and records n for the Live TV guard, which
// resolves it to a group. Unknown numbers 404 in the guard.
func (s *Server) rewriteLiveGroupProvider(next http.Handler) http.Handler {
	segment := func(path string) (int, string, bool) {
		const pfx = "/tv.plex.providers.epg.xmltv:"
		if !strings.HasPrefix(path, pfx) {
			return 0, "", false
		}
		rest := path[len(pfx):]
		seg, tail := rest, ""
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			seg, tail = rest[:i], rest[i:]
		}
		n, err := strconv.Atoi(seg)
		if err != nil || n <= 1 {
			return 0, "", false
		}
		return n, tail, true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n, tail, ok := segment(r.URL.Path); ok {
			r = r.WithContext(context.WithValue(r.Context(), liveProviderNumKey{}, n))
			r.URL.Path = liveProviderPath + tail
			if r.URL.RawPath != "" {
				if _, rawTail, rawOK := segment(r.URL.RawPath); rawOK {
					r.URL.RawPath = liveProviderPath + rawTail
				} else {
					r.URL.RawPath = ""
				}
			}
			if rctx := chi.RouteContext(r.Context()); rctx != nil && rctx.RoutePath != "" {
				if _, rawTail, rawOK := segment(rctx.RoutePath); rawOK {
					rctx.RoutePath = liveProviderPath + rawTail
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
