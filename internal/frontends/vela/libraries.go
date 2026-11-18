package vela

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/frontend/kit/ids"
)

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
	if isPlugin && (view.Card == "landscape" || view.Card == "square") {
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
	if !isPlugin {
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
				{ID: "library", Label: "Library", Kind: "library"},
			}
		}
		tabs = make([]object, 0, len(srcTabs))
		for _, t := range srcTabs {
			tab := object{"id": t.ID, "label": t.Label, "kind": t.Kind}
			if t.Kind == "collections" && t.Facet != "" {
				tab["facet"] = t.Facet
				effSort := t.Sort
				if effSort == "" {
					if slices.Contains(view.Sorts, "released") {
						effSort = "released"
					} else {
						effSort = defaultSort
					}
				}
				effDir := t.Direction
				if effDir == "" {
					switch effSort {
					case "server", "title", "index":
						effDir = "asc"
					default:
						effDir = "desc"
					}
				}
				tab["sort"] = effSort
				tab["direction"] = effDir
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

func libraryQuery(r *http.Request, library object) (frontend.ItemQuery, string, string, bool) {
	q := frontend.ItemQuery{Limit: limit(r), NoTotal: true}
	options := library["browse"].(object)
	sort, direction := r.URL.Query().Get("sort"), r.URL.Query().Get("direction")
	if sort == "" {
		sort = options["defaultSort"].(string)
	}
	if direction == "" {
		direction = options["defaultDirection"].(string)
	}
	allowed := false
	for _, candidate := range options["sorts"].([]object) {
		if candidate["id"] == sort {
			for _, d := range candidate["directions"].([]string) {
				if direction == d {
					allowed = true
				}
			}
		}
	}
	if !allowed {
		return q, sort, direction, false
	}
	if sort != "server" {
		q.Sort = frontend.SortOrder(sort)
	}
	q.Desc = direction == "desc"
	filters := options["filters"].(object)
	values := r.URL.Query()
	for name, entries := range values {
		if strings.HasPrefix(name, "facet.") {
			field := strings.TrimPrefix(name, "facet.")
			if !offeredFacet(filters, field) || len(entries) == 0 {
				return q, sort, direction, false
			}
			var bucketIDs []string
			seen := make(map[string]bool, len(entries))
			for _, raw := range entries {
				if raw == "" || len(raw) > 256 {
					return q, sort, direction, false
				}
				if !seen[raw] {
					seen[raw] = true
					bucketIDs = append(bucketIDs, raw)
				}
			}
			if q.Filter.Facets == nil {
				q.Filter.Facets = make(map[string][]string)
			}
			q.Filter.Facets[field] = bucketIDs
			continue
		}
		switch name {
		case "cursor", "limit", "sort", "direction", "kind", "genre", "year", "played", "favorite", "collectionId", "query":
		default:
			return q, sort, direction, false
		}
		if len(entries) != 1 {
			return q, sort, direction, false
		}
	}
	if rawQuery := strings.TrimSpace(values.Get("query")); rawQuery != "" {
		if len(rawQuery) > 256 {
			return q, sort, direction, false
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
			return q, sort, direction, false
		}
		q.Types = []frontend.MediaType{frontend.MediaType(kind)}
	}
	if genre := values.Get("genre"); genre != "" {
		if filters["genre"] != true || len(genre) > 256 {
			return q, sort, direction, false
		}
		q.Filter.Genres = []string{genre}
	}
	if year := values.Get("year"); year != "" {
		n, e := strconv.Atoi(year)
		if filters["year"] != true || e != nil || n < 1 || n > 9999 {
			return q, sort, direction, false
		}
		q.Filter.Years = []int{n}
	}
	if colID := values.Get("collectionId"); colID != "" {
		if filters["collectionId"] != true {
			return q, sort, direction, false
		}
		colKey, e := ids.ParseRatingKeyGUID(colID)
		if e != nil || (colKey.Kind() != frontend.KindCollection && colKey.Kind() != frontend.KindSmart) {
			return q, sort, direction, false
		}
		q.CollectionKey = colKey
	}
	for _, name := range []string{"played", "favorite"} {
		if raw, exists := values[name]; exists {
			if filters[name] != true || (raw[0] != "true" && raw[0] != "false") {
				return q, sort, direction, false
			}
			yes := raw[0] == "true"
			if name == "played" {
				q.Watched = &yes
			} else {
				q.Favorite = &yes
			}
		}
	}
	return q, sort, direction, true
}

func markLandscapeTitles(ts []frontend.Title, view frontend.View) {
	if view.Card != "landscape" {
		return
	}
	for i := range ts {
		ts[i].LandscapeCard = true
	}
}

func markLandscapeRows(rows []frontend.Row, view frontend.View) {
	if view.Card != "landscape" {
		return
	}
	for i := range rows {
		markLandscapeTitles(rows[i].Items, view)
		for j := range rows[i].Entries {
			if rows[i].Entries[j].Title != nil {
				rows[i].Entries[j].Title.LandscapeCard = true
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
	q, sort, direction, ok := libraryQuery(r, library)
	if !ok {
		problem(w, 400, "request", "unsupported_library_query")
		return
	}
	scope, err := i.livePageScope(r, v, token)
	if err != nil {
		fail(w, err, r)
		return
	}
	c, err := i.readLiveCursor(r, scope, hashJSON([]any{"library-items", id, q}))
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
		if len(more.Items) > 0 {
			next, e := i.nextLiveCursor(c, nextQuery.Offset, snapshot)
			if e != nil {
				fail(w, e, r)
				return
			}
			out["continuation"], out["nextCursor"] = "more", next
		}
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
			if view.Card == "landscape" {
				t.LandscapeCard = true
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
		case "cursor", "limit", "tab":
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
	pageLimit := limit(r)
	scope, err := i.livePageScope(r, v, token)
	if err != nil {
		fail(w, err, r)
		return
	}
	c, err := i.readLiveCursor(r, scope, hashJSON([]any{"library-collections", id, tabKey, pageLimit}))
	if err != nil {
		fail(w, err, r)
		return
	}
	snapshot := hashJSON(views)
	if c.Offset > 10000 || (c.Snapshot != "" && c.Snapshot != snapshot) {
		fail(w, frontend.ErrLiveGuideStale)
		return
	}
	if view.PluginID != "" || view.KindKey != "" {
		facetField := ""
		if tabKey != "" {
			matchedTab := false
			for _, t := range view.Tabs {
				if t.ID == tabKey && t.Kind == "collections" {
					matchedTab = true
					facetField = t.Facet
					break
				}
			}
			if !matchedTab {
				problem(w, 404, "not-found", "not_found")
				return
			}
		} else {
			for _, t := range view.Tabs {
				if t.Kind == "collections" && t.Facet != "" {
					facetField = t.Facet
					break
				}
			}
		}
		colReader, hasCols := i.host.Experience.(frontend.LibraryFacetCollections)
		if facetField == "" || !hasCols {
			cache(w, r, object{"items": []object{}, "continuation": "exhausted"})
			return
		}
		fcPage, err := colReader.LibraryFacetCollections(r.Context(), v, requestDevice(r), id, facetField, frontend.PageQuery{
			Offset: c.Offset,
			Limit:  pageLimit,
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
	if tabKey != "" && tabKey != "collections" {
		problem(w, 404, "not-found", "not_found")
		return
	}
	colPage, err := i.host.Experience.Collections(r.Context(), v, requestDevice(r), frontend.CollectionQuery{
		ViewID: id,
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
