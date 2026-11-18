package wasm

import (
	"context"
	"strings"
	"time"
)

// ViewerRequestPath identifies calls on a viewer's browse/search/playback path.
// Administrative actions, setup, catalogue sync and background jobs use
// owner-granted limits.
func ViewerRequestPath(path string) bool {
	for _, prefix := range []string{"/stream/", "/catalog/", "/meta/", "/subtitles/", "/ether/open", "/ether/metadata/", "/ether/subtitles/", "/ether/markers"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return strings.HasPrefix(path, "/ether/v1/kinds/") && !KindSyncPath(path)
}

// KindSyncPath identifies a media kind's catalogue sync calls (browse and
// changes). Ether makes them in the background to fill a library, never
// while a viewer waits, so they run on the plugin's granted limits (the
// manifest's request, capped only by the owner's ceilings) instead of the
// viewer budget. A big first sync can then take as long as it needs.
func KindSyncPath(path string) bool {
	return strings.HasPrefix(path, "/ether/v1/kinds/") &&
		(strings.HasSuffix(path, "/browse") || strings.HasSuffix(path, "/changes"))
}

// GrantedLimitsPath reports whether path runs on the plugin's owner-granted
// deadline and unit budget rather than a fixed protocol or viewer cap:
// background kind sync (/browse, /changes), jobs, setup, owner actions,
// agent tools, and event deliveries.
func GrantedLimitsPath(path string) bool {
	return KindSyncPath(path) ||
		strings.HasPrefix(path, "/ether/job/") ||
		strings.HasPrefix(path, "/ether/v1/setup/") ||
		strings.HasPrefix(path, "/ether/action/") ||
		strings.HasPrefix(path, "/ether/tool/") ||
		path == "/ether/event"
}

// UnitsForDeadline converts a call deadline into its matching governance unit
// budget at one unit per WallUnitInterval (100 µs, or 10,000 units per second),
// so a viewer route's unit budget matches the deadline it is advertised.
func UnitsForDeadline(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64(d / WallUnitInterval)
}

// StremioDeadline is the per-route deadline for a third-party Stremio add-on
// or an add-on-backed plugin (ether.stremio: true). Their stream searches
// routinely take seconds (they search trackers and debrid caches live); the
// caller's own deadline still bounds every call.
func StremioDeadline(path string) time.Duration {
	switch {
	case strings.HasPrefix(path, "/stream/"), strings.HasPrefix(path, "/catalog/"), strings.HasPrefix(path, "/manifest.json"):
		return 20 * time.Second
	case strings.HasPrefix(path, "/meta/"), strings.HasPrefix(path, "/subtitles/"):
		return 10 * time.Second
	default:
		return 30 * time.Second
	}
}

// DeadlineFor returns the baseline per-route deadline from SPEC §5.1.2.
// Routes in GrantedLimitsPath use the plugin's owner-granted deadline instead
// when dispatched through EffectiveDeadline.
func DeadlineFor(path string) time.Duration {
	switch {
	case strings.HasPrefix(path, "/ether/configure"), strings.HasPrefix(path, "/ether/health"):
		return 5 * time.Second
	case strings.HasPrefix(path, "/ether/test"):
		return 15 * time.Second
	case strings.HasPrefix(path, "/ether/options"), strings.HasPrefix(path, "/ether/view"):
		return 2 * time.Second
	case strings.HasPrefix(path, "/ether/open"), strings.HasPrefix(path, "/stream/"):
		return 3 * time.Second
	case strings.HasPrefix(path, "/ether/tool"):
		return 15 * time.Second
	case strings.HasPrefix(path, "/ether/job"):
		return 5 * time.Minute
	case strings.HasPrefix(path, "/meta/"), strings.HasPrefix(path, "/ether/metadata/"), strings.HasPrefix(path, "/catalog/"), strings.HasPrefix(path, "/ether/event"), strings.HasPrefix(path, "/ether/action"), strings.HasPrefix(path, "/ether/markers"):
		return 10 * time.Second
	case strings.HasPrefix(path, "/subtitles/"), strings.HasPrefix(path, "/ether/subtitles/"):
		return 5 * time.Second
	case strings.HasPrefix(path, "/ether/v1/setup/"):
		return 20 * time.Second
	case strings.HasPrefix(path, "/ether/v1/kinds/"):
		if strings.HasSuffix(path, "/resolve") || strings.HasSuffix(path, "/match") {
			return 5 * time.Second
		}
		return 10 * time.Second
	default:
		return 30 * time.Second
	}
}

// EffectiveDeadline returns the enforced deadline for path given the plugin's
// granted deadline ceiling and whether it speaks Stremio add-on routes.
func EffectiveDeadline(path string, granted time.Duration, stremio bool) time.Duration {
	if GrantedLimitsPath(path) {
		if granted > 0 {
			return granted
		}
		return DeadlineFor(path)
	}
	route := DeadlineFor(path)
	if stremio {
		route = StremioDeadline(path)
	}
	if granted > 0 && granted < route {
		return granted
	}
	return route
}

// WaitBackground yields before starting work and at each host-service boundary.
func WaitBackground(ctx context.Context, busy func() bool) error {
	for busy != nil && busy() {
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return ctx.Err()
}

func limitsForRequest(l Limits, path string) Limits {
	l.Deadline = EffectiveDeadline(path, l.Deadline, l.Stremio)
	if ViewerRequestPath(path) {
		l.UnitBudget = min(l.UnitBudget, UnitsForDeadline(l.Deadline))
	}
	return l
}
