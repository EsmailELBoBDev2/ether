package mediabrowser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/frontend/frontendtest"
	"github.com/braedonsaunders/ether/internal/frontend/kit/ids"
)

// embyWebHost mounts Emby with one viewer, two libraries and an in-memory
// preference store, and calls it the way Emby Web does: credentials and
// device fields in the query string.
func embyWebHost(t *testing.T) (call func(method, path string, body []byte) *httptest.ResponseRecorder, uid string) {
	t.Helper()
	viewer := &frontend.Viewer{ID: 1, Username: "sam"}
	var mu sync.Mutex
	store := map[string]json.RawMessage{}
	host := frontend.Host{
		Identity: frontendtest.Identity{ResolveFunc: func(_ context.Context, token string) (*frontend.Viewer, error) {
			if token != "valid" {
				return nil, errors.New("denied")
			}
			return viewer, nil
		}},
		Experience: frontendtest.Experience{ViewsFunc: func(context.Context, *frontend.Viewer) ([]frontend.View, error) {
			return []frontend.View{{ID: "1", Name: "Movies", Type: "movie"}, {ID: "2", Name: "TV", Type: "show"}}, nil
		}, FacetsFunc: func(_ context.Context, _ *frontend.Viewer, _ frontend.Device, _, kind string) ([]frontend.FacetValue, error) {
			if kind == "genre" {
				return []frontend.FacetValue{{ID: "genre-action", Title: "Action"}}, nil
			}
			if kind == "studio" {
				return []frontend.FacetValue{{ID: "studio-syncopy", Title: "Syncopy"}}, nil
			}
			return nil, nil
		}},
		// Emby numbers genres and studios from one counter.
		IDs: ids.Codecs{Allocator: &mapAllocator{ids: map[string]int32{"genre:genre-action": 4, "studio:studio-syncopy": 5}}},
		Preferences: frontendtest.Preferences{
			GetFunc: func(_ context.Context, _ *frontend.Viewer, client, key string) (json.RawMessage, error) {
				mu.Lock()
				defer mu.Unlock()
				if doc, ok := store[client+"/"+key]; ok {
					return doc, nil
				}
				return nil, frontend.ErrPreferencesNotFound
			},
			PutFunc: func(_ context.Context, _ *frontend.Viewer, client, key string, doc json.RawMessage) error {
				mu.Lock()
				defer mu.Unlock()
				store[client+"/"+key] = doc
				return nil
			},
		},
	}
	inst, err := (Emby{MachineID: "test"}).Mount(context.Background(), host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inst.Close(context.Background()) })
	uid, _ = ids.GUID(ids.User, 1)
	call = func(method, path string, body []byte) *httptest.ResponseRecorder {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		path += sep + "X-Emby-Client=Emby+Web&X-Emby-Device-Id=web1&X-Emby-Client-Version=4.10.2.0&X-Emby-Token=valid"
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		if body != nil {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		inst.Handler().ServeHTTP(w, r)
		return w
	}
	return call, uid
}

func TestEmbyWebHomeSectionsAreNamedRows(t *testing.T) {
	call, uid := embyWebHost(t)
	w := call(http.MethodGet, "/emby/Users/"+uid+"/HomeSections", nil)
	if w.Code != 200 {
		t.Fatalf("HomeSections: %d %s", w.Code, w.Body)
	}
	// Emby Web splices its own rows into the list: a plain array.
	var res struct {
		Items []struct {
			Id, SectionType, Name, CollectionType string
			ParentItem                            *struct{ Id, Name string }
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res.Items); err != nil {
		t.Fatalf("HomeSections is not an array: %s", w.Body)
	}
	movies, _ := ids.GUID(ids.View, 1)
	want := []struct{ typ, name string }{{"userviews", "My Media"}, {"resume", "Continue Watching"}, {"nextup", "Next Up"}, {"latestmedia", "Latest Movies"}, {"latestmedia", "Latest TV"}}
	if len(res.Items) != len(want) {
		t.Fatalf("sections %+v", res.Items)
	}
	for n, item := range res.Items {
		if item.SectionType != want[n].typ || item.Name != want[n].name {
			t.Fatalf("section %d: %+v", n, item)
		}
	}
	if p := res.Items[3].ParentItem; p == nil || p.Id != movies || p.Name != "Movies" || res.Items[3].CollectionType != "movies" {
		t.Fatalf("latest row library: %+v", res.Items[3])
	}
	// A row id that is not in the layout answers an empty result, not 404.
	if w := call(http.MethodGet, "/emby/Users/"+uid+"/Sections/nope/Items", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"TotalRecordCount":0`) {
		t.Fatalf("unknown section: %d %s", w.Code, w.Body)
	}
	// My Media resolves to the library views.
	tiles := res.Items[0].Id
	if w := call(http.MethodGet, "/emby/Users/"+uid+"/Sections/"+tiles+"/Items", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"Name":"Movies"`) {
		t.Fatalf("My Media row: %d %s", w.Code, w.Body)
	}
}

func TestEmbyWebUserSettingsRoundTrip(t *testing.T) {
	call, uid := embyWebHost(t)
	if w := call(http.MethodPost, "/emby/usersettings/"+uid, []byte(`{"theme":"dark","subtitle":"en"}`)); w.Code != 204 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if w := call(http.MethodPost, "/emby/usersettings/"+uid+"/Partial", []byte(`{"subtitle":"fr"}`)); w.Code != 204 {
		t.Fatalf("partial: %d %s", w.Code, w.Body)
	}
	w := call(http.MethodGet, "/emby/usersettings/"+uid, nil)
	var got map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got["theme"] != "dark" || got["subtitle"] != "fr" {
		t.Fatalf("read back: %d %s", w.Code, w.Body)
	}
	other, _ := ids.GUID(ids.User, 2)
	if w := call(http.MethodGet, "/emby/usersettings/"+other, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("another viewer's settings: %d", w.Code)
	}
}

func TestEmbyWebStartupListsAreEmptyNot404(t *testing.T) {
	call, _ := embyWebHost(t)
	for _, p := range []string{"/emby/Features", "/emby/Sessions/PlayQueue", "/emby/OfficialRatings", "/emby/Tags", "/emby/StreamLanguages"} {
		if w := call(http.MethodGet, p, nil); w.Code != 200 {
			t.Fatalf("%s: %d", p, w.Code)
		}
	}
}

func TestEmbyWebLibraryTabsPrefixesAndGenrePages(t *testing.T) {
	call, uid := embyWebHost(t)
	movies, _ := ids.GUID(ids.View, 1)
	w := call(http.MethodGet, "/emby/Users/"+uid+"/Items/"+movies, nil)
	var lib struct{ Subviews []string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &lib) != nil || len(lib.Subviews) == 0 || lib.Subviews[0] != "movies" {
		t.Fatalf("library without Subviews breaks Emby Web's tabs: %d %s", w.Code, w.Body)
	}
	w = call(http.MethodGet, "/emby/Items/Prefixes?ParentId="+movies, nil)
	var prefixes []struct{ Name string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &prefixes) != nil || len(prefixes) != 27 || prefixes[1].Name != "A" {
		t.Fatalf("prefixes: %d %s", w.Code, w.Body)
	}
	for _, tc := range []struct{ path, id, name, typ string }{
		{"/emby/Users/" + uid + "/Items/4", "4", "Action", "Genre"},
		{"/emby/Genres/action", "4", "Action", "Genre"},
		{"/emby/Users/" + uid + "/Items/5", "5", "Syncopy", "Studio"},
		{"/emby/Studios/syncopy", "5", "Syncopy", "Studio"},
	} {
		w = call(http.MethodGet, tc.path, nil)
		var g struct{ Id, Name, Type string }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &g) != nil || g.Name != tc.name || g.Type != tc.typ || g.Id != tc.id {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body)
		}
	}
	if w = call(http.MethodGet, "/emby/Users/"+uid+"/Items/6", nil); w.Code != http.StatusNotFound {
		t.Fatalf("unissued facet number: %d %s", w.Code, w.Body)
	}
	w = call(http.MethodGet, "/emby/Localization/Cultures", nil)
	var cultures []struct{ DisplayName, ThreeLetterISOLanguageName string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &cultures) != nil || len(cultures) < 50 {
		t.Fatalf("cultures: %d %s", w.Code, w.Body)
	}
	found := false
	for _, c := range cultures {
		found = found || (c.DisplayName == "English" && c.ThreeLetterISOLanguageName == "eng")
	}
	if !found {
		t.Fatalf("cultures miss English/eng: %s", w.Body)
	}
	for _, p := range []string{"/emby/Notifications/Services/Configured", "/emby/Notifications/Types"} {
		if w = call(http.MethodGet, p, nil); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
			t.Fatalf("%s: %d %s", p, w.Code, w.Body)
		}
	}
}

type mapAllocator struct{ ids map[string]int32 }

func (a *mapAllocator) Compact(_ context.Context, surface, kind, ref string) (int32, error) {
	if n, ok := a.ids[ref]; ok && surface == "emby" && kind == embyNamedFacetKind {
		return n, nil
	}
	return 0, errors.New("unknown")
}
func (a *mapAllocator) Resolve(_ context.Context, surface, kind string, id int32) (string, error) {
	for ref, n := range a.ids {
		if n == id && surface == "emby" && kind == embyNamedFacetKind {
			return ref, nil
		}
	}
	return "", errors.New("unknown")
}
