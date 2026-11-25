package debrid

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// closeBody records whether a response body was closed.
type closeBody struct {
	io.Reader
	closed atomic.Bool
	log    *bodyLog
}

func (b *closeBody) Close() error {
	b.closed.Store(true)
	if b.log.onClose != nil {
		b.log.onClose()
	}
	b.log.once.Do(func() { close(b.log.closed) })
	return nil
}

// bodyLog collects the bodies a failing leg handed out; closed is closed
// when the first of them is.
type bodyLog struct {
	mu      sync.Mutex
	bodies  []*closeBody
	closed  chan struct{}
	once    sync.Once
	onClose func()
}

func newBodyLog() *bodyLog { return &bodyLog{closed: make(chan struct{})} }

func (l *bodyLog) add(b *closeBody) {
	l.mu.Lock()
	l.bodies = append(l.bodies, b)
	l.mu.Unlock()
}

// allClosed reports how many bodies were handed out and whether all were closed.
func (l *bodyLog) allClosed() (int, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, b := range l.bodies {
		if !b.closed.Load() {
			return len(l.bodies), false
		}
	}
	return len(l.bodies), true
}

// failLeg answers every request at once with status, header and body.
func failLeg(status int, header http.Header, body string, bodies *bodyLog) deliveryTransport {
	return func(req *http.Request) (*http.Response, error) {
		b := &closeBody{Reader: strings.NewReader(body), log: bodies}
		bodies.add(b)
		h := make(http.Header)
		for k, v := range header {
			h[k] = v
		}
		return &http.Response{StatusCode: status, Header: h, Body: b, ContentLength: int64(len(body)), Request: req}, nil
	}
}

// slowLeg answers through next once open is closed, unless the request is
// cancelled first.
func slowLeg(open <-chan struct{}, calls *atomic.Int32, canceled *atomic.Int32, next http.RoundTripper) deliveryTransport {
	return func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		select {
		case <-open:
		case <-req.Context().Done():
			canceled.Add(1)
			return nil, req.Context().Err()
		}
		return next.RoundTrip(req)
	}
}

func hedgeRaceRig(t *testing.T) *engineRig {
	t.Helper()
	r := newEngineRig(t, testBlock, 10*time.Millisecond)
	cfg := r.svc.Config()
	cfg.ReadAheadChunks = -1
	r.svc.UpdateConfig(cfg)
	return r
}

func hedgeLosers() []struct {
	name   string
	status int
	header http.Header
	body   string
} {
	return []struct {
		name   string
		status int
		header http.Header
		body   string
	}{
		{"503", http.StatusServiceUnavailable, nil, "busy"},
		{"503 with Retry-After", http.StatusServiceUnavailable, http.Header{"Retry-After": {"2"}}, "busy"},
		{"429", http.StatusTooManyRequests, nil, "slow down"},
		{"200 page", http.StatusOK, http.Header{"Content-Type": {"text/html"}}, "<html>file not found</html>"},
		{"206 of another file", http.StatusPartialContent, http.Header{"Content-Range": {fmt.Sprintf("bytes 0-%d/999", testBlock-1)}}, "x"},
		{"empty 206", http.StatusPartialContent, http.Header{"Content-Range": {fmt.Sprintf("bytes 0-%d/%d", testBlock-1, testBlock)}}, ""},
	}
}

func checkHedgeRead(t *testing.T, r *engineRig, primaryCalls, canceled *atomic.Int32, bodies *bodyLog) {
	t.Helper()
	src, err := r.eng.Open(context.Background(), r.rel.ID, r.pl)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	buf := make([]byte, 1000)
	if n, err := src.ReadAt(buf, 0); err != nil || n != len(buf) || !bytes.Equal(buf, r.media.data[:len(buf)]) {
		t.Fatalf("read n=%d err=%v", n, err)
	}
	if primaryCalls.Load() != 1 || canceled.Load() != 0 {
		t.Fatalf("primary requests=%d canceled=%d, want one uninterrupted", primaryCalls.Load(), canceled.Load())
	}
	if n, closed := bodies.allClosed(); n != 1 || !closed {
		t.Fatalf("failed legs=%d closed=%v, want one with its body closed", n, closed)
	}
	r.eng.mediaPauseMu.Lock()
	paused := len(r.eng.mediaPauses)
	r.eng.mediaPauseMu.Unlock()
	if paused != 0 {
		t.Fatalf("media paused after the other leg delivered")
	}
}

// A fast HTTP error on the hedge must not end a healthy primary read.
func TestHedgeErrorLosesToLaterValidRange(t *testing.T) {
	for _, tc := range hedgeLosers() {
		t.Run(tc.name, func(t *testing.T) {
			r := hedgeRaceRig(t)
			var calls, canceled atomic.Int32
			bodies := newBodyLog()
			real := r.eng.hc.Transport
			r.eng.hc = &http.Client{Transport: slowLeg(bodies.closed, &calls, &canceled, real)}
			r.eng.hedge = &http.Client{Transport: failLeg(tc.status, tc.header, tc.body, bodies)}
			checkHedgeRead(t, r, &calls, &canceled, bodies)
			if st := r.eng.Stats(); st.Hedges != 1 || st.HedgeWins != 0 {
				t.Fatalf("stats %+v", st)
			}
		})
	}
}

// The same with the legs reversed: the primary fails after the hedge went
// out, and the hedge delivers.
func TestPrimaryErrorLosesToLaterValidHedge(t *testing.T) {
	for _, tc := range hedgeLosers() {
		t.Run(tc.name, func(t *testing.T) {
			r := hedgeRaceRig(t)
			var calls, canceled, primary atomic.Int32
			bodies := newBodyLog()
			real := r.eng.hc.Transport
			fail := failLeg(tc.status, tc.header, tc.body, bodies)
			started := make(chan struct{})
			r.eng.hc = &http.Client{Transport: slowLeg(started, &primary, &canceled, fail)}
			r.eng.hedge = &http.Client{Transport: tap(started, slowLeg(bodies.closed, &calls, &canceled, real))}
			checkHedgeRead(t, r, &calls, &canceled, bodies)
			if st := r.eng.Stats(); st.Hedges != 1 || st.HedgeWins != 1 || primary.Load() != 1 {
				t.Fatalf("stats %+v primary=%d", st, primary.Load())
			}
		})
	}
}

// raceGet runs one hedged request for bytes 0-3 of a 4-byte file.
func raceGet(t *testing.T, base context.Context, r *engineRig, primary, hedge deliveryTransport) (*http.Response, error, chan struct{}) {
	t.Helper()
	r.eng.hc = &http.Client{Transport: primary}
	r.eng.hedge = &http.Client{Transport: hedge}
	r.eng.hcReplay, r.eng.hedgeReplay = noRedirect(r.eng.hc), noRedirect(r.eng.hedge)
	slots := make(chan struct{}, 2)
	ctx, cancel := context.WithTimeout(base, 2*time.Second)
	t.Cleanup(cancel)
	resp, _, err := r.eng.get(ctx, "http://media.invalid/file", 0, 3, 4, true, slots)
	return resp, err, slots
}

// waitSlots waits for every request slot and host permit to be returned.
func waitSlots(t *testing.T, r *engineRig, slots chan struct{}) {
	t.Helper()
	addr := dialAddr("http://media.invalid/file")
	for deadline := time.Now().Add(time.Second); ; time.Sleep(time.Millisecond) {
		r.eng.gate.mu.Lock()
		busy := r.eng.gate.busy[addr]
		r.eng.gate.mu.Unlock()
		if len(slots) == 0 && busy == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("held slots=%d host permits=%d", len(slots), busy)
		}
	}
}

// gated answers through next once open is closed, unless the request is
// cancelled first.
func gated(open <-chan struct{}, next deliveryTransport) deliveryTransport {
	return func(req *http.Request) (*http.Response, error) {
		select {
		case <-open:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
		return next(req)
	}
}

// tap closes started when the request goes out.
func tap(started chan struct{}, next deliveryTransport) deliveryTransport {
	return func(req *http.Request) (*http.Response, error) {
		close(started)
		return next(req)
	}
}

func dropped(req *http.Request) (*http.Response, error) { return nil, io.ErrUnexpectedEOF }

func rangeLeg(status int, header http.Header, body string) deliveryTransport {
	return failLeg(status, header, body, newBodyLog())
}

// When both legs fail the caller gets an HTTP answer before a transport
// error: a throttle first, then a dead-link answer over a bare 5xx, and the
// later answer between equals. The second leg answers only after the first
// one's body was closed.
func TestHedgeDoubleFailureKeepsTheTellingAnswer(t *testing.T) {
	ra := http.Header{"Retry-After": {"2"}}
	type leg struct {
		status int // 0: the connection drops
		header http.Header
	}
	for _, tc := range []struct {
		name          string
		first, second leg
		primaryFirst  bool
		replay        bool
		status        int
	}{
		{"bare 503 does not mask 404", leg{503, nil}, leg{404, nil}, false, false, 404},
		{"bare 503 does not mask 403 the other way", leg{403, nil}, leg{503, nil}, true, false, 403},
		{"503 with Retry-After over 404", leg{503, ra}, leg{404, nil}, false, false, 503},
		{"429 over 410", leg{429, nil}, leg{410, nil}, false, false, 429},
		{"429 over a dropped connection", leg{429, nil}, leg{}, false, false, 429},
		{"404 over a dropped connection", leg{404, nil}, leg{}, true, false, 404},
		{"equal rank: the later answer", leg{404, nil}, leg{410, nil}, false, false, 410},
		{"replayed edge redirect over 503", leg{302, nil}, leg{503, nil}, false, true, 302},
		{"unmarked redirect ranks with 503", leg{302, nil}, leg{503, nil}, false, false, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newEngineRig(t, testBlock, 10*time.Millisecond)
			build := func(l leg, bodies *bodyLog) deliveryTransport {
				if l.status == 0 {
					return dropped
				}
				return failLeg(l.status, l.header, "", bodies)
			}
			fb := newBodyLog()
			first, second := build(tc.first, fb), gated(fb.closed, build(tc.second, newBodyLog()))
			primary, hedge := second, first
			if tc.primaryFirst {
				started := make(chan struct{})
				primary, hedge = gated(started, first), tap(started, second)
			}
			ctx := context.Background()
			if tc.replay {
				ctx = withReplay(ctx)
			}
			resp, err, slots := raceGet(t, ctx, r, primary, hedge)
			if err != nil || resp.StatusCode != tc.status {
				t.Fatalf("resp=%v err=%v, want HTTP %d", resp, err, tc.status)
			}
			resp.Body.Close()
			waitSlots(t, r, slots)
		})
	}
}

// An empty 206 is a cut connection: with the other leg dropped too the
// caller sees an error, not a successful empty range.
func TestHedgeEmpty206AndDroppedLegIsAnError(t *testing.T) {
	r := newEngineRig(t, testBlock, 10*time.Millisecond)
	bodies := newBodyLog()
	empty := failLeg(206, http.Header{"Content-Range": {"bytes 0-3/4"}}, "", bodies)
	resp, err, slots := raceGet(t, context.Background(), r, gated(bodies.closed, dropped), empty)
	if err == nil {
		resp.Body.Close()
		t.Fatalf("empty range returned as HTTP %d", resp.StatusCode)
	}
	waitSlots(t, r, slots)
}

// A 200 that announces the file size but ends before its first byte loses
// to the other leg's range.
func TestHedgeEmptyExactSize200Loses(t *testing.T) {
	r := newEngineRig(t, testBlock, 10*time.Millisecond)
	bodies := newBodyLog()
	empty := func(req *http.Request) (*http.Response, error) {
		b := &closeBody{Reader: strings.NewReader(""), log: bodies}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: b, ContentLength: 4, Request: req}, nil
	}
	resp, err, slots := raceGet(t, context.Background(), r, gated(bodies.closed, rangeLeg(206, nil, "data")), empty)
	if err != nil || resp.StatusCode != 206 {
		t.Fatalf("resp=%v err=%v, want the 206", resp, err)
	}
	if b, _ := io.ReadAll(resp.Body); string(b) != "data" {
		t.Fatalf("body %q", b)
	}
	resp.Body.Close()
	waitSlots(t, r, slots)
}

func TestHedgeExactSize200Wins(t *testing.T) {
	r := newEngineRig(t, testBlock, 10*time.Millisecond)
	whole := rangeLeg(200, nil, "data")
	resp, err, slots := raceGet(t, context.Background(), r, gated(make(chan struct{}), rangeLeg(206, nil, "data")), whole)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
	if b, _ := io.ReadAll(resp.Body); string(b) != "data" {
		t.Fatalf("body %q", b)
	}
	resp.Body.Close()
	waitSlots(t, r, slots)
	if st := r.eng.Stats(); st.HedgeWins != 1 {
		t.Fatalf("stats %+v", st)
	}
}

// A primary that answers with an HTTP error before the hedge delay gets no
// duplicate; one whose connection fails still does.
func TestHedgeAfterPrimaryAnswer(t *testing.T) {
	r := newEngineRig(t, testBlock, 30*time.Millisecond)
	var hedges atomic.Int32
	count := func(next deliveryTransport) deliveryTransport {
		return func(req *http.Request) (*http.Response, error) { hedges.Add(1); return next(req) }
	}
	resp, err, slots := raceGet(t, context.Background(), r, rangeLeg(429, nil, ""), count(rangeLeg(206, nil, "data")))
	if err != nil || resp.StatusCode != 429 {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
	resp.Body.Close()
	waitSlots(t, r, slots)
	time.Sleep(60 * time.Millisecond)
	if hedges.Load() != 0 {
		t.Fatalf("hedged after an HTTP answer: %d", hedges.Load())
	}
	r = newEngineRig(t, testBlock, 30*time.Millisecond) // the 429 lowered the host's cap
	resp, err, slots = raceGet(t, context.Background(), r, dropped, count(rangeLeg(206, nil, "data")))
	if err != nil || resp.StatusCode != 206 || hedges.Load() != 1 {
		t.Fatalf("resp=%v err=%v hedges=%d", resp, err, hedges.Load())
	}
	resp.Body.Close()
	waitSlots(t, r, slots)
}

func hostCap(r *engineRig) int {
	lim, _ := r.eng.limit.current(dialAddr("http://media.invalid/file"))
	return lim
}

// A losing 429 still lowers its host's connection cap.
func TestHedgeLosing429ReducesHostCap(t *testing.T) {
	r := newEngineRig(t, testBlock, 10*time.Millisecond)
	bodies := newBodyLog()
	resp, err, slots := raceGet(t, context.Background(), r, gated(bodies.closed, rangeLeg(206, nil, "data")), failLeg(429, nil, "", bodies))
	if err != nil || resp.StatusCode != 206 {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
	resp.Body.Close()
	waitSlots(t, r, slots)
	if lim := hostCap(r); lim != 1 {
		t.Fatalf("host cap=%d after a 429", lim)
	}
}

// So does a 429 that arrives after the other leg already won.
func TestHedgeLate429ReducesHostCap(t *testing.T) {
	r := newEngineRig(t, testBlock, 10*time.Millisecond)
	started, late := make(chan struct{}), make(chan struct{})
	throttled := rangeLeg(429, nil, "")
	hedge := tap(started, func(req *http.Request) (*http.Response, error) { <-late; return throttled(req) })
	resp, err, slots := raceGet(t, context.Background(), r, gated(started, rangeLeg(206, nil, "data")), hedge)
	if err != nil || resp.StatusCode != 206 {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
	close(late)
	resp.Body.Close()
	waitSlots(t, r, slots)
	if lim := hostCap(r); lim != 1 {
		t.Fatalf("host cap=%d after a late 429", lim)
	}
}

// Cancelling the read (a seek) while a 429 is held returns the
// cancellation, not the 429, even when the other leg's cancelled request
// reports back at the same moment. The held body is closed, the other
// request cancelled, every slot and permit returned, and the 429 still
// lowers the host's cap.
func TestHedgeCancelWhileHoldingFailure(t *testing.T) {
	for i := 0; i < 20; i++ {
		r := newEngineRig(t, testBlock, 10*time.Millisecond)
		var calls, canceled atomic.Int32
		ctx, cancel := context.WithCancel(context.Background())
		bodies := newBodyLog()
		bodies.onClose = func() {
			// Cancel once the failed leg is let go, and give the other leg's
			// error time to arrive alongside the cancellation.
			cancel()
			for deadline := time.Now().Add(time.Second); canceled.Load() != 1 && time.Now().Before(deadline); {
				time.Sleep(time.Millisecond)
			}
			time.Sleep(5 * time.Millisecond)
		}
		r.eng.hc = &http.Client{Transport: slowLeg(make(chan struct{}), &calls, &canceled, http.DefaultTransport)}
		r.eng.hedge = &http.Client{Transport: failLeg(429, nil, "slow down", bodies)}
		slots := make(chan struct{}, 2)
		resp, _, err := r.eng.get(ctx, "http://media.invalid/file", 0, 3, 4, true, slots)
		if !errors.Is(err, context.Canceled) {
			if err == nil {
				resp.Body.Close()
			}
			t.Fatalf("run %d: resp=%v err=%v, want the cancellation", i, resp, err)
		}
		waitSlots(t, r, slots)
		if n, closed := bodies.allClosed(); n != 1 || !closed {
			t.Fatalf("failed legs=%d closed=%v", n, closed)
		}
		if canceled.Load() != 1 {
			t.Fatal("primary not cancelled")
		}
		if lim := hostCap(r); lim != 1 {
			t.Fatalf("host cap=%d after a held 429", lim)
		}
	}
}
