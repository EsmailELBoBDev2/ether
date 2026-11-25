package core

import (
	"context"
	"sync"
)

// ProviderMissDemote is how many more "no such article" answers than
// deliveries a Usenet provider may give for one posted file before it is
// asked after its tier peers for that file's articles. A server with shorter
// retention then stops costing a round trip on every segment of an old post.
// It is still asked whenever its peers miss too, so nothing is proved
// missing that it could have served.
const ProviderMissDemote = 3

// ProviderMissHint remembers, for one posted file, which Usenet providers
// keep missing its articles. Keys are stable provider identities. Safe for
// concurrent use; the zero value is ready.
type ProviderMissHint struct {
	mu     sync.Mutex
	misses map[string]int
}

// Miss records a "no such article" answer from provider.
func (h *ProviderMissHint) Miss(provider string) {
	h.mu.Lock()
	if h.misses == nil {
		h.misses = map[string]int{}
	}
	h.misses[provider]++
	h.mu.Unlock()
}

// Hit records a delivery from provider, letting it work its way back up.
func (h *ProviderMissHint) Hit(provider string) {
	h.mu.Lock()
	if h.misses[provider] > 0 {
		h.misses[provider]--
	}
	h.mu.Unlock()
}

// Demoted reports whether provider should be asked after its tier peers.
func (h *ProviderMissHint) Demoted(provider string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.misses[provider] >= ProviderMissDemote
}

type providerMissHintKey struct{}

// WithProviderMissHint attaches a file's hint to the article requests made
// with ctx. A nil hint returns ctx unchanged.
func WithProviderMissHint(ctx context.Context, h *ProviderMissHint) context.Context {
	if h == nil {
		return ctx
	}
	return context.WithValue(ctx, providerMissHintKey{}, h)
}

// ProviderMissHintFrom returns the hint attached by WithProviderMissHint, or
// nil.
func ProviderMissHintFrom(ctx context.Context) *ProviderMissHint {
	h, _ := ctx.Value(providerMissHintKey{}).(*ProviderMissHint)
	return h
}
