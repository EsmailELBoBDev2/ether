package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/blobcache"
	"github.com/braedonsaunders/ether/internal/contentrating"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/kindstream"
	"github.com/braedonsaunders/ether/internal/libraries"
	"github.com/braedonsaunders/ether/internal/pluginitems"
	"github.com/braedonsaunders/ether/internal/plugins"
)

// DebridSourceBackend is the subset of debrid.Service used by KindResolver for
// host-mediated debrid cache checks and torrent acquisition.
type DebridSourceBackend interface {
	CheckCached(ctx context.Context, hashes []string) (map[string]bool, bool)
	Acquire(ctx context.Context, rel core.Release) ([]byte, error)
}

// KindResolverDeps configures a KindResolver.
type KindResolverDeps struct {
	Manager   *plugins.Manager
	Libraries *libraries.Service
	Items     *pluginitems.Service
	Blobs     *blobcache.Cache
	Catalog   *KindCatalog
	Indexer   core.Indexer
	Debrid    DebridSourceBackend
	Engine    core.Engine
	HTTP      *http.Client
	Authorize func(ctx context.Context, viewer *core.Viewer, key core.RatingKey) error
	Logger    *slog.Logger
}

type resolveFlightKey struct {
	ref  core.ItemRef
	part int
}

type resolveFlight struct {
	done    chan struct{}
	sources []core.PlayableSource
	err     error
}

// KindResolver implements frontend.KindStreams and frontend.KindPartStreams for
// plugin content kinds and supplies the host-side ResolveSources callback for
// plugins.KindBackends.
type KindResolver struct {
	mgr       *plugins.Manager
	libs      *libraries.Service
	items     *pluginitems.Service
	blobs     *blobcache.Cache
	catalog   *KindCatalog
	indexer   core.Indexer
	debrid    DebridSourceBackend
	engine    core.Engine
	http      *http.Client
	authorize func(ctx context.Context, viewer *core.Viewer, key core.RatingKey) error
	backoff   *kindstream.HostBackoff
	log       *slog.Logger

	flightMu sync.Mutex
	flights  map[resolveFlightKey]*resolveFlight
}

var (
	_ frontend.KindStreams     = (*KindResolver)(nil)
	_ frontend.KindPartStreams = (*KindResolver)(nil)
)

func defaultKindHTTPClient() *http.Client {
	if dt, ok := http.DefaultTransport.(*http.Transport); ok && dt != nil {
		tr := dt.Clone()
		tr.ResponseHeaderTimeout = 30 * time.Second
		return &http.Client{Transport: tr}
	}
	return &http.Client{}
}

// NewKindResolver constructs a KindResolver.
func NewKindResolver(deps KindResolverDeps) *KindResolver {
	log := deps.Logger
	if log == nil {
		log = slog.Default()
	}
	hc := deps.HTTP
	if hc == nil {
		hc = defaultKindHTTPClient()
	}
	return &KindResolver{
		mgr:       deps.Manager,
		libs:      deps.Libraries,
		items:     deps.Items,
		blobs:     deps.Blobs,
		catalog:   deps.Catalog,
		indexer:   deps.Indexer,
		debrid:    deps.Debrid,
		engine:    deps.Engine,
		http:      hc,
		authorize: deps.Authorize,
		backoff:   kindstream.NewHostBackoff(),
		log:       log.With("component", "kindresolver"),
		flights:   make(map[resolveFlightKey]*resolveFlight),
	}
}

// OpenKind implements frontend.KindStreams.
func (r *KindResolver) OpenKind(ctx context.Context, viewer *core.Viewer, key core.RatingKey, accept []string) (*frontend.KindMedia, error) {
	if r == nil || r.items == nil || r.libs == nil || !key.IsPluginKey() {
		return nil, core.ErrNotFound
	}
	item, lib, rec, err := r.loadAuthorized(ctx, viewer, key)
	if err != nil {
		return nil, err
	}
	if item.Unavailable != nil {
		return nil, core.ErrNotAvailable
	}
	lv, _, ok := rec.Decl.Level(item.Level)
	if !ok {
		return nil, core.ErrNotFound
	}
	if lv.Playable && item.Playable {
		return r.openPlayableItemMedia(ctx, rec, lib, item, accept)
	}

	leaves, err := r.collectPlayableLeaves(ctx, rec, item)
	if err != nil {
		return nil, err
	}
	if len(leaves) == 0 {
		return nil, core.ErrNotAvailable
	}
	seqLeaves := make([]kindstream.Leaf, len(leaves))
	var totalDur time.Duration
	for i, leaf := range leaves {
		leafItem := leaf
		totalDur += extractItemDuration(leafItem, rec.Decl)
		leafSize, leafMIME, lerr := r.resolveLeafSizeAndMIME(ctx, rec, lib, leafItem, accept)
		if lerr != nil {
			return nil, lerr
		}
		seqLeaves[i] = kindstream.Leaf{
			Name: leafItem.Title,
			MIME: leafMIME,
			Size: leafSize,
			Open: func(lctx context.Context) (kindstream.ByteSource, error) {
				bs, _, oerr := r.openPlayableItemByteSource(lctx, rec, lib, leafItem, accept)
				return bs, oerr
			},
		}
	}
	seq := kindstream.NewSequence(seqLeaves)
	return &frontend.KindMedia{
		ReaderAt: seq,
		Closer:   seq,
		Size:     seq.Size(),
		MIME:     seq.MIME(),
		Duration: totalDur,
		Name:     item.Title,
	}, nil
}

// OpenKindPart implements frontend.KindPartStreams (#576). It opens one zero-based
// part of a multi-part plugin kind item, passing KindResolveRequest.PartIndex
// to the plugin's /resolve route.
func (r *KindResolver) OpenKindPart(ctx context.Context, viewer *core.Viewer, key core.RatingKey, partIndex int, accept []string) (*frontend.KindMedia, error) {
	if r == nil || r.items == nil || r.libs == nil || !key.IsPluginKey() || partIndex < 0 {
		return nil, core.ErrNotFound
	}
	item, lib, rec, err := r.loadAuthorized(ctx, viewer, key)
	if err != nil {
		return nil, err
	}
	if item.Unavailable != nil {
		return nil, core.ErrNotAvailable
	}
	lv, _, ok := rec.Decl.Level(item.Level)
	if !ok {
		return nil, core.ErrNotFound
	}
	if lv.Playable && item.Playable {
		parts := core.ExtractKindItemPartsFromItem(item)
		if len(parts) > 0 && partIndex >= len(parts) {
			return nil, core.ErrNotFound
		}
		idx := partIndex
		bs, err := r.openItemByteSource(ctx, rec, lib, item, &idx, accept)
		if err != nil {
			return nil, err
		}
		var dur time.Duration
		if partIndex < len(parts) {
			bs = withPartDefaults(bs, parts[partIndex], item.Title)
			if parts[partIndex].DurationSec > 0 {
				dur = time.Duration(parts[partIndex].DurationSec * float64(time.Second))
			}
		}
		if dur == 0 && len(parts) <= 1 {
			dur = extractItemDuration(item, rec.Decl)
		}
		return &frontend.KindMedia{
			ReaderAt: bs,
			Closer:   bs,
			Size:     bs.Size(),
			MIME:     bs.MIME(),
			Duration: dur,
			Name:     bs.Name(),
		}, nil
	}

	leaves, err := r.collectPlayableLeaves(ctx, rec, item)
	if err != nil {
		return nil, err
	}
	if partIndex >= len(leaves) {
		return nil, core.ErrNotFound
	}
	return r.openPlayableItemMedia(ctx, rec, lib, leaves[partIndex], accept)
}

// KindParts implements frontend.KindPartStreams (#576). It returns the declared
// parts of item without opening bytes.
func (r *KindResolver) KindParts(ctx context.Context, viewer *core.Viewer, key core.RatingKey) ([]frontend.KindPart, error) {
	if r == nil || r.items == nil || r.libs == nil || !key.IsPluginKey() {
		return nil, core.ErrNotFound
	}
	item, _, _, err := r.loadAuthorized(ctx, viewer, key)
	if err != nil {
		return nil, err
	}
	if item.Unavailable != nil {
		return nil, core.ErrNotAvailable
	}
	parts := core.ExtractKindItemPartsFromItem(item)
	if len(parts) == 0 {
		return nil, nil
	}
	out := make([]frontend.KindPart, len(parts))
	for i, p := range parts {
		idx := i
		size := p.Size
		mime := p.MIME
		if size <= 0 || mime == "" {
			if cachedSrc, ok, cerr := r.items.GetPartResolution(ctx, item.Ref, &idx); cerr == nil && ok {
				if size <= 0 && cachedSrc.Size > 0 {
					size = cachedSrc.Size
				}
				if mime == "" && cachedSrc.MIME != "" {
					mime = cachedSrc.MIME
				}
			}
		}
		out[i] = frontend.KindPart{
			Index:    i,
			Name:     p.Name,
			MIME:     mime,
			Size:     size,
			Duration: time.Duration(p.DurationSec * float64(time.Second)),
		}
	}
	return out, nil
}

func (r *KindResolver) openPlayableItemMedia(ctx context.Context, rec pluginitems.KindRecord, lib core.Library, item core.KindItem, accept []string) (*frontend.KindMedia, error) {
	bs, dur, err := r.openPlayableItemByteSource(ctx, rec, lib, item, accept)
	if err != nil {
		return nil, err
	}
	return &frontend.KindMedia{
		ReaderAt: bs,
		Closer:   bs,
		Size:     bs.Size(),
		MIME:     bs.MIME(),
		Duration: dur,
		Name:     bs.Name(),
	}, nil
}

func (r *KindResolver) openPlayableItemByteSource(ctx context.Context, rec pluginitems.KindRecord, lib core.Library, item core.KindItem, accept []string) (kindstream.ByteSource, time.Duration, error) {
	parts := core.ExtractKindItemPartsFromItem(item)
	if len(parts) == 0 {
		bs, err := r.openItemByteSource(ctx, rec, lib, item, nil, accept)
		if err != nil {
			return nil, 0, err
		}
		return bs, extractItemDuration(item, rec.Decl), nil
	}
	if len(parts) == 1 {
		idx := 0
		bs, err := r.openItemByteSource(ctx, rec, lib, item, &idx, accept)
		if err != nil {
			return nil, 0, err
		}
		bs = withPartDefaults(bs, parts[0], item.Title)
		dur := extractItemDuration(item, rec.Decl)
		if dur == 0 && parts[0].DurationSec > 0 {
			dur = time.Duration(parts[0].DurationSec * float64(time.Second))
		}
		return bs, dur, nil
	}

	// Multi-part item (#576): open part 0 up front so any pending/unavailable
	// status surfaces before HTTP headers are written, and assemble a Sequence
	// over all parts so whole-item readers stream every file in order.
	zero := 0
	firstRaw, err := r.openItemByteSource(ctx, rec, lib, item, &zero, accept)
	if err != nil {
		return nil, 0, err
	}
	firstBS := withPartDefaults(firstRaw, parts[0], item.Title)

	var firstMu sync.Mutex
	preopened := firstBS
	releasePreopened := func() error {
		firstMu.Lock()
		bs := preopened
		preopened = nil
		firstMu.Unlock()
		if bs != nil {
			return bs.Close()
		}
		return nil
	}

	seqLeaves := make([]kindstream.Leaf, len(parts))
	part0Copy := parts[0]
	seqLeaves[0] = kindstream.Leaf{
		Name: firstNonEmpty(part0Copy.Name, firstBS.Name(), fmt.Sprintf("%s (part 1)", item.Title)),
		MIME: firstNonEmpty(firstBS.MIME(), part0Copy.MIME),
		Size: firstBS.Size(),
		Open: func(lctx context.Context) (kindstream.ByteSource, error) {
			firstMu.Lock()
			bs := preopened
			preopened = nil
			firstMu.Unlock()
			if bs != nil {
				return bs, nil
			}
			idx := 0
			reopened, oerr := r.openItemByteSource(lctx, rec, lib, item, &idx, accept)
			if oerr != nil {
				return nil, oerr
			}
			return withPartDefaults(reopened, part0Copy, item.Title), nil
		},
	}

	var sumPartDur time.Duration
	if parts[0].DurationSec > 0 {
		sumPartDur += time.Duration(parts[0].DurationSec * float64(time.Second))
	}

	for i := 1; i < len(parts); i++ {
		partCopy := parts[i]
		partIdx := i
		if partCopy.DurationSec > 0 {
			sumPartDur += time.Duration(partCopy.DurationSec * float64(time.Second))
		}
		partSize, partMIME, perr := r.resolvePartSizeAndMIME(ctx, rec, lib, item, partCopy, accept)
		if perr != nil {
			_ = releasePreopened()
			return nil, 0, perr
		}
		seqLeaves[i] = kindstream.Leaf{
			Name: firstNonEmpty(partCopy.Name, fmt.Sprintf("%s (part %d)", item.Title, partIdx+1)),
			MIME: partMIME,
			Size: partSize,
			Open: func(lctx context.Context) (kindstream.ByteSource, error) {
				bs, oerr := r.openItemByteSource(lctx, rec, lib, item, &partIdx, accept)
				if oerr != nil {
					return nil, oerr
				}
				return withPartDefaults(bs, partCopy, item.Title), nil
			},
		}
	}

	dur := extractItemDuration(item, rec.Decl)
	if dur == 0 {
		dur = sumPartDur
	}
	seq := kindstream.NewSequence(seqLeaves)
	return &preopenedSequence{
		ByteSource: seq,
		name:       firstNonEmpty(item.Title, seq.Name()),
		cleanup:    releasePreopened,
	}, dur, nil
}

func (r *KindResolver) resolvePartSizeAndMIME(ctx context.Context, rec pluginitems.KindRecord, lib core.Library, item core.KindItem, part core.KindItemPart, accept []string) (int64, string, error) {
	idx := part.Index
	size := part.Size
	mime := part.MIME
	cachedSrc, ok, cerr := r.items.GetPartResolution(ctx, item.Ref, &idx)
	if cerr != nil {
		return 0, "", cerr
	}
	if ok {
		if size <= 0 && cachedSrc.Size > 0 {
			size = cachedSrc.Size
		}
		if mime == "" && cachedSrc.MIME != "" {
			mime = cachedSrc.MIME
		}
	}
	if size > 0 {
		return size, mime, nil
	}
	bs, err := r.openItemByteSource(ctx, rec, lib, item, &idx, accept)
	if err != nil {
		return 0, "", err
	}
	size = bs.Size()
	if mime == "" && bs.MIME() != "" {
		mime = bs.MIME()
	}
	_ = bs.Close()
	return size, mime, nil
}

func (r *KindResolver) resolveLeafSizeAndMIME(ctx context.Context, rec pluginitems.KindRecord, lib core.Library, leaf core.KindItem, accept []string) (int64, string, error) {
	if parts := core.ExtractKindItemPartsFromItem(leaf); len(parts) > 0 {
		bs, _, err := r.openPlayableItemByteSource(ctx, rec, lib, leaf, accept)
		if err != nil {
			return 0, "", err
		}
		size := bs.Size()
		mime := bs.MIME()
		_ = bs.Close()
		return size, mime, nil
	}
	size, mime := extractItemSizeAndMIME(leaf, rec.Decl)
	if size > 0 {
		return size, mime, nil
	}
	cachedSrc, ok, cerr := r.items.GetResolution(ctx, leaf.Ref)
	if cerr != nil {
		return 0, "", cerr
	}
	if ok && cachedSrc.Size > 0 {
		if cachedSrc.MIME != "" {
			mime = cachedSrc.MIME
		}
		return cachedSrc.Size, mime, nil
	}
	bs, err := r.openItemByteSource(ctx, rec, lib, leaf, nil, accept)
	if err != nil {
		return 0, "", err
	}
	size = bs.Size()
	if bs.MIME() != "" {
		mime = bs.MIME()
	}
	_ = bs.Close()
	return size, mime, nil
}

// KindLeaves implements frontend.KindStreams.
func (r *KindResolver) KindLeaves(ctx context.Context, viewer *core.Viewer, parent core.RatingKey) ([]frontend.KindLeaf, error) {
	if r == nil || r.items == nil || r.libs == nil || !parent.IsPluginKey() {
		return nil, core.ErrNotFound
	}
	parentItem, _, rec, err := r.loadAuthorized(ctx, viewer, parent)
	if err != nil {
		return nil, err
	}
	leaves, err := r.collectPlayableLeaves(ctx, rec, parentItem)
	if err != nil {
		return nil, err
	}
	out := make([]frontend.KindLeaf, len(leaves))
	for i, leaf := range leaves {
		out[i] = frontend.KindLeaf{
			Key:      leaf.Key,
			Index:    leaf.Index,
			Title:    leaf.Title,
			Duration: extractItemDuration(leaf, rec.Decl),
		}
	}
	return out, nil
}

// KeyForRef implements frontend.KindRefKeys. It maps a plugin ItemRef to the
// stored item's RatingKey, refusing hidden libraries and viewers who cannot
// open them.
func (r *KindResolver) KeyForRef(ctx context.Context, viewer *core.Viewer, ref core.ItemRef) (core.RatingKey, error) {
	if r == nil || r.items == nil || r.libs == nil || !ref.Valid() {
		return 0, core.ErrNotFound
	}
	item, libID, err := r.items.GetByRef(ctx, ref)
	if err != nil {
		return 0, core.ErrNotFound
	}
	lib, err := r.libs.Get(ctx, libID)
	if err != nil || lib.Hidden || viewer == nil || viewer.ID <= 0 || !viewer.AllowsLibrary(lib.ID) {
		return 0, core.ErrNotFound
	}
	if r.authorize != nil {
		if err := r.authorize(ctx, viewer, item.Key); err != nil {
			return 0, err
		}
	}
	return item.Key, nil
}

// RefsForKeys implements frontend.KindRefKeys. It returns the ItemRef of each
// key the viewer may see; unknown or unauthorized keys are omitted. The page
// is caller-bounded, so this is a handful of indexed reads, never a walk.
func (r *KindResolver) RefsForKeys(ctx context.Context, viewer *core.Viewer, keys []core.RatingKey) (map[core.RatingKey]core.ItemRef, error) {
	out := make(map[core.RatingKey]core.ItemRef, len(keys))
	if r == nil || r.items == nil || r.libs == nil || viewer == nil || viewer.ID <= 0 {
		return out, nil
	}
	for _, key := range keys {
		if !key.IsPluginKey() {
			continue
		}
		item, libID, err := r.items.GetByKey(ctx, key)
		if err != nil {
			continue
		}
		lib, err := r.libs.Get(ctx, libID)
		if err != nil || lib.Hidden || !viewer.AllowsLibrary(lib.ID) {
			continue
		}
		out[key] = item.Ref
	}
	return out, nil
}

// ResolveSources implements plugins.KindBackends.ResolveSources for plugins
// granted permissions.sources ("search" or "acquire"). Capability and
// permission checks are enforced upstream in plugins/host_kinds.go.
func (r *KindResolver) ResolveSources(ctx context.Context, _ string, q core.SourceQuery) ([]core.SourceCandidate, error) {
	if r == nil {
		return nil, core.ErrNotAvailable
	}
	switch q.Kind {
	case "indexer_search":
		if _, err := core.SearchCategories(q.Category); err != nil {
			return nil, err
		}
		if r.indexer == nil {
			return []core.SourceCandidate{}, nil
		}
		rels, err := r.indexer.Search(ctx, core.SearchRequest{
			Title:    strings.TrimSpace(q.Query),
			Category: strings.TrimSpace(q.Category),
		})
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// One broken indexer (a missing site definition, an outage)
			// fails the whole fan-out when nobody else had a match. For a
			// plugin that is simply "no releases": its search must not
			// throw because of an indexer it never chose.
			r.log.Debug("plugin indexer search found nothing; some indexers failed", "query", q.Query, "err", err)
			return []core.SourceCandidate{}, nil
		}
		out := make([]core.SourceCandidate, 0, len(rels))
		for _, rel := range rels {
			out = append(out, core.SourceCandidate{
				InfoHash:  strings.ToLower(strings.TrimSpace(rel.InfoHash)),
				Magnet:    rel.MagnetURL,
				NZBURL:    rel.NZBURL,
				Title:     rel.Name,
				Size:      rel.Size,
				Seeders:   rel.Seeders,
				Cached:    len(rel.DebridCached) > 0,
				Indexer:   rel.Indexer,
				IndexerID: rel.IndexerID,
			})
		}
		return out, nil

	case "debrid_check":
		if r.debrid == nil {
			return []core.SourceCandidate{}, nil
		}
		var hashes []string
		seen := make(map[string]bool)
		addHash := func(h string) {
			h = strings.ToLower(strings.TrimSpace(h))
			if h != "" && !seen[h] {
				seen[h] = true
				hashes = append(hashes, h)
			}
		}
		for _, h := range q.InfoHashes {
			addHash(h)
		}
		addHash(q.InfoHash)
		if len(hashes) == 0 {
			return []core.SourceCandidate{}, nil
		}
		cached, _ := r.debrid.CheckCached(ctx, hashes)
		out := make([]core.SourceCandidate, len(hashes))
		for i, h := range hashes {
			out[i] = core.SourceCandidate{
				InfoHash: h,
				Cached:   cached[h],
			}
		}
		return out, nil

	case "debrid_acquire":
		if r.debrid == nil {
			return nil, core.ErrNotAvailable
		}
		hash := strings.ToLower(strings.TrimSpace(q.InfoHash))
		rel := core.Release{
			ID:        "kind:" + firstNonEmpty(hash, q.Magnet),
			Name:      firstNonEmpty(hash, "plugin-kind-acquire"),
			Protocol:  core.ProtocolTorrent,
			InfoHash:  hash,
			MagnetURL: strings.TrimSpace(q.Magnet),
		}
		if _, err := r.debrid.Acquire(ctx, rel); err != nil {
			return nil, err
		}
		return []core.SourceCandidate{{
			InfoHash: hash,
			Magnet:   q.Magnet,
			Cached:   true,
		}}, nil

	default:
		return nil, fmt.Errorf("kindresolver: unknown source query kind %q", q.Kind)
	}
}

func (r *KindResolver) loadAuthorized(ctx context.Context, viewer *core.Viewer, key core.RatingKey) (core.KindItem, core.Library, pluginitems.KindRecord, error) {
	if viewer == nil || viewer.ID <= 0 {
		return core.KindItem{}, core.Library{}, pluginitems.KindRecord{}, core.ErrNotFound
	}
	if r.authorize != nil {
		if err := r.authorize(ctx, viewer, key); err != nil {
			return core.KindItem{}, core.Library{}, pluginitems.KindRecord{}, err
		}
	}
	item, libID, err := r.items.GetByKey(ctx, key)
	if err != nil {
		return core.KindItem{}, core.Library{}, pluginitems.KindRecord{}, core.ErrNotFound
	}
	lib, err := r.libs.Get(ctx, libID)
	if err != nil || lib.Hidden || !viewer.AllowsLibrary(lib.ID) {
		return core.KindItem{}, core.Library{}, pluginitems.KindRecord{}, core.ErrNotFound
	}
	rec, err := r.items.GetKindByLibrary(ctx, libID)
	if err != nil {
		return core.KindItem{}, core.Library{}, pluginitems.KindRecord{}, core.ErrNotFound
	}
	if r.catalog != nil && !r.catalog.IsPluginLibraryActive(ctx, lib) {
		return core.KindItem{}, core.Library{}, pluginitems.KindRecord{}, core.ErrNotFound
	} else if r.catalog == nil && r.mgr != nil {
		pr, err := r.mgr.Get(ctx, rec.PluginID)
		if err != nil || !pr.Enabled {
			return core.KindItem{}, core.Library{}, pluginitems.KindRecord{}, core.ErrNotFound
		}
	}
	if r.authorize == nil && viewer.MaxContentRating != "" {
		proj := pluginitems.Project(item, rec.Decl, lib.Present)
		cr := proj.ContentRating
		if cr == "" {
			cr = extractContentRating(item.Fields)
		}
		if cr != "" && contentrating.Exceeds(cr, viewer.MaxContentRating) {
			return core.KindItem{}, core.Library{}, pluginitems.KindRecord{}, core.ErrNotFound
		}
	}
	return item, lib, rec, nil
}

func (r *KindResolver) collectPlayableLeaves(ctx context.Context, rec pluginitems.KindRecord, parent core.KindItem) ([]core.KindItem, error) {
	if parent.Unavailable != nil {
		return nil, core.ErrNotAvailable
	}
	lv, _, ok := rec.Decl.Level(parent.Level)
	if ok && lv.Playable && parent.Playable {
		return []core.KindItem{parent}, nil
	}
	var leaves []core.KindItem
	queue := []core.ItemRef{parent.Ref}
	const maxLeaves = 2000
	for len(queue) > 0 && len(leaves) < maxLeaves {
		cur := queue[0]
		queue = queue[1:]
		cursor := ""
		for {
			page, err := r.items.Children(ctx, cur, cursor, 200)
			if err != nil {
				return nil, err
			}
			for _, child := range page.Items {
				if child.Unavailable != nil {
					continue
				}
				clv, _, cok := rec.Decl.Level(child.Level)
				if cok && clv.Playable && child.Playable {
					leaves = append(leaves, child)
					if len(leaves) >= maxLeaves {
						break
					}
				} else {
					queue = append(queue, child.Ref)
				}
			}
			if page.NextCursor == "" || len(page.Items) == 0 || len(leaves) >= maxLeaves {
				break
			}
			cursor = page.NextCursor
		}
	}
	return leaves, nil
}

func (r *KindResolver) kindstreamDeps(ctx context.Context, rec pluginitems.KindRecord, lib core.Library, item core.KindItem, partIdx *int) kindstream.Deps {
	var quota blobcache.Quota
	if r.catalog != nil && lib.Key != "" {
		quota = r.catalog.QuotaFor(rec.PluginID, lib.Key)
	} else {
		quota = blobcache.Quota{
			MaxBytes: rec.Decl.Cache.MaxBytes,
			TTL:      parseKindDuration(rec.Decl.Cache.TTL, 0),
		}
	}
	var ns *blobcache.Namespace
	if r.blobs != nil && lib.Key != "" {
		r.blobs.BindPluginLibrary(rec.PluginID, lib.Key)
		ns = r.blobs.Namespace(lib.Key, quota)
	}
	var allowHost func(string) bool
	var secretFn func(context.Context, string) (string, error)
	if r.mgr != nil {
		allowHost = func(host string) bool {
			return r.mgr.AllowNetworkHost(ctx, rec.PluginID, host)
		}
		host := r.mgr.HostFor(rec.PluginID)
		secretFn = func(sctx context.Context, name string) (string, error) {
			return host.SecretGet(sctx, name)
		}
	}
	return kindstream.Deps{
		OpenFile: func(ctx context.Context, src core.PlayableSource) (kindstream.ByteSource, error) {
			s := &StreamSources{mgr: r.mgr}
			resolved, _, err := s.validateLocalFile(ctx, rec.PluginID, src.Path)
			if err != nil {
				return nil, err
			}
			f, err := s.openGrantedLocalFile(ctx, rec.PluginID, resolved)
			if err != nil {
				return nil, err
			}
			info, err := f.Stat()
			if err != nil {
				f.Close()
				return nil, err
			}
			if !info.Mode().IsRegular() {
				f.Close()
				return nil, core.ErrSourceUnavailable
			}
			return &kindLocalFile{File: f, size: info.Size(), mime: src.MIME}, nil
		},
		HTTP:             r.http,
		Cache:            ns,
		OpenRelease:      r.openRelease,
		Secret:           secretFn,
		AllowHost:        allowHost,
		MaxDownloadBytes: quota.EffectiveMaxBytes(),
		ItemIndex:        effectiveItemIndex(item.Index, partIdx),
		Backoff:          r.backoff,
	}
}

func (r *KindResolver) openItemByteSource(ctx context.Context, rec pluginitems.KindRecord, lib core.Library, item core.KindItem, partIdx *int, accept []string) (kindstream.ByteSource, error) {
	if item.Unavailable != nil {
		return nil, core.ErrNotAvailable
	}
	ksDeps := r.kindstreamDeps(ctx, rec, lib, item, partIdx)

	// 1. Check persisted positive/negative resolution cache first so repeat
	// opens never call into the plugin runtime while the resolution is fresh.
	cachedSrc, ok, err := r.items.GetPartResolution(ctx, item.Ref, partIdx)
	if err != nil {
		return nil, err
	}
	if ok {
		bs, oerr := kindstream.Open(ctx, cachedSrc, ksDeps)
		if oerr == nil {
			if cachedSrc.Size <= 0 && bs.Size() > 0 {
				cachedSrc.Size = bs.Size()
				if cachedSrc.MIME == "" {
					cachedSrc.MIME = bs.MIME()
				}
				_ = r.items.SavePartResolution(ctx, item.Ref, partIdx, cachedSrc)
			}
			return bs, nil
		}
		// Cached source went stale upstream: invalidate and re-resolve once.
		_ = r.items.InvalidatePartResolution(ctx, item.Ref, partIdx)
	}

	// 2. Singleflight plugin /resolve call per (item_ref, part) when uncached.
	sources, err := r.resolveItemSources(ctx, rec, lib, item, partIdx, accept)
	if err != nil {
		return nil, err
	}
	bs, winning, err := kindstream.OpenSources(ctx, sources, ksDeps)
	if err != nil {
		if errors.Is(err, core.ErrArchiveUnstreamable) && r.log != nil {
			r.log.Warn("this archive format or compression method cannot be streamed",
				"plugin", rec.PluginID,
				"library", lib.Key,
				"item", item.ID,
				"err", err,
			)
		}
		return nil, err
	}
	if winning.Size <= 0 && bs.Size() > 0 {
		winning.Size = bs.Size()
	}
	if winning.MIME == "" && bs.MIME() != "" {
		winning.MIME = bs.MIME()
	}
	_ = r.items.SavePartResolution(ctx, item.Ref, partIdx, winning)
	return bs, nil
}

func (r *KindResolver) resolveItemSources(ctx context.Context, rec pluginitems.KindRecord, lib core.Library, item core.KindItem, partIdx *int, accept []string) ([]core.PlayableSource, error) {
	if item.Unavailable != nil {
		return nil, core.ErrNotAvailable
	}
	if r.mgr == nil {
		return nil, core.ErrSourceUnavailable
	}
	fk := resolveFlightKey{ref: item.Ref, part: -1}
	var partCopy *int
	if partIdx != nil {
		v := *partIdx
		partCopy = &v
		fk.part = v
	}

	r.flightMu.Lock()
	fl, ok := r.flights[fk]
	if !ok {
		fl = &resolveFlight{done: make(chan struct{})}
		r.flights[fk] = fl
		go func() {
			defer func() {
				// Leave the map before waking waiters: a request arriving
				// after a waiter returns must start a new resolve, never
				// join this finished flight and reuse its result.
				r.flightMu.Lock()
				delete(r.flights, fk)
				r.flightMu.Unlock()
				close(fl.done)
			}()
			// Give the detached flight slightly more headroom than Manager.Call's
			// ResolverTotal so pending expiry returns HTTP 202 (ErrPreparing)
			// rather than racing parent context cancellation (#580).
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), plugins.ResolverTotal+5*time.Second)
			defer cancel()
			fl.sources, fl.err = r.callPluginResolve(rctx, rec, lib, item, partCopy, accept)
		}()
	}
	r.flightMu.Unlock()

	select {
	case <-fl.done:
		return fl.sources, fl.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func parsePreparingReply(body []byte) error {
	var pending plugins.PendingReply
	if len(body) > 0 {
		_ = json.Unmarshal(body, &pending)
	}
	return core.PreparingFromPending(pending.RetryInMS)
}

func (r *KindResolver) callPluginResolve(ctx context.Context, rec pluginitems.KindRecord, lib core.Library, item core.KindItem, partIdx *int, accept []string) ([]core.PlayableSource, error) {
	pr, err := r.mgr.Get(ctx, rec.PluginID)
	if err != nil || !pr.Enabled || !pr.Manifest.HasCapability(plugins.CapKindResolver) {
		return nil, core.ErrSourceUnavailable
	}
	var clientMap map[string]any
	if len(accept) > 0 {
		clientMap = map[string]any{"accept": accept}
	}
	body, err := json.Marshal(core.KindResolveRequest{
		LibraryKey: lib.Key,
		Item:       item,
		PartIndex:  partIdx,
		Client:     clientMap,
	})
	if err != nil {
		return nil, err
	}
	resp, err := r.mgr.Call(ctx, rec.PluginID, plugins.Request{
		Method: "POST",
		Path:   "/ether/v1/kinds/" + rec.KindKey + "/resolve",
		Body:   body,
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, core.ErrNotAvailable
		}
		return nil, fmt.Errorf("%w: %v", core.ErrSourceUnavailable, err)
	}
	if resp.Status == http.StatusAccepted {
		return nil, parsePreparingReply(resp.Body)
	}
	if resp.Status == http.StatusNotFound {
		_ = r.items.SaveNegativePartResolution(ctx, item.Ref, partIdx)
		return nil, core.ErrNotAvailable
	}
	if resp.Status != http.StatusOK {
		return nil, fmt.Errorf("%w: resolver status %d", core.ErrSourceUnavailable, resp.Status)
	}
	var resolved core.KindResolveResponse
	if err := json.Unmarshal(resp.Body, &resolved); err != nil {
		return nil, fmt.Errorf("%w: invalid resolver payload: %v", core.ErrSourceUnavailable, err)
	}
	if resolved.Pending {
		return nil, core.PreparingFromPending(resolved.RetryInMS)
	}
	for _, src := range resolved.Sources {
		if strings.EqualFold(strings.TrimSpace(src.Pattern), "file") {
			s := &StreamSources{mgr: r.mgr}
			if _, _, err := s.validateLocalFile(ctx, rec.PluginID, src.Path); err != nil {
				return nil, err
			}
		}
	}
	if len(resolved.Sources) == 0 {
		_ = r.items.SaveNegativePartResolution(ctx, item.Ref, partIdx)
		return nil, core.ErrNotAvailable
	}
	if err := r.validateNoRawSecretsInHeaders(ctx, pr.ID, pr.Manifest.Ether.Permissions.Secrets, resolved.Sources); err != nil {
		return nil, err
	}
	if resolved.TTLSec > 0 {
		exp := time.Now().Add(time.Duration(resolved.TTLSec) * time.Second).Unix()
		for i := range resolved.Sources {
			if resolved.Sources[i].ExpiresAt == 0 {
				resolved.Sources[i].ExpiresAt = exp
			}
		}
	}
	return resolved.Sources, nil
}

// validateNoRawSecretsInHeaders rejects resolver headers whose value equals or
// embeds a stored plugin secret, enforcing the {secret:name} placeholder rule.
func (r *KindResolver) validateNoRawSecretsInHeaders(ctx context.Context, pluginID string, secretNames []string, sources []core.PlayableSource) error {
	if r.mgr == nil || len(secretNames) == 0 {
		return nil
	}
	hasHeaders := false
	for _, src := range sources {
		if len(src.Headers) > 0 {
			hasHeaders = true
			break
		}
	}
	if !hasHeaders {
		return nil
	}
	host := r.mgr.HostFor(pluginID)
	var rawSecrets []string
	for _, name := range secretNames {
		if val, err := host.SecretGet(ctx, name); err == nil && val != "" {
			rawSecrets = append(rawSecrets, val)
		}
	}
	if len(rawSecrets) == 0 {
		return nil
	}
	for _, src := range sources {
		for hk, hv := range src.Headers {
			for _, raw := range rawSecrets {
				if hv == raw || strings.Contains(hv, raw) {
					return fmt.Errorf("%w: resolver header %q contains raw secret value; use {secret:name} placeholder", core.ErrSourceUnavailable, hk)
				}
			}
		}
	}
	return nil
}

func (r *KindResolver) openRelease(ctx context.Context, hint core.ReleaseHint) (core.MediaSource, error) {
	if r.engine == nil {
		return nil, core.ErrSourceUnavailable
	}
	var rel core.Release
	fileIndex := hint.FileIndex
	if fileIndex == nil && hint.Candidate != nil {
		fileIndex = hint.Candidate.FileIndex
	}
	if fileIndex != nil && *fileIndex < 0 {
		return nil, fmt.Errorf("release fileIndex must be non-negative")
	}
	if fileIndex != nil || strings.TrimSpace(hint.File) != "" {
		rel.Target = &core.ReleaseTarget{
			FileIndex:     fileIndex,
			ExactFile:     strings.TrimSpace(hint.File),
			AllowNonVideo: true,
		}
	}
	if hint.Candidate != nil {
		rel.InfoHash = strings.ToLower(strings.TrimSpace(hint.Candidate.InfoHash))
		rel.MagnetURL = strings.TrimSpace(hint.Candidate.Magnet)
		rel.NZBURL = strings.TrimSpace(hint.Candidate.NZBURL)
		rel.Name = hint.Candidate.Title
		rel.Size = hint.Candidate.Size
		rel.Indexer = hint.Candidate.Indexer
		rel.IndexerID = hint.Candidate.IndexerID
	}
	if h := strings.ToLower(strings.TrimSpace(hint.InfoHash)); h != "" {
		rel.InfoHash = h
	}
	if m := strings.TrimSpace(hint.Magnet); m != "" {
		rel.MagnetURL = m
	}
	if u := strings.TrimSpace(hint.NZBURL); u != "" {
		rel.NZBURL = u
	}
	rel.ID = "kind:" + firstNonEmpty(rel.InfoHash, rel.NZBURL, rel.MagnetURL)
	if rel.Target != nil {
		selection, _ := json.Marshal(rel.Target)
		rel.ID += fmt.Sprintf(":file:%x", sha256.Sum256(selection))
	}
	if rel.Name == "" {
		rel.Name = firstNonEmpty(hint.FileHint, rel.InfoHash, "kind-release")
	}

	if rel.InfoHash != "" || rel.MagnetURL != "" {
		if r.debrid == nil {
			return nil, core.ErrSourceUnavailable
		}
		rel.Protocol = core.ProtocolTorrent
		payload, err := r.debrid.Acquire(ctx, rel)
		if err != nil {
			return nil, err
		}
		return r.engine.Open(ctx, rel.ID, payload)
	}
	if rel.NZBURL != "" {
		if rel.Target != nil {
			return nil, fmt.Errorf("explicit release file selection requires a torrent; use a container source for NZB members")
		}
		if r.indexer == nil {
			return nil, core.ErrSourceUnavailable
		}
		rel.Protocol = core.ProtocolUsenet
		nzb, err := r.indexer.FetchNZB(ctx, rel)
		if err != nil {
			return nil, err
		}
		return r.engine.Open(ctx, rel.ID, nzb)
	}
	return nil, core.ErrSourceUnavailable
}

func extractItemDuration(item core.KindItem, decl plugins.MediaKindMeta) time.Duration {
	if len(item.Fields) == 0 {
		return 0
	}
	parseNum := func(raw json.RawMessage) int64 {
		var n float64
		if err := json.Unmarshal(raw, &n); err == nil && n > 0 {
			return int64(n)
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			if f, perr := strconv.ParseFloat(strings.TrimSpace(s), 64); perr == nil && f > 0 {
				return int64(f)
			}
		}
		return 0
	}
	for _, fd := range decl.Fields {
		if fd.Type == "duration" {
			if raw, ok := item.Fields[fd.Key]; ok {
				if sec := parseNum(raw); sec > 0 {
					return time.Duration(sec) * time.Second
				}
			}
		}
	}
	for _, key := range []string{"duration", "durationSec", "runtime"} {
		if raw, ok := item.Fields[key]; ok {
			if sec := parseNum(raw); sec > 0 {
				return time.Duration(sec) * time.Second
			}
		}
	}
	return 0
}

func extractItemSizeAndMIME(item core.KindItem, decl plugins.MediaKindMeta) (int64, string) {
	if len(item.Fields) == 0 {
		return 0, ""
	}
	var size int64
	var mime string
	parseNum := func(raw json.RawMessage) int64 {
		var n float64
		if err := json.Unmarshal(raw, &n); err == nil && n > 0 {
			return int64(n)
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			if v, perr := strconv.ParseInt(strings.TrimSpace(s), 10, 64); perr == nil && v > 0 {
				return v
			}
		}
		return 0
	}
	for _, fd := range decl.Fields {
		if fd.Type == "bytes" {
			if raw, ok := item.Fields[fd.Key]; ok {
				if v := parseNum(raw); v > 0 {
					size = v
					break
				}
			}
		}
	}
	if size == 0 {
		for _, key := range []string{"size", "sizeBytes", "byteSize"} {
			if raw, ok := item.Fields[key]; ok {
				if v := parseNum(raw); v > 0 {
					size = v
					break
				}
			}
		}
	}
	for _, key := range []string{"mime", "mimeType", "contentType"} {
		if raw, ok := item.Fields[key]; ok {
			var s string
			if err := json.Unmarshal(raw, &s); err == nil && strings.TrimSpace(s) != "" {
				mime = strings.TrimSpace(s)
				break
			}
		}
	}
	return size, mime
}

type kindLocalFile struct {
	*os.File
	size int64
	mime string
}

func (f *kindLocalFile) Size() int64  { return f.size }
func (f *kindLocalFile) Name() string { return filepath.Base(f.File.Name()) }
func (f *kindLocalFile) MIME() string {
	if strings.TrimSpace(f.mime) == "" {
		return "application/octet-stream"
	}
	return f.mime
}

func effectiveItemIndex(itemIdx int, partIdx *int) int {
	if partIdx != nil && *partIdx >= 0 && itemIdx == 0 {
		return *partIdx
	}
	return itemIdx
}

type partDefaultByteSource struct {
	kindstream.ByteSource
	name string
	mime string
}

func (p *partDefaultByteSource) Name() string {
	if p.name != "" {
		return p.name
	}
	return p.ByteSource.Name()
}

func (p *partDefaultByteSource) MIME() string {
	if p.mime != "" {
		return p.mime
	}
	return p.ByteSource.MIME()
}

func withPartDefaults(bs kindstream.ByteSource, part core.KindItemPart, itemTitle string) kindstream.ByteSource {
	name := bs.Name()
	if name == "" {
		name = firstNonEmpty(part.Name, itemTitle)
	}
	mime := bs.MIME()
	if (mime == "" || mime == "application/octet-stream") && part.MIME != "" {
		mime = part.MIME
	}
	if name == bs.Name() && mime == bs.MIME() {
		return bs
	}
	return &partDefaultByteSource{ByteSource: bs, name: name, mime: mime}
}

type preopenedSequence struct {
	kindstream.ByteSource
	name    string
	cleanup func() error
}

func (p *preopenedSequence) Name() string {
	if p.name != "" {
		return p.name
	}
	return p.ByteSource.Name()
}

func (p *preopenedSequence) Close() error {
	err1 := p.ByteSource.Close()
	var err2 error
	if p.cleanup != nil {
		err2 = p.cleanup()
	}
	if err1 != nil {
		return err1
	}
	return err2
}
