package trakt

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/state"
)

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (k *testClock) now() time.Time {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.t
}

func (k *testClock) add(d time.Duration) {
	k.mu.Lock()
	k.t = k.t.Add(d)
	k.mu.Unlock()
}

func newRetryClient(t *testing.T, f *fakeTrakt, users ...int64) (*Client, *testClock) {
	t.Helper()
	c := newTestClient(t, f)
	clock := &testClock{t: time.Now()}
	c.now = clock.now
	for _, u := range users {
		connect(t, c, u)
	}
	return c, clock
}

// connectAs links userID to the Trakt account username with token access.
func connectAs(t *testing.T, c *Client, userID int64, username, access string) {
	t.Helper()
	if err := c.saveTokens(context.Background(), Tokens{UserID: userID, AccessToken: access, RefreshToken: "ref-" + access, ExpiresAt: time.Now().Add(time.Hour), Username: username}); err != nil {
		t.Fatal(err)
	}
}

func stopEvent(user int64, key core.RatingKey, frac float64, completed bool) state.Event {
	return state.Event{Kind: state.EventProgress, UserID: user, Key: key, State: "stopped", Fraction: frac, Completed: completed, At: time.Now()}
}

// acceptedStops returns the stops Trakt accepted and their progress values.
func acceptedStops(f *fakeTrakt) []float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []float64
	for _, s := range f.scrobbles {
		if s["_action"] == "stop" {
			out = append(out, s["progress"].(float64))
		}
	}
	return out
}

func pendingStops(c *Client) int {
	c.scMu.Lock()
	defer c.scMu.Unlock()
	return len(c.stops)
}

// settledStop reports whether k has a pending stop whose last send has
// been settled.
func settledStop(c *Client, k scrobbleKey) bool {
	c.scMu.Lock()
	defer c.scMu.Unlock()
	p := c.stops[k]
	return p != nil && p.attempts > 0
}

func playEvent(user int64, key core.RatingKey, st string, frac float64) state.Event {
	return state.Event{Kind: state.EventProgress, UserID: user, Key: key, State: st, Fraction: frac, At: time.Now()}
}

func TestFailedStopIsRetriedByWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &fakeTrakt{scrobbleSeq: []int{503}}
	c, clock := newRetryClient(t, f, 1)
	done := make(chan struct{})
	go func() { c.Start(ctx); close(done) }()
	defer func() { c.Close(); <-done }()
	key := core.MovieKey(949)

	c.evMu.Lock()
	c.queue = append(c.queue, stopEvent(1, key, 0.95, true))
	c.evMu.Unlock()
	c.notify <- struct{}{}

	deadline := time.Now().Add(3 * time.Second)
	for !settledStop(c, scrobbleKey{1, key}) {
		if time.Now().After(deadline) {
			t.Fatal("the failed stop was not kept for a retry")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Move the clock past the retry spacing once and wake the worker, as its
	// ticker would.
	clock.add(31 * time.Second)
	c.notify <- struct{}{}
	for len(acceptedStops(f)) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := acceptedStops(f); len(got) != 1 || got[0] != 100 {
		t.Fatalf("accepted stops %v, want one at 100", got)
	}
	if n := f.count("/scrobbling/stop"); n != 2 {
		t.Fatalf("stop requests %d, want 2", n)
	}
}

func TestFailedStopDoesNotSuppressNextStop(t *testing.T) {
	ctx := context.Background()
	f := &fakeTrakt{scrobbleSeq: []int{400}}
	c, clock := newRetryClient(t, f, 1)
	key := core.MovieKey(949)

	c.HandleEvent(ctx, stopEvent(1, key, 0.95, true))
	clock.add(10 * time.Second)
	c.HandleEvent(ctx, stopEvent(1, key, 0.96, false))
	if got := acceptedStops(f); len(got) != 1 || got[0] != 96 {
		t.Fatalf("accepted stops %v, want one at 96", got)
	}
}

func TestDuplicateStopWhilePendingIsNotSent(t *testing.T) {
	ctx := context.Background()
	f := &fakeTrakt{scrobbleSeq: []int{503}}
	c, clock := newRetryClient(t, f, 1)
	key := core.MovieKey(949)

	c.HandleEvent(ctx, stopEvent(1, key, 0.6, false))
	clock.add(10 * time.Second)
	c.HandleEvent(ctx, stopEvent(1, key, 0.95, true)) // raises the pending stop
	if n := f.count("/scrobbling/stop"); n != 1 {
		t.Fatalf("stop requests %d, want 1: a duplicate was sent while pending", n)
	}
	clock.add(31 * time.Second)
	c.retryStops(ctx)
	c.HandleEvent(ctx, stopEvent(1, key, 0.96, false)) // de-duplicated as before
	if got := acceptedStops(f); len(got) != 1 || got[0] != 100 {
		t.Fatalf("accepted stops %v, want one at 100", got)
	}
	if n := f.count("/scrobbling/stop"); n != 2 {
		t.Fatalf("stop requests %d, want 2", n)
	}
}

func TestStopRetrySpacingAndBudget(t *testing.T) {
	ctx := context.Background()
	f := &fakeTrakt{scrobbleSeq: []int{503, 502, 500, 503}}
	c, clock := newRetryClient(t, f, 1)
	key := core.MovieKey(949)
	sends := func(want int, when string) {
		t.Helper()
		if n := f.count("/scrobbling/stop"); n != want {
			t.Fatalf("%s: stop requests %d, want %d", when, n, want)
		}
	}

	c.HandleEvent(ctx, stopEvent(1, key, 0.95, true))
	sends(1, "first stop")
	c.retryStops(ctx)
	sends(1, "pass before the retry is due")
	clock.add(31 * time.Second)
	c.retryStops(ctx)
	sends(2, "first retry")
	clock.add(31 * time.Second)
	c.retryStops(ctx)
	sends(2, "pass 31s after the second failure")
	clock.add(30 * time.Second)
	c.retryStops(ctx)
	sends(3, "second retry")
	if n := pendingStops(c); n != 0 {
		t.Fatalf("pending stops %d, want 0 after three attempts", n)
	}
	clock.add(time.Minute)
	c.HandleEvent(ctx, stopEvent(1, key, 0.95, true))
	c.retryStops(ctx)
	sends(3, "identical stop after giving up")
}

func TestLostStopResponseThenConflict(t *testing.T) {
	ctx := context.Background()
	f := &fakeTrakt{scrobbleSeq: []int{scrobbleLost, 409}}
	c, clock := newRetryClient(t, f, 1)

	c.HandleEvent(ctx, stopEvent(1, core.MovieKey(949), 0.95, true))
	for range 4 {
		clock.add(time.Minute)
		c.retryStops(ctx)
	}
	c.HandleEvent(ctx, stopEvent(1, core.MovieKey(949), 0.95, true)) // the 409 counts as sent
	if n := f.count("/scrobbling/stop"); n != 2 {
		t.Fatalf("stop requests %d, want 2 (409 counts as recorded)", n)
	}
	if got := acceptedStops(f); len(got) != 1 {
		t.Fatalf("recorded stops %v, want one", got)
	}
	if n := pendingStops(c); n != 0 {
		t.Fatalf("pending stops %d, want 0", n)
	}
}

func TestStopRetriedAfterTransportError(t *testing.T) {
	ctx := context.Background()
	f := &fakeTrakt{scrobbleSeq: []int{scrobbleCut}}
	c, clock := newRetryClient(t, f, 1)

	c.HandleEvent(ctx, stopEvent(1, core.MovieKey(949), 0.95, true))
	clock.add(31 * time.Second)
	c.retryStops(ctx)
	if got := acceptedStops(f); len(got) != 1 || got[0] != 100 {
		t.Fatalf("accepted stops %v, want one at 100", got)
	}
	if n := f.count("/scrobbling/stop"); n != 2 {
		t.Fatalf("stop requests %d, want 2", n)
	}
}

func TestStopRetriedAfterTruncated503(t *testing.T) {
	ctx := context.Background()
	f := &fakeTrakt{scrobbleSeq: []int{scrobbleShort503}}
	c, clock := newRetryClient(t, f, 1)

	c.HandleEvent(ctx, stopEvent(1, core.MovieKey(949), 0.95, true))
	clock.add(31 * time.Second)
	c.retryStops(ctx)
	if got := acceptedStops(f); len(got) != 1 || got[0] != 100 {
		t.Fatalf("accepted stops %v, want one at 100", got)
	}
	if n := f.count("/scrobbling/stop"); n != 2 {
		t.Fatalf("stop requests %d, want 2", n)
	}
}

func TestStopNotRetriedOnClientError(t *testing.T) {
	ctx := context.Background()
	f := &fakeTrakt{scrobbleSeq: []int{400}}
	c, clock := newRetryClient(t, f, 1)

	c.HandleEvent(ctx, stopEvent(1, core.MovieKey(949), 0.95, true))
	clock.add(time.Minute)
	c.retryStops(ctx)
	if n := f.count("/scrobbling/stop"); n != 1 {
		t.Fatalf("stop requests %d, want 1", n)
	}
}

func TestRelinkDropsPendingStop(t *testing.T) {
	ctx := context.Background()
	f := &fakeTrakt{scrobbleSeq: []int{503}}
	c, clock := newRetryClient(t, f)
	connectAs(t, c, 1, "alice", "acc-alice")

	c.HandleEvent(ctx, stopEvent(1, core.MovieKey(949), 0.95, true))
	connectAs(t, c, 1, "bob", "acc-bob") // another Trakt account linked meanwhile
	clock.add(31 * time.Second)
	c.retryStops(ctx)
	if n := f.count("/scrobbling/stop"); n != 1 {
		t.Fatalf("stop requests %d, want 1: the stop went to the new account", n)
	}
	if n := pendingStops(c); n != 0 {
		t.Fatalf("pending stops %d, want 0", n)
	}
}

func TestNewStartCancelsPendingStop(t *testing.T) {
	ctx := context.Background()
	f := &fakeTrakt{}
	c, clock := newRetryClient(t, f, 1)
	key := core.MovieKey(949)

	c.HandleEvent(ctx, playEvent(1, key, "playing", 0.5))
	f.mu.Lock()
	f.scrobbleSeq = []int{503}
	f.mu.Unlock()
	c.HandleEvent(ctx, stopEvent(1, key, 0.6, false))
	clock.add(10 * time.Second)
	c.HandleEvent(ctx, playEvent(1, key, "playing", 0.61))
	for range 3 {
		clock.add(time.Minute)
		c.retryStops(ctx)
	}
	if n := f.count("/scrobbling/stop"); n != 1 {
		t.Fatalf("stop requests %d, want 1: the stale stop was retried", n)
	}
	if n := f.count("/scrobbling/start"); n != 2 {
		t.Fatalf("start requests %d, want 2: the restart was suppressed", n)
	}
}

func TestPlaybackReportsCancelPendingStop(t *testing.T) {
	key := core.MovieKey(949)
	cases := []struct {
		name string
		ev   state.Event
	}{
		{"playing at zero", playEvent(1, key, "playing", 0)},
		{"buffering at zero", playEvent(1, key, "buffering", 0)},
		{"paused", playEvent(1, key, "paused", 0.5)},
		{"unscrobble", state.Event{Kind: state.EventUnscrobble, UserID: 1, Key: key, At: time.Now()}},
		{"scrobble", state.Event{Kind: state.EventScrobble, UserID: 1, Key: key, At: time.Now()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := &fakeTrakt{scrobbleSeq: []int{503}}
			c, clock := newRetryClient(t, f, 1)

			c.HandleEvent(ctx, stopEvent(1, key, 0.95, true))
			c.HandleEvent(ctx, tc.ev)
			if n := pendingStops(c); n != 0 {
				t.Fatalf("pending stops %d, want 0", n)
			}
			clock.add(time.Minute)
			c.retryStops(ctx)
			if n := f.count("/scrobbling/stop"); n != 1 {
				t.Fatalf("stop requests %d, want 1", n)
			}
			// The cancelled stop's stamp is gone: the next stop is sent.
			c.HandleEvent(ctx, stopEvent(1, key, 0.95, true))
			if n := f.count("/scrobbling/stop"); n != 2 {
				t.Fatalf("stop requests %d, want 2: the next stop was suppressed", n)
			}
		})
	}
}

func TestQueuedStartIsHandledBeforeRetry(t *testing.T) {
	ctx := context.Background()
	f := &fakeTrakt{scrobbleSeq: []int{503}}
	c, clock := newRetryClient(t, f, 1)
	key := core.MovieKey(949)

	c.HandleEvent(ctx, stopEvent(1, key, 0.95, true))
	c.evMu.Lock()
	c.queue = append(c.queue, playEvent(1, key, "playing", 0.1))
	c.evMu.Unlock()
	clock.add(time.Minute)
	c.retryStops(ctx) // leaves it to the queued start
	if n := f.count("/scrobbling/stop"); n != 1 {
		t.Fatalf("stop requests %d, want 1: the retry went ahead of the queued start", n)
	}
}

func TestDisconnectDropsPendingStop(t *testing.T) {
	ctx := context.Background()
	f := &fakeTrakt{scrobbleSeq: []int{503}}
	c, clock := newRetryClient(t, f, 1)
	key := core.MovieKey(949)

	c.HandleEvent(ctx, stopEvent(1, key, 0.95, true))
	if err := c.Disconnect(ctx, 1); err != nil {
		t.Fatal(err)
	}
	connect(t, c, 1)
	clock.add(time.Minute)
	c.retryStops(ctx)
	if n := f.count("/scrobbling/stop"); n != 1 {
		t.Fatalf("stop requests %d, want 1: the dropped stop was retried", n)
	}
	c.HandleEvent(ctx, stopEvent(1, key, 0.95, true))
	if got := acceptedStops(f); len(got) != 1 {
		t.Fatalf("accepted stops %v, want the next stop sent", got)
	}
}

func TestOtherViewerHandledWhileStopPending(t *testing.T) {
	ctx := context.Background()
	f := &fakeTrakt{scrobbleSeq: []int{503}}
	c, clock := newRetryClient(t, f, 1, 2)
	key := core.MovieKey(949)

	c.HandleEvent(ctx, stopEvent(1, key, 0.95, true))
	c.HandleEvent(ctx, playEvent(2, key, "playing", 0.2))
	if n := f.count("/scrobbling/start"); n != 1 {
		t.Fatalf("viewer 2 start requests %d, want 1", n)
	}
	if n := pendingStops(c); n != 1 {
		t.Fatalf("pending stops %d, want viewer 1's", n)
	}
	clock.add(31 * time.Second)
	c.retryStops(ctx)
	if got := acceptedStops(f); len(got) != 1 || got[0] != 100 {
		t.Fatalf("accepted stops %v, want viewer 1's at 100", got)
	}
}

func TestPendingStopDroppedWhenOld(t *testing.T) {
	ctx := context.Background()
	f := &fakeTrakt{scrobbleSeq: []int{503}}
	c, clock := newRetryClient(t, f, 1)

	c.HandleEvent(ctx, stopEvent(1, core.MovieKey(949), 0.95, true))
	c.evMu.Lock() // a busy live queue does not keep it alive
	c.queue = append(c.queue, playEvent(2, core.MovieKey(603), "playing", 0.1))
	c.evMu.Unlock()
	clock.add(11 * time.Minute)
	c.retryStops(ctx)
	if n := f.count("/scrobbling/stop"); n != 1 {
		t.Fatalf("stop requests %d, want 1", n)
	}
	if n := pendingStops(c); n != 0 {
		t.Fatalf("pending stops %d, want 0", n)
	}
}
