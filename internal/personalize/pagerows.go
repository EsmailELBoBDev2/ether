package personalize

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
)

// Rows built for a page: a page row whose source the engine's own rows
// don't serve as asked (another window, a pinned genre) or whose titles
// come from chosen libraries (a combined row over some libraries, or one
// row per library). They are built from the viewer's model rows, owner
// rows and cached TMDB lists, so a request does no more work than the
// page asks for, bounded by the row count and MaxRowsPerLibraryRow.

// pageRowListLen is how many titles a chart row built for a page reads
// from a TMDB list per media type.
const pageRowListLen = 120

// pageRowBase is the pool a page row draws from before its libraries.
type pageRowBase struct {
	kind, title, reason string
	items               []candItem
	typed               map[core.MediaType][]candItem
	keepOrder           bool
	source              string
}

// pageRows builds the page's derived rows for this viewer. owner holds the
// placed collections and lists, m the viewer's model rows.
func (e *Engine) pageRows(ctx context.Context, pl *pagePlan, m *userModel, owner []*candRow, user *core.User, section core.MediaType) []*candRow {
	if pl == nil || len(pl.derived) == 0 {
		return nil
	}
	var out []*candRow
	for _, r := range pl.derived {
		if r.Status == "off" || r.Gate != "" || r.row == nil {
			continue
		}
		row := r.row
		base := e.pageRowBase(ctx, row, m, owner)
		if base == nil || len(base.items) == 0 && len(base.typed) == 0 {
			continue
		}
		if pl.key != core.PageHome || len(row.Scope.Libraries) == 0 && !row.Scope.PerLibrary {
			// A library page's rows draw from that library (the assembly's
			// library gate); a Home row over "any library" is the pool.
			out = append(out, pageCandRow(r, base, base.items, "", row.ID, e.sourceTitle(row.Source.Kind)))
			continue
		}
		libs := pl.scopeLibraries(row, user, e.sourceNeeds(row.Source.Kind))
		if row.Scope.PerLibrary {
			for i, lib := range libs {
				if i >= core.MaxRowsPerLibraryRow {
					break
				}
				items := e.libraryItems(ctx, base, lib)
				if len(items) == 0 {
					continue
				}
				out = append(out, pageCandRow(r, base, items, lib.Name, row.ID+"@"+strconv.FormatInt(int64(lib.ID), 10), e.sourceTitle(row.Source.Kind)))
			}
			continue
		}
		var items []candItem
		seen := map[core.RatingKey]bool{}
		for _, lib := range libs {
			for _, it := range e.libraryItems(ctx, base, lib) {
				if !seen[it.t.Key] {
					seen[it.t.Key] = true
					items = append(items, it)
				}
			}
		}
		if len(items) > 0 {
			out = append(out, pageCandRow(r, base, items, "", row.ID, e.sourceTitle(row.Source.Kind)))
		}
	}
	return out
}

// pageCandRow makes the candidate row for a page row (or one library of it).
func pageCandRow(r *rule, base *pageRowBase, items []candItem, library, id, sourceTitle string) *candRow {
	title := sourceTitle
	if r.Name != "" {
		title = strings.ReplaceAll(r.Name, "{library}", library)
	} else if library != "" {
		title = sourceTitle + " " + library
	}
	title = strings.TrimSpace(title)
	info := infoFor(base.kind)
	c := &candRow{kind: base.kind, id: id, title: title, reason: base.reason, context: info.context, source: base.source,
		items: items, minItems: 1, scale: 1, keepOrder: base.keepOrder}
	if c.source == "" {
		c.source = core.RowSourceEngine
	}
	return c
}

// scopeLibraries are the libraries a Home row draws from: the row's own
// choice in its order, or every library the viewer can see that has what
// the source needs, in library order.
func (pl *pagePlan) scopeLibraries(row *core.Row, user *core.User, needs []string) []core.PageLibrary {
	var ids []core.LibraryID
	if len(row.Scope.Libraries) > 0 {
		ids = row.Scope.Libraries
	} else {
		for id := range pl.libraries {
			ids = append(ids, id)
		}
		slices.Sort(ids)
	}
	var out []core.PageLibrary
	for _, id := range ids {
		lib, ok := pl.libraries[id]
		if !ok || user != nil && !user.AllowsLibrary(id) || !hasCaps(lib.Caps, needs) {
			continue
		}
		out = append(out, lib)
	}
	return out
}

// libraryItems keeps a pool's titles that belong to a library: its media
// type's list, then the library's own membership (a rule-split catalogue
// library), checked over at most the pool's few hundred keys.
func (e *Engine) libraryItems(ctx context.Context, base *pageRowBase, lib core.PageLibrary) []candItem {
	var pool []candItem
	if lib.MediaType != "" {
		if typed := base.typed[lib.MediaType]; len(typed) > 0 {
			pool = typed
		} else {
			for _, it := range base.items {
				if baseType(it.t.Type) == lib.MediaType {
					pool = append(pool, it)
				}
			}
		}
	} else {
		pool = base.items
	}
	if len(pool) == 0 || e.opts.LibraryMembers == nil {
		return pool
	}
	keys := make([]core.RatingKey, len(pool))
	for i, it := range pool {
		keys[i] = it.t.Key
	}
	members, _, scoped, err := e.opts.LibraryMembers(ctx, lib.ID, keys)
	if err != nil {
		return nil
	}
	if !scoped {
		return pool
	}
	out := make([]candItem, 0, len(pool))
	for _, it := range pool {
		if members[it.t.Key] {
			out = append(out, it)
		}
	}
	return out
}

// pageRowBase finds or builds the pool for a page row's source and options.
func (e *Engine) pageRowBase(ctx context.Context, row *core.Row, m *userModel, owner []*candRow) *pageRowBase {
	src := row.Source
	genre := src.Param("genre", "")
	fromModel := func(kind string) *pageRowBase {
		if m == nil {
			return nil
		}
		for _, c := range m.rows {
			if c.kind == kind {
				return &pageRowBase{kind: kind, title: c.title, reason: c.reason, items: c.items, typed: c.typed, keepOrder: c.keepOrder, source: c.source}
			}
		}
		return nil
	}
	fromList := func(kind, list, reason string, types ...core.MediaType) *pageRowBase {
		b := &pageRowBase{kind: kind, reason: reason, typed: map[core.MediaType][]candItem{}, keepOrder: true}
		for _, t := range types {
			titles, err := e.catalog.List(ctx, list, t, pageRowListLen)
			if err != nil {
				continue
			}
			for i, ti := range titles {
				it := candItem{t: ti, base: 1 - float64(i)/float64(len(titles)+1), qual: quality(&ti), reason: reason}
				b.typed[t] = append(b.typed[t], it)
			}
		}
		b.items = mixDiscoveryTypes(append(append([]candItem{}, b.typed[core.TypeMovie]...), b.typed[core.TypeShow]...))
		return b
	}
	var base *pageRowBase
	switch src.Kind {
	case KindTrending:
		if src.Param("window", "week") == "day" {
			base = fromList(KindTrending, "trending_day", "Trending today on TMDB", core.TypeMovie, core.TypeShow)
		} else if base = fromModel(KindTrending); base == nil {
			base = fromList(KindTrending, "trending_week", "Trending this week on TMDB", core.TypeMovie, core.TypeShow)
		}
	case KindPopular:
		if base = fromModel(KindPopular); base == nil {
			base = fromList(KindPopular, "popular", "Popular right now", core.TypeMovie, core.TypeShow)
		}
	case KindTopRated:
		if base = fromModel(KindTopRated); base == nil {
			base = fromList(KindTopRated, "top_rated", "Top rated on TMDB", core.TypeMovie, core.TypeShow)
		}
	case KindNewReleases:
		if base = fromModel(KindNewReleases); base == nil {
			base = fromList(KindNewReleases, "now_playing", "New release", core.TypeMovie)
		}
	case KindAiringToday:
		if src.Param("window", "day") == "week" {
			base = fromList(KindAiringToday, "on_the_air", "New episode this week", core.TypeShow)
		} else if base = fromModel(KindAiringToday); base == nil {
			base = fromList(KindAiringToday, "airing_today", "New episode today", core.TypeShow)
		}
	case KindUpcoming:
		base = fromList(KindUpcoming, "upcoming", "Coming soon", core.TypeMovie)
	case SourceCollection:
		want := KindCollectionPrefix + src.Param("collection", "")
		for _, c := range owner {
			if c.kind == want {
				base = &pageRowBase{kind: c.kind, title: c.title, reason: c.reason, items: c.items, typed: splitTyped(c.items, 200), keepOrder: true, source: core.RowSourceOwner}
			}
		}
	default:
		if strings.HasPrefix(src.Kind, "plugin:") {
			for _, c := range owner {
				if c.kind == src.Kind {
					base = &pageRowBase{kind: c.kind, title: c.title, reason: c.reason, items: c.items, typed: splitTyped(c.items, 200), keepOrder: true, source: core.RowSourceOwner}
				}
			}
		} else {
			base = fromModel(src.Kind)
		}
	}
	if base == nil || genre == "" {
		return base
	}
	keep := func(items []candItem) []candItem {
		var out []candItem
		for _, it := range items {
			for _, g := range it.t.Genres {
				if strings.EqualFold(g, genre) {
					out = append(out, it)
					break
				}
			}
		}
		return out
	}
	filtered := &pageRowBase{kind: base.kind, title: base.title, reason: base.reason, items: keep(base.items), typed: map[core.MediaType][]candItem{}, keepOrder: base.keepOrder, source: base.source}
	for t, items := range base.typed {
		filtered.typed[t] = keep(items)
	}
	return filtered
}

func (e *Engine) sourceTitle(kind string) string {
	if s, ok := e.sourceInfo(kind); ok {
		return s.Title
	}
	return kind
}

func (e *Engine) sourceNeeds(kind string) []string {
	if s, ok := e.sourceInfo(kind); ok {
		return s.Needs
	}
	return nil
}
