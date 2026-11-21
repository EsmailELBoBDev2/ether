package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/stream"
)

const (
	maxUsenetCheckCacheEntries = 1024
	usenetCheckPositiveTTL     = 15 * time.Minute
	usenetCheckNegativeTTL     = 30 * time.Minute
	usenetCheckTimeout         = 15 * time.Second
)

type usenetArticleChecker interface {
	CheckArticles(ctx context.Context, releaseID string, nzbData []byte) (core.UsenetCheckResult, error)
}

type usenetCheckEntry struct {
	result      core.UsenetCheckResult
	releaseKeys []string
	nzbHash     string
	storedAt    time.Time
	expiresAt   time.Time
}

func normalizeNZBKey(nzbURL string) string {
	return strings.TrimSpace(nzbURL)
}

func hashNZBBytes(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (r *KindResolver) checkUsenetCandidate(ctx context.Context, cand core.SourceCandidate) (core.UsenetCheckResult, error) {
	releaseKey := normalizeNZBKey(cand.NZBURL)
	if releaseKey == "" {
		return core.UsenetCheckResult{}, fmt.Errorf("kindresolver: usenet_check requires an nzbUrl")
	}
	if cached, ok := r.lookupUsenetCheck(releaseKey, ""); ok {
		return cached, nil
	}
	if r.usenetServing != nil && !r.usenetServing() {
		return core.UsenetCheckResult{}, core.ErrNotAvailable
	}
	if r.indexer == nil || r.engine == nil {
		return core.UsenetCheckResult{}, core.ErrNotAvailable
	}
	checker, ok := r.engine.(usenetArticleChecker)
	if !ok {
		return core.UsenetCheckResult{}, core.ErrNotAvailable
	}

	// Build a whole-NZB release descriptor (ignoring any fileIndex on the
	// candidate so FetchNZB returns the un-narrowed NZB through its
	// originating indexer, sharing the vault with playback).
	wholeCand := cand
	wholeCand.FileIndex = nil
	rel, err := releaseFromHint(core.ReleaseHint{Candidate: &wholeCand}, "plugin-kind-usenet-check")
	if err != nil {
		return core.UsenetCheckResult{}, err
	}
	rel.Protocol = core.ProtocolUsenet
	rel.Target = nil

	data, err := r.indexer.FetchNZB(ctx, rel)
	if err != nil {
		return core.UsenetCheckResult{}, err
	}
	nzbHash := hashNZBBytes(data)
	if cached, ok := r.lookupUsenetCheck(releaseKey, nzbHash); ok {
		return cached, nil
	}

	checkCtx := ctx
	if !core.IsBackgroundCheck(checkCtx) {
		checkCtx = core.WithBackgroundBudget(checkCtx, func(context.Context, string, string, int64) error { return nil })
	}
	var cancel context.CancelFunc
	if deadline, hasDeadline := checkCtx.Deadline(); !hasDeadline || time.Until(deadline) > usenetCheckTimeout {
		checkCtx, cancel = context.WithTimeout(checkCtx, usenetCheckTimeout)
		defer cancel()
	}

	res, err := checker.CheckArticles(checkCtx, rel.ID, data)
	if err != nil {
		return core.UsenetCheckResult{}, err
	}
	res.Cached = false
	res.CheckedAt = r.now().Unix()
	return r.storeUsenetCheck(releaseKey, nzbHash, res), nil
}

func (r *KindResolver) initUsenetCheckCacheLocked() {
	if r.usenetByRelease == nil {
		r.usenetByRelease = make(map[string]*usenetCheckEntry)
	}
	if r.usenetByHash == nil {
		r.usenetByHash = make(map[string]*usenetCheckEntry)
	}
	if r.usenetEntries == nil {
		r.usenetEntries = make(map[*usenetCheckEntry]struct{})
	}
}

func (r *KindResolver) deleteUsenetEntryLocked(e *usenetCheckEntry) {
	if e == nil {
		return
	}
	for _, k := range e.releaseKeys {
		if r.usenetByRelease[k] == e {
			delete(r.usenetByRelease, k)
		}
	}
	if e.nzbHash != "" && r.usenetByHash[e.nzbHash] == e {
		delete(r.usenetByHash, e.nzbHash)
	}
	delete(r.usenetEntries, e)
}

func (r *KindResolver) detachReleaseKeyLocked(e *usenetCheckEntry, releaseKey string) {
	if e == nil {
		return
	}
	e.releaseKeys = slices.DeleteFunc(e.releaseKeys, func(k string) bool { return k == releaseKey })
	if len(e.releaseKeys) == 0 && (e.nzbHash == "" || r.usenetByHash[e.nzbHash] != e) {
		delete(r.usenetEntries, e)
	}
}

func (r *KindResolver) attachReleaseKeyLocked(e *usenetCheckEntry, releaseKey string) {
	if e == nil || releaseKey == "" {
		return
	}
	if old := r.usenetByRelease[releaseKey]; old != nil && old != e {
		r.detachReleaseKeyLocked(old, releaseKey)
	}
	r.usenetByRelease[releaseKey] = e
	if !slices.Contains(e.releaseKeys, releaseKey) {
		e.releaseKeys = append(e.releaseKeys, releaseKey)
	}
}

func (r *KindResolver) attachHashLocked(e *usenetCheckEntry, nzbHash string) {
	if e == nil || nzbHash == "" {
		return
	}
	if old := r.usenetByHash[nzbHash]; old != nil && old != e {
		old.nzbHash = ""
		if len(old.releaseKeys) == 0 {
			delete(r.usenetEntries, old)
		}
	}
	if e.nzbHash != "" && e.nzbHash != nzbHash && r.usenetByHash[e.nzbHash] == e {
		delete(r.usenetByHash, e.nzbHash)
	}
	r.usenetByHash[nzbHash] = e
	e.nzbHash = nzbHash
}

func (r *KindResolver) pruneUsenetCheckLocked(now time.Time) {
	for e := range r.usenetEntries {
		if now.After(e.expiresAt) {
			r.deleteUsenetEntryLocked(e)
		}
	}
	for len(r.usenetEntries) >= maxUsenetCheckCacheEntries {
		var oldest *usenetCheckEntry
		for e := range r.usenetEntries {
			if oldest == nil || e.storedAt.Before(oldest.storedAt) {
				oldest = e
			}
		}
		if oldest == nil {
			break
		}
		r.deleteUsenetEntryLocked(oldest)
	}
}

func (r *KindResolver) lookupUsenetCheck(releaseKey, nzbHash string) (core.UsenetCheckResult, bool) {
	r.usenetCheckMu.Lock()
	defer r.usenetCheckMu.Unlock()
	now := r.now()

	if releaseKey != "" {
		if e := r.usenetByRelease[releaseKey]; e != nil {
			if now.After(e.expiresAt) {
				r.deleteUsenetEntryLocked(e)
			} else {
				if nzbHash != "" && e.nzbHash == "" {
					r.attachHashLocked(e, nzbHash)
				}
				res := e.result
				res.Cached = true
				return res, true
			}
		}
	}
	if nzbHash != "" {
		if e := r.usenetByHash[nzbHash]; e != nil {
			if now.After(e.expiresAt) {
				r.deleteUsenetEntryLocked(e)
			} else {
				if releaseKey != "" {
					r.attachReleaseKeyLocked(e, releaseKey)
				}
				res := e.result
				res.Cached = true
				return res, true
			}
		}
	}
	return core.UsenetCheckResult{}, false
}

func (r *KindResolver) storeUsenetCheck(releaseKey, nzbHash string, res core.UsenetCheckResult) core.UsenetCheckResult {
	r.usenetCheckMu.Lock()
	defer r.usenetCheckMu.Unlock()
	r.initUsenetCheckCacheLocked()
	now := r.now()

	// Do not let a concurrent sampled check overwrite a recorded playback failure.
	for _, existing := range []*usenetCheckEntry{r.usenetByRelease[releaseKey], r.usenetByHash[nzbHash]} {
		if existing == nil {
			continue
		}
		if now.After(existing.expiresAt) {
			r.deleteUsenetEntryLocked(existing)
			continue
		}
		if existing.result.PlaybackFailed && !res.PlaybackFailed {
			r.attachReleaseKeyLocked(existing, releaseKey)
			r.attachHashLocked(existing, nzbHash)
			out := existing.result
			out.Cached = true
			return out
		}
	}

	ttl := usenetCheckPositiveTTL
	if !res.Available {
		ttl = usenetCheckNegativeTTL
	}
	res.Cached = false
	if res.CheckedAt == 0 {
		res.CheckedAt = now.Unix()
	}

	r.pruneUsenetCheckLocked(now)
	e := &usenetCheckEntry{
		result:    res,
		storedAt:  now,
		expiresAt: now.Add(ttl),
	}
	r.usenetEntries[e] = struct{}{}
	r.attachReleaseKeyLocked(e, releaseKey)
	r.attachHashLocked(e, nzbHash)
	return res
}

func (r *KindResolver) recordUsenetPlaybackFailure(releaseKey, nzbHash string, err error) {
	reason, articleMissing, ok := classifyUsenetPlaybackFailure(err)
	if !ok || (releaseKey == "" && nzbHash == "") {
		return
	}

	r.usenetCheckMu.Lock()
	defer r.usenetCheckMu.Unlock()
	r.initUsenetCheckCacheLocked()
	now := r.now()

	var eRel, eHash *usenetCheckEntry
	if releaseKey != "" {
		if e := r.usenetByRelease[releaseKey]; e != nil {
			if now.After(e.expiresAt) {
				r.deleteUsenetEntryLocked(e)
			} else {
				eRel = e
			}
		}
	}
	if nzbHash != "" {
		if e := r.usenetByHash[nzbHash]; e != nil {
			if now.After(e.expiresAt) {
				r.deleteUsenetEntryLocked(e)
			} else {
				eHash = e
			}
		}
	}

	e := eRel
	if e == nil {
		e = eHash
	} else if eHash != nil && eHash != eRel {
		// Merge any release keys from the hash-indexed entry so every alias
		// sees the playback failure on the unified entry.
		extraKeys := slices.Clone(eHash.releaseKeys)
		r.deleteUsenetEntryLocked(eHash)
		for _, k := range extraKeys {
			r.attachReleaseKeyLocked(e, k)
		}
	}

	var prevChecked, prevMissing int
	if e != nil {
		prevChecked = e.result.Checked
		prevMissing = e.result.Missing
	}
	checked, missing := prevChecked, prevMissing
	if articleMissing {
		missing = max(prevMissing, 1)
		checked = max(prevChecked, missing)
	}
	res := core.UsenetCheckResult{
		Available:      false,
		Checked:        checked,
		Missing:        missing,
		PlaybackFailed: true,
		Reason:         reason,
		CheckedAt:      now.Unix(),
	}

	if e != nil {
		e.result = res
		e.storedAt = now
		e.expiresAt = now.Add(usenetCheckNegativeTTL)
		r.attachReleaseKeyLocked(e, releaseKey)
		r.attachHashLocked(e, nzbHash)
		return
	}

	r.pruneUsenetCheckLocked(now)
	e = &usenetCheckEntry{
		result:    res,
		storedAt:  now,
		expiresAt: now.Add(usenetCheckNegativeTTL),
	}
	r.usenetEntries[e] = struct{}{}
	r.attachReleaseKeyLocked(e, releaseKey)
	r.attachHashLocked(e, nzbHash)
}

func classifyUsenetPlaybackFailure(err error) (reason string, articleMissing bool, ok bool) {
	if err == nil ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, stream.ErrClosed) {
		return "", false, false
	}
	// Provider backoff / transient unavailability must not taint the NZB.
	if errors.Is(err, core.ErrSourceUnavailable) || errors.Is(err, core.ErrNotAvailable) {
		return "", false, false
	}
	switch {
	case errors.Is(err, core.ErrArticleMissing), errors.Is(err, stream.ErrIncomplete):
		return "playback_article_missing", true, true
	case errors.Is(err, core.ErrArticleCorrupt), errors.Is(err, stream.ErrCorrupt):
		return "playback_article_corrupt", false, true
	case errors.Is(err, core.ErrArchiveUnstreamable):
		return "playback_archive_unstreamable", false, true
	case errors.Is(err, core.ErrReleaseRejected), errors.Is(err, core.ErrNoUsableFile), stream.IsRejection(err):
		return "playback_release_rejected", false, true
	default:
		return "", false, false
	}
}

func (r *KindResolver) wrapUsenetPlaybackSource(ms core.MediaSource, releaseKey, nzbHash string) core.MediaSource {
	if ms == nil || (releaseKey == "" && nzbHash == "") {
		return ms
	}
	record := func(err error) {
		r.recordUsenetPlaybackFailure(releaseKey, nzbHash, err)
	}
	base := &usenetObservedSource{
		MediaSource: ms,
		record:      record,
	}
	if avs, ok := ms.(core.ArchiveVolumeSource); ok {
		return &usenetObservedArchiveSource{
			usenetObservedSource: base,
			avs:                  avs,
		}
	}
	return base
}

type usenetObservedSource struct {
	core.MediaSource
	record func(error)
}

func (s *usenetObservedSource) ReadAt(p []byte, off int64) (int, error) {
	n, err := s.MediaSource.ReadAt(p, off)
	if err != nil {
		s.record(err)
	}
	return n, err
}

func (s *usenetObservedSource) ReadAtContext(ctx context.Context, p []byte, off int64) (int, error) {
	var (
		n   int
		err error
	)
	if cr, ok := s.MediaSource.(core.ContextReaderAt); ok {
		n, err = cr.ReadAtContext(ctx, p, off)
	} else {
		n, err = s.MediaSource.ReadAt(p, off)
	}
	if err != nil {
		s.record(err)
	}
	return n, err
}

func (s *usenetObservedSource) ReadMetadataAtContext(ctx context.Context, p []byte, off int64) (int, error) {
	var (
		n   int
		err error
	)
	if mr, ok := s.MediaSource.(core.MetadataReaderAt); ok {
		n, err = mr.ReadMetadataAtContext(ctx, p, off)
	} else {
		return s.ReadAtContext(ctx, p, off)
	}
	if err != nil {
		s.record(err)
	}
	return n, err
}

func (s *usenetObservedSource) ReadAtPrefetchContext(ctx context.Context, p []byte, off int64) (int, error) {
	var (
		n   int
		err error
	)
	if pr, ok := s.MediaSource.(core.PrefetchReaderAt); ok {
		n, err = pr.ReadAtPrefetchContext(ctx, p, off)
	} else {
		return s.ReadAtContext(ctx, p, off)
	}
	if err != nil {
		s.record(err)
	}
	return n, err
}

func (s *usenetObservedSource) BufferedAhead() int64 {
	if bs, ok := s.MediaSource.(core.BufferedSource); ok {
		return bs.BufferedAhead()
	}
	return 0
}

func (s *usenetObservedSource) SetPlaybackDuration(d time.Duration) {
	if ps, ok := s.MediaSource.(core.PlaybackDurationSetter); ok {
		ps.SetPlaybackDuration(d)
	}
}

func (s *usenetObservedSource) ResolvedArchiveMember() bool {
	if rm, ok := s.MediaSource.(core.ResolvedMemberSource); ok {
		return rm.ResolvedArchiveMember()
	}
	return false
}

type usenetObservedArchiveSource struct {
	*usenetObservedSource
	avs core.ArchiveVolumeSource
}

func (s *usenetObservedArchiveSource) ArchiveVolumes() ([]core.MediaSource, string, string) {
	vols, format, member := s.avs.ArchiveVolumes()
	out := make([]core.MediaSource, len(vols))
	for i, vol := range vols {
		out[i] = &usenetObservedSource{
			MediaSource: vol,
			record:      s.record,
		}
	}
	return out, format, member
}
