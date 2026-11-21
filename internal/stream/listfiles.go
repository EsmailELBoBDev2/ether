package stream

import (
	"context"
	"sort"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/nzb"
	"github.com/braedonsaunders/ether/internal/par2"
)

const (
	// maxReleaseFileProbes bounds how many files ListFiles fetches a first
	// article for when PAR2 does not already supply their exact decoded lengths.
	maxReleaseFileProbes = 32
	// listFilesProbeCap bounds best-effort PAR2/yEnc probing so release_files
	// degrades to approximate estimates rather than exhausting the resolve deadline.
	listFilesProbeCap = 4 * time.Second
)

// ListFiles returns the files of an NZB in document order with exact decoded
// sizes wherever PAR2 file descriptions or yEnc headers (=ybegin size=) supply
// them. Files whose decoded size cannot be determined fall back to their
// segment-derived decoded estimate with SizeApproximate=true.
//
// Probing reuses the release's PAR2 state and shared article cache, so
// subsequent Open calls do not re-fetch articles read here.
func (e *Engine) ListFiles(ctx context.Context, releaseID string, nzbData []byte) ([]core.ReleaseFile, error) {
	if err := e.ctx.Err(); err != nil {
		return nil, ErrClosed
	}
	rel, err := e.acquire(nzbData)
	if err != nil {
		return nil, err
	}
	defer e.release(rel)

	out := make([]core.ReleaseFile, len(rel.files))
	needProbe := false
	for i, pf := range rel.files {
		out[i] = core.ReleaseFile{
			Index: i,
			Path:  pf.Name(),
		}
		if pf.SizeKnown() {
			out[i].Size = pf.Size()
		} else {
			out[i].Size = pf.file.EstimatedDecodedBytes()
			out[i].SizeApproximate = true
			rel.infoMu.RLock()
			isPar2 := pf.file.Info.Kind == nzb.KindPar2
			rel.infoMu.RUnlock()
			if !isPar2 {
				needProbe = true
			}
		}
	}
	if !needProbe || e.fetcher == nil || ctx.Err() != nil {
		return out, nil
	}

	probeCtx, cancel := listFilesProbeContext(ctx)
	defer cancel()

	prio := core.PriorityLive
	if core.IsBackgroundCheck(probeCtx) {
		prio = core.PriorityPrefetch
	}
	fo := fetchOpts{cacheID: releaseID, layoutProbe: true}
	b := &builder{e: e, rel: rel, prio: prio, fo: fo}

	// 1. PAR2 index: one small fetch gives exact decoded lengths for every
	//    protected non-obfuscated file in the release.
	set := rel.loadPar2(probeCtx, prio, fo, rel.par2IndexFile())
	if set != nil {
		for i, pf := range rel.files {
			if !out[i].SizeApproximate {
				continue
			}
			rel.infoMu.RLock()
			obf := pf.file.Obfuscated
			rel.infoMu.RUnlock()
			if obf {
				continue
			}
			if length, ok := uniquePar2Length(set.Named(pf.Name())); ok {
				out[i].Size = length
				out[i].SizeApproximate = false
				pf.setAssumed(length)
			}
		}
	}

	// 2. yEnc =ybegin size= (and PAR2 first-16k MD5 identification for
	//    obfuscated files): fetch slot 0 for remaining non-PAR2 files.
	var toProbe []int
	for i, pf := range rel.files {
		if !out[i].SizeApproximate {
			continue
		}
		rel.infoMu.RLock()
		isPar2 := pf.file.Info.Kind == nzb.KindPar2
		rel.infoMu.RUnlock()
		if isPar2 || pf.numSlots() == 0 || pf.msgID(0) == "" {
			continue
		}
		toProbe = append(toProbe, i)
	}
	if len(toProbe) == 0 || probeCtx.Err() != nil {
		return out, nil
	}
	if len(toProbe) > maxReleaseFileProbes {
		// Prioritize the largest files so media payloads get exact sizes before
		// auxiliary text/image extras when a release has dozens of entries.
		sort.SliceStable(toProbe, func(a, b int) bool {
			return rel.files[toProbe[a]].file.Bytes() > rel.files[toProbe[b]].file.Bytes()
		})
		toProbe = toProbe[:maxReleaseFileProbes]
		sort.Ints(toProbe)
	}

	heads := make([]*segData, len(toProbe))
	_ = b.parallel(probeCtx, len(toProbe), func(pctx context.Context, j int) error {
		pf := rel.files[toProbe[j]]
		seg, serr := e.segment(pctx, rel, pf, 0, prio, fo)
		if serr == nil {
			heads[j] = seg
		}
		return nil // best-effort per file; never cancel siblings on one miss
	})

	for j, idx := range toProbe {
		pf := rel.files[idx]
		seg := heads[j]
		if pf.SizeKnown() {
			out[idx].Size = pf.Size()
			out[idx].SizeApproximate = false
		}
		if seg != nil && set != nil {
			if f := identifyByPar2(set, pf, seg); f != nil {
				if f.Length > 0 {
					out[idx].Size = f.Length
					out[idx].SizeApproximate = false
					if !pf.SizeKnown() {
						pf.setAssumed(f.Length)
					}
				}
				rel.infoMu.RLock()
				obf := pf.file.Obfuscated
				rel.infoMu.RUnlock()
				if obf {
					if base := f.BaseName(); base != "" && base != "." {
						out[idx].Path = base
					}
				}
			}
		}
	}
	return out, nil
}

func uniquePar2Length(cands []*par2.File) (int64, bool) {
	if len(cands) == 0 {
		return 0, false
	}
	length := cands[0].Length
	if length <= 0 {
		return 0, false
	}
	for _, c := range cands[1:] {
		if c.Length != length {
			return 0, false
		}
	}
	return length, true
}

func listFilesProbeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if deadline, ok := ctx.Deadline(); ok {
		rem := time.Until(deadline)
		if rem <= 500*time.Millisecond {
			return ctx, func() {}
		}
		return context.WithTimeout(ctx, min(listFilesProbeCap, rem-300*time.Millisecond))
	}
	return context.WithTimeout(ctx, listFilesProbeCap)
}
