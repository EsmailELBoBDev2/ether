package core

import (
	"context"
	"time"
)

// SearchCacheRefiller requests best-effort, non-blocking candidate recovery for
// an open detail page or an admitted warm-up reusing a remembered source. It searches only;
// it must not resolve or replace a playback source. The resolver owns the
// work independently of request/warm-up cancellation and joins it on Close.
// A nil signal means skipped; otherwise the signal closes when the existing
// or newly admitted refill finishes (not necessarily successfully).
type SearchCacheRefiller interface {
	RefillSearchCache(*Title) <-chan struct{}
}

// ReleaseBlockChecker lets stored source consumers honor durable owner blocks.
// Errors must prevent reuse, rather than silently bypassing the blocklist.
type ReleaseBlockChecker interface {
	ReleaseBlocked(context.Context, string) (bool, error)
}

// PreferenceFallbackObserver records picked-version cooldowns without waiting
// for persistence, and checks only refreshed in-memory search results for recovery.
// Recovery permits a new admission attempt; normal policy and health gates still apply.
type PreferenceFallbackObserver interface {
	RecordPreferenceFallback(context.Context, ResolveRequest, string, string, time.Duration)
	PreferredVersionRecovered(*Title, string, string, time.Time) bool
}
