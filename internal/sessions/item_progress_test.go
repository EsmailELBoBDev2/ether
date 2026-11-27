package sessions

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
)

func TestReportItemProgressWritesJellyfinWatchState(t *testing.T) {
	ctx := context.Background()
	active := func(_ context.Context, id int64) (*core.Viewer, error) { return &core.Viewer{ID: id}, nil }
	events := NewEventBus(nil, active)
	defer events.Close()
	progress := &fakeProgress{}
	key := core.MovieKey(9)
	b, err := NewBroker(BrokerDeps{SurfacePolicy: testSessionPolicy,
		Events: events, Playback: &fakePlayback{sessions: map[string]*core.PlaySession{}, limit: 1}, Progress: progress, ActiveViewer: active,
		CanPlay: func(*core.Viewer, bool, time.Time) bool { return true }, BitrateLimit: func(*core.Viewer) int { return 0 },
		OnTimeline: func(*core.Viewer, core.RatingKey, time.Duration, time.Duration, string) {},
		Lookup: func(_ context.Context, _ *core.Viewer, _ core.Device, k core.RatingKey) (*core.Title, error) {
			if k != key {
				return nil, core.ErrNotFound
			}
			return &core.Title{Key: k, Type: core.TypeMovie}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close(ctx)
	access := frontend.SessionAccess{Viewer: &core.Viewer{ID: 1}, Device: core.Device{ID: "appletv"}, Protocol: "jellyfin"}
	timeline := frontend.Timeline{PlaybackID: "infuse-session", Position: 1745 * time.Second, Duration: time.Hour, State: "paused"}
	if err := b.ReportItemProgress(ctx, access, key, timeline); err != nil {
		t.Fatal(err)
	}
	if progress.calls != 1 || progress.viewer != 1 || progress.key != key || progress.position != 1745*time.Second || progress.duration != time.Hour || progress.state != "paused" {
		t.Fatalf("progress: %+v", progress)
	}
	if err := b.ReportItemProgress(ctx, frontend.SessionAccess{Viewer: access.Viewer, Device: access.Device, Protocol: "plex"}, key, timeline); !errors.Is(err, core.ErrUnauthorized) {
		t.Fatal(err)
	}
	if err := b.ReportItemProgress(ctx, access, core.MovieKey(3), timeline); !errors.Is(err, core.ErrNotFound) || progress.calls != 1 {
		t.Fatal(err, progress.calls)
	}
}

// Jellyfin Web's progress reports carry no RunTimeTicks. Without the title's
// runtime the resume rule cannot measure 5% and keeps any position past a
// minute, so a movie opened for 79 seconds stayed in Continue Watching.
func TestReportItemProgressWithoutDurationUsesTitleRuntime(t *testing.T) {
	ctx := context.Background()
	active := func(_ context.Context, id int64) (*core.Viewer, error) { return &core.Viewer{ID: id}, nil }
	events := NewEventBus(nil, active)
	defer events.Close()
	progress := &fakeProgress{}
	key := core.MovieKey(9)
	runtime := 170 * time.Minute
	b, err := NewBroker(BrokerDeps{SurfacePolicy: testSessionPolicy,
		Events: events, Playback: &fakePlayback{sessions: map[string]*core.PlaySession{}, limit: 1}, Progress: progress, ActiveViewer: active,
		CanPlay: func(*core.Viewer, bool, time.Time) bool { return true }, BitrateLimit: func(*core.Viewer) int { return 0 },
		OnTimeline: func(*core.Viewer, core.RatingKey, time.Duration, time.Duration, string) {},
		Lookup: func(_ context.Context, _ *core.Viewer, _ core.Device, k core.RatingKey) (*core.Title, error) {
			return &core.Title{Key: k, Type: core.TypeMovie, Runtime: runtime}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close(ctx)
	access := frontend.SessionAccess{Viewer: &core.Viewer{ID: 1}, Device: core.Device{ID: "browser"}, Protocol: "jellyfin"}
	if err := b.ReportItemProgress(ctx, access, key, frontend.Timeline{Position: 79 * time.Second, State: "playing"}); err != nil {
		t.Fatal(err)
	}
	if progress.calls != 1 || progress.duration != runtime {
		t.Fatalf("progress without a client duration: %+v, want duration %v", progress, runtime)
	}
	// A duration the client did send is kept.
	if err := b.ReportItemProgress(ctx, access, key, frontend.Timeline{Position: 79 * time.Second, Duration: 169 * time.Minute, State: "playing"}); err != nil {
		t.Fatal(err)
	}
	if progress.duration != 169*time.Minute {
		t.Fatalf("client duration replaced: %v", progress.duration)
	}
}
