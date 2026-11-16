package resolver

import (
	"context"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/episodeorder"
	"github.com/braedonsaunders/ether/internal/releaseparse"
	"github.com/braedonsaunders/ether/internal/store"
	"github.com/braedonsaunders/ether/internal/tmdb"
)

type orderSearchSource struct{ calls int }

func (*orderSearchSource) EpisodeGroups(context.Context, int64) ([]tmdb.EpisodeGroupSummary, error) {
	return nil, nil
}
func (s *orderSearchSource) EpisodeGroup(context.Context, string) (*tmdb.EpisodeGroup, error) {
	s.calls++
	return &tmdb.EpisodeGroup{Groups: []tmdb.EpisodeSubgroup{{Order: 1, Episodes: []tmdb.OrderedEpisode{{Episode: tmdb.Episode{SeasonNumber: 1, EpisodeNumber: 28}, Order: 0}}}}}, nil
}

func TestOwnerEpisodeOrderSearchNumbering(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	source := &orderSearchSource{}
	orders := episodeorder.New(db, source, nil)
	svc := &Service{episodeNumbering: orders.SearchNumbering}
	title := &core.Title{Key: core.EpisodeKey(10, 1, 28), Type: core.TypeEpisode, ShowKey: core.ShowKey(10), ShowTitle: "Show", Season: 1, Episode: 28}
	canonical, alternate := releaseparse.Parse("Show S01E28 1080p"), releaseparse.Parse("Show S02E01 1080p")
	checkUnmapped := func() {
		t.Helper()
		req := svc.searchRequest(ctx, title)
		if req.AltEpisode != 0 || episodeMatchesRequest(alternate, req) || !episodeMatchesRequest(canonical, req) {
			t.Fatalf("unexpected default numbering: %+v", req)
		}
		if source.calls != 0 {
			t.Fatal("fetched group without a show default")
		}
	}
	checkUnmapped()
	// Viewer overrides must never influence release identity.
	if _, err = db.ExecContext(ctx, `INSERT INTO episodeorder_preferences(user_id,show_key,group_id) VALUES(9,?,'dvd')`, int64(title.ShowKey)); err != nil {
		t.Fatal(err)
	}
	checkUnmapped()
	if _, err = db.ExecContext(ctx, `INSERT INTO episodeorder_preferences(user_id,show_key,group_id) VALUES(0,?,'dvd')`, int64(title.ShowKey)); err != nil {
		t.Fatal(err)
	}
	req := svc.searchRequest(ctx, title)
	if req.AltSeason != 2 || req.AltEpisode != 1 || !episodeMatchesRequest(alternate, req) || !episodeMatchesRequest(canonical, req) {
		t.Fatalf("alternate numbering did not preserve both identities: %+v", req)
	}
	if !episodeMatchesRequest(releaseparse.Parse("Show - 28 1080p"), req) {
		t.Fatal("absolute numbering stopped matching")
	}
	pack := core.Release{ID: "pack", Protocol: core.ProtocolTorrent, Info: releaseparse.Parse("Show S02 Complete 1080p")}
	if !packCoversSeasonCour(pack.Info, req) {
		t.Fatal("alternate season pack rejected")
	}
	target := withTarget(pack, req).Target
	if target == nil || target.Season != 2 || target.Episode != 1 {
		t.Fatalf("wrong alternate pack file target: %+v", target)
	}
	_ = svc.searchRequest(ctx, title)
	if source.calls != 1 {
		t.Fatalf("group cache not reused: %d upstream calls", source.calls)
	}
}
