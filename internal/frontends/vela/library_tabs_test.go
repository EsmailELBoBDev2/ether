package vela

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/frontend/frontendtest"
)

type customTabsExp struct {
	frontendtest.Experience
	views               []frontend.View
	lastItemQuery       frontend.ItemQuery
	lastFacetField      string
	lastFacetOrder      string
	lastFacetPage       frontend.PageQuery
	lastPeopleField     string
	lastPeopleOrder     string
	lastPeoplePage      frontend.PageQuery
	lastCollectionQuery frontend.CollectionQuery
}

func (e *customTabsExp) LibraryFacetCollections(_ context.Context, _ *frontend.Viewer, _ frontend.Device, _ string, field, order string, page frontend.PageQuery) (frontend.FacetCollectionPage, error) {
	e.lastFacetField = field
	e.lastFacetOrder = order
	e.lastFacetPage = page
	return frontend.FacetCollectionPage{
		Items: []frontend.FacetCollection{
			{
				ID:         "Science Fiction",
				Name:       "Science Fiction",
				ItemCount:  12,
				NewestItem: &core.Title{Key: core.MovieKey(10), Type: core.TypeMovie, Title: "Dune"},
			},
		},
	}, nil
}

func (e *customTabsExp) LibraryFacetPeople(_ context.Context, _ *frontend.Viewer, _ frontend.Device, _ string, field, order string, page frontend.PageQuery) (frontend.FacetPersonPage, error) {
	e.lastPeopleField = field
	e.lastPeopleOrder = order
	e.lastPeoplePage = page
	return frontend.FacetPersonPage{
		Items: []frontend.FacetPerson{
			{
				Person:    frontend.PersonDetail{ID: "p-137427", Name: "Denis Villeneuve", TMDB: 137427},
				ItemCount: 6,
			},
		},
	}, nil
}

func TestVelaCustomLibraryTabsWireBrowseAndCollections(t *testing.T) {
	tabFilter := &core.TabFilter{
		Match: "all",
		Rules: []core.CollectionRule{
			{Field: "genre", Op: "in", Value: json.RawMessage(`["Science Fiction"]`)},
		},
	}

	exp := &customTabsExp{
		views: []frontend.View{
			{
				ID:   "1",
				Name: "Movies",
				Type: frontend.TypeMovie,
				Tabs: []frontend.ViewTab{
					{ID: "recommended", Label: "For You", Kind: core.TabRecommended},
					{ID: "library", Label: "All Movies", Kind: core.TabLibrary, Sort: "added", Direction: "desc", Search: true, SearchPlaceholder: "Search movies"},
					{ID: "scifi", Label: "Sci-Fi Picks", Kind: core.TabView, Sort: "rating", Direction: "desc", Search: false, Filters: tabFilter},
					{ID: "genres", Label: "Genres", Kind: core.TabCollections, Facet: "genre", Order: "name", Search: true},
					{ID: "curated", Label: "Curated Sets", Kind: core.TabCollections, Search: true},
					{ID: "directors", Label: "Directors", Kind: core.TabPeople, Facet: "director", Order: "count", Search: true},
				},
			},
			{
				ID:   "2",
				Name: "Legacy Movies",
				Type: frontend.TypeMovie,
			},
		},
	}
	exp.ViewsFunc = func(_ context.Context, _ *frontend.Viewer) ([]frontend.View, error) {
		return exp.views, nil
	}
	exp.LibraryItemsFunc = func(_ context.Context, _ *frontend.Viewer, _ frontend.Device, _ string, q frontend.ItemQuery) (frontend.Page, error) {
		exp.lastItemQuery = q
		return frontend.Page{Items: []frontend.Title{{Key: core.MovieKey(10), Type: core.TypeMovie, Title: "Dune"}}}, nil
	}
	exp.CollectionsFunc = func(_ context.Context, _ *frontend.Viewer, _ frontend.Device, q frontend.CollectionQuery) (frontend.CollectionPage, error) {
		exp.lastCollectionQuery = q
		return frontend.CollectionPage{
			Items: []core.Collection{
				{Key: core.CollectionKey(1), Title: "Sci-Fi Classics", ChildCount: 4},
			},
			Total: 1,
		}, nil
	}

	seal, err := encryption(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	inst := &instance{seal: seal, host: frontend.Host{Identity: identityFake{}, Experience: exp}}

	r := httptest.NewRequest("GET", "/vela/v0/libraries", nil)
	r.Header.Set("Authorization", "Bearer token")
	r.Header.Set("X-Vela-Device-Id", "device")
	w := httptest.NewRecorder()
	inst.libraries(w, r)
	if w.Code != 200 {
		t.Fatalf("libraries status = %d body = %s", w.Code, w.Body)
	}
	var libsOut map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &libsOut); err != nil {
		t.Fatal(err)
	}
	libs := libsOut["items"].([]any)
	customLib := libs[0].(map[string]any)
	tabs := customLib["tabs"].([]any)
	if len(tabs) != 6 {
		t.Fatalf("custom tabs count = %d, want 6", len(tabs))
	}
	scifiWire := tabs[2].(map[string]any)
	if scifiWire["id"] != "scifi" || scifiWire["kind"] != "view" || scifiWire["sort"] != "rating" || scifiWire["direction"] != "desc" || scifiWire["search"] != false {
		t.Fatalf("unexpected view tab wire: %+v", scifiWire)
	}
	if _, leaked := scifiWire["filters"]; leaked {
		t.Fatalf("server-side tab filters must not leak onto client wire: %+v", scifiWire)
	}

	legacyLib := libs[1].(map[string]any)
	if len(legacyLib["tabs"].([]any)) != 3 {
		t.Fatalf("legacy fallback tabs = %+v, want 3 tabs", legacyLib["tabs"])
	}
	legacyBrowse := legacyLib["browse"].(map[string]any)
	legacyFilters := legacyBrowse["filters"].(map[string]any)
	if legacyFilters["facets"] != nil {
		t.Fatalf("legacy catalogue browse.filters.facets = %+v, want nil", legacyFilters["facets"])
	}

	callSub := func(id, target string, fn func(http.ResponseWriter, *http.Request)) (int, map[string]any) {
		req := httptest.NewRequest("GET", target, nil)
		req.SetPathValue("id", id)
		req.Header.Set("Authorization", "Bearer token")
		req.Header.Set("X-Vela-Device-Id", "device")
		rec := httptest.NewRecorder()
		fn(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	code, _ := callSub("1", "/vela/v0/libraries/1/items?tab=scifi&facet.decade=2020s", inst.libraryItems)
	if code != 200 {
		t.Fatalf("libraryItems(?tab=scifi) status = %d", code)
	}
	if exp.lastItemQuery.Sort != core.SortRating || !exp.lastItemQuery.Desc || len(exp.lastItemQuery.Filter.TabRules) == 0 {
		t.Fatalf("view tab sort/filter not applied: %+v", exp.lastItemQuery)
	}
	if exp.lastItemQuery.Filter.YearFrom != 2020 || exp.lastItemQuery.Filter.YearTo != 2029 {
		t.Fatalf("facet.decade=2020s range = %d..%d, want 2020..2029", exp.lastItemQuery.Filter.YearFrom, exp.lastItemQuery.Filter.YearTo)
	}

	code, colsOut := callSub("1", "/vela/v0/libraries/1/collections?tab=genres&query=sci", inst.libraryCollections)
	if code != 200 {
		t.Fatalf("libraryCollections(genres) status = %d", code)
	}
	if exp.lastFacetField != "genre" || exp.lastFacetOrder != "name" || exp.lastFacetPage.Query != "sci" {
		t.Fatalf("facet collections dispatch = field %q order %q query %q", exp.lastFacetField, exp.lastFacetOrder, exp.lastFacetPage.Query)
	}
	colItems := colsOut["items"].([]any)
	if len(colItems) != 1 || colItems[0].(map[string]any)["id"] != "facet:genre:Science Fiction" {
		t.Fatalf("unexpected facet collections wire: %+v", colItems)
	}

	code, curatedOut := callSub("1", "/vela/v0/libraries/1/collections?tab=curated&query=classics", inst.libraryCollections)
	if code != 200 {
		t.Fatalf("libraryCollections(curated) status = %d", code)
	}
	if exp.lastCollectionQuery.Query != "classics" {
		t.Fatalf("owner collections query = %q, want classics", exp.lastCollectionQuery.Query)
	}
	curated := curatedOut["items"].([]any)
	if len(curated) != 1 {
		t.Fatalf("unexpected owner collections items: %+v", curated)
	}

	code, peopleOut := callSub("1", "/vela/v0/libraries/1/collections?tab=directors&query=denis", inst.libraryCollections)
	if code != 200 {
		t.Fatalf("libraryCollections(directors) status = %d", code)
	}
	if exp.lastPeopleField != "director" || exp.lastPeopleOrder != "count" || exp.lastPeoplePage.Query != "denis" {
		t.Fatalf("facet people dispatch = field %q order %q query %q", exp.lastPeopleField, exp.lastPeopleOrder, exp.lastPeoplePage.Query)
	}
	pItems := peopleOut["items"].([]any)
	if len(pItems) != 1 || pItems[0].(map[string]any)["kind"] != "person" {
		t.Fatalf("unexpected people tab wire: %+v", pItems)
	}

	code, _ = callSub("1", "/vela/v0/libraries/1/items?facet.rating=9-10&facet.rating=8-8.9&facet.runtime=60-90m&facet.addedMonth=2026-02&facet.director=137427&facet.resolution=4k&facet.hdr=dv&facet.audioLanguage=en", inst.libraryItems)
	if code != 200 {
		t.Fatalf("libraryItems(extra catalogue facets) status = %d", code)
	}
	gotF := exp.lastItemQuery.Filter
	if gotF.Group == nil || gotF.Group.Operator != "or" || len(gotF.Group.Children) != 2 ||
		gotF.MinRuntime == 0 || gotF.MaxRuntime == 0 ||
		gotF.EtherAddedFrom == 0 || gotF.EtherAddedTo == 0 ||
		len(gotF.Credits) != 1 || gotF.Credits[0].Kind != "director" ||
		len(gotF.EvidenceResolutions) != 1 || gotF.EvidenceResolutions[0] != "4k" ||
		len(gotF.EvidenceHDR) != 1 || gotF.EvidenceHDR[0] != "dv" ||
		len(gotF.EvidenceAudioLanguages) != 1 || gotF.EvidenceAudioLanguages[0] != "en" {
		t.Fatalf("extra catalogue facets not translated: %+v", gotF)
	}

	if code, _ := callSub("1", "/vela/v0/libraries/1/items?facet.rating=invalid-band", inst.libraryItems); code != 400 {
		t.Fatalf("invalid rating band status = %d, want 400", code)
	}
	if code, _ := callSub("1", "/vela/v0/libraries/1/items?facet.studio=A24&facet.studio=Neon", inst.libraryItems); code != 400 {
		t.Fatalf("multi studio facet status = %d, want 400", code)
	}
}
