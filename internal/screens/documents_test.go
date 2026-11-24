package screens

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plugins"
)

type detailHub struct {
	*fakeHub
	spoilerFree bool
}

func (d detailHub) Detail(_ context.Context, _ *core.User, id string) (*core.LiveEventDetail, error) {
	for _, ev := range d.events {
		if ev.ID == id {
			return &core.LiveEventDetail{LiveEvent: ev, Plays: []core.LiveEventPlay{
				{Period: 1, Text: "Goal", At: "2026-11-24T00:10:00Z", Score: map[string]string{"tor": "1", "mtl": "0"}},
				{Period: 2, Text: "Goal", At: "2026-11-24T00:40:00Z", Score: map[string]string{"tor": "2", "mtl": "1"}},
			}}, nil
		}
	}
	return nil, core.ErrNotFound
}

func (d detailHub) SpoilerFree(context.Context, *core.User) (bool, error) { return d.spoilerFree, nil }

func sportsDocsRecord(t *testing.T) (plugins.Record, map[string][]byte) {
	t.Helper()
	files := map[string][]byte{}
	for path, fixture := range map[string]string{"screens/sports.json": "sports-index", "screens/game.json": "sports-game"} {
		raw, err := os.ReadFile("../plugins/screendoc/testdata/" + fixture + ".json")
		if err != nil {
			t.Fatal(err)
		}
		files[path] = raw
	}
	scorebug := `{"schema":1,"kind":"screen","renderer":19,"sources":{"game":{"from":"liveEvent","feed":"sports","id":"{context.event}"}},"tree":{"type":"box"}}`
	files["surfaces/scorebug.json"] = []byte(scorebug)
	man := plugins.Manifest{ID: "org.ether.sports", Version: "2.0.0", Name: "Sports", Ether: &plugins.EtherBlock{
		Capabilities: []plugins.Capability{plugins.CapLiveEvents, plugins.CapScreens},
		Contributes: plugins.Contributes{
			LiveFeeds: []plugins.LiveFeedMeta{{ID: "sports", Title: "Sports", MinIntervalSec: 15, EntityKinds: []string{"team"}}},
			Screens: []plugins.ScreenMeta{{ID: "sports", Title: "Sports", Icon: "trophy",
				Pages: map[string]plugins.DocumentPaths{"index": {"screens/sports.json"}, "game": {"screens/game.json"}}}},
			Surfaces: []plugins.SurfaceMeta{{ID: "scorebug", Presentation: "playerOverlay", Title: "Score", Anchor: "topLeft",
				Setting: &plugins.SurfaceSetting{ID: "scorebug", Label: "Score", Default: true},
				When:    &plugins.SurfaceWhen{Event: &plugins.SurfaceWhenEvent{Feed: "sports"}}, Document: plugins.DocumentPaths{"surfaces/scorebug.json"}}},
		},
	}}
	return plugins.Record{ID: man.ID, Version: man.Version, Enabled: true, Manifest: man}, files
}

func TestDocumentScreensServeAndReadSportsData(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 24, 1, 0, 0, 0, time.UTC)
	reg := NewDocRegistry(DocRegistryOptions{})
	rec, files := sportsDocsRecord(t)
	if err := reg.Sync(rec, files, nil); err != nil {
		t.Fatal(err)
	}
	hub := &fakeHub{events: []core.LiveEvent{
		{ID: "org.ether.sports:sports:g1", Feed: "sports", League: core.LiveEventLeague{ID: "nhl", Name: "NHL"}, Start: "2026-11-24T00:00:00Z",
			Status:      core.LiveEventStatus{State: "in", Detail: "2nd · 4:40"},
			Competitors: []core.LiveEventCompetitor{{ID: "mtl", Name: "Montréal Canadiens", Score: "1", EntityID: "mtl"}, {ID: "tor", Name: "Toronto Maple Leafs", Score: "2", EntityID: "tor", Home: true}},
			Probability: &core.LiveEventProbability{Value: 0.82, CompetitorID: "tor"}},
		{ID: "org.ether.sports:sports:g2", Feed: "sports", League: core.LiveEventLeague{ID: "nba", Name: "NBA"}, Start: "2026-11-24T03:00:00Z",
			Status: core.LiveEventStatus{State: "pre"}},
	}}
	svc := NewService(ServiceOptions{Hub: detailHub{fakeHub: hub}, Documents: reg, Now: func() time.Time { return now }})
	viewer := &core.User{ID: 7}
	client := ClientInfo{Frontend: "vela", Renderer: 19, Locale: "en"}

	nav := reg.Navigation(ctx, viewer, client)
	if len(nav) != 1 || nav[0].ID != "sports" || nav[0].Page != "index" || nav[0].Document.Renderer != 19 || !strings.Contains(nav[0].Document.Href, "/vela/v0/screens/sports/pages/index/document?") {
		t.Fatalf("navigation = %+v", nav)
	}
	if got := reg.Navigation(ctx, viewer, ClientInfo{Frontend: "vela", Renderer: 18}); len(got) != 0 {
		t.Fatalf("an older renderer must see no document screen, got %+v", got)
	}
	surfaces := reg.SurfaceEntries(ctx, viewer, client, nil)
	if len(surfaces) != 1 || surfaces[0].ID != "sports.scorebug" || surfaces[0].When.Event.Feed != "org.ether.sports:sports" || !surfaces[0].Setting.Enabled {
		t.Fatalf("surfaces = %+v", surfaces)
	}
	body, tag, err := reg.Document(ctx, viewer, DocumentRequest{Kind: "screen", ID: "sports", Page: "index", ClientInfo: client})
	if err != nil || tag == "" {
		t.Fatalf("document: %v", err)
	}
	var served map[string]any
	if err := json.Unmarshal(body, &served); err != nil || served["screen"] != "sports" || served["presentation"] != "section" {
		t.Fatalf("served document head = %v %v", served["screen"], served["presentation"])
	}

	data, err := svc.Data(ctx, viewer, DataRequest{Kind: "screen", ID: "sports", Page: "index", TZ: "America/Toronto", ClientInfo: client})
	if err != nil {
		t.Fatal(err)
	}
	if data.Live == nil || data.Rev == 0 {
		t.Fatalf("missing live topic or rev: %+v", data)
	}
	if _, lazy := data.Sources["standings"]; lazy {
		t.Fatal("lazy source read on first paint")
	}
	live, _ := json.Marshal(data.Sources["live"])
	if !strings.Contains(string(live), `"$record":"event:org.ether.sports:sports:g1"`) || strings.Contains(string(live), "g2") {
		t.Fatalf("live source = %s", live)
	}
	rec1 := data.Records["event:org.ether.sports:sports:g1"]
	view, ok := rec1.Value.(EventView)
	if !ok || view.Phase != "live" || len(view.Sides) != 2 || view.Probability == nil || view.Probability.Home != 0.82 || view.Probability.SubjectSide != 1 || *view.Sides[1].Share != 82 {
		t.Fatalf("event view = %+v", rec1.Value)
	}

	// The game page reads summary and detail as two records.
	gp, err := svc.Data(ctx, viewer, DataRequest{Kind: "screen", ID: "sports", Page: "game", Params: map[string]string{"event": "event:org.ether.sports:sports:g1"}, ClientInfo: client})
	if err != nil {
		t.Fatal(err)
	}
	detail, ok := gp.Records["detail:org.ether.sports:sports:g1"].Value.(DetailView)
	if !ok || len(detail.Plays) != 2 || detail.PlaysNewest[0].Period != 2 {
		t.Fatalf("detail record = %+v", gp.Records["detail:org.ether.sports:sports:g1"])
	}

	// A surface reads its context; a foreign event is not readable.
	if _, err := svc.Data(ctx, viewer, DataRequest{Kind: "surface", ID: "sports.scorebug", Params: map[string]string{"event": "event:org.other:sports:x"}, ClientInfo: client}); err != nil {
		t.Fatal(err)
	} else if res, _ := svc.Data(ctx, viewer, DataRequest{Kind: "surface", ID: "sports.scorebug", Params: map[string]string{"event": "event:org.other:sports:x"}, ClientInfo: client}); res.Errors["game"].Code != "not_found" {
		t.Fatalf("foreign event read = %+v", res)
	}
	if _, err := svc.Data(ctx, viewer, DataRequest{Kind: "screen", ID: "sports", Page: "index", Params: map[string]string{"nope": "1"}, ClientInfo: client}); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("unknown parameter = %v", err)
	}

	// The topic reopens exactly this read, for this viewer only.
	tp, err := svc.OpenTopic(viewer, data.Live.Topic)
	if err != nil || tp.Page != "index" || tp.TZ != "America/Toronto" {
		t.Fatalf("topic = %+v %v", tp, err)
	}
	if _, err := svc.OpenTopic(&core.User{ID: 8}, data.Live.Topic); !errors.Is(err, ErrInvalidTopic) {
		t.Fatalf("another viewer opened the topic: %v", err)
	}

	// Revisions are stable while content is unchanged.
	again, _ := svc.Data(ctx, viewer, DataRequest{Kind: "screen", ID: "sports", Page: "index", TZ: "America/Toronto", ClientInfo: client})
	if again.Records["event:org.ether.sports:sports:g1"].Rev != rec1.Rev {
		t.Fatal("unchanged record changed revision")
	}
}
