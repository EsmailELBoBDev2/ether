package livetv

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

func TestWatchTimeIndexQueryPlanUsesIndex(t *testing.T) {
	db, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()

	rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT source_id, channel_id, data FROM livetv_programmes WHERE start_ms>=? AND start_ms<? AND end_ms>? ORDER BY start_ms ASC LIMIT ?`, int64(1000), int64(9000), int64(1000), 2000)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()

	var plans []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plans = append(plans, detail)
	}
	joined := strings.Join(plans, "\n")
	if !strings.Contains(joined, "livetv_programmes_time") {
		t.Fatalf("expected query plan to use livetv_programmes_time index, got:\n%s", joined)
	}
	if strings.Contains(joined, "SCAN livetv_programmes") {
		t.Fatalf("query plan performed full table scan:\n%s", joined)
	}

	// Joined time-slice lookup must probe livetv_search_channel_guide without
	// scanning either livetv_programmes or livetv_channels.
	joinRows, err := db.Query(`EXPLAIN QUERY PLAN SELECT c.id, p.data FROM (SELECT source_id, channel_id, data FROM livetv_programmes WHERE start_ms>=? AND start_ms<? AND end_ms>? ORDER BY start_ms ASC LIMIT ?) p JOIN livetv_channels c ON coalesce(nullif(json_extract(c.data,'$.guideSourceId'),''),json_extract(c.data,'$.sourceId'))=+p.source_id AND json_extract(c.data,'$.guideId')=+p.channel_id`, int64(1000), int64(9000), int64(1000), 2000)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN join: %v", err)
	}
	defer joinRows.Close()
	var joinPlans []string
	for joinRows.Next() {
		var id, parent, notused int
		var detail string
		if err := joinRows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan join plan: %v", err)
		}
		joinPlans = append(joinPlans, detail)
	}
	joinPlanText := strings.Join(joinPlans, "\n")
	if !strings.Contains(joinPlanText, "livetv_programmes_time") || !strings.Contains(joinPlanText, "livetv_search_channel_guide") {
		t.Fatalf("expected joined query plan to use livetv_programmes_time and livetv_search_channel_guide, got:\n%s", joinPlanText)
	}
	if strings.Contains(joinPlanText, "SCAN livetv_channels") || strings.Contains(joinPlanText, "SCAN livetv_programmes") {
		t.Fatalf("joined query plan performed full table scan:\n%s", joinPlanText)
	}
}

func TestWatchCandidatesWindowVisibilityAndStremioEventChannel(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()

	now := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
	svc, err := New(Options{DB: db, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("livetv.New: %v", err)
	}
	defer svc.Close()

	saveCh := func(c Channel) {
		t.Helper()
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("marshal channel: %v", err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO livetv_channels(id, data) VALUES(?, ?)`, c.ID, string(b)); err != nil {
			t.Fatalf("insert channel %s: %v", c.ID, err)
		}
	}
	saveProg := func(srcID, guideID string, p Programme) {
		t.Helper()
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal programme: %v", err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO livetv_programmes(source_id, channel_id, start_ms, end_ms, data) VALUES(?,?,?,?,?)`, srcID, guideID, p.Start.UnixMilli(), p.End.UnixMilli(), string(b)); err != nil {
			t.Fatalf("insert programme: %v", err)
		}
	}

	saveCh(Channel{ID: "ch-public", Number: "10", Name: "North Sports HD", Kind: "iptv", Enabled: true, SourceID: "src1", GuideID: "guide.north", StreamURL: "https://example.invalid/10.m3u8"})
	saveCh(Channel{ID: "ch-private", Number: "11", Name: "Private Sports", Kind: "iptv", Enabled: true, Private: true, SourceID: "src1", GuideID: "guide.priv", StreamURL: "https://example.invalid/11.m3u8"})
	saveCh(Channel{ID: "ch-addon", Number: "200", Name: "Live Events Addon", Kind: "iptv", Enabled: true, SourceID: "plugin:addon", GuideID: "event:sport:all", StreamURL: "plugin://addon/event/sport/all"})

	// Overlapping target window [17:30, 22:00].
	saveProg("src1", "guide.north", Programme{
		ID:        "p1",
		ChannelID: "guide.north",
		Start:     now.Add(-15 * time.Minute),
		End:       now.Add(2 * time.Hour),
		Item:      Item{Title: "Lions vs Tigers", Genres: []string{"Sports"}},
	})
	// Outside window (yesterday) must not appear.
	saveProg("src1", "guide.north", Programme{
		ID:        "p-old",
		ChannelID: "guide.north",
		Start:     now.Add(-30 * time.Hour),
		End:       now.Add(-28 * time.Hour),
		Item:      Item{Title: "Lions vs Tigers Replay", Genres: []string{"Sports"}},
	})
	// Private channel programme hidden from non-owner viewers.
	saveProg("src1", "guide.priv", Programme{
		ID:        "p-priv",
		ChannelID: "guide.priv",
		Start:     now,
		End:       now.Add(2 * time.Hour),
		Item:      Item{Title: "Lions vs Tigers VIP", Genres: []string{"Sports"}},
	})
	// Stremio event channel programme.
	saveProg("plugin:addon", "event:sport:all", Programme{
		ID:           "p-addon",
		ChannelID:    "event:sport:all",
		Start:        now,
		End:          now.Add(3 * time.Hour),
		EventStreams: []EventStreamRef{{Type: "sport", ID: "match-1"}},
		Item:         Item{Title: "Lions at Tigers"},
	})

	viewer := &core.User{ID: 7}
	owner := &core.User{ID: 1, IsOwner: true}

	cands, rev1, err := svc.WatchCandidates(ctx, viewer, now.Add(-30*time.Minute), now.Add(4*time.Hour), []string{"North Sports"})
	if err != nil {
		t.Fatalf("WatchCandidates viewer: %v", err)
	}
	if rev1 == "" || rev1 == "denied" {
		t.Fatalf("unexpected guide revision %q", rev1)
	}
	seenCh := map[string]WatchGuideEntry{}
	for _, c := range cands {
		seenCh[c.ChannelID] = c
	}
	if _, ok := seenCh["ch-public"]; !ok {
		t.Fatalf("expected ch-public in viewer candidates, got %+v", cands)
	}
	if _, ok := seenCh["ch-private"]; ok {
		t.Fatalf("private channel leaked to regular viewer")
	}
	addon, ok := seenCh["ch-addon"]
	if !ok || !addon.IsEventAddon {
		t.Fatalf("expected Stremio event channel marked IsEventAddon, got %+v", addon)
	}
	if len(cands) != 2 {
		t.Fatalf("expected exactly 2 viewer candidates (no old programme), got %d", len(cands))
	}

	// Owner sees private channel too.
	ownerCands, _, err := svc.WatchCandidates(ctx, owner, now.Add(-30*time.Minute), now.Add(4*time.Hour), nil)
	if err != nil {
		t.Fatalf("WatchCandidates owner: %v", err)
	}
	if len(ownerCands) != 3 {
		t.Fatalf("expected 3 owner candidates, got %d", len(ownerCands))
	}

	// Viewer with Live TV policy disabled gets nothing.
	denied := &core.User{ID: 8}
	if err := savePolicy(ctx, db, Policy{UserID: 8, Disabled: true}); err != nil {
		t.Fatalf("savePolicy disabled: %v", err)
	}
	deniedCands, _, err := svc.WatchCandidates(ctx, denied, now.Add(-30*time.Minute), now.Add(4*time.Hour), nil)
	if err != nil || len(deniedCands) != 0 {
		t.Fatalf("denied viewer got (%d, %v), want (0, nil)", len(deniedCands), err)
	}

	// CurrentProgrammeForWatch returns current programme and stable key, and blocks private channel for regular viewer.
	entry, key, err := svc.CurrentProgrammeForWatch(ctx, viewer, "ch-public", now)
	if err != nil || entry == nil || entry.Programme.Item.Title != "Lions vs Tigers" || key == "" {
		t.Fatalf("CurrentProgrammeForWatch public = (%+v, %q, %v)", entry, key, err)
	}
	privEntry, _, err := svc.CurrentProgrammeForWatch(ctx, viewer, "ch-private", now)
	if err != nil || privEntry != nil {
		t.Fatalf("CurrentProgrammeForWatch private leaked to viewer: (%+v, %v)", privEntry, err)
	}

	// Bumping livetv_channel_epoch advances GuideRevision.
	if _, err := db.ExecContext(ctx, `UPDATE livetv_channel_epoch SET generation=generation+1 WHERE id=1`); err != nil {
		t.Fatalf("bump epoch: %v", err)
	}
	rev2, err := svc.GuideRevision(ctx, viewer)
	if err != nil || rev2 == rev1 {
		t.Fatalf("GuideRevision did not advance after epoch bump: %q -> %q (err=%v)", rev1, rev2, err)
	}

	// No-EPG IPTV channel (GuideID == "") matching callsign emits ChannelOnly=true,
	// while a private no-EPG channel remains hidden from non-owner viewers.
	saveCh(Channel{ID: "ch-noepg-fox", Number: "134", Name: "FOX", Kind: "iptv", Enabled: true, SourceID: "iptv", StreamURL: "https://example.invalid/fox.m3u8"})
	saveCh(Channel{ID: "ch-noepg-priv", Number: "135", Name: "FOX VIP", Kind: "iptv", Enabled: true, Private: true, SourceID: "iptv", StreamURL: "https://example.invalid/foxvip.m3u8"})
	noEpgCands, _, err := svc.WatchCandidates(ctx, viewer, now.Add(-30*time.Minute), now.Add(4*time.Hour), []string{"FOX", "North Sports"})
	if err != nil {
		t.Fatalf("WatchCandidates no-EPG: %v", err)
	}
	var foxOnly, northOnly, privLeaked bool
	for _, c := range noEpgCands {
		if c.ChannelID == "ch-noepg-fox" && c.ChannelOnly {
			foxOnly = true
		}
		if c.ChannelID == "ch-public" && c.ChannelOnly {
			northOnly = true
		}
		if c.ChannelID == "ch-noepg-priv" {
			privLeaked = true
		}
	}
	if !foxOnly {
		t.Fatalf("expected ch-noepg-fox ChannelOnly candidate, got %+v", noEpgCands)
	}
	if northOnly {
		t.Fatalf("channel WITH overlapping EPG must not emit ChannelOnly fallback")
	}
	if privLeaked {
		t.Fatalf("private no-EPG channel leaked to regular viewer")
	}

	// CurrentProgrammeForWatch returns a ChannelOnly entry for a visible guide-less
	// channel (A6 guide-free fallback), while keeping private channels blocked.
	noEpgCurr, noEpgKey, err := svc.CurrentProgrammeForWatch(ctx, viewer, "ch-noepg-fox", now)
	if err != nil || noEpgCurr == nil || !noEpgCurr.ChannelOnly || noEpgCurr.ChannelID != "ch-noepg-fox" || noEpgKey == "" {
		t.Fatalf("CurrentProgrammeForWatch no-EPG = (%+v, %q, %v), want ChannelOnly entry", noEpgCurr, noEpgKey, err)
	}
	if privCurr, _, err := svc.CurrentProgrammeForWatch(ctx, viewer, "ch-noepg-priv", now); err != nil || privCurr != nil {
		t.Fatalf("CurrentProgrammeForWatch private no-EPG leaked: (%+v, %v)", privCurr, err)
	}
}

func TestWatchCandidatesIndexedOnLargeLineup(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()

	now := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
	svc, err := New(Options{DB: db, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("livetv.New: %v", err)
	}
	defer svc.Close()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO livetv_channels(id, data) VALUES(?, ?)`)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	insert := func(c Channel) {
		b, _ := json.Marshal(c)
		if _, err := stmt.ExecContext(ctx, c.ID, string(b)); err != nil {
			t.Fatalf("insert %s: %v", c.ID, err)
		}
	}
	// Populate 10,000 unrelated channels plus 100 local FOX decoys so any full-lineup
	// scan or naive un-boosted FTS cutoff would fail or blow up candidates.
	for i := 0; i < 10000; i++ {
		id := "filler-" + strings.Repeat("0", 5-len(itoa(i))) + itoa(i)
		insert(Channel{ID: id, Number: itoa(1000 + i), Name: "ZZ Filler Channel " + itoa(i), Group: "General", Kind: "iptv", Enabled: true, SourceID: "prov", StreamURL: "https://ex/filler.m3u8"})
	}
	for i := 0; i < 100; i++ {
		insert(Channel{ID: "fox-local-" + itoa(i), Number: itoa(20000 + i), Name: "US| FOX Local " + itoa(i) + " HD", Group: "US| Locals", Kind: "iptv", Enabled: true, SourceID: "prov", StreamURL: "https://ex/foxlocal.m3u8"})
	}
	insert(Channel{ID: "target-espn", Number: "30001", Name: "US| ESPN FHD", Group: "US| Sports", Kind: "iptv", Enabled: true, SourceID: "prov", StreamURL: "https://ex/espn.m3u8"})
	insert(Channel{ID: "target-sky", Number: "30002", Name: "UK - Sky Sports Main Event ᴴᴰ", Group: "UK| Sports", Kind: "iptv", Enabled: true, SourceID: "prov", StreamURL: "https://ex/sky.m3u8"})
	insert(Channel{ID: "target-event", Number: "30003", Name: "NFL 03: Giants vs Eagles 8:15 PM ET", Group: "NFL Game Pass", Kind: "iptv", Enabled: true, SourceID: "prov", StreamURL: "https://ex/nfl03.m3u8"})
	insert(Channel{ID: "target-fox-fav", Number: "30004", Name: "US| FOX FHD", Group: "US| Entertainment", Kind: "iptv", Enabled: true, SourceID: "prov", StreamURL: "https://ex/foxfhd.m3u8"})
	insert(Channel{ID: "hidden-espn", Number: "30005", Name: "US| ESPN 4K", Group: "Hidden", Kind: "iptv", Enabled: true, SourceID: "prov", StreamURL: "https://ex/hidden.m3u8"})
	stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := svc.SaveOwnerGroups(ctx, []OwnerGroup{
		{ID: "hidden", Name: "Hidden", Hidden: true, Groups: []OwnerGroupSource{{SourceID: "prov", Name: "Hidden"}}},
	}); err != nil {
		t.Fatalf("SaveOwnerGroups: %v", err)
	}

	viewer := &core.User{ID: 11}
	// Demand migration 27 search index and wait for background backfill of 10k channels.
	if err := svc.demandSearchIndex(ctx); err != nil {
		t.Fatalf("demandSearchIndex: %v", err)
	}
	for deadline := time.Now().Add(10 * time.Second); !svc.isSearchReady(); {
		if time.Now().After(deadline) {
			t.Fatalf("search index did not finish backfilling 10k channels in time")
		}
		time.Sleep(15 * time.Millisecond)
	}

	revBefore, err := svc.GuideRevision(ctx, viewer)
	if err != nil {
		t.Fatalf("GuideRevision before: %v", err)
	}
	svc.NoteViewerWatched(viewer.ID, "target-espn")
	revAfter, err := svc.GuideRevision(ctx, viewer)
	if err != nil || revAfter == revBefore {
		t.Fatalf("GuideRevision should advance after NoteViewerWatched: %q -> %q (err=%v)", revBefore, revAfter, err)
	}

	cands, _, err := svc.WatchCandidatesQuery(ctx, viewer, WatchQuery{
		From:      now.Add(-30 * time.Minute),
		Until:     now.Add(4 * time.Hour),
		Callsigns: []string{"ESPN", "Sky Sports Main Event", "FOX"},
		EventQueries: []WatchEventQuery{
			{SideA: []string{"giants", "nyg"}, SideB: []string{"eagles", "phi"}},
		},
		FavoriteChannelIDs: []string{"target-fox-fav"},
	})
	if err != nil {
		t.Fatalf("WatchCandidatesQuery: %v", err)
	}
	if len(cands) > watchSearchTotalChannelCap {
		t.Fatalf("candidates not bounded: got %d > cap %d", len(cands), watchSearchTotalChannelCap)
	}
	byID := make(map[string]WatchGuideEntry, len(cands))
	for _, c := range cands {
		byID[c.ChannelID] = c
	}
	for _, want := range []string{"target-espn", "target-sky", "target-event", "target-fox-fav"} {
		if _, ok := byID[want]; !ok {
			t.Fatalf("missing expected channel %s in indexed candidates (got %d total)", want, len(cands))
		}
	}
	if _, leaked := byID["hidden-espn"]; leaked {
		t.Fatalf("hidden owner-group channel leaked into WatchCandidatesQuery")
	}
	if byID["target-event"].Group != "NFL Game Pass" {
		t.Fatalf("target-event.Group = %q, want NFL Game Pass", byID["target-event"].Group)
	}
	if !byID["target-fox-fav"].Favorite {
		t.Fatalf("target-fox-fav should be marked Favorite=true")
	}
	if byID["target-espn"].WatchedAt.IsZero() {
		t.Fatalf("target-espn should carry non-zero WatchedAt after NoteViewerWatched")
	}
	// Verify snapshot was NOT populated by WatchCandidatesQuery (no full lineup scan).
	svc.chanSnap.mu.Lock()
	snapLoaded := len(svc.chanSnap.list)
	svc.chanSnap.mu.Unlock()
	if snapLoaded != 0 {
		t.Fatalf("WatchCandidatesQuery loaded full channelSnapshot (%d channels) instead of using migration 27 index", snapLoaded)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
