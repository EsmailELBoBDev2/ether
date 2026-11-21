package stream

import (
	"context"
	"fmt"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/nzb"
)

// MaxArticleCheckSamples is the hard ceiling on articles sampled by one
// CheckArticles call, regardless of how many files or segments an NZB carries.
const MaxArticleCheckSamples = 32

// CheckArticles runs Ether's sampled, bounded STAT/BODY article availability
// check against an NZB and reports {Available, Checked, Missing}.
//
// Every NNTP operation runs at background (prefetch) priority through the
// shared provider pool and connection limits, so plugin pre-checks never
// starve live playback. Transient provider/network errors return an error
// instead of a negative verdict so callers do not cache transient outages as
// missing articles.
func (e *Engine) CheckArticles(ctx context.Context, releaseID string, nzbData []byte) (core.UsenetCheckResult, error) {
	if err := e.ctx.Err(); err != nil {
		return core.UsenetCheckResult{}, ErrClosed
	}
	if e.fetcher == nil {
		return core.UsenetCheckResult{}, fmt.Errorf("stream: usenet check unavailable: %w", core.ErrSourceUnavailable)
	}
	// Always demote plugin article checks to prefetch priority so provider
	// connection limits, reserveLive, and priority queueing protect playback.
	if !core.IsBackgroundCheck(ctx) {
		ctx = core.WithBackgroundBudget(ctx, func(context.Context, string, string, int64) error { return nil })
	}
	ctx = context.WithValue(ctx, healthWorkKey{}, true)

	rel, err := e.acquire(nzbData)
	if err != nil {
		return core.UsenetCheckResult{}, err
	}
	defer e.release(rel)

	files, missingVols := e.selectArticleCheckFiles(ctx, rel)
	if missingVols > 0 {
		capped := min(missingVols, MaxArticleCheckSamples)
		return core.UsenetCheckResult{
			Available: false,
			Checked:   capped,
			Missing:   capped,
			Reason:    "volumes_missing",
		}, nil
	}
	if len(files) == 0 {
		return core.UsenetCheckResult{
			Available: false,
			Checked:   0,
			Missing:   0,
			Reason:    "no_articles",
		}, nil
	}

	// Structurally incomplete NZBs (gaps in XML segment numbering) are already
	// known incomplete without spending any NNTP round trips, matching planFor.
	xmlMissing := 0
	for _, pf := range files {
		xmlMissing += len(pf.file.Missing())
	}
	if xmlMissing > 0 {
		capped := min(xmlMissing, MaxArticleCheckSamples)
		return core.UsenetCheckResult{
			Available: false,
			Checked:   capped,
			Missing:   capped,
			Reason:    "segment_absent_from_nzb",
		}, nil
	}

	out := e.statSampleBounded(ctx, files, MaxArticleCheckSamples)
	if out.missing > 0 {
		reason := "articles_missing"
		if out.missingFromNZB {
			reason = "segment_absent_from_nzb"
		}
		checked := max(out.checked, out.missing)
		return core.UsenetCheckResult{
			Available: false,
			Checked:   checked,
			Missing:   out.missing,
			Reason:    reason,
		}, nil
	}
	if out.err != nil {
		return core.UsenetCheckResult{}, out.err
	}
	if out.total == 0 {
		return core.UsenetCheckResult{
			Available: false,
			Checked:   0,
			Missing:   0,
			Reason:    "no_articles",
		}, nil
	}
	return core.UsenetCheckResult{
		Available: true,
		Checked:   out.checked,
		Missing:   0,
	}, nil
}

// selectArticleCheckFiles chooses the payload files of an NZB to sample and
// reports any archive volumes missing from the selected archive set(s). For
// video/archive releases it mirrors CheckRelease's primary candidate (skipping
// sample clips, PAR2, and NFO); for loose non-video releases (audio albums,
// books, comics, ROMs) it selects all non-auxiliary payload files.
func (e *Engine) selectArticleCheckFiles(ctx context.Context, rel *release) ([]*postedFile, int) {
	var payload []*postedFile
	var payloadBytes int64
	var nonPar2 []*postedFile
	for _, pf := range rel.files {
		if pf.numSlots() == 0 {
			continue
		}
		rel.infoMu.RLock()
		info := pf.file.Info
		rel.infoMu.RUnlock()
		if info.Kind == nzb.KindPar2 {
			continue
		}
		nonPar2 = append(nonPar2, pf)
		if info.Sample || info.Kind == nzb.KindNFO || info.Kind == nzb.KindSFV || info.Kind == nzb.KindOther {
			continue
		}
		payload = append(payload, pf)
		payloadBytes += pf.file.Bytes()
	}

	b := &builder{e: e, rel: rel, prio: healthPriority(ctx)}
	cands := b.candidates(rel.files)
	if len(cands) > 0 && len(cands[0].files) > 0 && (payloadBytes == 0 || cands[0].bytes*2 >= payloadBytes) {
		missingVols := 0
		if cands[0].set != nil {
			missingVols = len(cands[0].set.Missing)
		}
		return cands[0].files, missingVols
	}
	selected := rel.files
	if len(payload) > 0 {
		selected = payload
	} else if len(nonPar2) > 0 {
		selected = nonPar2
	}
	return selected, missingArchiveVolumes(rel, selected)
}

func missingArchiveVolumes(rel *release, files []*postedFile) int {
	rel.infoMu.RLock()
	var vols []*nzb.File
	for _, pf := range files {
		if pf.file.Info.Kind.IsArchive() {
			vols = append(vols, pf.file)
		}
	}
	sets := nzb.GroupSets(vols)
	rel.infoMu.RUnlock()
	missing := 0
	for _, s := range sets {
		missing += len(s.Missing)
	}
	return missing
}
