package livetv

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/braedonsaunders/ether/internal/datafs"
	"github.com/braedonsaunders/ether/internal/livetv/dvr"
)

const (
	dvrStorageEvery = 5 * time.Minute
	// dvrDiskFloor stops a recording before the disk is completely full.
	dvrDiskFloor = int64(512) << 20
	// dvrBytesPerSecond estimates a recording's size before it starts
	// (8 Mbit/s, a high-bitrate HD channel).
	dvrBytesPerSecond = int64(1) << 20
)

// dvrDiskUsage reads the volume holding the recordings; tests replace it.
var dvrDiskUsage = datafs.Usage

// dvrSpace is the recordings folder's use and the disk's free space.
type dvrSpace struct {
	Used      int64 `json:"usedBytes"`
	Free      int64 `json:"freeBytes"`
	FreeKnown bool  `json:"freeKnown"`
	MaxBytes  int64 `json:"maxBytes"`
	MinFree   int64 `json:"minFreeBytes"`
}

func (d *dvrRuntime) diskSpace(set DVRSettings, used int64) dvrSpace {
	sp := dvrSpace{Used: used, MaxBytes: set.MaxBytes, MinFree: set.MinFreeBytes}
	dir := set.Dir
	for dir != "" {
		if _, err := os.Stat(dir); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if _, free, err := dvrDiskUsage(dir); err == nil {
		sp.Free, sp.FreeKnown = free, true
	}
	return sp
}

// short reports whether need more bytes would break a limit.
func (sp dvrSpace) short(need int64) bool {
	if sp.MaxBytes > 0 && sp.Used+need > sp.MaxBytes {
		return true
	}
	return sp.FreeKnown && sp.MinFree > 0 && sp.Free-need < sp.MinFree
}

// cleanStorage deletes recordings their rule no longer keeps, then, while
// the folder is over its cap or the disk is short of MinFreeBytes plus
// need, the recordings that may go when space is needed (watched first,
// oldest first). It returns the space afterwards.
func (d *dvrRuntime) cleanStorage(ctx context.Context, need int64) (dvrSpace, error) {
	set := d.settings()
	recs, err := d.store.Recordings(ctx, 0)
	if err != nil {
		return dvrSpace{}, err
	}
	rules, err := d.store.Rules(ctx, 0)
	if err != nil {
		return dvrSpace{}, err
	}
	byID := make(map[string]dvr.Rule, len(rules))
	for _, r := range rules {
		byID[r.ID] = r
	}
	used := dvr.UsedBytes(recs)
	expired, evictable := dvr.Retention(recs, byID, d.watched, d.s.o.Now())
	for _, r := range expired {
		if d.removeRecording(ctx, set.Dir, r, "its rule no longer keeps it") == nil {
			used -= r.SizeBytes
		}
	}
	sp := d.diskSpace(set, used)
	for _, r := range evictable {
		if !sp.short(need) {
			break
		}
		if d.removeRecording(ctx, set.Dir, r, "space was needed") == nil {
			sp.Used -= r.SizeBytes
			sp.Free += r.SizeBytes
		}
	}
	return sp, nil
}

// removeRecording deletes a recording's file and row. A file outside the
// recordings folder (the folder was moved) is never touched or evicted.
func (d *dvrRuntime) removeRecording(ctx context.Context, dir string, r dvr.Recording, why string) error {
	if !dvrInside(dir, r.Path) {
		return os.ErrPermission
	}
	for _, p := range []string{r.Path, r.Path + dvrPartialSuffix} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			d.s.logger().Warn("dvr cannot delete recording", "path", r.Path, "error", err)
			return err
		}
	}
	// Drop folders the recording leaves empty, up to the recordings folder.
	for parent := filepath.Dir(r.Path); dvrInside(dir, parent); parent = filepath.Dir(parent) {
		if os.Remove(parent) != nil {
			break
		}
	}
	if err := d.store.DeleteRecording(ctx, r.ID); err != nil {
		return err
	}
	d.s.logger().Info("dvr recording deleted", "path", r.Path, "reason", why)
	d.recordingsChanged()
	return nil
}

// preflight makes room for a job about to start. It returns a reason when
// the job cannot record at all.
func (d *dvrRuntime) preflight(ctx context.Context, j dvr.Job) string {
	start, end := j.Window()
	if now := d.s.o.Now(); start.Before(now) {
		start = now
	}
	need := int64(end.Sub(start)/time.Second) * dvrBytesPerSecond
	sp, err := d.cleanStorage(ctx, need)
	if err != nil {
		return ""
	}
	switch {
	case sp.MaxBytes > 0 && sp.Used >= sp.MaxBytes:
		return "the recordings folder is full"
	case sp.FreeKnown && sp.Free < dvrDiskFloor:
		return "the disk is full"
	}
	return ""
}

// diskFull is checked while recording: it frees space if it can and
// reports whether the recording must stop.
func (d *dvrRuntime) diskFull(ctx context.Context) bool {
	sp, err := d.cleanStorage(ctx, 0)
	return err == nil && sp.FreeKnown && sp.Free < dvrDiskFloor
}
