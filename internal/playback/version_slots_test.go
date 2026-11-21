package playback

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

type slotTestResolver struct {
	core.Resolver
	selector string
	release  string
}

func (r slotTestResolver) Resolve(ctx context.Context, req core.ResolveRequest) (*core.Resolution, error) {
	choice := req.Prefer
	if choice == r.selector {
		req.Prefer = r.release
	}
	res, err := r.Resolver.Resolve(ctx, req)
	if res != nil && choice == r.selector && res.Release.ID == r.release {
		cp := *res
		cp.VersionSelector = choice
		res = &cp
	}
	return res, err
}
func TestConfiguredVersionSlotResume(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, []*fakeRelease{mkv("A", 10000, time.Minute), mkv("B", 20000, time.Minute)}, []string{"A", "B"})
	selector := core.VersionSelectorPrefix + "test-choice"
	h.m.deps.Resolver = slotTestResolver{Resolver: h.resolver, selector: selector, release: "B"}
	caps := defaultCaps("slot-client")
	// A remembered Auto source must not replace a successful logical pick
	// simply because its concrete release id differs from the selector.
	if _, err := h.m.Prepare(ctx, owner, movieKey, caps); err != nil {
		t.Fatal(err)
	}
	picked, err := h.m.PrepareVariant(ctx, owner, movieKey, caps, selector)
	if err != nil || !picked.Resolution.MatchesVersion(selector) || picked.Resolution.Release.ID != "B" {
		t.Fatalf("slot pick: %+v %v", picked, err)
	}
	h.m.mu.Lock()
	stale := h.m.preferenceStaleLocked(movieKey, selector, h.m.now())
	h.m.mu.Unlock()
	if stale {
		t.Fatal("logical success marked unavailable")
	}
	h.m.Stop(picked.ID)
	resumed, err := h.m.PrepareVariant(ctx, owner, movieKey, caps, selector)
	if err != nil || !resumed.Resolution.MatchesVersion(selector) || resumed.Resolution.Release.ID != "B" {
		t.Fatalf("slot resume: %+v %v", resumed, err)
	}
	h.m.Stop(resumed.ID)
	missing := selector + "-unavailable"
	h.m.deps.Resolver = slotTestResolver{Resolver: h.resolver, selector: missing, release: "missing"}
	fallback, err := h.m.PrepareVariant(ctx, owner, movieKey, caps, missing)
	if err != nil || fallback == nil || fallback.Resolution.MatchesVersion(missing) {
		t.Fatalf("unavailable slot fallback: %+v %v", fallback, err)
	}
	h.m.mu.Lock()
	stale = h.m.preferenceStaleLocked(movieKey, missing, h.m.now())
	h.m.mu.Unlock()
	if !stale {
		t.Fatal("unavailable slot did not retain the normal fallback cooldown")
	}

}

func TestConfiguredVersionSlotResumePersistsSelectorAndPreservesAuto(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	selector := core.VersionSelectorPrefix + "eyJrIjoidGllciIsImgiOjIxNjB9"
	caps := defaultCaps("slot-persist")

	h1 := newHarness(t, []*fakeRelease{mkv("A", 10000, time.Minute), mkv("B", 20000, time.Minute)}, []string{"A", "B"}, func(h *harness, o *Options) {
		h.db = db
		h.dataDir = dir
		o.DataDir = dir
	})
	h1.m.deps.Resolver = slotTestResolver{Resolver: h1.resolver, selector: selector, release: "B"}

	picked, err := h1.m.PrepareVariant(ctx, owner, movieKey, caps, selector)
	if err != nil || picked.Resolution.Release.ID != "B" || !picked.Resolution.MatchesVersion(selector) {
		t.Fatalf("slot pick: %+v %v", picked, err)
	}
	h1.m.Stop(picked.ID)
	h1.m.Close()
	db.Close()

	// Reopen DB on a fresh Manager simulating restart: version_selector must
	// survive persistence so ResumedVersion reports the explicit slot, replaying
	// that slot reuses B, and plain Auto Prepare does not hijack Auto with B.
	db2, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	h2 := newHarness(t, []*fakeRelease{mkv("A", 10000, time.Minute), mkv("B", 20000, time.Minute)}, []string{"A", "B"}, func(h *harness, o *Options) {
		h.db = db2
		h.dataDir = dir
		o.DataDir = dir
	})
	defer h2.m.Close()
	h2.m.deps.Resolver = slotTestResolver{Resolver: h2.resolver, selector: selector, release: "B"}

	info, rel, gotSel, ok := h2.m.ResumedVersion(ctx, owner, movieKey, caps)
	if !ok || rel.ID != "B" || gotSel != selector || info.Estimated {
		t.Fatalf("ResumedVersion after restart = (%v, %s, %q, %v), want B with selector %q", info.Estimated, rel.ID, gotSel, ok, selector)
	}

	// Replaying the explicit slot resumes B directly without re-resolving.
	callsBefore := h2.resolver.callCount()
	replayed, err := h2.m.PrepareVariant(ctx, owner, movieKey, caps, selector)
	if err != nil || replayed.Resolution.Release.ID != "B" || !replayed.Resolution.MatchesVersion(selector) {
		t.Fatalf("slot replay: %+v %v", replayed, err)
	}
	if h2.resolver.callCount() != callsBefore {
		t.Fatalf("slot replay re-resolved (%d -> %d)", callsBefore, h2.resolver.callCount())
	}
	h2.m.Stop(replayed.ID)

	// Plain Auto Prepare must resolve Auto (A) rather than hijacking Auto with
	// the explicit non-Auto slot pick (B).
	autoSess, err := h2.m.Prepare(ctx, owner, movieKey, caps)
	if err != nil || autoSess.Resolution.Release.ID != "A" {
		t.Fatalf("Auto prepare after explicit slot pick = %+v, %v; want A", autoSess, err)
	}
	h2.m.Stop(autoSess.ID)
}
