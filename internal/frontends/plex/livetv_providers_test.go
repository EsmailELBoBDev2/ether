package plex

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/livetv"
)

func liveProviderByTitle(t *testing.T, f *fixture, title string) map[string]any {
	t.Helper()
	r := f.request(t, "GET", "/media/providers", map[string]string{"Accept": "application/json"})
	if r.Code != http.StatusOK {
		t.Fatalf("providers %d %s", r.Code, r.Body.String())
	}
	for _, raw := range decodeJSON(t, r)["MediaProvider"].([]any) {
		p := raw.(map[string]any)
		if p["title"] == title {
			return p
		}
	}
	return nil
}

func TestLiveGroupProviders(t *testing.T) {
	f, service, ch := liveFixture(t)
	// A second channel outside the Sports group.
	src := livetv.Source{Name: "Second", Playlist: "#EXTM3U\n#EXTINF:-1 tvg-id=\"two\" tvg-chno=\"8\",Second channel\nhttp://127.0.0.1:9/x.m3u8\n", Guide: `<tv><channel id="two"><display-name>Second channel</display-name></channel><programme channel="two" start="20261018110000 +0000" stop="20261018130000 +0000"><title>Second programme</title></programme></tv>`}
	if _, err := service.ImportSource(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	cs, err := service.Channels(context.Background())
	if err != nil || len(cs) != 2 {
		t.Fatalf("channels %v %+v", err, cs)
	}
	var other livetv.Channel
	for _, c := range cs {
		if c.ID != ch.ID {
			other = c
		}
	}
	groups := []livetv.OwnerGroup{
		{ID: "sports", Name: "Sports", Channels: []string{ch.ID}, Groups: []livetv.OwnerGroupSource{}},
		{ID: "movies", Name: "Movies", Groups: []livetv.OwnerGroupSource{{SourceID: "nope", Name: "Nope"}}},
	}
	if err := service.SaveOwnerGroups(context.Background(), groups); err != nil {
		t.Fatal(err)
	}
	// Main provider stays; Sports becomes its own source; the empty Movies
	// group (no visible channels) does not.
	main := liveProviderByTitle(t, f, "Live TV")
	sports := liveProviderByTitle(t, f, "Sports")
	if main == nil || main["identifier"] != livetv.Provider {
		t.Fatalf("main provider changed: %v", main)
	}
	if sports == nil {
		t.Fatal("Sports group provider missing")
	}
	if liveProviderByTitle(t, f, "Movies") != nil {
		t.Fatal("empty group advertised as a provider")
	}
	ident, _ := sports["identifier"].(string)
	if ident == "" || ident == livetv.Provider || !strings.HasPrefix(ident, "tv.plex.providers.epg.xmltv:") {
		t.Fatalf("group identifier %q", ident)
	}
	path := "/" + ident
	// Lineup scoped to the group.
	r := f.request(t, "GET", path+"/lineups/dvr/channels", map[string]string{"Accept": "application/json"})
	mc := decodeJSON(t, r)
	chs, _ := mc["Channel"].([]any)
	if r.Code != 200 || len(chs) != 1 || chs[0].(map[string]any)["id"] != ch.ID || mc["identifier"] != ident {
		t.Fatalf("group lineup %d %s", r.Code, r.Body.String())
	}
	// Grid scoped: own channel yields its programme under the group path;
	// the other channel yields nothing.
	r = f.request(t, "GET", path+"/grid?date=2026-10-18&channelGridKey="+ch.ID, map[string]string{"Accept": "application/json"})
	gm := decodeJSON(t, r)
	md, _ := gm["Metadata"].([]any)
	if len(md) != 1 || !strings.HasPrefix(md[0].(map[string]any)["key"].(string), path+"/metadata/") {
		t.Fatalf("group grid %d %s", r.Code, r.Body.String())
	}
	r = f.request(t, "GET", path+"/grid?date=2026-10-18&channelGridKey="+other.ID, map[string]string{"Accept": "application/json"})
	if md, _ := decodeJSON(t, r)["Metadata"].([]any); len(md) != 0 {
		t.Fatalf("group grid leaked other channel: %s", r.Body.String())
	}
	// Unknown provider numbers 404 instead of falling back to all channels.
	r = f.request(t, "GET", "/tv.plex.providers.epg.xmltv:424242/grid", map[string]string{"Accept": "application/json"})
	if r.Code != 404 {
		t.Fatalf("unknown provider status %d", r.Code)
	}
	// One DVR per provider; tuning through the group DVR works for its own
	// channel and refuses channels outside the group.
	r = f.request(t, "GET", "/livetv/dvrs", map[string]string{"Accept": "application/json"})
	dvrs, _ := decodeJSON(t, r)["Dvr"].([]any)
	if len(dvrs) != 2 {
		t.Fatalf("dvrs %s", r.Body.String())
	}
	var groupKey string
	for _, raw := range dvrs {
		dv := raw.(map[string]any)
		if dv["epgIdentifier"] == ident {
			groupKey, _ = dv["key"].(string)
		}
	}
	if groupKey == "" || groupKey == "1" {
		t.Fatalf("group DVR key %q in %s", groupKey, r.Body.String())
	}
	r = f.request(t, "POST", "/livetv/dvrs/"+groupKey+"/channels/"+other.ID+"/tune", map[string]string{"Accept": "application/json"})
	if r.Code != 404 {
		t.Fatalf("group DVR tuned outside channel: %d %s", r.Code, r.Body.String())
	}
	r = f.request(t, "POST", "/livetv/dvrs/"+groupKey+"/channels/"+ch.ID+"/tune", map[string]string{"Accept": "application/json"})
	if r.Code != 200 {
		t.Fatalf("group DVR tune %d %s", r.Code, r.Body.String())
	}
	// Renaming the group keeps the provider identifier stable.
	groups[0].Name = "Sport"
	if err := service.SaveOwnerGroups(context.Background(), groups); err != nil {
		t.Fatal(err)
	}
	renamed := liveProviderByTitle(t, f, "Sport")
	if renamed == nil || renamed["identifier"] != ident {
		t.Fatalf("rename changed provider identity: %v want %q", renamed, ident)
	}
}
