package admin

import (
	"testing"

	"github.com/braedonsaunders/ether/internal/config"
)

func TestValidateDownloadsAndResumeAfter(t *testing.T) {
	base := func() config.Config {
		return config.Defaults(t.TempDir())
	}

	// Valid resumeAfterSeconds bounds (0 and 3600).
	for _, secs := range []int{0, 1, 30, 3600} {
		c := base()
		c.Server.ResumeAfterSeconds = secs
		if errs := ValidateConfig(c); hasField(errs, "server.resumeAfterSeconds") {
			t.Fatalf("resumeAfterSeconds=%d unexpectedly invalid: %v", secs, errs)
		}
	}
	for _, secs := range []int{-1, 3601} {
		c := base()
		c.Server.ResumeAfterSeconds = secs
		if errs := ValidateConfig(c); !hasField(errs, "server.resumeAfterSeconds") {
			t.Fatalf("resumeAfterSeconds=%d expected validation error, got %v", secs, errs)
		}
	}

	// Download limits validation.
	c := base()
	c.Downloads.LimitUnit = "titles"
	c.Downloads.LimitPeriod = "month"
	c.Downloads.LimitAmount = 10
	if errs := ValidateConfig(c); hasField(errs, "downloads.limitUnit") || hasField(errs, "downloads.limitPeriod") || hasField(errs, "downloads.limitAmount") {
		t.Fatalf("valid downloads config rejected: %v", errs)
	}

	c = base()
	c.Downloads.LimitUnit = "mb"
	c.Downloads.LimitPeriod = "week"
	c.Downloads.LimitAmount = 100001
	errs := ValidateConfig(c)
	for _, field := range []string{"downloads.limitUnit", "downloads.limitPeriod", "downloads.limitAmount"} {
		if !hasField(errs, field) {
			t.Fatalf("expected validation error for %s, got %v", field, errs)
		}
	}
}
