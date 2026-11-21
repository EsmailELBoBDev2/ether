package adapters

import "time"

// FirstOpenWait returns the current first-open facet/person expansion wait.
func FirstOpenWait() time.Duration {
	return firstOpenWait
}

// SetFirstOpenWait overrides how long a first open waits for a fill and
// returns a function that restores the previous value.
func SetFirstOpenWait(d time.Duration) (restore func()) {
	prev := firstOpenWait
	firstOpenWait = d
	return func() { firstOpenWait = prev }
}
