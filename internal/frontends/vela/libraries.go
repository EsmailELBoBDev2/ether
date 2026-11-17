package vela

import (
	"net/http"
	"strconv"

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
	sorts := []object{{"id": "server", "directions": []string{"asc"}}}
	filters := object{}
	defaultSort := "server"
	if root != "" {
		filters["kind"] = []string{root}
	}
	// Plugin instances preserve their existing store/source order. Their
	// schema-specific filters are not inferred from a native presentation name.
	if view.PluginID == "" && view.KindKey == "" {
		defaultSort = "title"
		for _, sort := range []string{"title", "added", "released", "year", "rating", "duration", "popularity"} {
			sorts = append(sorts, object{"id": sort, "directions": []string{"asc", "desc"}})
		}
		filters["genre"], filters["year"], filters["favorite"], filters["collectionId"] = true, true, true, true
		if root == "movie" {
			filters["played"] = true
		}
	}
	return object{"id": view.ID, "name": view.Name, "kind": kind, "artwork": []object{}, "browse": object{"sorts": sorts, "defaultSort": defaultSort, "defaultDirection": "asc", "filters": filters}}
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
	// Repeated values are ambiguous, rather than an implicit OR filter.
	for name, entries := range values {
		switch name {
		case "cursor", "limit", "sort", "direction", "kind", "genre", "year", "played", "favorite", "collectionId":
		default:
			return q, sort, direction, false
		}
		if len(entries) != 1 {
			return q, sort, direction, false
		}
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
	pageLimit := limit(r)
	scope, err := i.livePageScope(r, v, token)
	if err != nil {
		fail(w, err, r)
		return
	}
	c, err := i.readLiveCursor(r, scope, hashJSON([]any{"library-collections", id, pageLimit}))
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
		cache(w, r, object{"items": []object{}, "continuation": "exhausted"})
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
