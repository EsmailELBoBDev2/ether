package livetv

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/store"
	"github.com/go-chi/chi/v5"
)

func init() {
	store.Register("livetv", 20, `CREATE TABLE livetv_owner_guides(id INTEGER PRIMARY KEY, data TEXT NOT NULL);`)

	sqliteV24 := `ALTER TABLE livetv_owner_guides RENAME TO livetv_owner_groups;
ALTER TABLE livetv_guide_visibility RENAME TO livetv_group_visibility;
CREATE TRIGGER livetv_owner_groups_epoch_insert AFTER INSERT ON livetv_owner_groups BEGIN UPDATE livetv_channel_epoch SET generation=generation+1 WHERE id=1; END;
CREATE TRIGGER livetv_owner_groups_epoch_update AFTER UPDATE ON livetv_owner_groups BEGIN UPDATE livetv_channel_epoch SET generation=generation+1 WHERE id=1; END;
CREATE TRIGGER livetv_owner_groups_epoch_delete AFTER DELETE ON livetv_owner_groups BEGIN UPDATE livetv_channel_epoch SET generation=generation+1 WHERE id=1; END;`
	postgresV24 := `ALTER TABLE livetv_owner_guides RENAME TO livetv_owner_groups;
ALTER TABLE livetv_guide_visibility RENAME TO livetv_group_visibility;
CREATE TRIGGER livetv_owner_groups_epoch AFTER INSERT OR UPDATE OR DELETE ON livetv_owner_groups FOR EACH STATEMENT EXECUTE FUNCTION livetv_channel_epoch_bump();`

	store.Register("livetv", 24, sqliteV24)
	store.RegisterDialect("livetv", 24, store.Postgres, postgresV24)

	store.Register("livetv", 25, "SELECT 1;")
	store.RegisterApply("livetv", 25, migrateOwnerGroupsV25)
}

func migrateOwnerGroupsV25(ctx context.Context, tx *sql.Tx) error {
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT data FROM livetv_owner_groups WHERE id=1`).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var gs []OwnerGroup
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &gs); err != nil {
			return fmt.Errorf("live TV owner groups: %w", err)
		}
	}
	if err := saveGroupVisibility(ctx, tx, gs); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, data FROM livetv_sources WHERE data LIKE '%"hubGroups"%'`)
	if err != nil {
		return err
	}
	type sourceUpdate struct {
		id   string
		data []byte
	}
	var updates []sourceUpdate
	for rows.Next() {
		var id string
		var srcRaw []byte
		if err := rows.Scan(&id, &srcRaw); err != nil {
			rows.Close()
			return err
		}
		var data map[string]json.RawMessage
		if err := json.Unmarshal(srcRaw, &data); err != nil {
			rows.Close()
			return fmt.Errorf("live TV source %s: %w", id, err)
		}
		delete(data, "hubGroups")
		b, err := json.Marshal(data)
		if err != nil {
			rows.Close()
			return err
		}
		updates = append(updates, sourceUpdate{id: id, data: b})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, u := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE livetv_sources SET data=? WHERE id=?`, string(u.data), u.id); err != nil {
			return err
		}
	}
	return nil
}

// OwnerGroupSource identifies a provider group without relying on its display name alone.
type OwnerGroupSource struct {
	SourceID string `json:"sourceId"`
	Name     string `json:"name"`
}

func (g *OwnerGroupSource) UnmarshalJSON(data []byte) error {
	var wire struct {
		SourceID   string `json:"sourceId"`
		Name       string `json:"name"`
		SourceName string `json:"sourceName,omitempty"`
		Count      int    `json:"count,omitempty"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return err
	}
	g.SourceID = wire.SourceID
	g.Name = wire.Name
	return nil
}

// OwnerGroup is one of the owner's named channel groups ("Your groups").
//
// Membership is a union of whole provider groups (Groups), individual channel IDs
// (Channels), or the entire lineup (All). Because provider-group membership is
// stored by (SourceID, Name), channels a provider adds on later refreshes join
// the group and inherit its UnratedRating and Apps publication rules automatically.
//
// Apps maps app surface names (see Apps in publication.go) to whether members of
// this group are published there (true = shown, false = hidden; omitted = no rule).
// Explicit per-channel overrides (Channel.HiddenApps and Channel.ShownApps) take
// precedence over group rules; when multiple visible groups match a channel, the
// first group in owner order that sets Apps[app] wins.
type OwnerGroup struct {
	ID            string             `json:"id"`
	Name          string             `json:"name"`
	Groups        []OwnerGroupSource `json:"groups"`
	Channels      []string           `json:"channels,omitempty"`
	All           bool               `json:"all,omitempty"`
	Hidden        bool               `json:"hidden,omitempty"`
	UnratedRating string             `json:"unratedRating,omitempty"`
	Apps          map[string]bool    `json:"apps,omitempty"`
	HiddenApps    []string           `json:"hiddenApps,omitempty"`
	// ChannelCount is computed on owner API responses and ignored when saving.
	ChannelCount int `json:"channelCount,omitempty"`
}

// OwnerGroupChoice describes one imported provider group available for membership.
type OwnerGroupChoice struct {
	OwnerGroupSource
	SourceName string `json:"sourceName,omitempty"`
	Count      int    `json:"count"`
}

func (g OwnerGroup) contains(source, group string) bool {
	return g.All || slices.Contains(g.Groups, OwnerGroupSource{SourceID: source, Name: group})
}

func (g OwnerGroup) matchesChannel(source, group, channelID string) bool {
	return g.contains(source, group) || slices.Contains(g.Channels, channelID)
}

type ownerGroupAppRule struct {
	order int
	show  bool
	set   bool
}

type ownerGroupEntry struct {
	group    OwnerGroup
	sources  map[OwnerGroupSource]struct{}
	channels map[string]struct{}
}

// ownerGroupIndex compiles saved OwnerGroup rules once per livetv_channel_epoch
// generation so channel publication, ratings, and group memberships run in O(1)
// without per-request channel scans or extra SQL reads.
type ownerGroupIndex struct {
	groups        []OwnerGroup
	byID          map[string]ownerGroupEntry
	ratings       *groupRatingIndex
	appAll        map[string]ownerGroupAppRule
	appGroups     map[string]map[OwnerGroupSource]ownerGroupAppRule
	appChans      map[string]map[string]ownerGroupAppRule
	appHasHide    map[string]bool
	visibleAll    []frontend.LiveGroup
	visibleGroups map[OwnerGroupSource][]frontend.LiveGroup
	visibleChans  map[string][]frontend.LiveGroup
	visibleOrder  map[string]int
}

func compileOwnerGroupIndex(gs []OwnerGroup) *ownerGroupIndex {
	if gs == nil {
		gs = []OwnerGroup{}
	}
	x := &ownerGroupIndex{
		groups:        gs,
		byID:          make(map[string]ownerGroupEntry, len(gs)),
		ratings:       indexGroupRatings(gs),
		appAll:        make(map[string]ownerGroupAppRule, len(Apps)),
		appGroups:     make(map[string]map[OwnerGroupSource]ownerGroupAppRule, len(Apps)),
		appChans:      make(map[string]map[string]ownerGroupAppRule, len(Apps)),
		appHasHide:    make(map[string]bool, len(Apps)),
		visibleGroups: map[OwnerGroupSource][]frontend.LiveGroup{},
		visibleChans:  map[string][]frontend.LiveGroup{},
		visibleOrder:  make(map[string]int, len(gs)),
	}
	for _, app := range Apps {
		x.appGroups[app] = map[OwnerGroupSource]ownerGroupAppRule{}
		x.appChans[app] = map[string]ownerGroupAppRule{}
	}
	for i, g := range gs {
		x.visibleOrder[g.ID] = i
		entry := ownerGroupEntry{
			group:    g,
			sources:  make(map[OwnerGroupSource]struct{}, len(g.Groups)),
			channels: make(map[string]struct{}, len(g.Channels)),
		}
		for _, sg := range g.Groups {
			entry.sources[sg] = struct{}{}
		}
		for _, cid := range g.Channels {
			entry.channels[cid] = struct{}{}
		}
		x.byID[g.ID] = entry
		if g.Hidden {
			continue
		}
		lg := frontend.LiveGroup{ID: g.ID, Name: g.Name}
		if g.All {
			x.visibleAll = append(x.visibleAll, lg)
		}
		for _, sg := range g.Groups {
			x.visibleGroups[sg] = append(x.visibleGroups[sg], lg)
		}
		for _, cid := range g.Channels {
			x.visibleChans[cid] = append(x.visibleChans[cid], lg)
		}
		for app, show := range g.Apps {
			if !slices.Contains(Apps, app) {
				continue
			}
			if !show {
				x.appHasHide[app] = true
			}
			rule := ownerGroupAppRule{order: i, show: show, set: true}
			if g.All && !x.appAll[app].set {
				x.appAll[app] = rule
			}
			gm := x.appGroups[app]
			for _, sg := range g.Groups {
				if _, exists := gm[sg]; !exists {
					gm[sg] = rule
				}
			}
			cm := x.appChans[app]
			for _, cid := range g.Channels {
				if _, exists := cm[cid]; !exists {
					cm[cid] = rule
				}
			}
		}
	}
	return x
}

func decodeOwnerGroupIndex(raw string) (*ownerGroupIndex, error) {
	var gs []OwnerGroup
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &gs); err != nil {
			return nil, err
		}
	}
	return compileOwnerGroupIndex(gs), nil
}

func (x *ownerGroupIndex) groupAppRule(sourceID, group, channelID, app string) (bool, bool) {
	if x == nil {
		return true, false
	}
	best := x.appAll[app]
	if gm := x.appGroups[app]; gm != nil {
		if cand := gm[OwnerGroupSource{SourceID: sourceID, Name: group}]; cand.set && (!best.set || cand.order < best.order) {
			best = cand
		}
	}
	if cm := x.appChans[app]; cm != nil {
		if cand := cm[channelID]; cand.set && (!best.set || cand.order < best.order) {
			best = cand
		}
	}
	if !best.set {
		return true, false
	}
	return best.show, true
}

func (x *ownerGroupIndex) publishedToFor(app, sourceID, group, channelID string, hiddenApps, shownApps []string) bool {
	if app == "" {
		return true
	}
	if slices.Contains(hiddenApps, app) {
		return false
	}
	if slices.Contains(shownApps, app) {
		return true
	}
	if show, set := x.groupAppRule(sourceID, group, channelID, app); set {
		return show
	}
	return true
}

func (x *ownerGroupIndex) publishedTo(app string, c Channel) bool {
	return x.publishedToFor(app, c.SourceID, c.Group, c.ID, c.HiddenApps, c.ShownApps)
}

func (x *ownerGroupIndex) effectiveHiddenAppsFor(sourceID, group, channelID string, hiddenApps, shownApps []string) []string {
	var out []string
	for _, app := range Apps {
		if !x.publishedToFor(app, sourceID, group, channelID, hiddenApps, shownApps) {
			out = append(out, app)
		}
	}
	if len(out) == 0 {
		return nil
	}
	slices.Sort(out)
	return out
}

func (x *ownerGroupIndex) effectiveHiddenApps(c Channel) []string {
	return x.effectiveHiddenAppsFor(c.SourceID, c.Group, c.ID, c.HiddenApps, c.ShownApps)
}

func (x *ownerGroupIndex) hasAppHides(app string) bool {
	return x != nil && x.appHasHide[app]
}

func (x *ownerGroupIndex) memberships(sourceID, group, channelID string) []frontend.LiveGroup {
	if x == nil || len(x.groups) == 0 {
		return []frontend.LiveGroup{}
	}
	byGroup := x.visibleGroups[OwnerGroupSource{SourceID: sourceID, Name: group}]
	byChan := x.visibleChans[channelID]
	if len(x.visibleAll) == 0 && len(byGroup) == 0 && len(byChan) == 0 {
		return []frontend.LiveGroup{}
	}
	out := make([]frontend.LiveGroup, 0, len(x.visibleAll)+len(byGroup)+len(byChan))
	add := func(lg frontend.LiveGroup) {
		if !slices.ContainsFunc(out, func(v frontend.LiveGroup) bool { return v.ID == lg.ID }) {
			out = append(out, lg)
		}
	}
	for _, lg := range x.visibleAll {
		add(lg)
	}
	for _, lg := range byGroup {
		add(lg)
	}
	for _, lg := range byChan {
		add(lg)
	}
	if len(out) > 1 {
		sort.SliceStable(out, func(i, j int) bool {
			return x.visibleOrder[out[i].ID] < x.visibleOrder[out[j].ID]
		})
	}
	return out
}

func (x *ownerGroupIndex) inGroup(groupID, sourceID, group, channelID string) bool {
	if x == nil {
		return false
	}
	if groupID == "none" || groupID == "__ungrouped__" {
		return len(x.memberships(sourceID, group, channelID)) == 0
	}
	entry, ok := x.byID[groupID]
	if !ok {
		return false
	}
	if entry.group.All {
		return true
	}
	if _, ok := entry.sources[OwnerGroupSource{SourceID: sourceID, Name: group}]; ok {
		return true
	}
	_, ok = entry.channels[channelID]
	return ok
}

type ownerGroupIndexContextKey struct{}

func contextWithOwnerGroupIndex(ctx context.Context, idx *ownerGroupIndex) context.Context {
	if idx == nil {
		return ctx
	}
	return context.WithValue(ctx, ownerGroupIndexContextKey{}, idx)
}

func (s *Service) withOwnerGroupIndex(ctx context.Context) context.Context {
	if idx, ok := ctx.Value(ownerGroupIndexContextKey{}).(*ownerGroupIndex); ok && idx != nil {
		return ctx
	}
	idx, err := s.cachedOwnerGroupIndex(ctx)
	if err != nil || idx == nil {
		return ctx
	}
	return contextWithOwnerGroupIndex(ctx, idx)
}

func (s *Service) getOwnerGroupCacheState() (int64, *ownerGroupIndex) {
	s.ownerGroupsMu.Lock()
	defer s.ownerGroupsMu.Unlock()
	if s.ownerGroupsIndex == nil {
		return -1, nil
	}
	return s.ownerGroupsIndexRev, s.ownerGroupsIndex
}

func (s *Service) setOwnerGroupCacheState(rev int64, idx *ownerGroupIndex) {
	s.ownerGroupsMu.Lock()
	s.ownerGroupsIndexRev = rev
	s.ownerGroupsIndex = idx
	s.ownerGroupsMu.Unlock()
}

func (s *Service) cachedOwnerGroupIndex(ctx context.Context) (*ownerGroupIndex, error) {
	if idx, ok := ctx.Value(ownerGroupIndexContextKey{}).(*ownerGroupIndex); ok && idx != nil {
		return idx, nil
	}
	prevRev, prevIdx := s.getOwnerGroupCacheState()
	var rev int64
	var raw string
	err := s.o.DB.QueryRowContext(ctx,
		`SELECT generation, CASE WHEN generation=? THEN '' ELSE COALESCE((SELECT data FROM livetv_owner_groups WHERE id=1),'[]') END FROM livetv_channel_epoch WHERE id=1`,
		prevRev,
	).Scan(&rev, &raw)
	if err != nil {
		return nil, err
	}
	if prevIdx != nil && rev == prevRev {
		return prevIdx, nil
	}
	idx, err := decodeOwnerGroupIndex(raw)
	if err != nil {
		return nil, err
	}
	s.setOwnerGroupCacheState(rev, idx)
	return idx, nil
}

// WithOwnerGroupIndex attaches the compiled Your-groups rule index to ctx so
// repeated ChannelGroups and publishedTo lookups on a request run in memory.
func (s *Service) WithOwnerGroupIndex(ctx context.Context) context.Context {
	return s.withOwnerGroupIndex(ctx)
}

// ChannelGroups returns the visible Your-groups memberships for one channel in owner order.
func (s *Service) ChannelGroups(ctx context.Context, sourceID, group, channelID string) ([]frontend.LiveGroup, error) {
	idx, err := s.cachedOwnerGroupIndex(ctx)
	if err != nil {
		return nil, err
	}
	return idx.memberships(sourceID, group, channelID), nil
}

func (s *Service) providerGroupChoices(ctx context.Context) ([]OwnerGroupChoice, error) {
	var rev int64
	if err := s.o.DB.QueryRowContext(ctx, `SELECT generation FROM livetv_channel_epoch WHERE id=1`).Scan(&rev); err != nil {
		return nil, err
	}
	s.ownerGroupsMu.Lock()
	if s.ownerGroupChoices != nil && s.ownerGroupChoicesRev == rev {
		cached := s.ownerGroupChoices
		s.ownerGroupsMu.Unlock()
		return cached, nil
	}
	s.ownerGroupsMu.Unlock()

	cs, err := s.ChannelSummaries(ctx)
	if err != nil {
		return nil, err
	}
	out := []OwnerGroupChoice{}
	at := map[OwnerGroupSource]int{}
	for _, c := range cs {
		key := OwnerGroupSource{SourceID: c.SourceID, Name: c.Group}
		if i, ok := at[key]; ok {
			out[i].Count++
		} else {
			at[key] = len(out)
			out = append(out, OwnerGroupChoice{OwnerGroupSource: key, Count: 1})
		}
	}
	sources, err := s.Sources(ctx)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, src := range sources {
		names[src.ID] = src.Name
	}
	for i := range out {
		out[i].SourceName = names[out[i].SourceID]
	}
	s.ownerGroupsMu.Lock()
	s.ownerGroupChoices = out
	s.ownerGroupChoicesRev = rev
	s.ownerGroupsMu.Unlock()
	return out, nil
}

// OwnerGroups returns the owner's saved groups in owner order. When no groups
// have been saved yet, it returns an empty slice ([]OwnerGroup{}, nil).
func (s *Service) OwnerGroups(ctx context.Context) ([]OwnerGroup, error) {
	var raw []byte
	err := s.o.DB.QueryRowContext(ctx, `SELECT data FROM livetv_owner_groups WHERE id=1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return []OwnerGroup{}, nil
	}
	if err != nil {
		return nil, err
	}
	gs := []OwnerGroup{}
	if err := json.Unmarshal(raw, &gs); err != nil {
		return nil, err
	}
	if gs == nil {
		gs = []OwnerGroup{}
	}
	return gs, nil
}

func validateAndNormalizeOwnerGroups(gs []OwnerGroup) ([]OwnerGroup, error) {
	if len(gs) > 200 {
		return nil, fmt.Errorf("too many groups")
	}
	if gs == nil {
		gs = []OwnerGroup{}
	}
	out := slices.Clone(gs)
	seen := map[string]bool{}
	names := map[string]bool{}
	for i := range out {
		g := &out[i]
		g.ChannelCount = 0
		g.Name = strings.TrimSpace(g.Name)
		if g.ID == "" {
			g.ID = randomID()
		}
		if g.Groups == nil {
			g.Groups = []OwnerGroupSource{}
		}
		if len(g.ID) > 100 || strings.ContainsAny(g.ID, "/\\?# \t\r\n") || seen[g.ID] || names[strings.ToLower(g.Name)] || g.Name == "" || len(g.Name) > 150 || len(g.Groups) > 2000 || len(g.Channels) > 20000 {
			return nil, fmt.Errorf("invalid group")
		}
		legacyHidden, err := normalizeHiddenApps(g.HiddenApps)
		if err != nil {
			return nil, err
		}
		apps, err := normalizeGroupApps(g.Apps)
		if err != nil {
			return nil, err
		}
		if len(legacyHidden) > 0 {
			if apps == nil {
				apps = make(map[string]bool, len(legacyHidden))
			}
			for _, app := range legacyHidden {
				if _, exists := apps[app]; !exists {
					apps[app] = false
				}
			}
		}
		g.Apps = apps
		g.HiddenApps = nil
		g.Channels = uniqueIDs(g.Channels)
		seen[g.ID] = true
		names[strings.ToLower(g.Name)] = true
		slices.SortFunc(g.Groups, func(a, b OwnerGroupSource) int {
			if n := strings.Compare(a.SourceID, b.SourceID); n != 0 {
				return n
			}
			return strings.Compare(a.Name, b.Name)
		})
		g.Groups = slices.Compact(g.Groups)
	}
	return out, nil
}

func (s *Service) SaveOwnerGroups(ctx context.Context, gs []OwnerGroup) error {
	normalized, err := validateAndNormalizeOwnerGroups(gs)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return err
	}
	tx, err := s.o.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO livetv_owner_groups(id,data) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data`, raw); err != nil {
		return err
	}
	if err = saveGroupVisibility(ctx, tx, normalized); err != nil {
		return err
	}
	if err = tx.Commit(); err == nil {
		s.bumpLineup()
	}
	return err
}

func populateOwnerGroupCounts(gs []OwnerGroup, cs []ChannelSummary) []OwnerGroup {
	out := make([]OwnerGroup, len(gs))
	idx := compileOwnerGroupIndex(gs)
	for i, g := range gs {
		copyGroup := g
		if copyGroup.Groups == nil {
			copyGroup.Groups = []OwnerGroupSource{}
		}
		entry := idx.byID[g.ID]
		count := 0
		if g.All {
			count = len(cs)
		} else {
			for _, c := range cs {
				if _, ok := entry.sources[OwnerGroupSource{SourceID: c.SourceID, Name: c.Group}]; ok {
					count++
					continue
				}
				if _, ok := entry.channels[c.ID]; ok {
					count++
				}
			}
		}
		copyGroup.ChannelCount = count
		out[i] = copyGroup
	}
	return out
}

func (s *Service) ownerGroupsWithCounts(ctx context.Context) ([]OwnerGroup, []ChannelSummary, error) {
	gs, err := s.OwnerGroups(ctx)
	if err != nil {
		return nil, nil, err
	}
	cs, err := s.ChannelSummaries(ctx)
	if err != nil {
		return nil, nil, err
	}
	return populateOwnerGroupCounts(gs, cs), cs, nil
}

type ownerGroupPatch struct {
	Name          *string             `json:"name,omitempty"`
	Groups        *[]OwnerGroupSource `json:"groups,omitempty"`
	Channels      *[]string           `json:"channels,omitempty"`
	All           *bool               `json:"all,omitempty"`
	Hidden        *bool               `json:"hidden,omitempty"`
	UnratedRating *string             `json:"unratedRating,omitempty"`
	Apps          *map[string]bool    `json:"apps,omitempty"`
	HiddenApps    *[]string           `json:"hiddenApps,omitempty"`
	Position      *int                `json:"position,omitempty"`
	ChannelCount  *int                `json:"channelCount,omitempty"`
}

func (s *Service) ownerGroupsRoute(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	switch r.Method {
	case http.MethodGet:
		gs, _, err := s.ownerGroupsWithCounts(ctx)
		if err != nil {
			ownerResult(w, nil, err)
			return
		}
		if r.URL.Query().Get("include") == "sources" {
			choices, err := s.providerGroupChoices(ctx)
			if err != nil {
				ownerResult(w, nil, err)
				return
			}
			ownerResult(w, map[string]any{
				"groups":  gs,
				"sources": choices,
				"apps":    Apps,
			}, nil)
			return
		}
		ownerResult(w, gs, nil)

	case http.MethodPut:
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
		if err != nil {
			ownerResult(w, nil, fmt.Errorf("invalid request body: %w", err))
			return
		}
		trimmed := bytes.TrimSpace(raw)
		var gs []OwnerGroup
		if len(trimmed) > 0 && trimmed[0] == '{' {
			var wrapped struct {
				Groups []OwnerGroup `json:"groups"`
			}
			dec := json.NewDecoder(bytes.NewReader(trimmed))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&wrapped); err != nil {
				ownerResult(w, nil, fmt.Errorf("invalid request body: %w", err))
				return
			}
			gs = wrapped.Groups
		} else {
			dec := json.NewDecoder(bytes.NewReader(trimmed))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&gs); err != nil {
				ownerResult(w, nil, fmt.Errorf("invalid request body: %w", err))
				return
			}
		}
		if err := s.SaveOwnerGroups(ctx, gs); err != nil {
			ownerResult(w, nil, err)
			return
		}
		saved, _, err := s.ownerGroupsWithCounts(ctx)
		ownerResult(w, saved, err)

	case http.MethodPost:
		var g OwnerGroup
		if !ownerBody(w, r, &g) {
			return
		}
		if g.ID == "" {
			g.ID = randomID()
		}
		gs, err := s.OwnerGroups(ctx)
		if err != nil {
			ownerResult(w, nil, err)
			return
		}
		gs = append(gs, g)
		if err := s.SaveOwnerGroups(ctx, gs); err != nil {
			ownerResult(w, nil, err)
			return
		}
		saved, _, err := s.ownerGroupsWithCounts(ctx)
		if err != nil {
			ownerResult(w, nil, err)
			return
		}
		var created OwnerGroup
		for _, item := range saved {
			if item.ID == g.ID {
				created = item
				break
			}
		}
		ownerResult(w, map[string]any{"group": created, "groups": saved}, nil)
	}
}

func (s *Service) ownerGroupsOrderRoute(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		ownerResult(w, nil, fmt.Errorf("invalid request body: %w", err))
		return
	}
	trimmed := bytes.TrimSpace(raw)
	var ids []string
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &ids); err != nil {
			ownerResult(w, nil, fmt.Errorf("invalid request body: %w", err))
			return
		}
	} else {
		var wrapped struct {
			IDs []string `json:"ids"`
		}
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&wrapped); err != nil {
			ownerResult(w, nil, fmt.Errorf("invalid request body: %w", err))
			return
		}
		ids = wrapped.IDs
	}
	existing, err := s.OwnerGroups(ctx)
	if err != nil {
		ownerResult(w, nil, err)
		return
	}
	if len(ids) != len(existing) {
		ownerResult(w, nil, fmt.Errorf("group order must name all %d groups", len(existing)))
		return
	}
	byID := make(map[string]OwnerGroup, len(existing))
	for _, g := range existing {
		byID[g.ID] = g
	}
	reordered := make([]OwnerGroup, 0, len(existing))
	seen := make(map[string]bool, len(existing))
	for _, id := range ids {
		g, ok := byID[id]
		if !ok || seen[id] {
			ownerResult(w, nil, fmt.Errorf("invalid group order id %q", id))
			return
		}
		seen[id] = true
		reordered = append(reordered, g)
	}
	if err := s.SaveOwnerGroups(ctx, reordered); err != nil {
		ownerResult(w, nil, err)
		return
	}
	saved, _, err := s.ownerGroupsWithCounts(ctx)
	ownerResult(w, map[string]any{"groups": saved}, err)
}

func (s *Service) ownerGroupItemRoute(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	gs, err := s.OwnerGroups(ctx)
	if err != nil {
		ownerResult(w, nil, err)
		return
	}
	idx := slices.IndexFunc(gs, func(g OwnerGroup) bool { return g.ID == id })
	if idx < 0 {
		ownerResult(w, nil, core.ErrNotFound)
		return
	}
	switch r.Method {
	case http.MethodDelete:
		gs = slices.Delete(gs, idx, idx+1)
		if err := s.SaveOwnerGroups(ctx, gs); err != nil {
			ownerResult(w, nil, err)
			return
		}
		saved, _, err := s.ownerGroupsWithCounts(ctx)
		ownerResult(w, map[string]any{"ok": true, "groups": saved}, err)

	case http.MethodPut:
		var g OwnerGroup
		if !ownerBody(w, r, &g) {
			return
		}
		g.ID = id
		gs[idx] = g
		if err := s.SaveOwnerGroups(ctx, gs); err != nil {
			ownerResult(w, nil, err)
			return
		}
		saved, _, err := s.ownerGroupsWithCounts(ctx)
		if err != nil {
			ownerResult(w, nil, err)
			return
		}
		ownerResult(w, map[string]any{"group": saved[idx], "groups": saved}, nil)

	case http.MethodPatch:
		var patch ownerGroupPatch
		if !ownerBody(w, r, &patch) {
			return
		}
		g := gs[idx]
		if patch.Name != nil {
			g.Name = *patch.Name
		}
		if patch.Groups != nil {
			g.Groups = *patch.Groups
		}
		if patch.Channels != nil {
			g.Channels = *patch.Channels
		}
		if patch.All != nil {
			g.All = *patch.All
		}
		if patch.Hidden != nil {
			g.Hidden = *patch.Hidden
		}
		if patch.UnratedRating != nil {
			g.UnratedRating = strings.TrimSpace(*patch.UnratedRating)
		}
		if patch.Apps != nil {
			g.Apps = *patch.Apps
		}
		if patch.HiddenApps != nil {
			g.HiddenApps = *patch.HiddenApps
		}
		gs[idx] = g
		targetIdx := idx
		if patch.Position != nil {
			pos := max(0, min(*patch.Position, len(gs)-1))
			if pos != idx {
				gs = slices.Delete(gs, idx, idx+1)
				gs = slices.Insert(gs, pos, g)
				targetIdx = pos
			}
		}
		if err := s.SaveOwnerGroups(ctx, gs); err != nil {
			ownerResult(w, nil, err)
			return
		}
		saved, _, err := s.ownerGroupsWithCounts(ctx)
		if err != nil {
			ownerResult(w, nil, err)
			return
		}
		ownerResult(w, map[string]any{"group": saved[targetIdx], "groups": saved}, nil)
	}
}

func (s *Service) ownerGroupAppsRoute(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	gs, err := s.OwnerGroups(ctx)
	if err != nil {
		ownerResult(w, nil, err)
		return
	}
	idx := slices.IndexFunc(gs, func(g OwnerGroup) bool { return g.ID == id })
	if idx < 0 {
		ownerResult(w, nil, core.ErrNotFound)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		ownerResult(w, nil, fmt.Errorf("invalid request body: %w", err))
		return
	}
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		ownerResult(w, nil, fmt.Errorf("invalid request body: %w", err))
		return
	}
	var appEntries map[string]*bool
	clearOverrides := false
	if _, wrapped := rawMap["apps"]; wrapped {
		var body struct {
			Apps                  map[string]*bool `json:"apps"`
			ClearChannelOverrides bool             `json:"clearChannelOverrides,omitempty"`
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			ownerResult(w, nil, fmt.Errorf("invalid request body: %w", err))
			return
		}
		appEntries = body.Apps
		clearOverrides = body.ClearChannelOverrides
	} else {
		if err := json.Unmarshal(raw, &appEntries); err != nil {
			ownerResult(w, nil, fmt.Errorf("invalid request body: %w", err))
			return
		}
	}

	var next map[string]bool
	if r.Method == http.MethodPut {
		next = make(map[string]bool, len(appEntries))
	} else {
		next = make(map[string]bool, len(gs[idx].Apps)+len(appEntries))
		for k, v := range gs[idx].Apps {
			next[k] = v
		}
	}
	var touchedApps []string
	for app, val := range appEntries {
		if !slices.Contains(Apps, app) {
			ownerResult(w, nil, fmt.Errorf("unknown Live TV app %q", app))
			return
		}
		touchedApps = append(touchedApps, app)
		if val == nil {
			delete(next, app)
		} else {
			next[app] = *val
		}
	}
	normalized, err := normalizeGroupApps(next)
	if err != nil {
		ownerResult(w, nil, err)
		return
	}
	gs[idx].Apps = normalized
	if err := s.SaveOwnerGroups(ctx, gs); err != nil {
		ownerResult(w, nil, err)
		return
	}
	if clearOverrides && len(touchedApps) > 0 {
		if err := s.clearGroupChannelAppOverrides(ctx, gs[idx], touchedApps); err != nil {
			ownerResult(w, nil, err)
			return
		}
	}
	saved, _, err := s.ownerGroupsWithCounts(ctx)
	if err != nil {
		ownerResult(w, nil, err)
		return
	}
	ownerResult(w, map[string]any{"group": saved[idx], "groups": saved}, nil)
}

func (s *Service) clearGroupChannelAppOverrides(ctx context.Context, g OwnerGroup, apps []string) error {
	all, err := s.allChannels(ctx)
	if err != nil {
		return err
	}
	var ids []string
	for _, c := range all {
		if !g.matchesChannel(c.SourceID, c.Group, c.ID) {
			continue
		}
		hasOverride := false
		for _, app := range apps {
			if slices.Contains(c.HiddenApps, app) || slices.Contains(c.ShownApps, app) {
				hasOverride = true
				break
			}
		}
		if hasOverride {
			ids = append(ids, c.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	_, err = s.UpdateChannels(ctx, ChannelBatch{IDs: ids, ClearApps: apps})
	return err
}

func (h liveTVHost) Groups(ctx context.Context, u *core.User) ([]frontend.LiveGroup, error) {
	cs, err := h.Channels(ctx, u)
	if err != nil {
		return nil, err
	}
	present := map[string]bool{}
	for _, c := range cs {
		for _, g := range c.Groups {
			present[g.ID] = true
		}
	}
	gs, err := h.s.OwnerGroups(ctx)
	if err != nil {
		return nil, err
	}
	out := []frontend.LiveGroup{}
	for _, g := range gs {
		if !g.Hidden && present[g.ID] {
			out = append(out, frontend.LiveGroup{ID: g.ID, Name: g.Name})
		}
	}
	return out, nil
}

func (h liveTVHost) GroupChannels(ctx context.Context, u *core.User, id string) ([]frontend.LiveTVChannel, error) {
	gs, err := h.s.OwnerGroups(ctx)
	if err != nil {
		return nil, err
	}
	var chosen *OwnerGroup
	for i := range gs {
		if gs[i].ID == id && !gs[i].Hidden {
			chosen = &gs[i]
			break
		}
	}
	if chosen == nil {
		return nil, core.ErrNotFound
	}
	cs, err := h.s.AllowedChannels(ctx, u)
	if err != nil {
		return nil, err
	}
	groups := map[OwnerGroupSource]bool{}
	for _, g := range chosen.Groups {
		groups[g] = true
	}
	individual := map[string]bool{}
	for _, cid := range chosen.Channels {
		individual[cid] = true
	}
	out := []frontend.LiveTVChannel{}
	for _, c := range cs {
		if chosen.All || groups[OwnerGroupSource{SourceID: c.SourceID, Name: c.Group}] || individual[c.ID] {
			out = append(out, ToFrontendChannel(c))
		}
	}
	return out, nil
}
