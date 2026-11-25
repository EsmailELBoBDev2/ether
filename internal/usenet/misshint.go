package usenet

import (
	"cmp"
	"context"

	"github.com/braedonsaunders/ether/internal/core"
)

// Per-file miss hints (core.ProviderMissHint). The stream engine attaches one
// hint per posted file to its article requests; eachOnce records each
// provider's 430s and deliveries in it, and route asks a provider that keeps
// missing this file's articles after its tier peers. Within a tier only: a
// hint never moves a provider ahead of or behind another tier, and a demoted
// provider is still asked when its peers miss, so missing-article proof is
// unchanged. This matters for tiers of several providers (balanced mode):
// without it a short-retention account at the head of the rotation costs a
// round trip on every segment of an old post.

// providerID is a pool's stable provider identity, the same across the pool
// rebuilds that change pool.key.
func (p *pool) providerID() string { return cmp.Or(p.cfg.ID, p.cfg.Host) }

func samePoolTier(a, b *pool) bool {
	return a.cfg.Backfill == b.cfg.Backfill && a.tier.Load() == b.tier.Load()
}

// demoteMissed returns pools with, inside each run of one tier, the providers
// ctx's hint demotes moved behind the others (stable). pools is returned as
// is when nothing moves.
func demoteMissed(ctx context.Context, pools []*pool) []*pool {
	h := core.ProviderMissHintFrom(ctx)
	if h == nil || len(pools) < 2 {
		return pools
	}
	demoted := make([]bool, len(pools))
	moved := false
	for i, p := range pools {
		demoted[i] = h.Demoted(p.providerID())
		moved = moved || demoted[i]
	}
	if !moved {
		return pools
	}
	out := make([]*pool, 0, len(pools))
	for start := 0; start < len(pools); {
		end := start + 1
		for end < len(pools) && samePoolTier(pools[start], pools[end]) {
			end++
		}
		for _, late := range []bool{false, true} {
			for i := start; i < end; i++ {
				if demoted[i] == late {
					out = append(out, pools[i])
				}
			}
		}
		start = end
	}
	return out
}
