package debrid

import "testing"

// The depth/parallelism decision must retain time-based headroom independently
// of single-connection throughput, while obeying memory, disk and host limits.
func TestSustainPolicy(t *testing.T) {
	const mib = 1 << 20
	for _, tt := range []struct {
		name          string
		rate, tput    float64
		budget, fixed int64
		cap           int
		window        int64
		runs          int
	}{
		{"startup", 0, 0, 128 * mib, 0, 4, 8 * mib, 2},
		{"steady UHD", 8 * mib, 4 * mib, 128 * mib, 0, 4, 100 * mib, 3},
		{"slow connection uses host cap", 8 * mib, 2 * mib, 128 * mib, 0, 4, 100 * mib, 4},
		{"fast connection", 8 * mib, 16 * mib, 128 * mib, 0, 4, 100 * mib, 1},
		{"reduced host", 8 * mib, 2 * mib, 128 * mib, 0, 1, 100 * mib, 1},
		{"configured connections", 8 * mib, 2 * mib, 128 * mib, 0, 2, 100 * mib, 2},
		{"memory only", 8 * mib, 2 * mib, 8 * mib, 0, 4, 8 * mib, 4},
		{"cache capacity", 8 * mib, 2 * mib, 24 * mib, 0, 4, 24 * mib, 4},
		{"high bitrate bounded", 100 * mib, 2 * mib, 1024 * mib, 0, 4, 128 * mib, 4},
		{"fixed window", 8 * mib, 2 * mib, 128 * mib, 16 * mib, 4, 16 * mib, 4},
		{"fixed also bounded", 8 * mib, 2 * mib, 8 * mib, 16 * mib, 4, 8 * mib, 4},
	} {
		t.Run(tt.name, func(t *testing.T) {
			window, runs := sustainPolicy(tt.rate, tt.tput, tt.budget, tt.fixed, tt.cap)
			if window != tt.window || runs != tt.runs {
				t.Fatalf("got depth=%d runs=%d; want depth=%d runs=%d", window, runs, tt.window, tt.runs)
			}
		})
	}
}
