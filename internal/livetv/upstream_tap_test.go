package livetv

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

// A provider sold by the connection sees one connection per playing
// channel: the sniff, the probe, ffmpeg and every other viewer of that
// channel share it, the account's limit refuses a further channel, and
// stopping the last viewer closes the connection before Stop returns.
func TestOneProviderConnectionPerChannel(t *testing.T) {
	var open, total atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authenticated(r) {
			http.Error(w, "denied", http.StatusUnauthorized)
			return
		}
		total.Add(1)
		open.Add(1)
		defer open.Add(-1)
		w.Header().Set("Content-Type", "video/mp2t")
		for i := 0; ; i++ {
			if _, err := fmt.Fprintf(w, "PKT%05d", i); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}))
	defer up.Close()
	tc := &fakeTranscoder{}
	s := testService(t, Options{Transcoder: tc, HTTPClient: up.Client()})
	ctx := context.Background()
	playlist := "#EXTM3U\n#EXTINF:-1 tvg-id=\"one\",One\n" + up.URL + "/live/1.ts\n#EXTINF:-1 tvg-id=\"two\",Two\n" + up.URL + "/live/2.ts\n"
	if _, err := s.ImportSource(ctx, Source{ID: "provider", Name: "Provider", Auth: relayAuth(), Playlist: playlist}); err != nil {
		t.Fatal(err)
	}
	// The account allows one connection (an Xtream panel reports it).
	e, err := s.sourceEntry(ctx, "provider")
	if err != nil {
		t.Fatal(err)
	}
	e.limit = 1
	s.setSourceEntry("provider", e)
	cs, _ := s.Channels(ctx)
	one, two := cs[0], cs[1]

	first, err := s.Tune(ctx, &core.User{ID: 1}, one.ID, core.ClientCaps{})
	if err != nil {
		t.Fatal(err)
	}
	// ffprobe and ffmpeg both read the relay input from the first byte.
	input := tc.requests[0].InputURL
	for range 2 {
		res, err := http.Get(input)
		if err != nil {
			t.Fatal(err)
		}
		head := make([]byte, 8)
		_, err = io.ReadFull(res.Body, head)
		res.Body.Close()
		if err != nil || string(head) != "PKT00000" {
			t.Fatalf("reader started at %q: %v", head, err)
		}
	}
	second, err := s.Tune(ctx, &core.User{ID: 2}, one.ID, core.ClientCaps{})
	if err != nil {
		t.Fatal(err)
	}
	if total.Load() != 1 {
		t.Fatalf("one channel, two viewers, %d provider connections", total.Load())
	}
	if _, err := s.Tune(ctx, &core.User{ID: 3}, two.ID, core.ClientCaps{}); !errors.Is(err, ErrProviderBusy) || !errors.Is(err, core.ErrStreamLimit) {
		t.Fatalf("second channel on a one-connection account: %v", err)
	}
	s.Stop(ctx, first.ID, &core.User{ID: 1})
	if s.inputRelay.taps.open("provider") != 1 {
		t.Fatal("the remaining viewer lost the shared connection")
	}
	s.Stop(ctx, second.ID, &core.User{ID: 2})
	if s.inputRelay.taps.open("provider") != 0 {
		t.Fatal("the connection outlived its last viewer")
	}
	deadline := time.Now().Add(2 * time.Second)
	for open.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if open.Load() != 0 {
		t.Fatal("provider connection still open after the last viewer stopped")
	}
	third, err := s.Tune(ctx, &core.User{ID: 3}, two.ID, core.ClientCaps{})
	if err != nil {
		t.Fatalf("freed connection not reusable: %v", err)
	}
	s.Stop(ctx, third.ID, &core.User{ID: 3})
	if total.Load() != 2 {
		t.Fatalf("provider connections = %d", total.Load())
	}
}

// New provider streams are spaced by the gap and capped per hour.
func TestTapPacesNewProviderStreams(t *testing.T) {
	var r tapRegistry
	now := time.Unix(1_000_000, 0)
	for i, want := range []time.Duration{0, 2 * time.Second, 4 * time.Second} {
		if wait, err := r.reserveOpen("p", 2*time.Second, 3, now); err != nil || wait != want {
			t.Fatalf("open %d: wait %v err %v", i, wait, err)
		}
	}
	if _, err := r.reserveOpen("p", 2*time.Second, 3, now.Add(time.Minute)); !errors.Is(err, ErrProviderPaced) {
		t.Fatalf("fourth open in the hour: %v", err)
	}
	if wait, err := r.reserveOpen("p", 2*time.Second, 3, now.Add(time.Hour+5*time.Second)); err != nil || wait != 0 {
		t.Fatalf("after the hour: %v %v", wait, err)
	}
	if wait, err := r.reserveOpen("other", 0, 0, now); err != nil || wait != 0 {
		t.Fatalf("unpaced source: %v %v", wait, err)
	}
}

// A slow background sourceEntry fill started before ImportSource/DeleteSource
// or setSourceEntry must not overwrite a fresher authCache entry once its DB
// query returns (PostgreSQL multi-connection pool race).
func TestAuthCacheRejectsStaleConcurrentFill(t *testing.T) {
	s := testService(t, Options{})
	ctx := context.Background()
	if _, err := s.ImportSource(ctx, Source{
		ID:       "provider",
		Name:     "Provider",
		Auth:     relayAuth(),
		Playlist: "#EXTM3U\n#EXTINF:-1 tvg-id=\"one\",One\nhttp://example.invalid/live/1.ts\n",
	}); err != nil {
		t.Fatal(err)
	}
	s.forgetSourceAuth("provider")

	// Simulate a background loop reading authCache.gen before a mutation, then
	// losing the race to setSourceEntry while its DB read is in flight.
	s.authCache.mu.Lock()
	staleGen := s.authCache.gen
	s.authCache.mu.Unlock()

	fresh := authEntry{limit: 1, at: s.o.Now()}
	s.setSourceEntry("provider", fresh)

	stale := authEntry{limit: 0, at: s.o.Now()}
	s.authCache.mu.Lock()
	if s.authCache.gen == staleGen {
		s.authCache.m["provider"] = stale
	}
	s.authCache.mu.Unlock()

	if got := s.sourceConnectionLimit("provider"); got != 1 {
		t.Fatalf("stale background fill overwrote connection limit: got %d, want 1", got)
	}
}

type pipeResponseWriter struct {
	hdr  http.Header
	code int
	pw   *io.PipeWriter
}

func (w *pipeResponseWriter) Header() http.Header { return w.hdr }
func (w *pipeResponseWriter) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
}
func (w *pipeResponseWriter) Write(p []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.pw.Write(p)
}
func (w *pipeResponseWriter) Flush() {}

type syncedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// An HLS-as-TS restreamer that delivers 4 s bursts every 3.5 s (2.5 s silent
// gaps) must never cut and reconnect, logs its adapted threshold once, caps the
// learned threshold at tapStallMax, keeps a continuous source at 2 s, survives
// a short post-reconnect burst followed by silence, and still detects a dead
// source within tapStallMax.
func TestTapBurstyStreamAdaptsWithoutReconnecting(t *testing.T) {
	t.Run("bursty-then-dead", func(t *testing.T) {
		db, err := store.OpenMemory(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()

		synctest.Test(t, func(t *testing.T) {
			var logs syncedBuffer
			logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
			var opens atomic.Int32
			var silentAt atomic.Int64

			client := &http.Client{Transport: lifecycleTransport(func(r *http.Request) (*http.Response, error) {
				n := opens.Add(1)
				pr, pw := io.Pipe()
				go func() {
					defer pw.Close()
					if n > 1 {
						<-r.Context().Done()
						return
					}
					gen := &tsGen{cc: map[int]byte{}, dts: 900000, gop: 12}
					// Six 4 s bursts (100 frames at 25 fps) every 3.5 s: 1.0 s
					// of delivery (11 writes spaced by 100 ms) followed by 2.5 s
					// of silence.
					for burst := range 6 {
						for step := range 11 {
							frames := 9
							if step == 0 {
								frames = 10
							}
							var chunk []byte
							for range frames {
								chunk = append(chunk, gen.next()...)
							}
							if _, werr := pw.Write(chunk); werr != nil {
								return
							}
							if step < 10 {
								select {
								case <-r.Context().Done():
									return
								case <-time.After(100 * time.Millisecond):
								}
							}
						}
						if burst < 5 {
							select {
							case <-r.Context().Done():
								return
							case <-time.After(2500 * time.Millisecond):
							}
						}
					}
					silentAt.Store(time.Now().UnixNano())
					<-r.Context().Done()
				}()
				h := make(http.Header)
				h.Set("Content-Type", "video/mp2t")
				return &http.Response{StatusCode: http.StatusOK, Header: h, Body: pr, Request: r}, nil
			})}

			s := testService(t, Options{DB: db, HTTPClient: client, Logger: logger, Stability: func() string { return stabilityBalanced }})
			s.inputRelay.host = "127.0.0.1:1"
			s.inputRelay.grants = map[[sha256.Size]byte]*inputGrant{}
			s.inputRelay.bySess = map[*session]*inputGrant{}
			s.setSourceEntry("madnet", authEntry{limit: 1, gap: 2 * time.Second, perHour: 10, at: time.Now()})

			sctx, cancel := context.WithCancel(context.Background())
			ss := &session{ctx: sctx, cancel: cancel, info: SessionInfo{ID: "cnn", Channel: Channel{ID: "cnn", Kind: "iptv", SourceID: "madnet"}}}
			if err := s.grantSteadyInput(ss, StreamSource{ID: "a", URL: "https://cdn.invalid/cnn.ts", AuthSourceID: "madnet"}, http.Header{}); err != nil {
				t.Fatal(err)
			}

			stalled := make(chan time.Time, 4)
			stopWatch := s.watchInput(ss, func(ev UpstreamEvent) {
				if ev.Kind == InputStalled {
					stalled <- ev.At
				}
			})

			relayURL, err := s.relayInput(ss)
			if err != nil {
				t.Fatal(err)
			}
			rctx, rcancel := context.WithCancel(context.Background())
			rr, rw := io.Pipe()
			go func() {
				defer rw.Close()
				req := httptest.NewRequest(http.MethodGet, relayURL, nil).WithContext(rctx)
				req.RemoteAddr = "127.0.0.1:12345"
				s.serveInputRelay(&pipeResponseWriter{hdr: make(http.Header), pw: rw}, req)
			}()
			go io.Copy(io.Discard, rr)

			// Run through five full 3.5 s cycles plus the 6th 1.0 s burst (18.5 s).
			time.Sleep(18500 * time.Millisecond)
			synctest.Wait()

			if got := opens.Load(); got != 1 {
				t.Fatalf("bursty stream opened %d provider connections, want 1", got)
			}
			select {
			case at := <-stalled:
				t.Fatalf("healthy bursty stream reported stall at %v", at)
			default:
			}
			h, ok := s.inputHealth(ss)
			if !ok || h.Reconnects != 0 || h.Stalled || !h.Connected {
				t.Fatalf("unexpected health on healthy bursty stream: %+v (ok=%v)", h, ok)
			}
			logText := logs.String()
			if n := strings.Count(logText, "live provider stall threshold adapted"); n != 1 {
				t.Fatalf("adapted threshold logged %d times, want 1:\n%s", n, logText)
			}
			if !strings.Contains(logText, "gap=2.5s") || !strings.Contains(logText, "stall_after=3.75s") {
				t.Fatalf("adapted log missing learned gap/threshold:\n%s", logText)
			}

			// The upstream is now silent for good; stall must fire within tapStallMax.
			var detected time.Time
			for range 40 {
				time.Sleep(250 * time.Millisecond)
				synctest.Wait()
				select {
				case detected = <-stalled:
				default:
				}
				if !detected.IsZero() {
					break
				}
			}
			if detected.IsZero() {
				t.Fatal("dead source was not detected")
			}
			silence := detected.Sub(time.Unix(0, silentAt.Load()))
			if silence < 3500*time.Millisecond || silence > tapStallMax {
				t.Fatalf("dead source detected after %v of silence, want within [3.5s, %v]", silence, tapStallMax)
			}

			stopWatch()
			rcancel()
			rr.Close()
			s.revokeInput(ss)
			cancel()
			s.Close()
			synctest.Wait()
		})
	})

	t.Run("cap-at-8s", func(t *testing.T) {
		db, err := store.OpenMemory(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()

		synctest.Test(t, func(t *testing.T) {
			var logs syncedBuffer
			logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
			var silentAt atomic.Int64

			client := &http.Client{Transport: lifecycleTransport(func(r *http.Request) (*http.Response, error) {
				pr, pw := io.Pipe()
				go func() {
					defer pw.Close()
					gen := &tsGen{cc: map[int]byte{}, dts: 900000, gop: 12}
					// Two 7 s bursts over 1 s separated by a 6 s silence: 1.5 * 6 s = 9 s,
					// which must clamp to tapStallMax (8 s).
					for burst := range 2 {
						for step := range 11 {
							frames := 16
							if step == 10 {
								frames = 15
							}
							var chunk []byte
							for range frames {
								chunk = append(chunk, gen.next()...)
							}
							if _, werr := pw.Write(chunk); werr != nil {
								return
							}
							if step < 10 {
								select {
								case <-r.Context().Done():
									return
								case <-time.After(100 * time.Millisecond):
								}
							}
						}
						if burst == 0 {
							select {
							case <-r.Context().Done():
								return
							case <-time.After(6 * time.Second):
							}
						}
					}
					silentAt.Store(time.Now().UnixNano())
					<-r.Context().Done()
				}()
				h := make(http.Header)
				h.Set("Content-Type", "video/mp2t")
				return &http.Response{StatusCode: http.StatusOK, Header: h, Body: pr, Request: r}, nil
			})}

			s := testService(t, Options{DB: db, HTTPClient: client, Logger: logger, Stability: func() string { return stabilityBalanced }})
			s.inputRelay.host = "127.0.0.1:1"
			s.inputRelay.grants = map[[sha256.Size]byte]*inputGrant{}
			s.inputRelay.bySess = map[*session]*inputGrant{}

			sctx, cancel := context.WithCancel(context.Background())
			ss := &session{ctx: sctx, cancel: cancel, info: SessionInfo{ID: "cap", Channel: Channel{ID: "cap", Kind: "iptv"}}}
			if err := s.grantSteadyInput(ss, StreamSource{ID: "a", URL: "https://cdn.invalid/cap.ts"}, http.Header{}); err != nil {
				t.Fatal(err)
			}

			stalled := make(chan time.Time, 2)
			stopWatch := s.watchInput(ss, func(ev UpstreamEvent) {
				if ev.Kind == InputStalled {
					stalled <- ev.At
				}
			})

			relayURL, _ := s.relayInput(ss)
			rctx, rcancel := context.WithCancel(context.Background())
			rr, rw := io.Pipe()
			go func() {
				defer rw.Close()
				req := httptest.NewRequest(http.MethodGet, relayURL, nil).WithContext(rctx)
				req.RemoteAddr = "127.0.0.1:12345"
				s.serveInputRelay(&pipeResponseWriter{hdr: make(http.Header), pw: rw}, req)
			}()
			go io.Copy(io.Discard, rr)

			// 1 s burst + 6 s silence + 1 s burst = 8 s.
			time.Sleep(8 * time.Second)
			synctest.Wait()

			if !strings.Contains(logs.String(), "gap=6s") || !strings.Contains(logs.String(), "stall_after=8s") {
				t.Fatalf("expected 6s gap clamped to stall_after=8s:\n%s", logs.String())
			}

			var detected time.Time
			for range 36 {
				time.Sleep(250 * time.Millisecond)
				synctest.Wait()
				select {
				case detected = <-stalled:
				default:
				}
				if !detected.IsZero() {
					break
				}
			}
			if detected.IsZero() {
				t.Fatal("dead source was not detected at cap")
			}
			silence := detected.Sub(time.Unix(0, silentAt.Load()))
			if silence < tapStallMax || silence > tapStallMax+250*time.Millisecond {
				t.Fatalf("capped source detected after %v, want %v (+<=250ms tick)", silence, tapStallMax)
			}

			stopWatch()
			rcancel()
			rr.Close()
			s.revokeInput(ss)
			cancel()
			s.Close()
			synctest.Wait()
		})
	})

	t.Run("continuous-stays-2s", func(t *testing.T) {
		db, err := store.OpenMemory(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()

		synctest.Test(t, func(t *testing.T) {
			var logs syncedBuffer
			logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
			var silentAt atomic.Int64

			client := &http.Client{Transport: lifecycleTransport(func(r *http.Request) (*http.Response, error) {
				pr, pw := io.Pipe()
				go func() {
					defer pw.Close()
					gen := &tsGen{cc: map[int]byte{}, dts: 900000, gop: 12}
					// Initial 6 s connect backlog, then 10 s of continuous 1x frames.
					var initBurst []byte
					for range 150 {
						initBurst = append(initBurst, gen.next()...)
					}
					if _, werr := pw.Write(initBurst); werr != nil {
						return
					}
					for range 250 {
						select {
						case <-r.Context().Done():
							return
						case <-time.After(40 * time.Millisecond):
						}
						if _, werr := pw.Write(gen.next()); werr != nil {
							return
						}
					}
					silentAt.Store(time.Now().UnixNano())
					<-r.Context().Done()
				}()
				h := make(http.Header)
				h.Set("Content-Type", "video/mp2t")
				return &http.Response{StatusCode: http.StatusOK, Header: h, Body: pr, Request: r}, nil
			})}

			s := testService(t, Options{DB: db, HTTPClient: client, Logger: logger, Stability: func() string { return stabilityBalanced }})
			s.inputRelay.host = "127.0.0.1:1"
			s.inputRelay.grants = map[[sha256.Size]byte]*inputGrant{}
			s.inputRelay.bySess = map[*session]*inputGrant{}

			sctx, cancel := context.WithCancel(context.Background())
			ss := &session{ctx: sctx, cancel: cancel, info: SessionInfo{ID: "fox", Channel: Channel{ID: "fox", Kind: "iptv"}}}
			if err := s.grantSteadyInput(ss, StreamSource{ID: "a", URL: "https://cdn.invalid/fox.ts"}, http.Header{}); err != nil {
				t.Fatal(err)
			}

			stalled := make(chan time.Time, 2)
			stopWatch := s.watchInput(ss, func(ev UpstreamEvent) {
				if ev.Kind == InputStalled {
					stalled <- ev.At
				}
			})

			relayURL, _ := s.relayInput(ss)
			rctx, rcancel := context.WithCancel(context.Background())
			rr, rw := io.Pipe()
			go func() {
				defer rw.Close()
				req := httptest.NewRequest(http.MethodGet, relayURL, nil).WithContext(rctx)
				req.RemoteAddr = "127.0.0.1:12345"
				s.serveInputRelay(&pipeResponseWriter{hdr: make(http.Header), pw: rw}, req)
			}()
			go io.Copy(io.Discard, rr)

			// 10 s of continuous streaming completes; threshold must stay 2 s.
			time.Sleep(10 * time.Second)
			synctest.Wait()

			grant := s.healthGrant(ss)
			grant.mu.Lock()
			tap := grant.tap
			grant.mu.Unlock()
			tap.mu.Lock()
			gotThreshold := tap.stallThresholdLocked(time.Now())
			tap.mu.Unlock()
			if gotThreshold != tapStallAfter {
				t.Fatalf("continuous source threshold = %v, want %v", gotThreshold, tapStallAfter)
			}
			if strings.Contains(logs.String(), "live provider stall threshold adapted") {
				t.Fatalf("continuous source logged threshold adaptation:\n%s", logs.String())
			}

			var detected time.Time
			for range 12 {
				time.Sleep(250 * time.Millisecond)
				synctest.Wait()
				select {
				case detected = <-stalled:
				default:
				}
				if !detected.IsZero() {
					break
				}
			}
			if detected.IsZero() {
				t.Fatal("continuous source stall not detected within 3 s")
			}
			silence := detected.Sub(time.Unix(0, silentAt.Load()))
			if silence < tapStallAfter || silence > tapStallAfter+250*time.Millisecond {
				t.Fatalf("continuous source stall detected after %v, want ~%v", silence, tapStallAfter)
			}

			stopWatch()
			rcancel()
			rr.Close()
			s.revokeInput(ss)
			cancel()
			s.Close()
			synctest.Wait()
		})
	})

	t.Run("post-reconnect-short-burst", func(t *testing.T) {
		db, err := store.OpenMemory(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()

		synctest.Test(t, func(t *testing.T) {
			var opens atomic.Int32
			client := &http.Client{Transport: lifecycleTransport(func(r *http.Request) (*http.Response, error) {
				n := opens.Add(1)
				pr, pw := io.Pipe()
				go func() {
					defer pw.Close()
					gen := &tsGen{cc: map[int]byte{}, dts: int64(n) * 900000, gop: 12}
					// Short 1 s initial burst (mid-segment join), then silence.
					var short []byte
					for range 25 {
						short = append(short, gen.next()...)
					}
					if _, werr := pw.Write(short); werr != nil {
						return
					}
					if n == 1 {
						// First connection drops/stalls before the next burst.
						<-r.Context().Done()
						return
					}
					// Reconnected stream waits 3.5 s after the short burst,
					// then delivers healthy 4 s bursts every 3.5 s.
					for range 4 {
						select {
						case <-r.Context().Done():
							return
						case <-time.After(3500 * time.Millisecond):
						}
						var burst []byte
						for range 100 {
							burst = append(burst, gen.next()...)
						}
						if _, werr := pw.Write(burst); werr != nil {
							return
						}
					}
					<-r.Context().Done()
				}()
				h := make(http.Header)
				h.Set("Content-Type", "video/mp2t")
				return &http.Response{StatusCode: http.StatusOK, Header: h, Body: pr, Request: r}, nil
			})}

			s := testService(t, Options{DB: db, HTTPClient: client})
			s.inputRelay.host = "127.0.0.1:1"
			s.inputRelay.grants = map[[sha256.Size]byte]*inputGrant{}
			s.inputRelay.bySess = map[*session]*inputGrant{}
			s.setSourceEntry("madnet", authEntry{limit: 1, gap: 2 * time.Second, perHour: 5, at: time.Now()})

			sctx, cancel := context.WithCancel(context.Background())
			ss := &session{ctx: sctx, cancel: cancel, info: SessionInfo{ID: "short", Channel: Channel{ID: "short", Kind: "iptv", SourceID: "madnet"}}}
			if err := s.grantInput(ss, StreamSource{ID: "a", URL: "https://cdn.invalid/short.ts", AuthSourceID: "madnet"}, http.Header{}); err != nil {
				t.Fatal(err)
			}

			relayURL, _ := s.relayInput(ss)
			rctx, rcancel := context.WithCancel(context.Background())
			rr, rw := io.Pipe()
			go func() {
				defer rw.Close()
				req := httptest.NewRequest(http.MethodGet, relayURL, nil).WithContext(rctx)
				req.RemoteAddr = "127.0.0.1:12345"
				s.serveInputRelay(&pipeResponseWriter{hdr: make(http.Header), pw: rw}, req)
			}()
			go io.Copy(io.Discard, rr)

			// 2 s initial cut + 2 s tune gap + 4 * 3.5 s bursty cycles = 18 s.
			time.Sleep(18 * time.Second)
			synctest.Wait()

			if got := opens.Load(); got != 2 {
				t.Fatalf("post-reconnect short burst caused %d opens, want 2 (no reconnect loop)", got)
			}

			rcancel()
			rr.Close()
			s.revokeInput(ss)
			cancel()
			s.Close()
			synctest.Wait()
		})
	})
}
