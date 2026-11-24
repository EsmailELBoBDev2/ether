package sessions

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
)

// A queued next item (gapless audio) opens beside the playing session: the
// playing one keeps streaming and reporting until the queued one is observed
// playing. Preloading the queued item's media does not end the playing one.
func TestQueuedOpenKeepsThePlayingSessionUntilItPlays(t *testing.T) {
	ctx := context.Background()
	p := &fakePlayback{sessions: map[string]*core.PlaySession{}, limit: 4}
	active := func(_ context.Context, id int64) (*core.Viewer, error) { return &core.Viewer{ID: id}, nil }
	bus := NewEventBus(nil, active)
	defer bus.Close()
	b, err := NewBroker(BrokerDeps{SurfacePolicy: testSessionPolicy, Lookup: func(_ context.Context, _ *core.Viewer, _ core.Device, k core.RatingKey) (*core.Title, error) {
		return &core.Title{Key: k}, nil
	}, Playback: p, Transcoder: fakeBrokerTranscoder{}, Progress: &fakeProgress{}, ActiveViewer: active, CanPlay: func(*core.Viewer, bool, time.Time) bool { return true }, BitrateLimit: func(*core.Viewer) int { return 0 }, OnTimeline: func(*core.Viewer, core.RatingKey, time.Duration, time.Duration, string) {}, Events: bus})
	if err != nil {
		t.Fatal(err)
	}
	v := &core.Viewer{ID: 2}
	d := core.Device{ID: "web"}
	a := frontend.SessionAccess{Viewer: v, Device: d, Protocol: "vela"}
	open := func(item int64, queued bool) frontend.Session {
		t.Helper()
		pl, err := b.Plan(ctx, frontend.PlanRequest{Viewer: v, Device: d, Protocol: "vela", Item: core.MovieKey(item), Intent: frontend.PlayIntent{Options: core.DecideOptions{DirectPlay: true, AudioStream: -1, SubtitleStream: -1}, Queued: queued}})
		if err != nil {
			t.Fatal(err)
		}
		s, err := b.Open(ctx, frontend.OpenRequest{Viewer: v, Device: d, Protocol: "vela", PlanID: pl.ID})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	live := func() []string {
		t.Helper()
		rows, err := b.Sessions(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		slices.Sort(ids)
		return ids
	}
	sorted := func(ids ...string) []string { slices.Sort(ids); return ids }

	playing := open(1, false)
	if _, err := b.StartStream(ctx, a, playing.ID, frontend.StreamRequest{Format: frontend.StreamDirect}); err != nil {
		t.Fatal(err)
	}
	next := open(2, true)
	if _, err := b.StartStream(ctx, a, next.ID, frontend.StreamRequest{Format: frontend.StreamDirect}); err != nil { // the client preloads it
		t.Fatal(err)
	}
	if got := live(); !slices.Equal(got, sorted(playing.ID, next.ID)) {
		t.Fatalf("queued open ended the playing session: %v", got)
	}
	if err := b.Report(ctx, a, playing.ID, frontend.Timeline{Position: 3 * time.Minute, State: "playing"}); err != nil {
		t.Fatalf("the playing session stopped taking reports: %v", err)
	}
	// A newer queued item replaces the stale queued one, never the playing one.
	after := open(3, true)
	if got := live(); !slices.Equal(got, sorted(playing.ID, after.ID)) {
		t.Fatalf("second queued open: %v", got)
	}
	// Observed playing, the queued item takes over the device.
	if err := b.Report(ctx, a, after.ID, frontend.Timeline{Position: time.Second, State: "playing"}); err != nil {
		t.Fatal(err)
	}
	if got := live(); !slices.Equal(got, []string{after.ID}) {
		t.Fatalf("activation left %v", got)
	}
	// An ordinary open still supersedes a queued item that never played.
	stale := open(4, true)
	fresh := open(5, false)
	if got := live(); !slices.Equal(got, []string{fresh.ID}) || slices.Contains(got, stale.ID) {
		t.Fatalf("ordinary open left %v", got)
	}
}
