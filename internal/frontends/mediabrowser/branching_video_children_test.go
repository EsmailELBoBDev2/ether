package mediabrowser

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/frontend/frontendtest"
	"github.com/braedonsaunders/ether/internal/frontend/kit/ids"
)

// TestMediaBrowserVideoChildUnderMusicArtist proves a branching video level
// (MBItemType: "Video") under a MusicArtist renders as a playable Video item
// with MediaType="Video", IsFolder=false, ParentId=artistGUID, and video
// MediaSources (not mislabelled as Audio or Folder).
func TestMediaBrowserVideoChildUnderMusicArtist(t *testing.T) {
	viewer := &frontend.Viewer{ID: 1}
	ident := frontendtest.Identity{ResolveFunc: func(context.Context, string) (*frontend.Viewer, error) { return viewer, nil }}

	artistKey := core.PluginKey(401)
	albumKey := core.PluginKey(402)
	concertKey := core.PluginKey(403)
	artistGUID := ids.RatingKeyGUID(artistKey)
	concertGUID := ids.RatingKeyGUID(concertKey)

	artist := core.Title{
		Key:        artistKey,
		LibraryID:  77,
		Type:       core.TypeShow,
		MBItemType: "MusicArtist",
		MediaClass: core.MediaClassMusic,
		MediaRole:  core.MediaRoleArtist,
		Title:      "Miles Davis",
	}
	album := core.Title{
		Key:         albumKey,
		LibraryID:   77,
		Type:        core.TypeSeason,
		MBItemType:  "MusicAlbum",
		MediaClass:  core.MediaClassMusic,
		MediaRole:   core.MediaRoleAlbum,
		ParentKey:   artistKey,
		ParentTitle: "Miles Davis",
		Title:       "Kind of Blue",
	}
	concert := core.Title{
		Key:         concertKey,
		LibraryID:   77,
		Type:        core.TypeMovie,
		MBItemType:  "Video",
		MediaClass:  core.MediaClassVideo,
		MediaRole:   "concert",
		ParentKey:   artistKey,
		ParentTitle: "Miles Davis",
		Title:       "Live at Newport 1958",
	}

	exp := &frontendtest.Experience{}
	exp.ViewsFunc = func(context.Context, *core.Viewer) ([]frontend.View, error) {
		return []frontend.View{{
			ID:        "77",
			LibraryID: 77,
			Name:      "Music",
			Type:      core.TypeShow,
			PluginID:  "community.example.music",
			KindKey:   "music",
			Native:    "music",
		}}, nil
	}
	exp.ChildrenFunc = func(_ context.Context, _ *core.Viewer, _ core.Device, parent core.RatingKey, _ frontend.ItemQuery) (core.Page, error) {
		if parent == artistKey {
			return core.Page{Items: []core.Title{album, concert}, Total: 2, TotalExact: true}, nil
		}
		return core.Page{}, nil
	}
	exp.ItemFunc = func(_ context.Context, req frontend.ItemRequest) (frontend.ItemDetail, error) {
		switch req.Key {
		case artistKey:
			return frontend.ItemDetail{Title: artist}, nil
		case concertKey:
			canPlay := true
			return frontend.ItemDetail{Title: concert, CanPlay: &canPlay}, nil
		default:
			return frontend.ItemDetail{}, core.ErrNotFound
		}
	}

	inst, err := (Jellyfin{MachineID: "test"}).Mount(context.Background(), frontend.Host{
		Identity:   ident,
		Experience: exp,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Listing children of MusicArtist includes MusicAlbum (folder) and Video (playable video leaf).
	req := httptest.NewRequest(http.MethodGet, "/Items?ParentId="+artistGUID+"&Fields=ParentId", nil)
	req.Header.Set("X-Emby-Token", "grant")
	w := httptest.NewRecorder()
	inst.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("/Items?ParentId=artist status = %d %s", w.Code, w.Body.String())
	}
	var list struct {
		Items []map[string]any `json:"Items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("artist children len = %d, want 2: %+v", len(list.Items), list.Items)
	}
	vid := list.Items[1]
	if vid["Type"] != "Video" || vid["MediaType"] != "Video" || vid["IsFolder"] != false || vid["ParentId"] != artistGUID {
		t.Fatalf("video child DTO = %+v, want Type=Video MediaType=Video IsFolder=false ParentId=%s", vid, artistGUID)
	}

	// 2. Direct detail lookup on the Video item preserves Type=Video, MediaType=Video, ParentId=artistGUID.
	userGUID, err := ids.GUID(ids.User, 1)
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/Users/"+userGUID+"/Items/"+concertGUID, nil)
	req.Header.Set("X-Emby-Token", "grant")
	w = httptest.NewRecorder()
	inst.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("/Users/1/Items/concert status = %d %s", w.Code, w.Body.String())
	}
	var detail map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail["Type"] != "Video" || detail["MediaType"] != "Video" || detail["IsFolder"] != false || detail["ParentId"] != artistGUID {
		t.Fatalf("concert detail DTO = %+v", detail)
	}
}
