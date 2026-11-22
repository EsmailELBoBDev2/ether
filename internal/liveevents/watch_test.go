package liveevents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
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
			// Playable HLS broadcast URL -> allowed as kind "plugin".
			{Name: "Direct Stream", Kind: "stream", URL: "https://stream.example.invalid/e1.m3u8"},
			// Informational ESPN web page -> must NOT appear in watch[].
			{Name: "Commentary", Kind: "stream", URL: "https://www.espn.com/soccer/commentary/_/gameId/123"},
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

	targets := matchEventToCandidates(ev, cands)
	if len(targets) != 3 {
		t.Fatalf("expected 3 targets (channel, addon, playable plugin stream), got %d: %+v", len(targets), targets)
	}
	if targets[0].ChannelID != "ch-exact" || targets[0].Kind != core.WatchTargetKindChannel {
		t.Fatalf("top target = %+v, want ch-exact channel", targets[0])
	}
	if targets[1].ChannelID != "ch-addon-ok" || targets[1].Kind != core.WatchTargetKindAddon {
		t.Fatalf("second target = %+v, want ch-addon-ok addon", targets[1])
	}
	if targets[2].Kind != core.WatchTargetKindPlugin || targets[2].URL != "https://stream.example.invalid/e1.m3u8" {
		t.Fatalf("third target = %+v, want playable stream URL", targets[2])
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

func TestWatchMatchingRealisticGuideTitles(t *testing.T) {
	now := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC).Format(time.RFC3339)

	cases := []struct {
		name      string
		event     core.LiveEvent
		candidate GuideCandidate
	}{
		{
			name: "NFL Football: Giants at Eagles",
			event: core.LiveEvent{
				ID:       "org.ether.sports:sports:nfl-1",
				Feed:     "sports",
				Category: "football",
				League:   core.LiveEventLeague{ID: "nfl", Name: "NFL", Abbr: "NFL"},
				Start:    now,
				Status:   core.LiveEventStatus{State: core.LiveEventStateIn},
				Competitors: []core.LiveEventCompetitor{
					{ID: "nfl-19", Name: "New York Giants", Short: "Giants", Abbr: "NYG"},
					{ID: "nfl-21", Name: "Philadelphia Eagles", Short: "Eagles", Abbr: "PHI", Home: true},
				},
				Broadcasts: []core.LiveEventBroadcast{{Name: "FOX", Kind: "tv"}},
				MatchHints: &core.LiveEventMatchHints{
					Terms:     []string{"New York Giants", "Giants", "NYG", "Philadelphia Eagles", "Eagles", "PHI"},
					Callsigns: []string{"FOX"},
				},
			},
			candidate: GuideCandidate{
				ChannelID: "ch-fox", ChannelName: "FOX", ChannelNumber: "134",
				Title: "NFL Football: Giants at Eagles", Genres: []string{"Sports"}, Live: true,
			},
		},
		{
			name: "Premier League: Liverpool v Arsenal",
			event: core.LiveEvent{
				ID:       "org.ether.sports:sports:epl-1",
				Feed:     "sports",
				Category: "soccer",
				League:   core.LiveEventLeague{ID: "epl", Name: "Premier League", Abbr: "EPL"},
				Start:    now,
				Status:   core.LiveEventStatus{State: core.LiveEventStateIn},
				Competitors: []core.LiveEventCompetitor{
					{ID: "epl-364", Name: "Liverpool", Short: "Liverpool", Abbr: "LIV"},
					{ID: "epl-359", Name: "Arsenal", Short: "Arsenal", Abbr: "ARS", Home: true},
				},
				Broadcasts: []core.LiveEventBroadcast{{Name: "USA Net", Kind: "tv"}},
				MatchHints: &core.LiveEventMatchHints{
					Terms:     []string{"Liverpool", "LIV", "Arsenal", "ARS"},
					Callsigns: []string{"USA Net", "USA Network"},
				},
			},
			candidate: GuideCandidate{
				ChannelID: "ch-usa", ChannelName: "USA Network", ChannelNumber: "138",
				Title: "Premier League: Liverpool v Arsenal", Live: true,
			},
		},
		{
			name: "Formula 1: Las Vegas Grand Prix (sponsor-prefixed event title)",
			event: core.LiveEvent{
				ID:         "org.ether.sports:sports:f1-600057449",
				Feed:       "sports",
				Category:   "motorsport",
				League:     core.LiveEventLeague{ID: "f1", Name: "Formula 1", Abbr: "F1"},
				Title:      "Heineken Las Vegas Grand Prix",
				Start:      now,
				Status:     core.LiveEventStatus{State: core.LiveEventStateIn},
				Ranking:    []core.LiveEventRankRow{{Pos: 1, Name: "Max Verstappen", Abbr: "VER"}},
				Broadcasts: []core.LiveEventBroadcast{{Name: "Sky Sports F1", Kind: "tv"}},
				MatchHints: &core.LiveEventMatchHints{
					Terms:     []string{"Max Verstappen", "VER", "Formula 1", "F1", "Heineken Las Vegas Grand Prix"},
					Callsigns: []string{"Sky Sports F1"},
				},
			},
			candidate: GuideCandidate{
				ChannelID: "ch-skyf1", ChannelName: "Sky Sports F1", ChannelNumber: "113",
				Title: "Formula 1: Las Vegas Grand Prix", Live: true,
			},
		},
		{
			name: "NBA Basketball: Celtics at Knicks",
			event: core.LiveEvent{
				ID:       "org.ether.sports:sports:nba-1",
				Feed:     "sports",
				Category: "basketball",
				League:   core.LiveEventLeague{ID: "nba", Name: "NBA", Abbr: "NBA"},
				Start:    now,
				Status:   core.LiveEventStatus{State: core.LiveEventStatePre},
				Competitors: []core.LiveEventCompetitor{
					{ID: "nba-2", Name: "Boston Celtics", Short: "Celtics", Abbr: "BOS"},
					{ID: "nba-18", Name: "New York Knicks", Short: "Knicks", Abbr: "NYK", Home: true},
				},
				Broadcasts: []core.LiveEventBroadcast{{Name: "ESPN", Kind: "tv"}},
				MatchHints: &core.LiveEventMatchHints{
					Terms:     []string{"Boston Celtics", "Celtics", "BOS", "New York Knicks", "Knicks", "NYK"},
					Callsigns: []string{"ESPN"},
				},
			},
			candidate: GuideCandidate{
				ChannelID: "ch-espn", ChannelName: "ESPN", ChannelNumber: "139",
				Title: "NBA Basketball: Celtics at Knicks", Genres: []string{"Sports"}, Live: false,
			},
		},
		{
			name: "Live: prefix on matching broadcast channel without EPG genres",
			event: core.LiveEvent{
				ID:       "org.ether.sports:sports:epl-2",
				Feed:     "sports",
				Category: "soccer",
				League:   core.LiveEventLeague{ID: "epl", Name: "Premier League", Abbr: "EPL"},
				Start:    now,
				Status:   core.LiveEventStatus{State: core.LiveEventStateIn},
				Competitors: []core.LiveEventCompetitor{
					{ID: "epl-360", Name: "Manchester United", Short: "Man Utd", Abbr: "MAN"},
					{ID: "epl-364", Name: "Liverpool", Short: "Liverpool", Abbr: "LIV", Home: true},
				},
				Broadcasts: []core.LiveEventBroadcast{{Name: "USA Net", Kind: "tv"}},
				MatchHints: &core.LiveEventMatchHints{
					Terms:     []string{"Manchester United", "Liverpool"},
					Callsigns: []string{"USA Net", "USA Network"},
				},
			},
			candidate: GuideCandidate{
				ChannelID: "ch-usa", ChannelName: "USA Network", ChannelNumber: "138",
				Title: "Live: Premier League Football", Live: true,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			targets := matchEventToCandidates(tc.event, []GuideCandidate{tc.candidate})
			if len(targets) != 1 || targets[0].ChannelID != tc.candidate.ChannelID {
				t.Fatalf("got targets %+v, want match on %s", targets, tc.candidate.ChannelID)
			}
		})
	}
}

func TestWatchCallsignMappingWithoutEPGAndBatchLookup(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()

	now := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
	ltv, err := livetv.New(livetv.Options{DB: db, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("livetv.New: %v", err)
	}
	defer ltv.Close()

	// Realistic IPTV lineup without EPG guides (mirrors real lab channels),
	// including lookalike sister channels that must NOT falsely match.
	lineup := []livetv.Channel{
		{ID: "ch-cbs", Number: "132", Name: "CBS", Kind: "iptv", Enabled: true, SourceID: "iptv1", StreamURL: "https://ex/cbs.m3u8"},
		{ID: "ch-fox", Number: "134", Name: "FOX", Kind: "iptv", Enabled: true, SourceID: "iptv1", StreamURL: "https://ex/fox.m3u8"},
		{ID: "ch-foxnews", Number: "150", Name: "Fox News", Kind: "iptv", Enabled: true, SourceID: "iptv1", StreamURL: "https://ex/foxnews.m3u8"},
		{ID: "ch-nbc", Number: "133", Name: "NBC", Kind: "iptv", Enabled: true, SourceID: "iptv1", StreamURL: "https://ex/nbc.m3u8"},
		{ID: "ch-cnbc", Number: "151", Name: "CNBC", Kind: "iptv", Enabled: true, SourceID: "iptv1", StreamURL: "https://ex/cnbc.m3u8"},
		{ID: "ch-abc", Number: "131", Name: "ABC", Kind: "iptv", Enabled: true, SourceID: "iptv1", StreamURL: "https://ex/abc.m3u8"},
		{ID: "ch-abcau", Number: "152", Name: "ABC Australia", Kind: "iptv", Enabled: true, SourceID: "iptv1", StreamURL: "https://ex/abcau.m3u8"},
		{ID: "ch-tnt", Number: "137", Name: "TNT", Kind: "iptv", Enabled: true, SourceID: "iptv1", StreamURL: "https://ex/tnt.m3u8"},
		{ID: "ch-tntsp1", Number: "115", Name: "TNT Sports 1", Kind: "iptv", Enabled: true, SourceID: "iptv1", StreamURL: "https://ex/tntsp1.m3u8"},
		{ID: "ch-tntsp2", Number: "116", Name: "TNT Sports 2", Kind: "iptv", Enabled: true, SourceID: "iptv1", StreamURL: "https://ex/tntsp2.m3u8"},
		{ID: "ch-usa", Number: "138", Name: "USA Network", Kind: "iptv", Enabled: true, SourceID: "iptv1", StreamURL: "https://ex/usa.m3u8"},
		{ID: "ch-espn", Number: "139", Name: "ESPN", Kind: "iptv", Enabled: true, SourceID: "iptv1", StreamURL: "https://ex/espn.m3u8"},
		{ID: "ch-espn2", Number: "140", Name: "ESPN2", Kind: "iptv", Enabled: true, SourceID: "iptv1", StreamURL: "https://ex/espn2.m3u8"},
		{ID: "ch-fs1", Number: "141", Name: "FS1", Kind: "iptv", Enabled: true, SourceID: "iptv1", StreamURL: "https://ex/fs1.m3u8"},
		{ID: "ch-golf", Number: "505", Name: "Golf Channel", Kind: "iptv", Enabled: true, SourceID: "iptv2", StreamURL: "https://ex/golf.m3u8"},
		{ID: "ch-bein", Number: "501", Name: "beIN Sports", Kind: "iptv", Enabled: true, SourceID: "iptv2", StreamURL: "https://ex/bein.m3u8"},
		{ID: "ch-skymain", Number: "111", Name: "Sky Sports Main Event", Kind: "iptv", Enabled: true, SourceID: "iptv1", StreamURL: "https://ex/skymain.m3u8"},
		{ID: "ch-skypl", Number: "112", Name: "Sky Sports Premier League", Kind: "iptv", Enabled: true, SourceID: "iptv1", StreamURL: "https://ex/skypl.m3u8"},
		{ID: "ch-peacock", Number: "510", Name: "Peacock", Kind: "iptv", Enabled: true, SourceID: "iptv2", StreamURL: "https://ex/peacock.m3u8"},
	}
	for _, ch := range lineup {
		cb, _ := json.Marshal(ch)
		if _, err := db.ExecContext(ctx, `INSERT INTO livetv_channels(id, data) VALUES(?, ?)`, ch.ID, string(cb)); err != nil {
			t.Fatalf("insert channel %s: %v", ch.ID, err)
		}
	}

	matcher := NewWatchMatcher(WatchMatcherOptions{
		Guide: NewLiveTVGuideSource(ltv),
		Now:   func() time.Time { return now },
	})
	viewer := &core.User{ID: 7}

	events := []core.LiveEvent{
		{
			ID: "org.ether.sports:sports:nfl-fox", Feed: "sports", Start: now.Add(time.Hour).Format(time.RFC3339),
			Status:     core.LiveEventStatus{State: core.LiveEventStatePre},
			Broadcasts: []core.LiveEventBroadcast{{Name: "Fox", Kind: "tv"}},
			MatchHints: &core.LiveEventMatchHints{Callsigns: []string{"Fox", "FOX"}},
		},
		{
			ID: "org.ether.sports:sports:fs1-alias", Feed: "sports", Start: now.Add(time.Hour).Format(time.RFC3339),
			Status:     core.LiveEventStatus{State: core.LiveEventStatePre},
			Broadcasts: []core.LiveEventBroadcast{{Name: "FOX Sports 1", Kind: "tv"}},
		},
		{
			ID: "org.ether.sports:sports:epl-usa", Feed: "sports", Start: now.Format(time.RFC3339),
			Status:     core.LiveEventStatus{State: core.LiveEventStateIn},
			Broadcasts: []core.LiveEventBroadcast{{Name: "USA", Kind: "tv"}},
		},
		{
			// Multi-day PGA tournament started 3 days ago, currently live on Golf Channel.
			ID: "org.ether.sports:sports:pga-live", Feed: "sports", Start: now.Add(-72 * time.Hour).Format(time.RFC3339),
			Status:     core.LiveEventStatus{State: core.LiveEventStateIn},
			Broadcasts: []core.LiveEventBroadcast{{Name: "Golf Chnl", Kind: "tv"}},
			MatchHints: &core.LiveEventMatchHints{Callsigns: []string{"Golf Chnl", "Golf Channel"}},
		},
		{
			ID: "org.ether.sports:sports:nba-tnt", Feed: "sports", Start: now.Add(2 * time.Hour).Format(time.RFC3339),
			Status:     core.LiveEventStatus{State: core.LiveEventStatePre},
			Broadcasts: []core.LiveEventBroadcast{{Name: "TNT", Kind: "tv"}},
			MatchHints: &core.LiveEventMatchHints{Callsigns: []string{"TNT"}},
		},
		{
			ID: "org.ether.sports:sports:uk-tnt1", Feed: "sports", Start: now.Format(time.RFC3339),
			Status:     core.LiveEventStatus{State: core.LiveEventStateIn},
			Broadcasts: []core.LiveEventBroadcast{{Name: "TNT Sports 1", Kind: "tv"}},
		},
		{
			ID: "org.ether.sports:sports:espn-game", Feed: "sports", Start: now.Add(2 * time.Hour).Format(time.RFC3339),
			Status:     core.LiveEventStatus{State: core.LiveEventStatePre},
			Broadcasts: []core.LiveEventBroadcast{{Name: "ESPN", Kind: "tv"}},
			MatchHints: &core.LiveEventMatchHints{Callsigns: []string{"ESPN"}},
		},
		{
			ID: "org.ether.sports:sports:espn2-game", Feed: "sports", Start: now.Add(2 * time.Hour).Format(time.RFC3339),
			Status:     core.LiveEventStatus{State: core.LiveEventStatePre},
			Broadcasts: []core.LiveEventBroadcast{{Name: "ESPN2", Kind: "tv"}},
		},
		{
			// Peacock is a streaming app (not a linear TV channel): must match ONLY NBC, never Peacock.
			ID: "org.ether.sports:sports:nbc-peacock", Feed: "sports", Start: now.Add(2 * time.Hour).Format(time.RFC3339),
			Status:     core.LiveEventStatus{State: core.LiveEventStatePre},
			Broadcasts: []core.LiveEventBroadcast{{Name: "NBC", Kind: "tv"}, {Name: "Peacock", Kind: "stream"}},
			MatchHints: &core.LiveEventMatchHints{Callsigns: []string{"NBC", "Peacock"}},
		},
		{
			// Peacock-only event must produce ZERO guide-free linear TV watch targets.
			ID: "org.ether.sports:sports:peacock-only", Feed: "sports", Start: now.Format(time.RFC3339),
			Status:     core.LiveEventStatus{State: core.LiveEventStateIn},
			Broadcasts: []core.LiveEventBroadcast{{Name: "Peacock", Kind: "stream"}},
			MatchHints: &core.LiveEventMatchHints{Callsigns: []string{"Peacock"}},
		},
		{
			ID: "org.ether.sports:sports:sky-main", Feed: "sports", Start: now.Format(time.RFC3339),
			Status:     core.LiveEventStatus{State: core.LiveEventStateIn},
			Broadcasts: []core.LiveEventBroadcast{{Name: "Sky Sports Main Event", Kind: "tv"}},
		},
		{
			ID: "org.ether.sports:sports:sky-pl", Feed: "sports", Start: now.Format(time.RFC3339),
			Status:     core.LiveEventStatus{State: core.LiveEventStateIn},
			Broadcasts: []core.LiveEventBroadcast{{Name: "Sky Sports Premier League", Kind: "tv"}},
			MatchHints: &core.LiveEventMatchHints{Callsigns: []string{"Sky Sports Premier League"}},
		},
		{
			// Completed game must NOT emit callsign-only channel fallback.
			ID: "org.ether.sports:sports:nfl-post", Feed: "sports", Start: now.Add(-5 * time.Hour).Format(time.RFC3339),
			Status:     core.LiveEventStatus{State: core.LiveEventStatePost},
			Broadcasts: []core.LiveEventBroadcast{{Name: "CBS", Kind: "tv"}},
			MatchHints: &core.LiveEventMatchHints{Callsigns: []string{"CBS"}},
		},
	}

	batch, err := matcher.WatchTargetsBatch(ctx, events, viewer)
	if err != nil {
		t.Fatalf("WatchTargetsBatch: %v", err)
	}

	expectExact := func(evID string, wantIDs ...string) {
		t.Helper()
		got := batch[evID]
		if len(got) != len(wantIDs) {
			t.Fatalf("%s: got %d targets %+v, want %v", evID, len(got), got, wantIDs)
		}
		for i, want := range wantIDs {
			if got[i].ChannelID != want {
				t.Fatalf("%s[%d] = %s, want %s (all=%+v)", evID, i, got[i].ChannelID, want, got)
			}
			if got[i].Confidence >= 0.68 {
				t.Fatalf("%s[%d] guide-free confidence %.2f should be lower than EPG guide match", evID, i, got[i].Confidence)
			}
		}
	}

	expectExact("org.ether.sports:sports:nfl-fox", "ch-fox")   // NOT Fox News or FS1!
	expectExact("org.ether.sports:sports:fs1-alias", "ch-fs1") // FOX Sports 1 <-> FS1, NOT FOX!
	expectExact("org.ether.sports:sports:epl-usa", "ch-usa")   // USA <-> USA Network!
	expectExact("org.ether.sports:sports:pga-live", "ch-golf")
	if !batch["org.ether.sports:sports:pga-live"][0].Live {
		t.Fatalf("live PGA Golf Channel target should have Live=true")
	}
	expectExact("org.ether.sports:sports:nba-tnt", "ch-tnt")      // NOT TNT Sports 1!
	expectExact("org.ether.sports:sports:uk-tnt1", "ch-tntsp1")   // NOT TNT or TNT Sports 2!
	expectExact("org.ether.sports:sports:espn-game", "ch-espn")   // NOT ESPN2!
	expectExact("org.ether.sports:sports:espn2-game", "ch-espn2") // NOT ESPN!
	expectExact("org.ether.sports:sports:nbc-peacock", "ch-nbc")  // NBC only; Peacock excluded!
	expectExact("org.ether.sports:sports:peacock-only")           // Peacock is NOT a linear channel!
	expectExact("org.ether.sports:sports:sky-main", "ch-skymain") // NOT Sky Sports Premier League!
	expectExact("org.ether.sports:sports:sky-pl", "ch-skypl")     // NOT Sky Sports Main Event!
	expectExact("org.ether.sports:sports:nfl-post")               // finished game -> no callsign-only fallback
}

func TestLiveEventForChannelGuideFreeFallbackAndFavoriteTieBreak(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()

	now := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
	ltv, err := livetv.New(livetv.Options{DB: db, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("livetv.New: %v", err)
	}
	defer ltv.Close()

	for _, ch := range []livetv.Channel{
		{ID: "ch-usa", Number: "138", Name: "USA Network", Kind: "iptv", Enabled: true, SourceID: "iptv1", GuideID: "strong-38", StreamURL: "https://ex/usa.m3u8"},
		{ID: "ch-espn", Number: "139", Name: "ESPN", Kind: "iptv", Enabled: true, SourceID: "iptv1", GuideID: "strong-39", StreamURL: "https://ex/espn.m3u8"},
		{ID: "ch-espn2", Number: "140", Name: "ESPN2", Kind: "iptv", Enabled: true, SourceID: "iptv1", GuideID: "strong-40", StreamURL: "https://ex/espn2.m3u8"},
		{ID: "ch-fox", Number: "134", Name: "FOX", Kind: "iptv", Enabled: true, SourceID: "iptv1", GuideID: "strong-34", StreamURL: "https://ex/fox.m3u8"},
		{ID: "ch-fs1", Number: "141", Name: "FS1", Kind: "iptv", Enabled: true, SourceID: "iptv1", GuideID: "strong-41", StreamURL: "https://ex/fs1.m3u8"},
		{ID: "ch-nbc", Number: "133", Name: "NBC", Kind: "iptv", Enabled: true, SourceID: "iptv1", GuideID: "strong-33", StreamURL: "https://ex/nbc.m3u8"},
	} {
		cb, _ := json.Marshal(ch)
		if _, err := db.ExecContext(ctx, `INSERT INTO livetv_channels(id, data) VALUES(?, ?)`, ch.ID, string(cb)); err != nil {
			t.Fatalf("insert channel: %v", err)
		}
	}

	evUSALive := core.LiveEvent{
		ID: "org.ether.sports:sports:epl-1", Feed: "sports", Start: now.Format(time.RFC3339),
		Status:      core.LiveEventStatus{State: core.LiveEventStateIn},
		Competitors: []core.LiveEventCompetitor{{ID: "364", Name: "Liverpool", EntityID: "epl-364"}, {ID: "359", Name: "Arsenal", EntityID: "epl-359", Home: true}},
		Broadcasts:  []core.LiveEventBroadcast{{Name: "USA", Kind: "tv"}},
	}
	evUSAPre := core.LiveEvent{
		ID: "org.ether.sports:sports:epl-2", Feed: "sports", Start: now.Add(2 * time.Hour).Format(time.RFC3339),
		Status:     core.LiveEventStatus{State: core.LiveEventStatePre},
		Broadcasts: []core.LiveEventBroadcast{{Name: "USA Network", Kind: "tv"}},
	}
	// Two live regional NFL games airing simultaneously on FOX.
	evFOX1 := core.LiveEvent{
		ID: "org.ether.sports:sports:nfl-1", Feed: "sports", Start: now.Format(time.RFC3339),
		Status:      core.LiveEventStatus{State: core.LiveEventStateIn},
		Competitors: []core.LiveEventCompetitor{{ID: "19", Name: "Giants", EntityID: "nfl-19"}, {ID: "21", Name: "Eagles", EntityID: "nfl-21", Home: true}},
		Broadcasts:  []core.LiveEventBroadcast{{Name: "FOX", Kind: "tv"}},
	}
	evFOX2 := core.LiveEvent{
		ID: "org.ether.sports:sports:nfl-2", Feed: "sports", Start: now.Format(time.RFC3339),
		Status:      core.LiveEventStatus{State: core.LiveEventStateIn},
		Competitors: []core.LiveEventCompetitor{{ID: "6", Name: "Cowboys", EntityID: "nfl-6"}, {ID: "9", Name: "Packers", EntityID: "nfl-9", Home: true}},
		Broadcasts:  []core.LiveEventBroadcast{{Name: "FOX", Kind: "tv"}},
	}
	evFS1 := core.LiveEvent{
		ID: "org.ether.sports:sports:mls-1", Feed: "sports", Start: now.Format(time.RFC3339),
		Status:     core.LiveEventStatus{State: core.LiveEventStateIn},
		Broadcasts: []core.LiveEventBroadcast{{Name: "FOX Sports 1", Kind: "tv"}},
	}
	evESPN2 := core.LiveEvent{
		ID: "org.ether.sports:sports:nba-2", Feed: "sports", Start: now.Format(time.RFC3339),
		Status:     core.LiveEventStatus{State: core.LiveEventStateIn},
		Broadcasts: []core.LiveEventBroadcast{{Name: "ESPN2", Kind: "tv"}},
	}
	evPeacock := core.LiveEvent{
		ID: "org.ether.sports:sports:epl-3", Feed: "sports", Start: now.Format(time.RFC3339),
		Status:     core.LiveEventStatus{State: core.LiveEventStateIn},
		Broadcasts: []core.LiveEventBroadcast{{Name: "Peacock", Kind: "stream"}},
	}

	favsByViewer := map[int64][]string{
		1: nil,                                                                           // no favourites
		2: {"org.ether.sports:sports:team:nfl-21"},                                       // Eagles -> resolves FOX ambiguity to nfl-1!
		3: {"org.ether.sports:sports:team:nfl-21", "org.ether.sports:sports:team:nfl-9"}, // both FOX games favourited -> ambiguous -> nil!
	}

	matcher := NewWatchMatcher(WatchMatcherOptions{
		Guide: NewLiveTVGuideSource(ltv),
		ActiveEvents: &fakeActiveEvents{
			events: []core.LiveEvent{evUSALive, evUSAPre, evFOX1, evFOX2, evFS1, evESPN2, evPeacock},
			rev:    "r1",
		},
		FavoriteEntities: func(_ context.Context, u *core.User) ([]string, error) {
			return favsByViewer[u.ID], nil
		},
		Now: func() time.Time { return now },
	})

	// 1. Single live event on USA Network (ignoring pre-game evUSAPre) -> linked!
	progKey, ref, err := matcher.LiveEventForChannel(ctx, &core.User{ID: 1}, "ch-usa")
	if err != nil || progKey == "" || ref == nil || ref.ID != evUSALive.ID {
		t.Fatalf("ch-usa = (%q, %+v, %v), want %s", progKey, ref, err, evUSALive.ID)
	}

	// 2. ESPN2 live event links ch-espn2, NOT ch-espn!
	_, refESPN2, err := matcher.LiveEventForChannel(ctx, &core.User{ID: 1}, "ch-espn2")
	if err != nil || refESPN2 == nil || refESPN2.ID != evESPN2.ID {
		t.Fatalf("ch-espn2 = (%+v, %v), want %s", refESPN2, err, evESPN2.ID)
	}
	_, refESPN, err := matcher.LiveEventForChannel(ctx, &core.User{ID: 1}, "ch-espn")
	if err != nil || refESPN != nil {
		t.Fatalf("ch-espn = (%+v, %v), want nil (must not match ESPN2)", refESPN, err)
	}

	// 3. FOX Sports 1 links ch-fs1 (not confused with FOX).
	_, refFS1, err := matcher.LiveEventForChannel(ctx, &core.User{ID: 1}, "ch-fs1")
	if err != nil || refFS1 == nil || refFS1.ID != evFS1.ID {
		t.Fatalf("ch-fs1 = (%+v, %v), want %s", refFS1, err, evFS1.ID)
	}

	// 4. Two live events on FOX:
	//    - Viewer 1 (no favs) -> nil
	//    - Viewer 2 (1 fav in nfl-1) -> nfl-1
	//    - Viewer 3 (favs in BOTH nfl-1 and nfl-2) -> nil
	_, refFOX1, err := matcher.LiveEventForChannel(ctx, &core.User{ID: 1}, "ch-fox")
	if err != nil || refFOX1 != nil {
		t.Fatalf("ch-fox viewer 1 (ambiguous, no favs) = (%+v, %v), want nil", refFOX1, err)
	}
	_, refFOX2, err := matcher.LiveEventForChannel(ctx, &core.User{ID: 2}, "ch-fox")
	if err != nil || refFOX2 == nil || refFOX2.ID != evFOX1.ID {
		t.Fatalf("ch-fox viewer 2 (single fav tie-break) = (%+v, %v), want %s", refFOX2, err, evFOX1.ID)
	}
	_, refFOX3, err := matcher.LiveEventForChannel(ctx, &core.User{ID: 3}, "ch-fox")
	if err != nil || refFOX3 != nil {
		t.Fatalf("ch-fox viewer 3 (both favs -> still ambiguous) = (%+v, %v), want nil", refFOX3, err)
	}
	// Verify Viewer 1 cache stays isolated from Viewer 2.
	_, refFOX1Again, err := matcher.LiveEventForChannel(ctx, &core.User{ID: 1}, "ch-fox")
	if err != nil || refFOX1Again != nil {
		t.Fatalf("ch-fox viewer 1 re-read leaked viewer 2 fav: (%+v, %v)", refFOX1Again, err)
	}

	// 5. NBC with only a Peacock-streamed live event -> nil.
	_, refNBC, err := matcher.LiveEventForChannel(ctx, &core.User{ID: 1}, "ch-nbc")
	if err != nil || refNBC != nil {
		t.Fatalf("ch-nbc = (%+v, %v), want nil", refNBC, err)
	}
}

func TestWatchMatcherExcludesUnhealthyAndFallsBackToUnavailable(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)

	ev := core.LiveEvent{
		ID:     "demo:sports:game-1",
		Feed:   "sports",
		Start:  now.UTC().Format(time.RFC3339),
		Status: core.LiveEventStatus{State: core.LiveEventStateIn},
		Competitors: []core.LiveEventCompetitor{
			{ID: "a", Name: "Metro Lions", Abbr: "MLI"},
			{ID: "b", Name: "Bay Tigers", Abbr: "BTI"},
		},
		Broadcasts: []core.LiveEventBroadcast{{Name: "ESPN", Kind: "tv"}},
	}

	// 1. Healthy + unhealthy match (even when unhealthy has higher confidence):
	//    unhealthy channel is excluded and only healthy target is returned with available omitted.
	targets := matchEventToCandidates(ev, []GuideCandidate{
		{
			ChannelID: "ch-dead-hq", ChannelName: "ESPN",
			Title: "Metro Lions vs Bay Tigers", Genres: []string{"Sports"}, Live: true, Unhealthy: true,
		},
		{
			ChannelID: "ch-live-ok", ChannelName: "Sports Backup",
			Title: "Metro Lions vs Bay Tigers", Genres: []string{"Sports"}, Live: true, Unhealthy: false,
		},
	})
	if len(targets) != 1 || targets[0].ChannelID != "ch-live-ok" {
		t.Fatalf("healthy+unhealthy = %+v, want only ch-live-ok", targets)
	}
	if targets[0].Available != nil || !targets[0].IsAvailable() {
		t.Fatalf("healthy target should omit Available: %+v", targets[0])
	}

	// 2. Only unhealthy matches exist -> returned with available=false on wire.
	unavail := matchEventToCandidates(ev, []GuideCandidate{
		{
			ChannelID: "ch-dead-1", ChannelName: "ESPN",
			Title: "Metro Lions vs Bay Tigers", Genres: []string{"Sports"}, Live: true, Unhealthy: true,
		},
		{
			ChannelID: "ch-dead-2", ChannelName: "Sports Alt",
			Title: "Metro Lions vs Bay Tigers", Genres: []string{"Sports"}, Live: true, Unhealthy: true,
		},
	})
	if len(unavail) != 2 {
		t.Fatalf("only-unhealthy len = %d, want 2: %+v", len(unavail), unavail)
	}
	for _, tgt := range unavail {
		if tgt.Available == nil || *tgt.Available || tgt.IsAvailable() {
			t.Fatalf("unavailable fallback target must set Available=false: %+v", tgt)
		}
	}
	raw, err := json.Marshal(unavail[0])
	if err != nil {
		t.Fatalf("marshal unavailable target: %v", err)
	}
	if !strings.Contains(string(raw), `"available":false`) {
		t.Fatalf("expected wire field \"available\":false, got %s", raw)
	}

	// 3. Unhealthy Live TV channel + playable plugin stream -> excludes unhealthy channel.
	evWithStream := ev
	evWithStream.Broadcasts = append(evWithStream.Broadcasts, core.LiveEventBroadcast{
		Name: "Direct Feed", Kind: "stream", URL: "plugin://sports/stream/game-1",
	})
	streamTargets := matchEventToCandidates(evWithStream, []GuideCandidate{
		{
			ChannelID: "ch-dead-1", ChannelName: "ESPN",
			Title: "Metro Lions vs Bay Tigers", Genres: []string{"Sports"}, Live: true, Unhealthy: true,
		},
	})
	if len(streamTargets) != 1 || streamTargets[0].Kind != core.WatchTargetKindPlugin || !streamTargets[0].IsAvailable() {
		t.Fatalf("unhealthy channel + playable plugin stream = %+v, want only plugin stream", streamTargets)
	}

	// 4. sortWatchTargets always orders healthy targets before unavailable targets regardless of confidence.
	mixed := []core.WatchTarget{
		{Kind: core.WatchTargetKindChannel, ChannelID: "ch-unavail", Name: "A", Confidence: 0.99, Live: true, Available: boolPtr(false)},
		{Kind: core.WatchTargetKindChannel, ChannelID: "ch-avail", Name: "Z", Confidence: 0.50, Live: false},
	}
	sortWatchTargets(mixed)
	if mixed[0].ChannelID != "ch-avail" || mixed[1].ChannelID != "ch-unavail" {
		t.Fatalf("sortWatchTargets order = %+v, want healthy first", mixed)
	}

	// 5. A6 LiveEventForChannel ignores an unhealthy channel candidate.
	guide := &fakeGuideSource{
		rev: "g1:h1",
		currentByChan: map[string]*GuideCandidate{
			"ch-dead": {
				ChannelID: "ch-dead", ChannelName: "ESPN",
				Title: "Metro Lions vs Bay Tigers", Genres: []string{"Sports"}, Live: true, Unhealthy: true,
			},
		},
		progKeyByChan: map[string]string{"ch-dead": "ch-dead|1|2|Lions"},
	}
	m := NewWatchMatcher(WatchMatcherOptions{
		Guide:        guide,
		ActiveEvents: &fakeActiveEvents{events: []core.LiveEvent{ev}, rev: "ev1"},
		Now:          func() time.Time { return now },
	})
	if _, ref, err := m.LiveEventForChannel(ctx, &core.User{ID: 1}, "ch-dead"); err != nil || ref != nil {
		t.Fatalf("LiveEventForChannel on unhealthy candidate = (%+v, %v), want nil", ref, err)
	}
}

func TestWatchMatcherHealthRevisionInvalidationAndCacheBound(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()

	baseNow := time.Date(2026, 11, 22, 19, 0, 0, 0, time.UTC)
	var clockNano atomic.Int64
	clockNano.Store(baseNow.UnixNano())
	nowFn := func() time.Time { return time.Unix(0, clockNano.Load()).UTC() }

	// Seed a channel and an active cooldown in livetv_source_history before livetv.New loads it.
	ch := livetv.Channel{
		ID: "ch-espn", Number: "40", Name: "ESPN HD",
		Kind: "iptv", Enabled: true, SourceID: "s1", GuideID: "espn.1",
		StreamURL: "https://example.invalid/espn.m3u8",
	}
	cb, _ := json.Marshal(ch)
	if _, err := db.ExecContext(ctx, `INSERT INTO livetv_channels(id, data) VALUES(?, ?)`, ch.ID, string(cb)); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	prog := livetv.Programme{
		ID:        "espn.1-live",
		ChannelID: "espn.1",
		Start:     baseNow.Add(-10 * time.Minute),
		End:       baseNow.Add(2 * time.Hour),
		Item:      livetv.Item{Title: "Metro Sharks vs Harbor Seals", Genres: []string{"Sports"}},
	}
	pb, _ := json.Marshal(prog)
	if _, err := db.ExecContext(ctx, `INSERT INTO livetv_programmes(source_id, channel_id, start_ms, end_ms, data) VALUES(?,?,?,?,?)`,
		"s1", "espn.1", prog.Start.UnixMilli(), prog.End.UnixMilli(), string(pb)); err != nil {
		t.Fatalf("insert programme: %v", err)
	}
	// Seed a persisted 1-failure cooldown ending at baseNow+15s for ch-espn's playlist input.
	ltvSeed, err := livetv.New(livetv.Options{DB: db, Now: nowFn})
	if err != nil {
		t.Fatalf("livetv.New seed: %v", err)
	}
	statuses, err := ltvSeed.StreamSourceStatuses(ctx, ch.ID)
	if err != nil || len(statuses) != 1 {
		t.Fatalf("StreamSourceStatuses = (%+v, %v)", statuses, err)
	}
	srcID := statuses[0].ID
	ltvSeed.Close()

	// Derive the exact sourceHistory key format (stableID(channelID, src.ID, src.URL)) via a second open after writing history.
	histKeySum := sha256.Sum256([]byte(ch.ID + "\x00" + srcID + "\x00" + ch.StreamURL))
	histKey := hex.EncodeToString(histKeySum[:10])
	hist := livetv.SourceHistory{
		Key:          histKey,
		ChannelID:    ch.ID,
		SourceID:     srcID,
		Updated:      baseNow,
		Failures:     1,
		OpenFailures: 1,
		RetryAfter:   baseNow.Add(15 * time.Second),
		LastError:    "unavailable",
		LastErrorAt:  baseNow,
	}
	hb, _ := json.Marshal(hist)
	if _, err := db.ExecContext(ctx, `INSERT INTO livetv_source_history(key, updated_ms, data) VALUES(?, ?, ?)`, hist.Key, baseNow.UnixMilli(), string(hb)); err != nil {
		t.Fatalf("insert source history: %v", err)
	}

	ltv, err := livetv.New(livetv.Options{DB: db, Now: nowFn})
	if err != nil {
		t.Fatalf("livetv.New: %v", err)
	}
	defer ltv.Close()

	ev := core.LiveEvent{
		ID:     "demo:feed:m1",
		Feed:   "feed",
		Start:  baseNow.UTC().Format(time.RFC3339),
		Status: core.LiveEventStatus{State: core.LiveEventStateIn},
		Competitors: []core.LiveEventCompetitor{
			{ID: "a", Name: "Metro Sharks", Abbr: "MSH"},
			{ID: "b", Name: "Harbor Seals", Abbr: "HSE"},
		},
		Broadcasts: []core.LiveEventBroadcast{{Name: "ESPN", Kind: "tv"}},
	}
	matcher := NewWatchMatcher(WatchMatcherOptions{
		Guide:        NewLiveTVGuideSource(ltv),
		ActiveEvents: &fakeActiveEvents{events: []core.LiveEvent{ev}, rev: "r1"},
		Now:          nowFn,
		CacheCap:     2,
	})
	viewer := &core.User{ID: 5}

	// 1. While cooldown is active, WatchTargets returns ch-espn with available=false (cached),
	//    and A6 LiveEventForChannel returns nil.
	t1, err := matcher.WatchTargets(ctx, ev, viewer)
	if err != nil || len(t1) != 1 || t1[0].ChannelID != ch.ID || t1[0].IsAvailable() {
		t.Fatalf("WatchTargets during cooldown = (%+v, %v), want unavailable ch-espn", t1, err)
	}
	if _, ref, err := matcher.LiveEventForChannel(ctx, viewer, ch.ID); err != nil || ref != nil {
		t.Fatalf("LiveEventForChannel during cooldown = (%+v, %v), want nil", ref, err)
	}

	// 2. Advance past cooldown -> GuideRevision advances -> cached unavailable entry invalidates -> playable!
	clockNano.Store(baseNow.Add(20 * time.Second).UnixNano())
	t2, err := matcher.WatchTargets(ctx, ev, viewer)
	if err != nil || len(t2) != 1 || t2[0].ChannelID != ch.ID || !t2[0].IsAvailable() {
		t.Fatalf("WatchTargets after cooldown expiry = (%+v, %v), want playable ch-espn", t2, err)
	}
	if _, ref, err := matcher.LiveEventForChannel(ctx, viewer, ch.ID); err != nil || ref == nil || ref.ID != ev.ID {
		t.Fatalf("LiveEventForChannel after recovery = (%+v, %v), want %s", ref, err, ev.ID)
	}

	// 3. Cache stays strictly bounded by CacheCap across multiple health revisions.
	for i := 0; i < 6; i++ {
		e := ev
		e.ID = fmt.Sprintf("demo:feed:m-%d", i)
		if _, err := matcher.WatchTargets(ctx, e, viewer); err != nil {
			t.Fatalf("WatchTargets %d: %v", i, err)
		}
	}
	matcher.mu.Lock()
	cacheLen := len(matcher.watchCache)
	matcher.mu.Unlock()
	if cacheLen > 2 {
		t.Fatalf("watchCache grew to %d, want <= 2", cacheLen)
	}
}
