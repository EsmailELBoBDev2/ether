package experience

import (
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

type resumeAfterState struct {
	core.State
	after time.Duration
}

func (s resumeAfterState) ResumeAfter() time.Duration { return s.after }

// A position saved without the title's length is measured against the
// title's runtime: 79 seconds into a 170-minute film was only opened.
func TestContinueOpenedOnlyMeasuresAgainstRuntime(t *testing.T) {
	movie := core.Title{Key: core.MovieKey(1), Type: core.TypeMovie, Runtime: 170 * time.Minute}
	c := Continue{State: resumeAfterState{}}
	for _, tc := range []struct {
		name string
		st   core.WatchStatus
		t    core.Title
		want bool
	}{
		{"opened, no stored length", core.WatchStatus{ViewOffset: 79 * time.Second}, movie, true},
		{"real progress, no stored length", core.WatchStatus{ViewOffset: 30 * time.Minute}, movie, false},
		{"stored length decides in State", core.WatchStatus{ViewOffset: 79 * time.Second, Duration: 170 * time.Minute}, movie, false},
		{"no runtime known", core.WatchStatus{ViewOffset: 79 * time.Second}, core.Title{Key: movie.Key, Type: core.TypeMovie}, false},
		{"audiobook", core.WatchStatus{ViewOffset: 79 * time.Second}, core.Title{Key: movie.Key, Type: core.MediaType("track"), Runtime: 10 * time.Hour}, false},
	} {
		if got := c.openedOnly(tc.st, tc.t); got != tc.want {
			t.Errorf("%s: openedOnly = %v, want %v", tc.name, got, tc.want)
		}
	}
	// An owner threshold already applied in State; it is not second-guessed.
	owner := Continue{State: resumeAfterState{after: 30 * time.Second}}
	if owner.openedOnly(core.WatchStatus{ViewOffset: 79 * time.Second}, movie) {
		t.Fatal("owner threshold overridden")
	}
}
