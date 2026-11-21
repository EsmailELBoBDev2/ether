package mediabrowser

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/frontend/frontendtest"
	"github.com/braedonsaunders/ether/internal/frontend/kit/ids"
)

type mbLevelExperience struct {
	frontendtest.Experience
	lastFacetLevel string
	lastFacetView  string
	lastQuery      frontend.ItemQuery
	lastDeviceLang string
}

func (e *mbLevelExperience) FacetsAtLevel(_ context.Context, _ *core.Viewer, dev core.Device, viewID, level, _ string) ([]core.FacetValue, error) {
	e.lastFacetView = viewID
	e.lastFacetLevel = level
	e.lastDeviceLang = dev.Language
	return []core.FacetValue{{ID: "g1", Key: "Jazz", Title: "Jazz", Count: 3}}, nil
}

func TestMediaBrowserLevelFacetsArtistsAndLanguage(t *testing.T) {
	viewer := &frontend.Viewer{ID: 1}
	ident := frontendtest.Identity{ResolveFunc: func(context.Context, string) (*frontend.Viewer, error) { return viewer, nil }}

	exp := &mbLevelExperience{}
	exp.LibraryItemsFunc = func(_ context.Context, _ *core.Viewer, dev core.Device, _ string, q frontend.ItemQuery) (core.Page, error) {
		exp.lastQuery = q
		exp.lastDeviceLang = dev.Language
		return core.Page{
			Items: []core.Title{{Key: core.PluginKey(101), Type: core.TypeShow, MBItemType: "MusicArtist", Title: "Miles Davis"}},
			Total: 1,
		}, nil
	}

	inst, err := (Jellyfin{MachineID: "test"}).Mount(context.Background(), frontend.Host{
		Identity:   ident,
		Experience: exp,
	})
	if err != nil {
		t.Fatal(err)
	}

	viewGUID, err := ids.GUID(ids.View, 77)
	if err != nil {
		t.Fatal(err)
	}

	// 1. /Genres?ParentId={view}&IncludeItemTypes=MusicAlbum delegates level="MusicAlbum"
	//    and forwards Accept-Language into Device.Language.
	req := httptest.NewRequest(http.MethodGet, "/Genres?ParentId="+viewGUID+"&IncludeItemTypes=MusicAlbum", nil)
	req.Header.Set("X-Emby-Token", "grant")
	req.Header.Set("Accept-Language", "de-DE,de;q=0.9")
	w := httptest.NewRecorder()
	inst.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("/Genres status = %d %s", w.Code, w.Body.String())
	}
	if exp.lastFacetView != "77" || exp.lastFacetLevel != "MusicAlbum" || exp.lastDeviceLang != "de-DE,de;q=0.9" {
		t.Fatalf("FacetsAtLevel = (view=%q level=%q lang=%q), want (77, MusicAlbum, de-DE,de;q=0.9)", exp.lastFacetView, exp.lastFacetLevel, exp.lastDeviceLang)
	}

	// 2. /Artists?ParentId={view} browses MusicArtist items recursively.
	req = httptest.NewRequest(http.MethodGet, "/Artists?ParentId="+viewGUID, nil)
	req.Header.Set("X-Emby-Token", "grant")
	req.Header.Set("Accept-Language", "ja")
	w = httptest.NewRecorder()
	inst.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("/Artists status = %d %s", w.Code, w.Body.String())
	}
	if !exp.lastQuery.Recursive || !slices.Contains(exp.lastQuery.UnservedTypes, "musicartist") || exp.lastDeviceLang != "ja" {
		t.Fatalf("/Artists query = %+v lang=%q", exp.lastQuery, exp.lastDeviceLang)
	}
	var payload struct {
		Items            []map[string]any `json:"Items"`
		TotalRecordCount int              `json:"TotalRecordCount"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.TotalRecordCount != 1 || len(payload.Items) != 1 || payload.Items[0]["Type"] != "MusicArtist" {
		t.Fatalf("/Artists response = %+v", payload)
	}
}
