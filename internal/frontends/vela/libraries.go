package vela

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/frontend/kit/ids"
)

func hasOfferedSort(sorts []object, id string) bool {
	for _, s := range sorts {
		if s["id"] == id {
			return true
		}
	}
	return false
}

func allowedSortDirection(sorts []object, sort, direction string) bool {
	for _, candidate := range sorts {
		if candidate["id"] == sort {
			for _, d := range candidate["directions"].([]string) {
				if direction == d {
					return true
				}
			}
		}
	}
	return false
}

func defaultDirectionForSort(sorts []object, sort, preferred string) string {
	if preferred != "" && allowedSortDirection(sorts, sort, preferred) {
		return preferred
	}
	def := "desc"
	switch sort {
	case "server", "title", "index":
		def = "asc"
	}
	if allowedSortDirection(sorts, sort, def) {
		return def
	}
	if allowedSortDirection(sorts, sort, "asc") {
		return "asc"
	}
	return "desc"
}

func libraryWire(view frontend.View) object {
	kind := "mixed"
	root := ""
	switch view.Type {
	case frontend.TypeMovie:
		kind, root = "movies", "movie"
	case frontend.TypeShow:
		kind, root = "shows", "show"
	}
	isPlugin := view.PluginID != "" || view.KindKey != ""
	card := "poster"
	if view.Card == "landscape" || view.Card == "square" {
		card = view.Card
	}
	sorts := []object{{"id": "server", "directions": []string{"asc"}}}
	filters := object{}
	defaultSort := "server"
	defaultDirection := "asc"
	if root != "" {
		filters["kind"] = []string{root}
	}
	if !isPlugin {
		defaultSort = "title"
		for _, sort := range []string{"title", "added", "released", "year", "rating", "duration", "popularity"} {
			sorts = append(sorts, object{"id": sort, "directions": []string{"asc", "desc"}})
		}
		filters["genre"], filters["year"], filters["favorite"], filters["collectionId"] = true, true, true, true
		if root == "movie" {
			filters["played"] = true
		}
	} else {
		if len(view.Sorts) > 0 {
			sorts = make([]object, 0, len(view.Sorts))
			for _, s := range view.Sorts {
				sorts = append(sorts, object{"id": s, "directions": []string{"asc", "desc"}})
			}
			defaultSort = view.Sorts[0]
			if defaultSort == "title" || defaultSort == "index" {
				defaultDirection = "asc"
			} else {
				defaultDirection = "desc"
			}
		}
		if len(view.Facets) > 0 {
			facetWires := make([]object, 0, len(view.Facets))
			for _, f := range view.Facets {
				facetWires = append(facetWires, object{
					"id":    f.ID,
					"label": f.Label,
					"type":  f.Type,
					"multi": f.Multi,
				})
			}
			filters["facets"] = facetWires
		}
	}
	var tabs []object
	if !isPlugin && len(view.Tabs) == 0 {
		tabs = []object{
			{"id": "recommended", "label": "Recommended", "kind": "recommended"},
			{"id": "library", "label": "Library", "kind": "library"},
		}
		if kind == "movies" || kind == "shows" {
			tabs = append(tabs, object{"id": "collections", "label": "Collections", "kind": "collections"})
		}
	} else {
		srcTabs := view.Tabs
		if len(srcTabs) == 0 {
			srcTabs = []frontend.ViewTab{
				{ID: "recommended", Label: "Recommended", Kind: "recommended"},
				{ID: "library", Label: "Library", Kind: "library", Search: true},
			}
		}
		tabs = make([]object, 0, len(srcTabs))
		appliedLibrarySort := false
		for _, t := range srcTabs {
			tab := object{"id": t.ID, "label": t.Label, "kind": t.Kind}
			if t.Kind == "collections" || t.Kind == "people" || t.Kind == "view" {
				tab["search"] = t.Search
				if t.Search && t.SearchPlaceholder != "" {
					tab["searchPlaceholder"] = t.SearchPlaceholder
				}
			} else if t.Kind == "library" && t.Search && t.SearchPlaceholder != "" {
				tab["searchPlaceholder"] = t.SearchPlaceholder
			}
			switch {
			case t.Kind == "collections" && t.Facet != "":
				tab["facet"] = t.Facet
				effOrder := t.Order
				if effOrder == "" {
					effOrder = "count"
				}
				tab["order"] = effOrder
				effSort := t.Sort
				if effSort == "" {
					if hasOfferedSort(sorts, "released") {
						effSort = "released"
					} else {
						effSort = defaultSort
					}
				}
				effDir := defaultDirectionForSort(sorts, effSort, t.Direction)
				tab["sort"] = effSort
				tab["direction"] = effDir
			case t.Kind == "people" && t.Facet != "":
				tab["facet"] = t.Facet
				effOrder := t.Order
				if effOrder == "" {
					effOrder = "name"
				}
				tab["order"] = effOrder
			case t.Kind == "view" || t.Kind == "library":
				if t.Sort != "" && hasOfferedSort(sorts, t.Sort) {
					effSort := t.Sort
					effDir := defaultDirectionForSort(sorts, effSort, t.Direction)
					tab["sort"] = effSort
					tab["direction"] = effDir
					if t.Kind == "library" && !appliedLibrarySort {
						defaultSort = effSort
						defaultDirection = effDir
						appliedLibrarySort = true
					}
				}
			}
			tabs = append(tabs, tab)
		}
	}
	return object{
		"id":           view.ID,
		"name":         view.Name,
		"kind":         kind,
		"artwork":      []object{},
		"presentation": object{"card": card},
		"tabs":         tabs,
		"browse":       object{"sorts": sorts, "defaultSort": defaultSort, "defaultDirection": defaultDirection, "filters": filters},
	}
}

func (i *instance) libraries(w http.ResponseWriter, r *http.Request) {
	v, token, err := i.viewer(r)
	if err != nil {
		fail(w, err, r)
		return
	}
	views, err := i.host.Experience.Views(r.Context(), v)
	if err != nil {
		fail(w, err, r)
		return
	}
	scope, err := i.livePageScope(r, v, token)
	if err != nil {
		fail(w, err, r)
		return
	}
	c, err := i.readLiveCursor(r, scope, hashJSON([]any{"libraries", limit(r)}))
	if err != nil {
		fail(w, err, r)
		return
	}
	snapshot := hashJSON(views)
	if c.Snapshot != "" && c.Snapshot != snapshot {
		fail(w, frontend.ErrLiveGuideStale)
		return
	}
	start := min(c.Offset, len(views))
	end := min(start+limit(r), len(views))
	items := []object{}
	for _, view := range views[start:end] {
		wire := libraryWire(view)
		i.addGenreNames(r, v, view, wire)
		items = append(items, wire)
	}
	page := object{"items": items, "continuation": "exhausted"}
	if end < len(views) {
		next, e := i.nextLiveCursor(c, end, snapshot)
		if e != nil {
			fail(w, e, r)
			return
		}
		page["continuation"], page["nextCursor"] = "more", next
	}
	cache(w, r, page)
}

func offeredFacet(filters object, field string) bool {
	if field == "" {
		return false
	}
	facets, _ := filters["facets"].([]object)
	for _, f := range facets {
		if f["id"] == field {
			return true
		}
	}
	return false
}

func applyCatalogueFacetBuckets(f *core.Filter, canon string, bucketIDs []string) bool {
	switch canon {
	case "genre":
		f.Genres = append(f.Genres, bucketIDs...)
		return true
	case "studio", "network":
		if len(bucketIDs) != 1 || f.Studio != "" {
			return false
		}
		f.Studio = bucketIDs[0]
		return true
	case "country":
		f.Countries = append(f.Countries, bucketIDs...)
		return true
	case "year":
		for _, raw := range bucketIDs {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > 9999 {
				return false
			}
			f.Years = append(f.Years, n)
		}
		return true
	case "decade":
		if len(bucketIDs) == 1 && len(f.Years) == 0 && f.YearFrom == 0 && f.YearTo == 0 {
			trimmed := strings.TrimSuffix(strings.TrimSpace(bucketIDs[0]), "s")
			n, err := strconv.Atoi(trimmed)
			if err != nil || n < 1800 || n > 9990 || n%10 != 0 {
				return false
			}
			f.YearFrom = n
			f.YearTo = n + 9
			return true
		}
		for _, raw := range bucketIDs {
			trimmed := strings.TrimSuffix(strings.TrimSpace(raw), "s")
			n, err := strconv.Atoi(trimmed)
			if err != nil || n < 1800 || n > 9990 || n%10 != 0 {
				return false
			}
			for y := n; y <= n+9; y++ {
				f.Years = append(f.Years, y)
			}
		}
		return true
	case "franchise":
		for _, raw := range bucketIDs {
			id, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || id <= 0 {
				return false
			}
			f.Collections = append(f.Collections, id)
		}
		return true
	case "language":
		f.Languages = append(f.Languages, bucketIDs...)
		return true
	case "contentRating":
		f.ContentRatings = append(f.ContentRatings, bucketIDs...)
		return true
	case "tag":
		f.Tags = append(f.Tags, bucketIDs...)
		return true
	case "keyword":
		f.Keywords = append(f.Keywords, bucketIDs...)
		return true
	case "director", "writer":
		people := make([]int64, 0, len(bucketIDs))
		for _, raw := range bucketIDs {
			id, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || id <= 0 {
				return false
			}
			people = append(people, id)
		}
		f.Credits = append(f.Credits, core.CreditFilter{People: people, Kind: canon})
		return true
	case "resolution":
		for _, raw := range bucketIDs {
			v := strings.ToLower(strings.TrimSpace(raw))
			switch v {
			case "4k", "1080p", "720p", "sd":
				f.EvidenceResolutions = append(f.EvidenceResolutions, v)
			default:
				return false
			}
		}
		return true
	case "hdr":
		for _, raw := range bucketIDs {
			v := strings.ToLower(strings.TrimSpace(raw))
			switch v {
			case "dv", "hdr10", "sdr":
				f.EvidenceHDR = append(f.EvidenceHDR, v)
			default:
				return false
			}
		}
		return true
	case "audioLanguage":
		for _, raw := range bucketIDs {
			v := strings.TrimSpace(raw)
			if v == "" {
				return false
			}
			f.EvidenceAudioLanguages = append(f.EvidenceAudioLanguages, v)
		}
		return true
	case "rating":
		if len(bucketIDs) == 1 && f.MinRating == 0 && f.MaxRating == 0 {
			minR, maxR, excl, ok := core.ParseCatalogueRatingBand(bucketIDs[0])
			if !ok {
				return false
			}
			f.MinRating = minR
			f.MaxRating = maxR
			f.MaxRatingExclusive = excl
			return true
		}
		children := make([]core.FilterGroup, 0, len(bucketIDs))
		for _, raw := range bucketIDs {
			minR, maxR, excl, ok := core.ParseCatalogueRatingBand(raw)
			if !ok {
				return false
			}
			children = append(children, core.FilterGroup{
				Operator: "and",
				Filter:   &core.Filter{MinRating: minR, MaxRating: maxR, MaxRatingExclusive: excl},
			})
		}
		appendOrFilterGroup(f, children)
		return true
	case "runtime":
		if len(bucketIDs) == 1 && f.MinRuntime == 0 && f.MaxRuntime == 0 {
			minS, maxS, ok := core.ParseCatalogueRuntimeBand(bucketIDs[0])
			if !ok {
				return false
			}
			f.MinRuntime = time.Duration(minS) * time.Second
			if maxS > 0 {
				f.MaxRuntime = time.Duration(maxS) * time.Second
			}
			return true
		}
		children := make([]core.FilterGroup, 0, len(bucketIDs))
		for _, raw := range bucketIDs {
			minS, maxS, ok := core.ParseCatalogueRuntimeBand(raw)
			if !ok {
				return false
			}
			sub := &core.Filter{MinRuntime: time.Duration(minS) * time.Second}
			if maxS > 0 {
				sub.MaxRuntime = time.Duration(maxS) * time.Second
			}
			children = append(children, core.FilterGroup{Operator: "and", Filter: sub})
		}
		appendOrFilterGroup(f, children)
		return true
	case "addedMonth":
		if len(bucketIDs) == 1 && f.EtherAddedFrom == 0 && f.EtherAddedTo == 0 {
			fromTS, toTS, ok := core.ParseCatalogueAddedMonth(bucketIDs[0])
			if !ok {
				return false
			}
			f.EtherAddedFrom = fromTS
			f.EtherAddedTo = toTS
			return true
		}
		children := make([]core.FilterGroup, 0, len(bucketIDs))
		for _, raw := range bucketIDs {
			fromTS, toTS, ok := core.ParseCatalogueAddedMonth(raw)
			if !ok {
				return false
			}
			children = append(children, core.FilterGroup{
				Operator: "and",
				Filter:   &core.Filter{EtherAddedFrom: fromTS, EtherAddedTo: toTS},
			})
		}
		appendOrFilterGroup(f, children)
		return true
	default:
		return false
	}
}

func appendOrFilterGroup(f *core.Filter, children []core.FilterGroup) {
	orGroup := core.FilterGroup{Operator: "or", Children: children}
	if f.Group == nil {
		f.Group = &orGroup
		return
	}
	f.Group = &core.FilterGroup{
		Operator: "and",
		Children: []core.FilterGroup{*f.Group, orGroup},
	}
}

func libraryQuery(r *http.Request, view frontend.View, library object) (frontend.ItemQuery, string, string, int) {
	q := frontend.ItemQuery{Limit: limit(r), NoTotal: true}
	options := library["browse"].(object)
	sorts := options["sorts"].([]object)
	filters := options["filters"].(object)
	isPlugin := view.PluginID != "" || view.KindKey != ""
	values := r.URL.Query()
	for name, entries := range values {
		if strings.HasPrefix(name, "facet.") {
			continue
		}
		switch name {
		case "cursor", "limit", "sort", "direction", "kind", "genre", "year", "played", "favorite", "collectionId", "query", "tab":
		default:
			return q, "", "", 400
		}
		if len(entries) != 1 {
			return q, "", "", 400
		}
	}

	var activeTab *frontend.ViewTab
	if tabKey := values.Get("tab"); tabKey != "" {
		if len(view.Tabs) > 0 {
			for idx := range view.Tabs {
				t := &view.Tabs[idx]
				if t.ID == tabKey && (t.Kind == "view" || t.Kind == "library") {
					activeTab = t
					break
				}
			}
		} else if tabKey == "library" {
			activeTab = &frontend.ViewTab{ID: "library", Kind: "library", Search: true}
		}
		if activeTab == nil {
			return q, "", "", 404
		}
	} else if len(view.Tabs) > 0 {
		for idx := range view.Tabs {
			if view.Tabs[idx].Kind == "library" {
				activeTab = &view.Tabs[idx]
				break
			}
		}
	}

	defSort := options["defaultSort"].(string)
	defDir := options["defaultDirection"].(string)
	if activeTab != nil && activeTab.Sort != "" && hasOfferedSort(sorts, activeTab.Sort) {
		defSort = activeTab.Sort
		defDir = defaultDirectionForSort(sorts, defSort, activeTab.Direction)
	} else if activeTab != nil && activeTab.Direction != "" && allowedSortDirection(sorts, defSort, activeTab.Direction) {
		defDir = activeTab.Direction
	}

	sort, direction := values.Get("sort"), values.Get("direction")
	if sort == "" {
		sort = defSort
	}
	if direction == "" {
		if values.Get("sort") != "" {
			direction = options["defaultDirection"].(string)
			if !allowedSortDirection(sorts, sort, direction) {
				direction = defaultDirectionForSort(sorts, sort, "")
			}
		} else {
			direction = defDir
		}
	}
	if !allowedSortDirection(sorts, sort, direction) {
		return q, sort, direction, 400
	}
	if sort != "server" {
		q.Sort = frontend.SortOrder(sort)
	}
	q.Desc = direction == "desc"

	if activeTab != nil && activeTab.Filters != nil {
		q.Filter.TabRules, q.Filter.TabFallback = activeTab.Filters.ToPolicy(view.Type)
	}

	for name, entries := range values {
		if !strings.HasPrefix(name, "facet.") {
			continue
		}
		field := strings.TrimPrefix(name, "facet.")
		if len(entries) == 0 {
			return q, sort, direction, 400
		}
		var bucketIDs []string
		seen := make(map[string]bool, len(entries))
		for _, raw := range entries {
			if raw == "" || len(raw) > 256 {
				return q, sort, direction, 400
			}
			if !seen[raw] {
				seen[raw] = true
				bucketIDs = append(bucketIDs, raw)
			}
		}
		if isPlugin {
			if !offeredFacet(filters, field) {
				return q, sort, direction, 400
			}
			if q.Filter.Facets == nil {
				q.Filter.Facets = make(map[string][]string)
			}
			q.Filter.Facets[field] = bucketIDs
			continue
		}
		canon, valid := core.NormalizeCatalogueTabFacet(core.TabCollections, field)
		if !valid || canon == "" || !applyCatalogueFacetBuckets(&q.Filter, canon, bucketIDs) {
			return q, sort, direction, 400
		}
	}

	if rawQuery := strings.TrimSpace(values.Get("query")); rawQuery != "" {
		if len(rawQuery) > 256 || (activeTab != nil && activeTab.Kind == "view" && !activeTab.Search) {
			return q, sort, direction, 400
		}
		q.Filter.Query = rawQuery
	}
	if kind := values.Get("kind"); kind != "" {
		ok := false
		kinds, _ := filters["kind"].([]string)
		for _, offered := range kinds {
			if kind == offered {
				ok = true
			}
		}
		if !ok {
			return q, sort, direction, 400
		}
		q.Types = []frontend.MediaType{frontend.MediaType(kind)}
	}
	if genre := values.Get("genre"); genre != "" {
		if filters["genre"] != true || len(genre) > 256 {
			return q, sort, direction, 400
		}
		q.Filter.Genres = append(q.Filter.Genres, genre)
	}
	if year := values.Get("year"); year != "" {
		n, e := strconv.Atoi(year)
		if filters["year"] != true || e != nil || n < 1 || n > 9999 {
			return q, sort, direction, 400
		}
		q.Filter.Years = append(q.Filter.Years, n)
	}
	if colID := values.Get("collectionId"); colID != "" {
		if isPlugin && strings.HasPrefix(colID, "facet:") {
			field, bucketID, ok := strings.Cut(strings.TrimPrefix(colID, "facet:"), ":")
			if !ok || !offeredFacet(filters, field) || bucketID == "" || len(bucketID) > 256 {
				return q, sort, direction, 400
			}
			if q.Filter.Facets == nil {
				q.Filter.Facets = make(map[string][]string)
			}
			if !slices.Contains(q.Filter.Facets[field], bucketID) {
				q.Filter.Facets[field] = append(q.Filter.Facets[field], bucketID)
			}
		} else {
			if filters["collectionId"] != true {
				return q, sort, direction, 400
			}
			colKey, e := ids.ParseRatingKeyGUID(colID)
			if e != nil || (colKey.Kind() != frontend.KindCollection && colKey.Kind() != frontend.KindSmart) {
				return q, sort, direction, 400
			}
			q.CollectionKey = colKey
		}
	}
	for _, name := range []string{"played", "favorite"} {
		if raw, exists := values[name]; exists {
			if filters[name] != true || (raw[0] != "true" && raw[0] != "false") {
				return q, sort, direction, 400
			}
			yes := raw[0] == "true"
			if name == "played" {
				q.Watched = &yes
			} else {
				q.Favorite = &yes
			}
		}
	}
	return q, sort, direction, 0
}

func markLandscapeTitles(ts []frontend.Title, view frontend.View) {
	if view.Card == "" {
		return
	}
	landscape := view.Card == "landscape"
	for i := range ts {
		ts[i].LandscapeCard = landscape
	}
}

func markLandscapeRows(rows []frontend.Row, view frontend.View) {
	if view.Card == "" {
		return
	}
	landscape := view.Card == "landscape"
	for i := range rows {
		markLandscapeTitles(rows[i].Items, view)
		for j := range rows[i].Entries {
			if rows[i].Entries[j].Title != nil {
				rows[i].Entries[j].Title.LandscapeCard = landscape
			}
		}
	}
}

func (i *instance) libraryItems(w http.ResponseWriter, r *http.Request) {
	v, token, err := i.viewer(r)
	if err != nil {
		fail(w, err, r)
		return
	}
	views, err := i.host.Experience.Views(r.Context(), v)
	if err != nil {
		fail(w, err, r)
		return
	}
	id := r.PathValue("id")
	var view frontend.View
	found := false
	for _, candidate := range views {
		if candidate.ID == id {
			view = candidate
			found = true
			break
		}
	}
	// Even collection libraries must be published before Vela will browse them.
	if !found {
		problem(w, 404, "not-found", "not_found")
		return
	}
	library := libraryWire(view)
	q, sort, direction, errCode := libraryQuery(r, view, library)
	if errCode == 404 {
		problem(w, 404, "not-found", "not_found")
		return
	}
	if errCode != 0 {
		problem(w, 400, "request", "unsupported_library_query")
		return
	}
	scope, err := i.livePageScope(r, v, token)
	if err != nil {
		fail(w, err, r)
		return
	}
	c, err := i.readLiveCursor(r, scope, hashJSON([]any{"library-items", id, r.URL.Query().Get("tab"), q, q.Filter.TabRules, q.Filter.TabFallback}))
	if err != nil {
		fail(w, err, r)
		return
	}
	snapshot := hashJSON(views)
	if c.Offset > 10000 || (c.Snapshot != "" && c.Snapshot != snapshot) {
		fail(w, frontend.ErrLiveGuideStale)
		return
	}
	q.Offset = c.Offset
	page, err := i.host.Experience.LibraryItems(r.Context(), v, requestDevice(r), id, q)
	if err != nil {
		fail(w, err, r)
		return
	}
	if len(page.Items) > q.Limit {
		page.Items = page.Items[:q.Limit]
	}
	markLandscapeTitles(page.Items, view)
	converted, err := i.titles(r, v, token, page.Items)
	if err != nil {
		fail(w, err, r)
		return
	}
	entries := []object{}
	for _, title := range converted {
		entries = append(entries, object{"id": title["id"], "kind": "title", "title": title, "receiptToken": ""})
	}
	out := object{"library": library, "items": entries, "sort": sort, "direction": direction, "continuation": "exhausted"}
	expanding := page.Expanding
	if len(page.Items) == q.Limit {
		// Ask for one next item only after the page establishes its keyset end.
		// This preserves neutral paging caches and avoids counts or hydration walks.
		nextQuery := q
		nextQuery.Offset += len(page.Items)
		nextQuery.Limit = 1
		more, e := i.host.Experience.LibraryItems(r.Context(), v, requestDevice(r), id, nextQuery)
		if e != nil {
			fail(w, e, r)
			return
		}
		// The one-item lookahead never reports a fill: the first page already
		// waited for it, and a flag here would make the client reload the list.
		if len(more.Items) > 0 {
			next, e := i.nextLiveCursor(c, nextQuery.Offset, snapshot)
			if e != nil {
				fail(w, e, r)
				return
			}
			out["continuation"], out["nextCursor"] = "more", next
		}
	}
	if expanding {
		out["expanding"] = true
	}
	cache(w, r, out)
}

func (i *instance) libraryFacets(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(i.host.Experience.NoProviderWait(r.Context()))
	v, token, err := i.viewer(r)
	if err != nil {
		fail(w, err, r)
		return
	}
	values := r.URL.Query()
	for name, entries := range values {
		switch name {
		case "cursor", "limit", "query":
		default:
			problem(w, 400, "request", "unsupported_library_query")
			return
		}
		if len(entries) != 1 {
			problem(w, 400, "request", "unsupported_library_query")
			return
		}
	}
	query := strings.TrimSpace(values.Get("query"))
	if len(query) > 256 {
		problem(w, 400, "request", "unsupported_library_query")
		return
	}
	views, err := i.host.Experience.Views(r.Context(), v)
	if err != nil {
		fail(w, err, r)
		return
	}
	id := r.PathValue("id")
	field := r.PathValue("field")
	var view frontend.View
	found := false
	for _, candidate := range views {
		if candidate.ID == id {
			view = candidate
			found = true
			break
		}
	}
	if !found {
		problem(w, 404, "not-found", "not_found")
		return
	}
	declared := false
	for _, f := range view.Facets {
		if f.ID == field {
			declared = true
			break
		}
	}
	bucketReader, hasBuckets := i.host.Experience.(frontend.LibraryFacetBuckets)
	if !declared || !hasBuckets {
		problem(w, 404, "not-found", "not_found")
		return
	}
	pageLimit := limit(r)
	scope, err := i.livePageScope(r, v, token)
	if err != nil {
		fail(w, err, r)
		return
	}
	c, err := i.readLiveCursor(r, scope, hashJSON([]any{"library-facets", id, field, query, pageLimit}))
	if err != nil {
		fail(w, err, r)
		return
	}
	snapshot := hashJSON(views)
	if c.Offset > 10000 || (c.Snapshot != "" && c.Snapshot != snapshot) {
		fail(w, frontend.ErrLiveGuideStale)
		return
	}
	buckets, err := bucketReader.LibraryFacetBuckets(r.Context(), v, requestDevice(r), id, field)
	if err != nil {
		fail(w, err, r)
		return
	}
	if query != "" {
		needle := strings.ToLower(query)
		filtered := buckets[:0]
		for _, b := range buckets {
			if strings.Contains(strings.ToLower(b.Label), needle) || strings.Contains(strings.ToLower(b.ID), needle) {
				filtered = append(filtered, b)
			}
		}
		buckets = filtered
	}
	start := min(c.Offset, len(buckets))
	end := min(start+pageLimit, len(buckets))
	items := make([]object, 0, end-start)
	for _, b := range buckets[start:end] {
		items = append(items, object{
			"id":    b.ID,
			"label": b.Label,
			"count": b.Count,
		})
	}
	out := object{"items": items, "continuation": "exhausted"}
	if end < len(buckets) {
		next, e := i.nextLiveCursor(c, end, snapshot)
		if e != nil {
			fail(w, e, r)
			return
		}
		out["continuation"], out["nextCursor"] = "more", next
	}
	cache(w, r, out)
}

func (i *instance) libraryRows(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(i.host.Experience.NoProviderWait(r.Context()))
	v, token, err := i.viewer(r)
	if err != nil {
		fail(w, err, r)
		return
	}
	if !noCursor(w, r) {
		return
	}
	for name, entries := range r.URL.Query() {
		switch name {
		case "cursor", "limit":
		default:
			problem(w, 400, "request", "unsupported_library_query")
			return
		}
		if len(entries) != 1 {
			problem(w, 400, "request", "unsupported_library_query")
			return
		}
	}
	views, err := i.host.Experience.Views(r.Context(), v)
	if err != nil {
		fail(w, err, r)
		return
	}
	id := r.PathValue("id")
	var view frontend.View
	found := false
	for _, candidate := range views {
		if candidate.ID == id {
			view = candidate
			found = true
			break
		}
	}
	if !found {
		problem(w, 404, "not-found", "not_found")
		return
	}
	if view.PluginID != "" || view.KindKey != "" {
		reader, ok := i.host.Experience.(frontend.PluginLibraryRowsReader)
		if !ok {
			cache(w, r, object{"items": []object{}, "continuation": "exhausted"})
			return
		}
		srcRows, err := reader.PluginLibraryRows(r.Context(), v, requestDevice(r), id)
		if err != nil {
			fail(w, err, r)
			return
		}
		more := len(srcRows) > limit(r)
		if more {
			srcRows = srcRows[:limit(r)]
		}
		markLandscapeRows(srcRows, view)
		rows, err := i.rowsToWire(r, v, token, srcRows, nil, false, defaultRowItemLimit)
		if err != nil {
			fail(w, err, r)
			return
		}
		continuation := "exhausted"
		if more {
			continuation = "unknown"
		}
		cache(w, r, object{"items": rows, "continuation": continuation})
		return
	}
	sub := r.Clone(r.Context())
	sub.SetPathValue("id", "")
	q := url.Values{}
	q.Set("placement", string(frontend.PlacementLibrary))
	q.Set("libraryId", id)
	if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
		q.Set("limit", rawLimit)
	}
	subURL := *r.URL
	subURL.RawQuery = q.Encode()
	sub.URL = &subURL
	rows, more, err := i.home(sub, v, token)
	if err != nil {
		fail(w, err, r)
		return
	}
	continuation := "exhausted"
	if more {
		continuation = "unknown"
	}
	cache(w, r, object{"items": rows, "continuation": continuation})
}

func (i *instance) collectionsWire(r *http.Request, v *frontend.Viewer, token string, cols []frontend.Collection) ([]object, error) {
	art := map[frontend.RatingKey][]object{}
	if len(cols) > 0 && i.host.ImageGrants != nil {
		kinds := []string{frontend.ImagePoster, frontend.ImageBackdrop}
		if signer, ok := i.host.ImageGrants.(frontend.ImagePageSigner); ok {
			titles := make([]frontend.Title, 0, len(cols))
			seen := make(map[frontend.RatingKey]bool, len(cols))
			for _, col := range cols {
				if !seen[col.Key] {
					seen[col.Key] = true
					titles = append(titles, frontend.Title{Key: col.Key})
				}
			}
			images, err := signer.SignPage(r.Context(), frontend.ImagePageRequest{
				Credential: token,
				Device:     requestDevice(r),
				Titles:     titles,
				Kinds:      kinds,
			})
			if err != nil {
				return nil, err
			}
			for _, item := range images {
				for _, g := range item.Images {
					im := g.Image
					if im.Path == "" || g.Secret == "" {
						continue
					}
					art[item.Item] = append(art[item.Item], object{
						"id":        hash(im.Path),
						"kind":      im.Kind,
						"tag":       im.Tag,
						"sizes":     []object{imageSize(im, g.Secret)},
						"blurHash":  im.BlurHash,
						"thumbHash": im.ThumbHash,
					})
				}
			}
		} else if artReader, hasArt := i.host.Experience.(frontend.ItemArtwork); hasArt {
			seen := make(map[frontend.RatingKey]bool, len(cols))
			for _, col := range cols {
				if seen[col.Key] {
					continue
				}
				seen[col.Key] = true
				imgs, ok, err := artReader.ItemArtwork(r.Context(), v, requestDevice(r), col.Key)
				if err != nil || !ok {
					continue
				}
				for _, im := range imgs {
					if (im.Kind != frontend.ImagePoster && im.Kind != frontend.ImageBackdrop) || im.Path == "" {
						continue
					}
					secret, err := i.host.ImageGrants.Issue(r.Context(), frontend.ImageGrantRequest{
						Credential: token,
						Device:     requestDevice(r),
						Item:       col.Key,
						Image:      im,
					})
					if err != nil || secret == "" {
						continue
					}
					art[col.Key] = append(art[col.Key], object{
						"id":        hash(im.Path),
						"kind":      im.Kind,
						"tag":       im.Tag,
						"sizes":     []object{imageSize(im, secret)},
						"blurHash":  im.BlurHash,
						"thumbHash": im.ThumbHash,
					})
				}
			}
		}
	}
	out := make([]object, 0, len(cols))
	for _, col := range cols {
		colID := ids.RatingKeyGUID(col.Key)
		images := art[col.Key]
		if images == nil {
			images = []object{}
		}
		out = append(out, object{
			"id":        colID,
			"name":      col.Title,
			"summary":   col.Summary,
			"itemCount": col.ChildCount,
			"artwork":   images,
		})
	}
	return out, nil
}

func (i *instance) facetCollectionsWire(r *http.Request, v *frontend.Viewer, token, facetField string, view frontend.View, items []frontend.FacetCollection) ([]object, error) {
	var newest []frontend.Title
	seen := make(map[frontend.RatingKey]bool, len(items))
	for _, fc := range items {
		if fc.NewestItem != nil && !seen[fc.NewestItem.Key] {
			seen[fc.NewestItem.Key] = true
			t := *fc.NewestItem
			if view.Card != "" {
				t.LandscapeCard = view.Card == "landscape"
			}
			newest = append(newest, t)
		}
	}
	art, err := i.titleArtworkBatch(r, v, token, newest)
	if err != nil {
		return nil, err
	}
	entries := make([]object, 0, len(items))
	for _, fc := range items {
		colID := "facet:" + facetField + ":" + fc.ID
		images := []object{}
		if fc.NewestItem != nil {
			if a := art[fc.NewestItem.Key]; a != nil {
				images = a
			}
		}
		col := object{
			"id":        colID,
			"name":      fc.Name,
			"summary":   "",
			"itemCount": fc.ItemCount,
			"artwork":   images,
		}
		entries = append(entries, object{
			"id":           colID,
			"kind":         "collection",
			"collection":   col,
			"receiptToken": "",
		})
	}
	return entries, nil
}

func (i *instance) libraryCollections(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(i.host.Experience.NoProviderWait(r.Context()))
	v, token, err := i.viewer(r)
	if err != nil {
		fail(w, err, r)
		return
	}
	values := r.URL.Query()
	for name, entries := range values {
		switch name {
		case "cursor", "limit", "tab", "query":
		default:
			problem(w, 400, "request", "unsupported_library_query")
			return
		}
		if len(entries) != 1 {
			problem(w, 400, "request", "unsupported_library_query")
			return
		}
	}
	tabKey := values.Get("tab")
	query := strings.TrimSpace(values.Get("query"))
	if utf8.RuneCountInString(query) > 100 {
		problem(w, 400, "request", "unsupported_library_query")
		return
	}
	views, err := i.host.Experience.Views(r.Context(), v)
	if err != nil {
		fail(w, err, r)
		return
	}
	id := r.PathValue("id")
	var view frontend.View
	found := false
	for _, candidate := range views {
		if candidate.ID == id {
			view = candidate
			found = true
			break
		}
	}
	if !found {
		problem(w, 404, "not-found", "not_found")
		return
	}

	isPlugin := view.PluginID != "" || view.KindKey != ""
	var selectedTab frontend.ViewTab
	hasSelected := false
	if isPlugin || len(view.Tabs) > 0 {
		if tabKey != "" {
			for _, t := range view.Tabs {
				if t.ID == tabKey && (t.Kind == "collections" || t.Kind == "people") {
					hasSelected = true
					selectedTab = t
					break
				}
			}
			if !hasSelected {
				problem(w, 404, "not-found", "not_found")
				return
			}
		} else {
			for _, t := range view.Tabs {
				if isPlugin {
					if (t.Kind == "collections" || t.Kind == "people") && t.Facet != "" {
						hasSelected = true
						selectedTab = t
						break
					}
				} else if t.Kind == "collections" || t.Kind == "people" {
					hasSelected = true
					selectedTab = t
					break
				}
			}
		}
		if query != "" && (!hasSelected || !selectedTab.Search) {
			problem(w, 400, "request", "unsupported_library_query")
			return
		}
	} else {
		if tabKey != "" && tabKey != "collections" {
			problem(w, 404, "not-found", "not_found")
			return
		}
		if query != "" {
			problem(w, 400, "request", "unsupported_library_query")
			return
		}
	}

	pageLimit := limit(r)
	scope, err := i.livePageScope(r, v, token)
	if err != nil {
		fail(w, err, r)
		return
	}
	c, err := i.readLiveCursor(r, scope, hashJSON([]any{"library-collections", id, tabKey, query, pageLimit}))
	if err != nil {
		fail(w, err, r)
		return
	}
	snapshot := hashJSON(views)
	if c.Offset > 10000 || (c.Snapshot != "" && c.Snapshot != snapshot) {
		fail(w, frontend.ErrLiveGuideStale)
		return
	}

	if hasSelected && selectedTab.Kind == "people" {
		peopleReader, hasPeople := i.host.Experience.(frontend.LibraryFacetPeople)
		if selectedTab.Facet == "" || !hasPeople {
			cache(w, r, object{"items": []object{}, "continuation": "exhausted"})
			return
		}
		fpPage, err := peopleReader.LibraryFacetPeople(r.Context(), v, requestDevice(r), id, selectedTab.Facet, selectedTab.Order, frontend.PageQuery{
			Offset: c.Offset,
			Limit:  pageLimit,
			Query:  query,
		})
		if err != nil {
			fail(w, err, r)
			return
		}
		entries, err := i.facetPeopleWire(r, token, fpPage.Items)
		if err != nil {
			fail(w, err, r)
			return
		}
		out := object{"items": entries, "continuation": "exhausted"}
		if fpPage.More {
			next, err := i.nextLiveCursor(c, c.Offset+len(fpPage.Items), snapshot)
			if err != nil {
				fail(w, err, r)
				return
			}
			out["continuation"], out["nextCursor"] = "more", next
		}
		cache(w, r, out)
		return
	}

	if hasSelected && selectedTab.Kind == "collections" && selectedTab.Facet != "" {
		colReader, hasCols := i.host.Experience.(frontend.LibraryFacetCollections)
		if !hasCols {
			cache(w, r, object{"items": []object{}, "continuation": "exhausted"})
			return
		}
		facetField := selectedTab.Facet
		if !isPlugin {
			if canon, ok := core.NormalizeCatalogueTabFacet(core.TabCollections, facetField); ok && canon != "" {
				facetField = canon
			}
		}
		fcPage, err := colReader.LibraryFacetCollections(r.Context(), v, requestDevice(r), id, facetField, selectedTab.Order, frontend.PageQuery{
			Offset: c.Offset,
			Limit:  pageLimit,
			Query:  query,
		})
		if err != nil {
			fail(w, err, r)
			return
		}
		entries, err := i.facetCollectionsWire(r, v, token, facetField, view, fcPage.Items)
		if err != nil {
			fail(w, err, r)
			return
		}
		out := object{"items": entries, "continuation": "exhausted"}
		if fcPage.More {
			next, err := i.nextLiveCursor(c, c.Offset+len(fcPage.Items), snapshot)
			if err != nil {
				fail(w, err, r)
				return
			}
			out["continuation"], out["nextCursor"] = "more", next
		}
		cache(w, r, out)
		return
	}

	if isPlugin {
		cache(w, r, object{"items": []object{}, "continuation": "exhausted"})
		return
	}

	colPage, err := i.host.Experience.Collections(r.Context(), v, requestDevice(r), frontend.CollectionQuery{
		ViewID: id,
		Query:  query,
		Types:  []frontend.MediaType{view.Type},
		Offset: c.Offset,
		Limit:  pageLimit + 1,
	})
	if err != nil {
		fail(w, err, r)
		return
	}
	items := colPage.Items
	hasMore := false
	if len(items) > pageLimit {
		items = items[:pageLimit]
		hasMore = true
	} else if c.Offset+len(items) < colPage.Total {
		hasMore = true
	}
	converted, err := i.collectionsWire(r, v, token, items)
	if err != nil {
		fail(w, err, r)
		return
	}
	entries := make([]object, 0, len(converted))
	for _, col := range converted {
		entries = append(entries, object{
			"id":           col["id"],
			"kind":         "collection",
			"collection":   col,
			"receiptToken": "",
		})
	}
	out := object{"items": entries, "continuation": "exhausted"}
	if hasMore {
		next, err := i.nextLiveCursor(c, c.Offset+len(items), snapshot)
		if err != nil {
			fail(w, err, r)
			return
		}
		out["continuation"], out["nextCursor"] = "more", next
	}
	cache(w, r, out)
}

// addGenreNames lists a library's genres (server order) in
// browse.filters.genres, so a client can offer a Genres view. A library
// whose genres cannot be listed keeps the plain genre filter.
func (i *instance) addGenreNames(r *http.Request, v *frontend.Viewer, view frontend.View, wire object) {
	browse, _ := wire["browse"].(object)
	filters, _ := browse["filters"].(object)
	if filters == nil || filters["genre"] != true {
		return
	}
	values, err := i.host.Experience.Facets(r.Context(), v, requestDevice(r), view.ID, "genre")
	if err != nil {
		return
	}
	names := make([]string, 0, len(values))
	for _, f := range values {
		if f.Title != "" {
			names = append(names, f.Title)
		}
	}
	filters["genres"] = names
}
