package liveevents

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/livetv"
	"github.com/braedonsaunders/ether/internal/store"
)

type fakeGuideSource struct {
	rev           string
	candidates    []GuideCandidate
	currentByChan map[string]*GuideCandidate
	progKeyByChan map[string]string
	queryCalls    atomic.Int32
}

func (f *fakeGuideSource) GuideRevision(_ context.Context, u *core.User) (string, error) {
	if u == nil {
		return "denied", nil
	}
	return f.rev, nil
}

func (f *fakeGuideSource) WatchCandidates(_ context.Context, _ *core.User, _, _ time.Time, _ []string) ([]GuideCandidate, string, error) {
	f.queryCalls.Add(1)
	return f.candidates, f.rev, nil
}

func (f *fakeGuideSource) CurrentProgramme(_ context.Context, _ *core.User, channelID string, _ time.Time) (*GuideCandidate, string, error) {
	f.queryCalls.Add(1)
	return f.currentByChan[channelID], f.progKeyByChan[channelID], nil
}

type fakeActiveEvents struct {
	events []core.LiveEvent
	rev    string
}

func (f *fakeActiveEvents) ActiveEvents(_ context.Context, _ *core.User, _ time.Time) ([]core.LiveEvent, string, error) {
	return f.events, f.rev, nil
}

func TestNormalizeTokensStripsConnectorsAndPunctuation(t *testing.T) {
	got := normalizeTokens("North City vs. South Bay @ Stadium!")
	want := []string{"north", "city", "south", "bay", "stadium"}
	if len(got) != len(want) {
		t.Fatalf("normalizeTokens = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("token[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	gotAt := normalizeTokens("NTH at STH (Live)")
	wantAt := []string{"nth", "sth", "live"}
	if len(gotAt) != len(wantAt) {
		t.Fatalf("normalizeTokens(at) = %v, want %v", gotAt, wantAt)
	}
}

func TestWatchScoringChannelAddonPluginAndFalsePositives(t *testing.T) {
	now := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
	ev := core.LiveEvent{
		ID:       "p1:main:e1",
		Feed:     "main",
		Category: "fieldball",
		League:   core.LiveEventLeague{ID: "l1", Name: "Premier League", Abbr: "PL"},
		Title:    "North Wolves at South Hawks",
		Start:    now.UTC().Format(time.RFC3339),
		Status:   core.LiveEventStatus{State: core.LiveEventStateIn},
		Competitors: []core.LiveEventCompetitor{
			{ID: "away", Name: "North Wolves", Short: "Wolves", Abbr: "NW"},
			{ID: "home", Name: "South Hawks", Short: "Hawks", Abbr: "SH", Home: true},
		},
		Broadcasts: []core.LiveEventBroadcast{
			{Name: "Net 2", Kind: "tv"},
			{Name: "Radio Net", Kind: "radio"},
		},
		MatchHints: &core.LiveEventMatchHints{
			Terms:     []string{"North Wolves", "Wolves", "NW", "South Hawks", "Hawks", "SH"},
			Callsigns: []string{"Net 2"},
		},
	}

	cands := []GuideCandidate{
		{
			ChannelID: "ch-exact", ChannelName: "US: Net 2 HD", ChannelNumber: "202",
			Title: "North Wolves vs South Hawks", Genres: []string{"Sports"}, Live: true,
		},
		{
			// Wrong numbered channel ("Net 1" != "Net 2"), generic title -> must NOT match.
			ChannelID: "ch-wrong-num", ChannelName: "US: Net 1 HD", ChannelNumber: "201",
			Title: "Live Sports Center", Genres: []string{"Sports"}, Live: true,
		},
		{
			// Non-sports show on broadcast channel -> must NOT match.
			ChannelID: "ch-sitcom", ChannelName: "Net 2", ChannelNumber: "203",
			Title: "Evening News", Genres: []string{"News"}, Live: true,
		},
		{
			// Single ambiguous team name on non-sports documentary -> must NOT match.
			ChannelID: "ch-doc", ChannelName: "Nature Channel", ChannelNumber: "301",
			Title: "Wolves of the Tundra", Genres: []string{"Documentary"}, Live: true,
		},
		{
			// Stremio event channel with BOTH competitors -> matches as kind "addon".
			ChannelID: "ch-addon-ok", ChannelName: "Addon Events", IsEventAddon: true,
			Title: "NW @ SH Live", Live: true,
		},
		{
			// Stremio event channel with only ONE competitor -> must NOT match.
			ChannelID: "ch-addon-one", ChannelName: "Addon Events 2", IsEventAddon: true,
			Title: "Wolves Pre-Match Show", Live: true,
		},
	}

	links := []core.LiveEventLink{
		{Label: "Plugin Stream", URL: "https://stream.example.invalid/e1"},
	}

	targets := matchEventToCandidates(ev, cands, links)
	if len(targets) != 3 {
		t.Fatalf("expected 3 targets (channel, addon, plugin), got %d: %+v", len(targets), targets)
	}
	if targets[0].ChannelID != "ch-exact" || targets[0].Kind != core.WatchTargetKindChannel {
		t.Fatalf("top target = %+v, want ch-exact channel", targets[0])
	}
	if targets[1].ChannelID != "ch-addon-ok" || targets[1].Kind != core.WatchTargetKindAddon {
		t.Fatalf("second target = %+v, want ch-addon-ok addon", targets[1])
	}
	if targets[2].Kind != core.WatchTargetKindPlugin || targets[2].URL != "https://stream.example.invalid/e1" {
		t.Fatalf("third target = %+v, want plugin stream link", targets[2])
	}
}

func TestWatchMatcherCachesPerEventAndGuideRevision(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
	guide := &fakeGuideSource{
		rev: "g1:abc",
		candidates: []GuideCandidate{
			{
				ChannelID: "ch-1", ChannelName: "Alpha Sports HD", ChannelNumber: "10",
				Title: "Red Lions vs Blue Bears", Genres: []string{"Sports"}, Live: true,
			},
		},
	}
	m := NewWatchMatcher(WatchMatcherOptions{
		Guide:    guide,
		Now:      func() time.Time { return now },
		CacheCap: 16,
	})
	viewer := &core.User{ID: 42}
	ev := core.LiveEvent{
		ID:    "p1:main:g1",
		Feed:  "main",
		Start: now.UTC().Format(time.RFC3339),
		Competitors: []core.LiveEventCompetitor{
			{ID: "a", Name: "Red Lions", Abbr: "RL"},
			{ID: "b", Name: "Blue Bears", Abbr: "BB"},
		},
	}

	first, err := m.WatchTargets(ctx, ev, viewer)
	if err != nil || len(first) != 1 || first[0].ChannelID != "ch-1" {
		t.Fatalf("first WatchTargets = (%+v, %v), want ch-1", first, err)
	}
	second, err := m.WatchTargets(ctx, ev, viewer)
	if err != nil || len(second) != 1 {
		t.Fatalf("second WatchTargets = (%+v, %v)", second, err)
	}
	if calls := guide.queryCalls.Load(); calls != 1 {
		t.Fatalf("expected cache hit on same guide revision (1 guide call), got %d", calls)
	}

	// Mutating guide revision invalidates the cached result.
	guide.rev = "g2:def"
	guide.candidates = append(guide.candidates, GuideCandidate{
		ChannelID: "ch-2", ChannelName: "Beta Sports", ChannelNumber: "11",
		Title: "Red Lions at Blue Bears", Genres: []string{"Sports"}, Live: true,
	})
	third, err := m.WatchTargets(ctx, ev, viewer)
	if err != nil || len(third) != 2 {
		t.Fatalf("third WatchTargets after rev bump = (%+v, %v), want 2 targets", third, err)
	}
	if calls := guide.queryCalls.Load(); calls != 2 {
		t.Fatalf("expected 2nd guide call after revision change, got %d", calls)
	}
}

func TestReverseMatchLiveEventForChannelAndCache(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
	ev := core.LiveEvent{
		ID:     "p1:main:e99",
		Feed:   "main",
		Start:  now.UTC().Format(time.RFC3339),
		Status: core.LiveEventStatus{State: core.LiveEventStateIn},
		Competitors: []core.LiveEventCompetitor{
			{ID: "a", Name: "City Owls", Abbr: "OWL"},
			{ID: "b", Name: "Valley Foxes", Abbr: "FOX"},
		},
		Broadcasts: []core.LiveEventBroadcast{{Name: "Apex TV", Kind: "tv"}},
	}

	guide := &fakeGuideSource{
		rev: "g1",
		currentByChan: map[string]*GuideCandidate{
			"ch-apex": {
				ChannelID: "ch-apex", ChannelName: "Apex TV HD",
				Title: "City Owls at Valley Foxes", Genres: []string{"Sports"}, Live: true,
			},
		},
		progKeyByChan: map[string]string{
			"ch-apex": "ch-apex|1000|2000|City Owls at Valley Foxes",
		},
	}
	active := &fakeActiveEvents{events: []core.LiveEvent{ev}, rev: "ev-rev-1"}
	m := NewWatchMatcher(WatchMatcherOptions{
		Guide:        guide,
		ActiveEvents: active,
		Now:          func() time.Time { return now },
	})
	viewer := &core.User{ID: 7}

	key, ref, err := m.LiveEventForChannel(ctx, viewer, "ch-apex")
	if err != nil || ref == nil || ref.ID != "p1:main:e99" || ref.Feed != "main" || key == "" {
		t.Fatalf("LiveEventForChannel = (%q, %+v, %v), want p1:main:e99", key, ref, err)
	}

	// Programme rolls over to a non-matching news show -> returns new key and nil ref.
	guide.currentByChan["ch-apex"] = &GuideCandidate{
		ChannelID: "ch-apex", ChannelName: "Apex TV HD",
		Title: "Late Night News", Genres: []string{"News"}, Live: true,
	}
	guide.progKeyByChan["ch-apex"] = "ch-apex|2000|3000|Late Night News"

	key2, ref2, err := m.LiveEventForChannel(ctx, viewer, "ch-apex")
	if err != nil || ref2 != nil || key2 == key {
		t.Fatalf("after rollover LiveEventForChannel = (%q, %+v, %v), want new key and nil ref", key2, ref2, err)
	}
}

func TestWatchMatcherIntegrationWithLiveTVService(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()

	now := time.Date(2026, 11, 22, 19, 0, 0, 0, time.UTC)
	ltv, err := livetv.New(livetv.Options{DB: db, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("livetv.New: %v", err)
	}
	defer ltv.Close()

	ch := livetv.Channel{
		ID: "ch-sports-1", Number: "40", Name: "Metro Sports 1 HD",
		Kind: "iptv", Enabled: true, SourceID: "s1", GuideID: "metro.1",
		StreamURL: "https://example.invalid/40.m3u8",
	}
	cb, _ := json.Marshal(ch)
	if _, err := db.ExecContext(ctx, `INSERT INTO livetv_channels(id, data) VALUES(?, ?)`, ch.ID, string(cb)); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	prog := livetv.Programme{
		ID:        "metro.1-live",
		ChannelID: "metro.1",
		Start:     now.Add(-10 * time.Minute),
		End:       now.Add(2 * time.Hour),
		Item:      livetv.Item{Title: "Metro Sharks vs Harbor Seals", Genres: []string{"Sports"}},
	}
	pb, _ := json.Marshal(prog)
	if _, err := db.ExecContext(ctx, `INSERT INTO livetv_programmes(source_id, channel_id, start_ms, end_ms, data) VALUES(?,?,?,?,?)`,
		"s1", "metro.1", prog.Start.UnixMilli(), prog.End.UnixMilli(), string(pb)); err != nil {
		t.Fatalf("insert programme: %v", err)
	}

	ev := core.LiveEvent{
		ID:     "demo:feed:m1",
		Feed:   "feed",
		Start:  now.UTC().Format(time.RFC3339),
		Status: core.LiveEventStatus{State: core.LiveEventStateIn},
		Competitors: []core.LiveEventCompetitor{
			{ID: "a", Name: "Metro Sharks", Abbr: "MSH"},
			{ID: "b", Name: "Harbor Seals", Abbr: "HSE"},
		},
		Broadcasts: []core.LiveEventBroadcast{{Name: "Metro Sports 1", Kind: "tv"}},
	}
	matcher := NewWatchMatcher(WatchMatcherOptions{
		Guide:        NewLiveTVGuideSource(ltv),
		ActiveEvents: &fakeActiveEvents{events: []core.LiveEvent{ev}, rev: "r1"},
		Now:          func() time.Time { return now },
	})
	viewer := &core.User{ID: 5}

	targets, err := matcher.WatchTargets(ctx, ev, viewer)
	if err != nil || len(targets) != 1 || targets[0].ChannelID != "ch-sports-1" || !targets[0].Live {
		t.Fatalf("WatchTargets integration = (%+v, %v), want live ch-sports-1", targets, err)
	}

	progKey, ref, err := matcher.LiveEventForChannel(ctx, viewer, "ch-sports-1")
	if err != nil || progKey == "" || ref == nil || ref.ID != ev.ID || ref.Feed != ev.Feed {
		t.Fatalf("LiveEventForChannel integration = (%q, %+v, %v), want %s", progKey, ref, err, ev.ID)
	}
}
