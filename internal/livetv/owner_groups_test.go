package livetv

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
)

// The invariant is membership rather than duplicated channels: ordered overlaps,
// rename-stable identity, and viewer visibility hold at the same boundary.
func TestOwnerGroupMembership(t *testing.T) {
	ctx := context.Background()
	s := testService(t, Options{})
	for _, c := range []Channel{
		{ID: "news", Name: "News", Number: "1", Kind: "iptv", Enabled: true, SourceID: "s", Group: "News", StreamURL: "https://example.invalid/news"},
		{ID: "kids", Name: "Kids", Number: "2", Kind: "iptv", Enabled: true, SourceID: "s", Group: "Kids", StreamURL: "https://example.invalid/kids"},
		{ID: "off", Name: "Off", Number: "3", Kind: "iptv", Enabled: false, SourceID: "s", Group: "News", StreamURL: "https://example.invalid/off"},
	} {
		if _, err := s.SaveChannel(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	gs := []OwnerGroup{
		{ID: "family", Name: "Family", Groups: []OwnerGroupSource{{SourceID: "s", Name: "News"}, {SourceID: "s", Name: "Kids"}}},
		{ID: "news", Name: "News", Groups: []OwnerGroupSource{{SourceID: "s", Name: "News"}}},
		{ID: "hidden", Name: "Hidden", All: true, Hidden: true},
	}
	if err := s.SaveOwnerGroups(ctx, gs); err != nil {
		t.Fatal(err)
	}
	host := liveTVHost{s: s}
	viewer := &core.User{ID: 1, IsOwner: true}
	cs, err := host.Channels(ctx, viewer)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 {
		t.Fatalf("visible channels: %+v", cs)
	}
	for _, c := range cs {
		if c.ID == "news" && !reflect.DeepEqual(c.Groups, []frontend.LiveGroup{{ID: "family", Name: "Family"}, {ID: "news", Name: "News"}}) {
			t.Fatalf("memberships: %+v", c.Groups)
		}
	}
	selected, err := host.GroupChannels(ctx, viewer, "news")
	if err != nil || len(selected) != 1 || selected[0].ID != "news" {
		t.Fatalf("group: %+v %v", selected, err)
	}
	gs[0].Name = "Renamed"
	gs[0], gs[1] = gs[1], gs[0]
	if err := s.SaveOwnerGroups(ctx, gs); err != nil {
		t.Fatal(err)
	}
	groups, err := host.Groups(ctx, viewer)
	if err != nil || len(groups) != 2 || groups[1].ID != "family" || groups[1].Name != "Renamed" {
		t.Fatalf("renamed order: %+v %v", groups, err)
	}
	if _, err := host.GroupChannels(ctx, viewer, "hidden"); err == nil {
		t.Fatal("hidden group exposed")
	}
}

func TestOwnerGroupsEndpointsAndMigration(t *testing.T) {
	ctx := context.Background()
	s := testService(t, Options{})
	for _, c := range []Channel{
		{ID: "c1", Name: "BBC One", Number: "101", Kind: "iptv", Enabled: true, SourceID: "uk", Group: "Entertainment", StreamURL: "https://example.invalid/1"},
		{ID: "c2", Name: "Sky Sports", Number: "102", Kind: "iptv", Enabled: true, SourceID: "uk", Group: "Sports", StreamURL: "https://example.invalid/2"},
	} {
		if _, err := s.SaveChannel(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}

	// Empty list returns JSON [] (never null).
	if rec := call(http.MethodGet, "/groups", ""); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("empty GET /groups: %d %q", rec.Code, rec.Body.String())
	}

	// PUT /groups accepts W1's ProviderGroupRef extra UI fields (sourceName, count) and returns OwnerGroup[].
	putBody := `[
		{"id":"sports","name":"Sports","groups":[{"sourceId":"uk","name":"Sports","sourceName":"UK","count":1}],"hiddenApps":["emby"]},
		{"id":"ent","name":"Entertainment","channels":["c1"]}
	]`
	rec := call(http.MethodPut, "/groups", putBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /groups: %d %s", rec.Code, rec.Body.String())
	}
	var list []OwnerGroup
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list) != 2 {
		t.Fatalf("unmarshal PUT /groups: %v %+v", err, list)
	}
	if list[0].ID != "sports" || list[0].ChannelCount != 1 || list[0].Apps["emby"] != false {
		t.Fatalf("unexpected sports group: %+v", list[0])
	}

	// POST /groups appends, PUT /groups/order reorders, PATCH /groups/{id}/apps updates app map, DELETE removes.
	rec = call(http.MethodPost, "/groups", `{"name":"All Live","all":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /groups: %d %s", rec.Code, rec.Body.String())
	}
	var createEnv struct {
		Group  OwnerGroup   `json:"group"`
		Groups []OwnerGroup `json:"groups"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &createEnv)
	created := createEnv.Group
	if created.ID == "" || created.ChannelCount != 2 || len(createEnv.Groups) != 3 {
		t.Fatalf("created group: %+v", createEnv)
	}
	if rec = call(http.MethodPut, "/groups/order", `{"ids":["ent","sports","`+created.ID+`"]}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT /groups/order: %d %s", rec.Code, rec.Body.String())
	}
	if rec = call(http.MethodPatch, "/groups/sports/apps", `{"plex":false}`); rec.Code != http.StatusOK {
		t.Fatalf("PATCH /groups/sports/apps: %d %s", rec.Code, rec.Body.String())
	}
	if rec = call(http.MethodDelete, "/groups/"+created.ID, ""); rec.Code != http.StatusOK {
		t.Fatalf("DELETE /groups/{id}: %d %s", rec.Code, rec.Body.String())
	}

	// Migration v24 strips hubGroups from livetv_sources JSON cleanly.
	tx, err := s.o.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO livetv_sources(id, data) VALUES('src1', '{"id":"src1","name":"Src","hubGroups":true}')`); err != nil {
		t.Fatal(err)
	}
	if err := migrateOwnerGroupsV25(ctx, tx); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT data FROM livetv_sources WHERE id='src1'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "hubGroups") {
		t.Fatalf("hubGroups was not stripped by migration: %s", raw)
	}
}

func TestOwnerGroupDecisionALateProviderChannel(t *testing.T) {
	ctx := context.Background()
	s := testService(t, Options{})
	if _, err := s.SaveChannel(ctx, Channel{
		ID: "sky-main", Name: "Sky Sports Main", Number: "401", Kind: "iptv",
		Enabled: true, SourceID: "uk", Group: "Sports", StreamURL: "https://example.invalid/401",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveOwnerGroups(ctx, []OwnerGroup{{
		ID: "sports", Name: "Sports",
		Groups: []OwnerGroupSource{{SourceID: "uk", Name: "Sports"}},
		Apps:   map[string]bool{"plex": false},
	}}); err != nil {
		t.Fatal(err)
	}

	// Provider later imports a brand new channel in ("uk", "Sports").
	if _, err := s.SaveChannel(ctx, Channel{
		ID: "sky-f1", Name: "Sky Sports F1", Number: "406", Kind: "iptv",
		Enabled: true, SourceID: "uk", Group: "Sports", StreamURL: "https://example.invalid/406",
	}); err != nil {
		t.Fatal(err)
	}

	viewer := &core.User{ID: 1, IsOwner: true}
	plexChans, err := s.AllowedChannels(core.WithLiveApp(ctx, "plex"), viewer)
	if err != nil {
		t.Fatal(err)
	}
	if len(plexChans) != 0 {
		t.Fatalf("late provider channel leaked into plex despite group hide: %+v", plexChans)
	}
	jfChans, err := s.AllowedChannels(core.WithLiveApp(ctx, "jellyfin"), viewer)
	if err != nil || len(jfChans) != 2 {
		t.Fatalf("jellyfin should still see both sports channels: %+v %v", jfChans, err)
	}

	// Explicit per-channel show override via PUT /channels/{id} wins over the group hide.
	body := bytes.NewBufferString(`{"id":"sky-f1","name":"Sky Sports F1","number":"406","kind":"iptv","enabled":true,"sourceId":"uk","group":"Sports","streamUrl":"https://example.invalid/406","hiddenApps":[]}`)
	req := httptest.NewRequest(http.MethodPut, "/channels/sky-f1", body)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /channels/sky-f1: %d %s", rec.Code, rec.Body.String())
	}
	plexChans, err = s.AllowedChannels(core.WithLiveApp(ctx, "plex"), viewer)
	if err != nil || len(plexChans) != 1 || plexChans[0].ID != "sky-f1" {
		t.Fatalf("explicit channel show override failed: %+v %v", plexChans, err)
	}
	summaries, err := s.ChannelSummaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, sum := range summaries {
		if sum.ID == "sky-main" && !slices.Contains(sum.HiddenApps, "plex") {
			t.Fatalf("sky-main summary should report effective plex hide: %+v", sum)
		}
		if sum.ID == "sky-f1" && (slices.Contains(sum.HiddenApps, "plex") || !slices.Contains(sum.ShownApps, "plex")) {
			t.Fatalf("sky-f1 summary should report shownApps=[plex] and not hidden: %+v", sum)
		}
	}
}
