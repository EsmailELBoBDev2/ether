package resolver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/episodeorder"
	"github.com/braedonsaunders/ether/internal/releaseparse"
	"github.com/braedonsaunders/ether/internal/scenenumbering"
	"github.com/braedonsaunders/ether/internal/store"
)

func TestAutomaticReleaseNumbering(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("origin") != "tvdb" {
			t.Error("wrong XEM origin")
		}
		if r.URL.Query().Get("id") == "777" {
			_, _ = w.Write([]byte(`{"result":"success","data":[{"tvdb":{"season":2,"episode":1,"absolute":28},"scene":{"season":2,"episode":1,"absolute":28}}]}`))
		} else {
			_, _ = w.Write([]byte(`{"result":"failure","data":[],"message":"No mapping available"}`))
		}
	}))
	defer server.Close()
	scene := scenenumbering.New(scenenumbering.Options{URL: server.URL, Client: server.Client()})
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	orders := episodeorder.New(db, nil, nil)
	indexer := &fakeIndexer{}
	svc := New(indexer, nil, db, testCfg(), Options{EpisodeNumbering: func(ctx context.Context, req core.SearchRequest, cachedOnly bool) (int, int, error) {
		p, err := orders.ReleasePreference(ctx, core.ShowKey(req.IDs.TMDB))
		if err != nil || p.Mode == "tmdb" {
			return 0, 0, err
		}
		if cachedOnly {
			return scene.LookupCached(req)
		}
		return scene.Lookup(ctx, req)
	}})
	defer svc.Close()
	title := &core.Title{Key: core.EpisodeKey(10, 1, 28), Type: core.TypeEpisode, ShowKey: core.ShowKey(10), ShowTitle: "Show", Season: 1, Episode: 28, ShowIDs: core.IDs{TVDB: 777}}
	// Display preferences never select release numbering.
	if _, err := db.ExecContext(ctx, `INSERT INTO episodeorder_preferences(user_id,show_key,group_id) VALUES(0,?,'display-only')`, int64(title.ShowKey)); err != nil {
		t.Fatal(err)
	}
	canonical := releaseparse.Parse("Show S01E28 1080p")
	alternate := releaseparse.Parse("Show S02E01 1080p")
	// Canonical results settle discovery without loading XEM.
	indexer.releases = []core.Release{{ID: "canonical", Name: "Show S01E28 1080p"}}
	req := svc.searchRequest(ctx, title)
	if _, _, err := svc.searchWithNumbering(ctx, &req, true); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("canonical discovery fetched XEM")
	}
	// A returned alternate result can be matched without another indexer call.
	indexer.releases = []core.Release{{ID: "alternate", Name: "Show S02E01 1080p"}}
	req = svc.searchRequest(ctx, title)
	before := indexer.searches.Load()
	if _, _, err := svc.searchWithNumbering(ctx, &req, true); err != nil {
		t.Fatal(err)
	}
	if req.AltSeason != 2 || req.AltEpisode != 1 || !episodeMatchesRequest(alternate, req) || !episodeMatchesRequest(canonical, req) || !episodeMatchesRequest(releaseparse.Parse("Show - 28 1080p"), req) {
		t.Fatalf("lost episode identity: %+v", req)
	}
	if indexer.searches.Load() != before+1 {
		t.Fatal("matching spent another indexer call")
	}
	pack := core.Release{ID: "pack", Protocol: core.ProtocolTorrent, Info: releaseparse.Parse("Show S02 Complete 1080p")}
	if !packCoversSeasonCour(pack.Info, req) {
		t.Fatal("mapped pack rejected")
	}
	target := withTarget(pack, req).Target
	if target == nil || target.Season != 2 || target.Episode != 1 || target.CanonicalSeason != 1 || target.CanonicalEpisode != 28 {
		t.Fatalf("wrong pack target: %+v", target)
	}
	// A different mapping enables alternate discovery only when needed.
	indexer.releases = nil
	req = svc.searchRequest(ctx, title)
	before = indexer.searches.Load()
	if _, _, err := svc.searchWithNumbering(ctx, &req, true); err != nil {
		t.Fatal(err)
	}
	if req.AltEpisode != 1 || indexer.searches.Load() != before+2 {
		t.Fatal("mapped episode did not enable alternate search")
	}
	indexer.releases = []core.Release{{ID: "alternate", Name: "Show S02E01 1080p"}}
	// No entry means no alternate search; the negative answer is cached.
	title.ShowIDs.TVDB = 888
	for range 2 {
		req = svc.searchRequest(ctx, title)
		before = indexer.searches.Load()
		if _, _, err := svc.searchWithNumbering(ctx, &req, true); err != nil {
			t.Fatal(err)
		}
		if req.AltEpisode != 0 || indexer.searches.Load() != before+1 {
			t.Fatal("no entry caused alternate search")
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("per-show cache not reused: %d", calls.Load())
	}
	title.ShowIDs.TVDB = 777
	if err := orders.SaveReleasePreference(ctx, title.ShowKey, episodeorder.ReleasePreference{Mode: "tmdb"}); err != nil {
		t.Fatal(err)
	}
	req = svc.searchRequest(ctx, title)
	if _, _, err := svc.searchWithNumbering(ctx, &req, true); err != nil {
		t.Fatal(err)
	}
	if req.AltEpisode != 0 || calls.Load() != 2 {
		t.Fatal("TMDB override did not disable mapping")
	}
	// Shows without TVDB ids and detail/background requests never fetch XEM.
	if err := orders.SaveReleasePreference(ctx, title.ShowKey, episodeorder.ReleasePreference{Mode: "automatic"}); err != nil {
		t.Fatal(err)
	}
	title.ShowIDs.TVDB = 0
	req = svc.searchRequest(ctx, title)
	_, _, _ = svc.searchWithNumbering(ctx, &req, true)
	title.ShowIDs.TVDB = 999
	req = svc.searchRequest(ctx, title)
	_, _, _ = svc.searchWithNumbering(core.WithIndexerPurpose(ctx, core.IndexerPurposeDetail), &req, true)
	if calls.Load() != 2 {
		t.Fatal("detail or id-less request fetched XEM")
	}
}
