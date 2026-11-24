package pluginitems

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plugins"
)

// A list level (a playlist) is walked as a root of its own; its page brings
// its members with their parents, and its members come back in its order.
func TestListLevelSyncStoresMembersInOrder(t *testing.T) {
	ctx := context.Background()
	s, _ := openTestService(t, Options{})
	defer s.Close()
	const pluginID, kindKey, libID = "test.music", "music", core.LibraryID(92)
	decl := branchingMusicDecl()
	decl.Levels = append(decl.Levels, plugins.KindLevel{Key: "playlist", Label: "Playlist", Role: "playlist", Container: true, Members: true})
	if _, err := s.UpsertKind(ctx, pluginID, libID, decl, false); err != nil {
		t.Fatal(err)
	}
	members := []string{"trk-2", "trk-missing", "trk-1"}
	caller := callerFunc(func(_ context.Context, _ string, req plugins.Request) (plugins.Response, error) {
		if req.Path != "/ether/v1/kinds/music/browse" {
			return plugins.Response{Status: http.StatusNotFound}, nil
		}
		var br core.KindBrowseRequest
		_ = json.Unmarshal(req.Body, &br)
		var items []core.KindItem
		switch {
		case br.Level == "artist":
			items = []core.KindItem{{ID: "art-1", Level: "artist", Title: "Seed"}}
		case br.Level == "playlist":
			items = []core.KindItem{
				{ID: "art-2", Level: "artist", Title: "Guest"},
				{ID: "alb-9", Level: "album", ParentID: "art-2", Title: "Guest Album"},
				{ID: "trk-1", Level: "track", ParentID: "alb-9", Title: "First", Playable: true},
				{ID: "trk-2", Level: "track", ParentID: "alb-9", Title: "Second", Playable: true},
				{ID: "pl-1", Level: "playlist", Title: "Top Worldwide", Members: members},
			}
		}
		body, _ := json.Marshal(core.KindPage{Items: items})
		return plugins.Response{Status: http.StatusOK, Body: body}, nil
	})
	if _, err := s.SyncKind(ctx, pluginID, kindKey, SyncOptions{Caller: caller}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	ref := core.NewItemRef(pluginID, kindKey, "pl-1")
	got, total, err := s.MembersPage(ctx, ref, 0, 10)
	if err != nil || total != 3 || len(got) != 2 || got[0].ID != "trk-2" || got[1].ID != "trk-1" {
		t.Fatalf("members: %+v total=%d err=%v", got, total, err)
	}
	// The list names its members again in a new order on the next walk.
	members = []string{"trk-1", "trk-2"}
	if _, err := s.SyncKind(ctx, pluginID, kindKey, SyncOptions{Caller: caller, FullWalk: true}); err != nil {
		t.Fatal(err)
	}
	if got, total, _ = s.MembersPage(ctx, ref, 0, 10); total != 2 || got[0].ID != "trk-1" {
		t.Fatalf("reordered members: %+v %d", got, total)
	}
	// A list item cannot name a parent, and only a list level carries members.
	if _, err := ValidateItem(decl, core.KindItem{ID: "pl-2", Level: "playlist", ParentID: "art-1", Title: "x"}, "artist"); err == nil {
		t.Fatal("a list item with a parent was accepted")
	}
	if _, err := ValidateItem(decl, core.KindItem{ID: "alb-3", Level: "album", ParentID: "art-1", Title: "x", Members: []string{"trk-1"}}, "artist"); err == nil {
		t.Fatal("members on a non-list level were accepted")
	}
}
