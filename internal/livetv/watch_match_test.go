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
}
