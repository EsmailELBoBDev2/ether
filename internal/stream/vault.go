package stream

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"maps"
	"math"
	"path"

	"github.com/braedonsaunders/ether/internal/archive"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/nzb"
	"github.com/braedonsaunders/ether/internal/releaseparse"
	"github.com/braedonsaunders/ether/internal/vault"
)

// OpenRecord uses the parsed posting and an optional previously learned mapping.
func (e *Engine) OpenRecord(ctx context.Context, releaseID string, rec *vault.Record) (core.MediaSource, error) {
	return e.openRecord(ctx, releaseID, releaseID, rec)
}

// OpenRecordAlias keeps the resolver ID visible to the player while the complete
// posting uses its own cache slots and vault feedback ID.
func (e *Engine) OpenRecordAlias(ctx context.Context, releaseID, cacheID string, rec *vault.Record) (core.MediaSource, error) {
	return e.openRecord(ctx, releaseID, cacheID, rec)
}

func (e *Engine) openRecord(ctx context.Context, releaseID, cacheID string, rec *vault.Record) (core.MediaSource, error) {
	if e.ctx.Err() != nil {
		return nil, ErrClosed
	}
	if rec == nil || rec.NZB == nil {
		return nil, ErrNoPlayableFile
	}
	doc := *rec.NZB
	doc.Files = make([]*nzb.File, len(rec.NZB.Files))
	for i, f := range rec.NZB.Files {
		if f == nil {
			return nil, ErrCorrupt
		}
		copyFile := *f
		doc.Files[i] = &copyFile
	}
	if rec.Password != "" {
		doc.Meta = maps.Clone(doc.Meta)
		if doc.Meta == nil {
			doc.Meta = map[string][]string{}
		}
		doc.Meta["password"] = []string{rec.Password}
	}
	key := rec.Key
	if key == "" {
		key = vault.ContentKey(&doc)
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s", key, doc.MetaValue(nzb.MetaEpisode), doc.MetaValue(nzb.MetaMovie), doc.Password())))
	rel := e.acquireDoc(fmt.Sprintf("vault-%x", digest[:]), &doc, rec.Layout)
	return e.openRelease(ctx, releaseID, cacheID, rel)
}

func (p *plan) vaultLayout(ctx context.Context, cacheID string) *vault.Layout {
	if p.encrypted != nil || len(p.clips)+len(p.spans)+len(p.sub) > 0 {
		return nil
	}
	l := &vault.Layout{Container: string(p.kind), FirstArticles: map[int][]byte{}}
	if p.kind == kindPlain {
		l.Container = "raw"
	}
	b := builder{e: p.rel.e, rel: p.rel, prio: core.PriorityPrefetch, fo: fetchOpts{cacheID: cacheID}}
	plans := append([]*plan{p}, p.siblings...)
	if p.kind == kindPlain {
		for _, c := range b.candidates(p.rel.files) {
			if c.kind != nzb.KindVideo || c.files[0] == p.vols[0] {
				continue
			}
			if q, err := b.planPlain(ctx, c.files[0]); err == nil {
				plans = append(plans, q)
			}
		}
	}
	// The store retains at most 2 MiB of complete articles per posting.
	remaining := 2 << 20
	seen := map[int]bool{}
	for _, q := range plans {
		q.mu.Lock()
		snapshot := q.lay
		infos := append([]volInfo(nil), q.info...)
		flags := append([]bool(nil), q.verified...)
		q.mu.Unlock()
		for i, pf := range q.vols {
			if seen[pf.idx] {
				continue
			}
			seen[pf.idx] = true
			v := vault.VolumeLayout{File: pf.idx, Size: infos[i].size, Verified: flags[i]}
			if remaining > 0 {
				if seg, err := b.e.segment(ctx, b.rel, pf, 0, b.prio, b.fo); err == nil && seg.begin == 0 && !seg.repairedSpan && len(seg.data) <= remaining {
					remaining -= len(seg.data)
					v.HeadSize = len(seg.data)
					l.FirstArticles[pf.idx] = seg.data
				}
			}
			l.Volumes = append(l.Volumes, v)
		}

		name := q.name
		if q.member != "" {
			name = q.member
		}
		m := vault.Member{Name: name, Size: q.size, Stored: true}
		info := releaseparse.Parse(path.Base(name))
		if len(info.Seasons) > 0 && len(info.Episodes) > 0 {
			m.Season, m.Episode = info.Seasons[0], info.Episodes[0]
		}
		for _, x := range snapshot.ext {
			m.Extents = append(m.Extents, vault.Extent{File: q.vols[x.vol].idx, Offset: x.off, Size: x.size})
		}
		l.Members = append(l.Members, m)
	}
	return l
}

// restore checks the selected member against real bytes before installing cached
// heads. A truncated header can confirm a prefix but cannot fill an article slot.
func (b *builder) restore(ctx context.Context, l *vault.Layout) (*plan, error) {
	if len(l.Members) == 0 {
		return nil, nil
	}
	m := &l.Members[0]
	if _, ok := b.rel.doc.EpisodeHint(); ok {
		m = nil
	}
	if _, ok := b.rel.doc.MovieHint(); ok {
		m = nil
	}
	if m == nil {
		var candidates []archive.Member
		for _, v := range l.Members {
			candidates = append(candidates, archive.Member{Name: v.Name, Size: v.Size, Stored: v.Stored})
		}
		chosen := b.chooseMember(candidates)
		if chosen == nil {
			return nil, nil
		}
		for i := range l.Members {
			if l.Members[i].Name == chosen.Name {
				m = &l.Members[i]
				break
			}
		}
	}
	if m == nil || !m.Stored || len(m.Extents) == 0 || m.Size <= 0 {
		return nil, nil
	}
	kind := planKind(l.Container)
	switch kind {
	case "raw":
		kind = kindPlain
	case kindRAR, kind7z, kindZIP, kindSplit:
	default:
		return nil, nil
	}
	var vols []*postedFile
	var info []volInfo
	var verified []bool
	indices := map[int]int{}
	for _, v := range l.Volumes {
		if kind == kindPlain && v.File != m.Extents[0].File {
			continue
		}
		if v.File < 0 || v.File >= len(b.rel.files) || v.Size <= 0 {
			return nil, nil
		}
		if _, dup := indices[v.File]; dup {
			return nil, nil
		}
		indices[v.File] = len(vols)
		vols = append(vols, b.rel.files[v.File])
		info = append(info, volInfo{size: v.Size})
		verified = append(verified, v.Verified)
	}
	lay := &layout{version: 1}
	for _, x := range m.Extents {
		if x.File < 0 || x.File >= len(b.rel.files) || x.Offset < 0 || x.Size <= 0 || x.Size > m.Size-lay.size || x.Offset > math.MaxInt64-x.Size {
			return nil, nil
		}
		k, ok := indices[x.File]
		if !ok {
			if len(l.Volumes) > 0 {
				return nil, nil
			}
			k = len(vols)
			indices[x.File] = k
			vols = append(vols, b.rel.files[x.File])
			info = append(info, volInfo{size: x.Offset + x.Size})
			verified = append(verified, true)
		}
		if len(l.Volumes) > 0 && x.Offset+x.Size > info[k].size {
			return nil, nil
		}
		info[k].dataOff = x.Offset
		info[k].dataSize = x.Size
		lay.ext = append(lay.ext, extent{inner: lay.size, size: x.Size, vol: k, off: x.Offset})
		lay.size += x.Size
	}
	if lay.size != m.Size {
		return nil, nil
	}
	p := newPlan(b.rel, kind, path.Base(m.Name), m.Size, vols, info, verified)
	if kind == kind7z || kind == kindZIP {
		starts := make([]int64, len(info))
		var sum int64
		for i := range info {
			starts[i] = sum
			if info[i].size > math.MaxInt64-sum {
				return nil, nil
			}
			sum += info[i].size
			info[i].dataOff = 0
			info[i].dataSize = 0
		}
		for _, x := range lay.ext {
			off := starts[x.vol] + x.off
			n := len(p.concatExt)
			if n > 0 && p.concatExt[n-1].Offset+p.concatExt[n-1].Size == off {
				p.concatExt[n-1].Size += x.size
			} else {
				p.concatExt = append(p.concatExt, archive.Extent{Offset: off, Size: x.size})
			}
		}
	}
	p.member = m.Name
	p.lay = lay
	p.restored = true
	first := m.Extents[0]
	pf := b.rel.files[first.File]
	seg, err := b.e.segment(ctx, b.rel, pf, 0, b.prio, b.fo)
	if err != nil {
		return nil, err
	}
	if seg.begin != 0 || (seg.fileSize > 0 && len(l.Volumes) > 0 && seg.fileSize != info[indices[first.File]].size) || !bytes.HasPrefix(seg.data, l.FirstArticles[first.File]) {
		return nil, nil
	}
	var probe [256]byte
	n, err := readPlan(ctx, b.e, b.rel, p, probe[:min(int64(len(probe)), m.Size)], 0, b.prio, b.fo)
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || !looksLikeVideo(probe[:n]) {
		return nil, nil
	}
	for _, v := range l.Volumes {
		if _, ok := indices[v.File]; !ok {
			continue
		}
		pf := b.rel.files[v.File]
		pf.setAssumed(v.Size)
		head := l.FirstArticles[v.File]
		if v.File != first.File && len(head) > 0 && len(head) == v.HeadSize && int64(len(head)) <= v.Size {
			b.e.hot.put(segKey{b.rel.digest, pf.segBase}, &segData{data: head, fileSize: v.Size})
		}
	}
	return p, nil
}
