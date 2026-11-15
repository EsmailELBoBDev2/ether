package debrid

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

// A foreground refill can use every freed slot without paying a staircase
// of dial gaps. Speculation and hosts recovering from failures stay paced.
func TestConnCapForegroundRefill(t *testing.T) {
	for _, event := range []string{"open", "seek", "early close"} {
		t.Run(event, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				const addr = "cdn:443"
				const gap = 250 * time.Millisecond
				c := newConnCap(defaultHostConnections)
				c.dialGap = gap
				background := context.WithValue(t.Context(), fetchModeKey{}, fetchReadAhead)
				// Leave spacing debt even on open; subsequent lifecycle events
				// release a full set of old connections at the same instant.
				n := 1
				if event != "open" {
					n = defaultHostConnections
				}
				for i := 0; i < n; i++ {
					if err := c.acquire(t.Context(), addr); err != nil {
						t.Fatal(err)
					}
				}
				for i := 0; i < n; i++ {
					c.release(addr)
				}
				began := time.Now()
				// Background queues before foreground and must not hold up
				// the burst, even though it owns the oldest spacing waiter.
				bgDone := make(chan error, 1)
				go func() { bgDone <- c.acquire(background, addr) }()
				synctest.Wait()
				for i := 0; i < defaultHostConnections; i++ {
					if err := c.acquire(t.Context(), addr); err != nil {
						t.Fatal(err)
					}
				}
				if elapsed := time.Since(began); elapsed != 0 {
					t.Fatalf("foreground refill spent %v in spacing", elapsed)
				}
				if got := c.inUse(addr); got != defaultHostConnections {
					t.Fatalf("refill connections = %d, want %d", got, defaultHostConnections)
				}
				// A fifth foreground dial still waits for the physical cap.
				ctx, cancel := context.WithCancel(t.Context())
				extra := make(chan error, 1)
				go func() { extra <- c.acquire(ctx, addr) }()
				synctest.Wait()
				select {
				case err := <-extra:
					t.Fatalf("extra dial escaped cap: %v", err)
				default:
				}
				cancel()
				if err := <-extra; err == nil {
					t.Fatal("cancelled extra dial admitted")
				}
				for i := 0; i < defaultHostConnections; i++ {
					c.release(addr)
				}
				if err := <-bgDone; err != nil {
					t.Fatal(err)
				}
				if elapsed := time.Since(began); elapsed != gap {
					t.Fatalf("background after burst waited %v, want %v", elapsed, gap)
				}
				c.release(addr)
				for i := 0; i < defaultHostConnections; i++ {
					before := time.Now()
					if err := c.acquire(background, addr); err != nil {
						t.Fatal(err)
					}
					if elapsed := time.Since(before); elapsed != gap {
						t.Fatalf("background dial spacing = %v, want %v", elapsed, gap)
					}
					c.release(addr)
				}
				// Hedge duplicates retain spacing despite foreground priority.
				before := time.Now()
				if err := c.acquire(context.WithValue(t.Context(), hedgeDialKey{}, true), addr); err != nil {
					t.Fatal(err)
				}
				if time.Since(before) != gap {
					t.Fatal("hedge skipped spacing")
				}
				c.release(addr)
				c.reduce(addr)
				before = time.Now()
				if err := c.acquire(t.Context(), addr); err != nil {
					t.Fatal(err)
				}
				if time.Since(before) != gap {
					t.Fatal("foreground skipped spacing on reduced host")
				}
				if limit, _ := c.current(addr); limit != 1 {
					t.Fatalf("reduced cap = %d, want 1", limit)
				}
				c.release(addr)
			})
		})
	}
}
