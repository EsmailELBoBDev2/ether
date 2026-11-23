package safety

import (
	"context"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

func init() {
	// One row per title a viewer queued on a UTC day; bytes is filled in when
	// the download has been prepared. Limits are sums over a day or a month.
	store.Register("safety", 3, `CREATE TABLE safety_download_usage(user_id INTEGER NOT NULL, day TEXT NOT NULL, item_key INTEGER NOT NULL, bytes INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(user_id, day, item_key));`)
	store.RegisterViewerTable("safety", "safety_download_usage", "user_id", store.ViewerMergeDrop)
}

var _ core.DownloadLimitPolicy = (*Service)(nil)

// SetDownloadDefaults installs the server-wide download limit, read on every
// decision so a saved setting applies at once.
func (s *Service) SetDownloadDefaults(fn func() core.DownloadAllowance) {
	s.mu.Lock()
	s.downloadDefaults = fn
	s.mu.Unlock()
}

// DownloadAllowance is the server default with u's overrides. The owner is
// never limited.
func (s *Service) DownloadAllowance(u *core.User) core.DownloadAllowance {
	var out core.DownloadAllowance
	if u == nil || u.IsOwner {
		return out
	}
	s.mu.RLock()
	fn := s.downloadDefaults
	s.mu.RUnlock()
	if fn != nil {
		out = fn()
	}
	p := s.ForUser(u)
	switch p.DownloadLimitUnit {
	case "none":
		out.LimitUnit, out.LimitAmount, out.LimitPeriod = "", 0, ""
	case "titles", "gb":
		out.LimitUnit, out.LimitAmount, out.LimitPeriod = p.DownloadLimitUnit, p.DownloadLimitAmount, p.DownloadLimitPeriod
	}
	if out.LimitAmount <= 0 || (out.LimitUnit != "titles" && out.LimitUnit != "gb") {
		out.LimitUnit, out.LimitAmount = "", 0
	}
	if out.LimitPeriod != "month" {
		out.LimitPeriod = "day"
	}
	if p.DownloadMaxHeight > 0 && (out.MaxHeight == 0 || p.DownloadMaxHeight < out.MaxHeight) {
		out.MaxHeight = p.DownloadMaxHeight
	}
	out.NoTranscode = out.NoTranscode || p.DownloadTranscodingDisabled
	return out
}

// downloadPeriod is the half-open [from, to) range of UTC days the limit
// covers at now, as the day strings stored in safety_download_usage.
func downloadPeriod(period string, now time.Time) (day, from, to string) {
	now = now.UTC()
	day = now.Format("2006-01-02")
	if period == "month" {
		first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		return day, first.Format("2006-01-02"), first.AddDate(0, 1, 0).Format("2006-01-02")
	}
	return day, day, now.AddDate(0, 0, 1).Format("2006-01-02")
}

// AdmitDownload counts key as one of u's downloads today and reports whether
// u is within the limit. A title already counted today is free, so queuing it
// again or restarting it is not a new download. A storage error admits the
// download: a limit must not turn a database hiccup into a refusal.
func (s *Service) AdmitDownload(ctx context.Context, u *core.User, key core.RatingKey) (bool, string) {
	if u == nil || u.IsOwner || s.db == nil {
		return true, ""
	}
	allowance := s.DownloadAllowance(u)
	day, from, to := downloadPeriod(allowance.LimitPeriod, time.Now())
	var counted int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM safety_download_usage WHERE user_id=? AND day=? AND item_key=?`, u.ID, day, int64(key)).Scan(&counted); err != nil {
		return true, ""
	}
	if counted > 0 {
		return true, ""
	}
	if allowance.LimitUnit != "" {
		var titles, bytes int64
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(bytes),0) FROM safety_download_usage WHERE user_id=? AND day>=? AND day<?`, u.ID, from, to).Scan(&titles, &bytes); err != nil {
			return true, ""
		}
		switch allowance.LimitUnit {
		case "titles":
			if titles >= int64(allowance.LimitAmount) {
				return false, "titles"
			}
		case "gb":
			if bytes >= int64(allowance.LimitAmount)<<30 {
				return false, "gb"
			}
		}
	}
	_, _ = s.db.ExecContext(ctx, `INSERT INTO safety_download_usage(user_id,day,item_key,bytes) VALUES(?,?,?,0) ON CONFLICT(user_id,day,item_key) DO NOTHING`, u.ID, day, int64(key))
	return true, ""
}

// RecordDownloadBytes stores the size of a prepared download against the day
// it was queued, for the gigabyte limit. A download still being prepared
// counts as zero until then, so the limit is checked against finished ones.
func (s *Service) RecordDownloadBytes(ctx context.Context, u *core.User, key core.RatingKey, bytes int64) {
	if u == nil || u.IsOwner || s.db == nil || bytes <= 0 {
		return
	}
	// The newest row for the title: a download queued before midnight UTC and
	// finished after it belongs to the day it was queued.
	_, _ = s.db.ExecContext(ctx, `UPDATE safety_download_usage SET bytes=? WHERE user_id=? AND item_key=? AND day=(SELECT MAX(day) FROM safety_download_usage WHERE user_id=? AND item_key=?)`, bytes, u.ID, int64(key), u.ID, int64(key))
}
