package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/braedonsaunders/ether/internal/admin"
	"github.com/braedonsaunders/ether/internal/core"
)

type libraryAwareEngine struct {
	*fakeEngine
	rows []core.RowDescriptor
}

func (e *libraryAwareEngine) AvailableRows(context.Context) ([]core.RowDescriptor, error) {
	return e.rows, nil
}

type libraryLayoutResp struct {
	Library       admin.LayoutLibrary       `json:"library"`
	Customized    bool                      `json:"customized"`
	InheritedFrom string                    `json:"inheritedFrom"`
	EffectivePage core.LayoutPage           `json:"effectivePage"`
	ViewerPages   []admin.ViewerLibraryPage `json:"viewerPages"`
	Defaults      struct {
		MaxRows int `json:"maxRows"`
	} `json:"defaults"`
	AvailableRows []core.RowDescriptor `json:"availableRows"`
}

func TestPerLibraryLayoutEndpointsAndPreviewOverlay(t *testing.T) {
	base := &fakeEngine{
		doc: core.HomeLayout{
			Global: []core.LayoutPage{
				{
					Section: "movies",
					MaxRows: 8,
					Rows:    []core.RowPolicy{{Row: "movies.top10", Status: "always", Slot: 1}},
				},
			},
			Viewers: []core.ViewerLayout{
				{
					UserID: 2,
					Pages: []core.LayoutPage{
						{Section: "movies", Rows: []core.RowPolicy{{Row: "movies.gems", Status: "always", Slot: 2}}},
					},
				},
			},
		},
	}
	eng := &libraryAwareEngine{
		fakeEngine: base,
		rows: []core.RowDescriptor{
			{HubID: "home.only", Title: "Home Only", Kind: "home-only", Sections: []string{"home"}},
			{HubID: "movies.top10", Title: "Top 10 Movies", Kind: "top-10", Sections: []string{"home", "movies"}},
			{HubID: "movies.gems", Title: "Hidden Gems", Kind: "gems", Sections: []string{"movies", "shows"}},
			{HubID: "shows.only", Title: "Shows Only", Kind: "shows-only", Sections: []string{"shows"}},
		},
	}
	cfg := newStore(t, nil)
	h := admin.NewHandler(admin.Deps{
		Config:          cfg,
		IsOwnerToken:    storedOwner(cfg),
		Personalization: eng,
		Users:           &fakeUsers{users: []core.User{{ID: 1, IsOwner: true}, {ID: 2}}},
		LayoutLibraries: func(context.Context) ([]admin.LayoutLibrary, error) {
			return []admin.LayoutLibrary{
				{ID: 1, Name: "Movies", Slug: "movies", Type: "movie"},
				{ID: 2, Name: "TV Shows", Slug: "shows", Type: "show"},
				{ID: 3, Name: "Kids Movies", Slug: "kids-movies", Type: "movie"},
			}, nil
		},
	})

	rec := serve(h, req(http.MethodGet, "/ether/api/layout", ""))
	if rec.Code != 200 {
		t.Fatalf("GET /api/layout: %d %s", rec.Code, rec.Body)
	}
	var rootResp struct {
		Libraries []admin.LayoutLibrary `json:"libraries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rootResp); err != nil {
		t.Fatal(err)
	}
	if len(rootResp.Libraries) != 3 || rootResp.Libraries[2].Section != "library:3" || rootResp.Libraries[2].BasePage != "movies" || rootResp.Libraries[2].Customized {
		t.Fatalf("unexpected libraries in GET /api/layout: %+v", rootResp.Libraries)
	}

	rec = serve(h, req(http.MethodGet, "/ether/api/layout/libraries/3", ""))
	if rec.Code != 200 {
		t.Fatalf("GET /api/layout/libraries/3: %d %s", rec.Code, rec.Body)
	}
	var libGet libraryLayoutResp
	if err := json.Unmarshal(rec.Body.Bytes(), &libGet); err != nil {
		t.Fatal(err)
	}
	if libGet.Customized || libGet.InheritedFrom != "movies" || libGet.EffectivePage.Section != "library:3" || libGet.EffectivePage.MaxRows != 8 {
		t.Fatalf("unexpected inherited library layout: %+v", libGet)
	}
	if len(libGet.ViewerPages) != 1 || libGet.ViewerPages[0].UserID != 2 || libGet.ViewerPages[0].Page.Section != "library:3" {
		t.Fatalf("expected inherited viewer override for user 2: %+v", libGet.ViewerPages)
	}
	if len(libGet.AvailableRows) != 2 || libGet.AvailableRows[0].HubID != "movies.top10" || libGet.AvailableRows[1].HubID != "movies.gems" {
		t.Fatalf("availableRows not filtered to movies basePage: %+v", libGet.AvailableRows)
	}
	if libGet.Defaults.MaxRows != 10 {
		t.Fatalf("defaults.maxRows = %d, want 10", libGet.Defaults.MaxRows)
	}

	rec = serve(h, req(http.MethodPost, "/ether/api/layout/libraries/3", ""))
	if rec.Code != 200 {
		t.Fatalf("POST /api/layout/libraries/3: %d %s", rec.Code, rec.Body)
	}
	var afterFork libraryLayoutResp
	if err := json.Unmarshal(rec.Body.Bytes(), &afterFork); err != nil {
		t.Fatal(err)
	}
	if !afterFork.Customized || afterFork.InheritedFrom != "" || base.writes != 1 {
		t.Fatalf("after fork: customized=%v inheritedFrom=%q writes=%d", afterFork.Customized, afterFork.InheritedFrom, base.writes)
	}

	putBody := `{
		"page": {"maxRows": 5, "rows": [{"row": "movies.gems", "status": "always", "slot": 1}]},
		"viewerPages": [{"userId": 2, "page": {"maxRows": 4, "rows": [{"row": "movies.top10", "status": "always", "slot": 2}]}}]
	}`
	rec = serve(h, req(http.MethodPut, "/ether/api/layout/libraries/3", putBody))
	if rec.Code != 200 {
		t.Fatalf("PUT /api/layout/libraries/3: %d %s", rec.Code, rec.Body)
	}
	var afterPut libraryLayoutResp
	if err := json.Unmarshal(rec.Body.Bytes(), &afterPut); err != nil {
		t.Fatal(err)
	}
	if !afterPut.Customized || afterPut.EffectivePage.MaxRows != 5 || len(afterPut.EffectivePage.Rows) != 1 || afterPut.EffectivePage.Rows[0].Row != "movies.gems" {
		t.Fatalf("after PUT: effectivePage = %+v", afterPut.EffectivePage)
	}
	if len(base.doc.Global) != 2 || base.doc.Global[0].Section != "movies" || base.doc.Global[0].MaxRows != 8 {
		t.Fatalf("shared movies page clobbered: %+v", base.doc.Global)
	}

	writesBefore := base.writes
	prevBody := `{"viewerId": 2, "section": "library:3", "page": {"maxRows": 3, "rows": [{"row": "movies.top10", "status": "always", "slot": 1}]}}`
	rec = serve(h, req(http.MethodPost, "/ether/api/layout/preview", prevBody))
	if rec.Code != 200 {
		t.Fatalf("POST /api/layout/preview: %d %s", rec.Code, rec.Body)
	}
	if base.writes != writesBefore || base.section != "library:3" {
		t.Fatalf("preview mutated state or lost section: writes=%d section=%q", base.writes, base.section)
	}

	rec = serve(h, req(http.MethodDelete, "/ether/api/layout/libraries/3", ""))
	if rec.Code != 200 {
		t.Fatalf("DELETE /api/layout/libraries/3: %d %s", rec.Code, rec.Body)
	}
	var afterReset libraryLayoutResp
	if err := json.Unmarshal(rec.Body.Bytes(), &afterReset); err != nil {
		t.Fatal(err)
	}
	if afterReset.Customized || afterReset.InheritedFrom != "movies" || afterReset.EffectivePage.MaxRows != 8 {
		t.Fatalf("after DELETE reset: %+v", afterReset)
	}

	if r404 := serve(h, req(http.MethodGet, "/ether/api/layout/libraries/99", "")); r404.Code != 404 {
		t.Fatalf("unknown library = %d, want 404", r404.Code)
	}
	if r400 := serve(h, req(http.MethodGet, "/ether/api/layout/libraries/not-an-id", "")); r400.Code != 400 {
		t.Fatalf("bad library id = %d, want 400", r400.Code)
	}
}
