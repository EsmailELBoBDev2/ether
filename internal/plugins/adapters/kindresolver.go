package adapters

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/blobcache"
	"github.com/braedonsaunders/ether/internal/contentrating"
	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/debrid"
	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/kindstream"
	"github.com/braedonsaunders/ether/internal/libraries"
	"github.com/braedonsaunders/ether/internal/newznab"
	"github.com/braedonsaunders/ether/internal/nzb"
	"github.com/braedonsaunders/ether/internal/pluginitems"
	"github.com/braedonsaunders/ether/internal/plugins"
	"github.com/braedonsaunders/ether/internal/releaseparse"
	"github.com/braedonsaunders/ether/internal/resolver"
)

// maxRecentSourceCandidates bounds category-only recent-release searches
// (empty query with category set) to the newest 100 candidates.
const maxRecentSourceCandidates = 100

// DebridSourceBackend is the subset of debrid.Service used by KindResolver for
// host-mediated debrid cache checks and torrent acquisition.
type DebridSourceBackend interface {
	CheckCached(ctx context.Context, hashes []string) (map[string]bool, bool)
	Acquire(ctx context.Context, rel core.Release) ([]byte, error)
	ListReleaseFiles(ctx context.Context, rel core.Release) ([]core.ReleaseFile, error)
}

const (
	maxKindCandidateCacheEntries = 512
	defaultKindCandidateCacheTTL = 15 * time.Minute
)

// KindResolverDeps configures a KindResolver.
type KindResolverDeps struct {
	Manager       *plugins.Manager
	Libraries     *libraries.Service
	Items         *pluginitems.Service
	Blobs         *blobcache.Cache
	Catalog       *KindCatalog
	Indexer       core.Indexer
	Resolver      *resolver.Service
	Debrid        DebridSourceBackend
	Engine        core.Engine
	UsenetServing func() bool
	Now           func() time.Time
	HTTP          *http.Client
	Authorize     func(ctx context.Context, viewer *core.Viewer, key core.RatingKey) error
	Logger        *slog.Logger
}

type resolveFlightKey struct {
	ref    core.ItemRef
	part   int
	source string
}

type resolveFlight struct {
	done    chan struct{}
	sources []core.PlayableSource
	err     error
}

type cachedKindCandidates struct {
	sources   []core.PlayableSource
	expiresAt time.Time
	storedAt  time.Time
}

// KindResolver implements frontend.KindStreams and frontend.KindPartStreams for
// plugin content kinds and supplies the host-side ResolveSources callback for
// plugins.KindBackends.
type KindResolver struct {
	mgr           *plugins.Manager
	libs          *libraries.Service
	items         *pluginitems.Service
	blobs         *blobcache.Cache
	catalog       *KindCatalog
	indexer       core.Indexer
	resolver      *resolver.Service
	debrid        DebridSourceBackend
	engine        core.Engine
	usenetServing func() bool
	now           func() time.Time
	http          *http.Client
	authorize     func(ctx context.Context, viewer *core.Viewer, key core.RatingKey) error
	backoff       *kindstream.HostBackoff
	log           *slog.Logger

	flightMu sync.Mutex
	flights  map[resolveFlightKey]*resolveFlight

	candMu    sync.Mutex
	candCache map[resolveFlightKey]cachedKindCandidates

	usenetCheckMu   sync.Mutex
	usenetByRelease map[string]*usenetCheckEntry
	usenetByHash    map[string]*usenetCheckEntry
	usenetEntries   map[*usenetCheckEntry]struct{}
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
	nowFn := deps.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	return &KindResolver{
		mgr:             deps.Manager,
		libs:            deps.Libraries,
		items:           deps.Items,
		blobs:           deps.Blobs,
		catalog:         deps.Catalog,
		indexer:         deps.Indexer,
		resolver:        deps.Resolver,
		debrid:          deps.Debrid,
		engine:          deps.Engine,
		usenetServing:   deps.UsenetServing,
		now:             nowFn,
		http:            hc,
		authorize:       deps.Authorize,
		backoff:         kindstream.NewHostBackoff(),
		log:             log.With("component", "kindresolver"),
		flights:         make(map[resolveFlightKey]*resolveFlight),
		candCache:       make(map[resolveFlightKey]cachedKindCandidates),
		usenetByRelease: make(map[string]*usenetCheckEntry),
		usenetByHash:    make(map[string]*usenetCheckEntry),
		usenetEntries:   make(map[*usenetCheckEntry]struct{}),
	}
}

// OpenKind implements frontend.KindStreams.
func (r *KindResolver) OpenKind(ctx context.Context, viewer *core.Viewer, key core.RatingKey, accept []string) (*frontend.KindMedia, error) {
	return r.OpenKindExcluding(ctx, viewer, key, accept, nil)
}

func singlePartIndex(item core.KindItem) (*int, bool) {
	parts := core.ExtractKindItemPartsFromItem(item)
	switch len(parts) {
	case 0:
		return nil, true
	case 1:
		zero := 0
		return &zero, true
	default:
		return nil, false
	}
}

func (r *KindResolver) projectedTitle(ctx context.Context, item core.KindItem, rec pluginitems.KindRecord, lib core.Library) *core.Title {
	var resolve pluginitems.TextResolver
	if r != nil && r.mgr != nil {
		resolve = r.mgr.ResolveTextFunc(ctx, rec.PluginID, "")
	}
	t := pluginitems.ProjectWithText(item, rec.Decl, lib.Present, resolve, lib.CardShape)
	if (t.Type == core.TypeEpisode || t.Type == core.TypeSeason) && item.ParentID != "" {
		cur := item.ParentID
		for range 8 {
			parent, _, err := r.items.GetByRef(ctx, core.NewItemRef(rec.PluginID, rec.KindKey, cur))
			if err != nil {
				break
			}
			if parent.ParentID == "" {
				t.ShowKey = parent.Key
				t.ShowTitle = parent.Title
				break
			}
			cur = parent.ParentID
		}
	}
	return &t
}

func (r *KindResolver) openSingleSource(ctx context.Context, rec pluginitems.KindRecord, lib core.Library, item core.KindItem, partIdx *int, targetSrc core.PlayableSource) (*frontend.KindMedia, error) {
	ksDeps := r.kindstreamDeps(ctx, rec, lib, item, partIdx, nil)
	bs, err := kindstream.Open(ctx, targetSrc, ksDeps)
	if err != nil {
		return nil, err
	}
	parts := core.ExtractKindItemPartsFromItem(item)
	var dur time.Duration
	if partIdx != nil && *partIdx < len(parts) {
		bs = withPartDefaults(bs, parts[*partIdx], item.Title)
		if parts[*partIdx].DurationSec > 0 {
			dur = time.Duration(parts[*partIdx].DurationSec * float64(time.Second))
		}
	}
	if dur == 0 && len(parts) <= 1 {
		dur = extractItemDuration(item, rec.Decl)
	}
	if dur == 0 && targetSrc.DurationSec > 0 {
		dur = time.Duration(targetSrc.DurationSec * float64(time.Second))
	}
	rel := playableSourceRelease(targetSrc, item.Title)
	if rel.Size <= 0 {
		rel.Size = bs.Size()
	}
	if rel.Name == "" {
		rel.Name = firstNonEmpty(bs.Name(), item.Title)
	}
	return &frontend.KindMedia{
		ReaderAt:    bs,
		Closer:      bs,
		Size:        bs.Size(),
		MIME:        bs.MIME(),
		Duration:    dur,
		Name:        bs.Name(),
		Release:     rel,
		MediaSource: unwrapKindMediaSource(bs),
	}, nil
}

func matchPlayableSource(sources []core.PlayableSource, defaultTitle, sourceID string, nowUnix int64) (core.PlayableSource, bool) {
	if sourceID == "" {
		return core.PlayableSource{}, false
	}
	for _, src := range sources {
		if src.ExpiresAt > 0 && nowUnix >= src.ExpiresAt {
			continue
		}
		if src.ID == sourceID {
			return src, true
		}
	}
	for _, src := range sources {
		if src.ExpiresAt > 0 && nowUnix >= src.ExpiresAt {
			continue
		}
		if playableSourceRelease(src, defaultTitle).ID == sourceID {
			return src, true
		}
	}
	return core.PlayableSource{}, false
}

// OpenKindSource opens exactly one plugin kind source by its stable candidate
// release id (a version-slot pick) or PlayableSource.ID. It reuses the
// in-memory and persisted resolution candidate caches so a warm list does not
// re-run the plugin, opens only the matching candidate, and leaves the shared
// default winner untouched. A source id not among the current non-expired
// candidates returns ErrNotAvailable.
func (r *KindResolver) OpenKindSource(ctx context.Context, viewer *core.Viewer, key core.RatingKey, sourceID string, accept []string) (*frontend.KindMedia, error) {
	if r == nil || r.items == nil || r.libs == nil || !key.IsPluginKey() || sourceID == "" {
		return nil, core.ErrNotFound
	}
	var forcedPart *int
	if parentKey, pIdx, isPart := key.PluginPart(); isPart {
		key = parentKey
		idx := pIdx
		forcedPart = &idx
	}
	item, lib, rec, err := r.loadAuthorized(ctx, viewer, key)
	if err != nil {
		return nil, err
	}
	if item.Unavailable != nil {
		return nil, core.ErrNotAvailable
	}
	lv, _, ok := rec.Decl.Level(item.Level)
	if !ok || !lv.Playable || !item.Playable {
		return nil, core.ErrNotAvailable
	}
	var partIdx *int
	if forcedPart != nil {
		parts := core.ExtractKindItemPartsFromItem(item)
		if len(parts) > 0 && *forcedPart >= len(parts) {
			return nil, core.ErrNotFound
		}
		partIdx = forcedPart
	} else {
		partIdx, ok = singlePartIndex(item)
		if !ok {
			// Multi-part items expose a single composite sequence, not per-release versions.
			return nil, core.ErrNotAvailable
		}
	}
	sources, err := r.resolveCandidates(ctx, rec, lib, item, partIdx, accept)
	if err != nil {
		return nil, err
	}
	src, matched := matchPlayableSource(sources, item.Title, sourceID, r.now().Unix())
	if !matched {
		return nil, core.ErrNotAvailable
	}
	return r.openSingleSource(ctx, rec, lib, item, partIdx, src)
}

// OpenKindPartSource opens exactly one resolver source (and optional zero-based
// part) for a source-scoped frontend adapter stream ticket (#661). It passes
// KindResolveRequest.Source to /resolve, opens only the matching source via
// openSingleSource with no failover to other sources, and leaves the shared
// default winner and candidate caches untouched. A plugin that ignores
// KindResolveRequest.Source still works: Ether picks the source with the
// matching PlayableSource.ID from the response, or returns ErrNotAvailable if
// it is absent.
func (r *KindResolver) OpenKindPartSource(ctx context.Context, viewer *core.Viewer, key core.RatingKey, partIndex *int, sourceID string, accept []string) (*frontend.KindMedia, error) {
	if r == nil || r.items == nil || r.libs == nil || !key.IsPluginKey() || sourceID == "" {
		return nil, core.ErrNotFound
	}
	if partIndex != nil && *partIndex < 0 {
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

	targetItem := item
	var effectivePart *int
	if lv.Playable && item.Playable {
		parts := core.ExtractKindItemPartsFromItem(item)
		if partIndex != nil {
			if len(parts) == 0 || *partIndex >= len(parts) {
				return nil, core.ErrNotFound
			}
			idx := *partIndex
			effectivePart = &idx
		} else {
			p, ok := singlePartIndex(item)
			if !ok {
				return nil, core.ErrNotAvailable
			}
			effectivePart = p
		}
	} else {
		if partIndex == nil {
			return nil, core.ErrNotAvailable
		}
		leaves, err := r.collectPlayableLeaves(ctx, rec, item)
		if err != nil {
			return nil, err
		}
		if *partIndex >= len(leaves) {
			return nil, core.ErrNotFound
		}
		targetItem = leaves[*partIndex]
		p, ok := singlePartIndex(targetItem)
		if !ok {
			return nil, core.ErrNotAvailable
		}
		effectivePart = p
	}

	fk := flightKeyForItemSource(targetItem.Ref, effectivePart, sourceID)
	if cached, ok := r.cachedCandidates(fk); ok {
		if src, matched := matchPlayableSource(cached, targetItem.Title, sourceID, r.now().Unix()); matched {
			km, oerr := r.openSingleSource(ctx, rec, lib, targetItem, effectivePart, src)
			if oerr == nil {
				return km, nil
			}
			if ctx.Err() != nil {
				return nil, oerr
			}
		}
		// Cached source expired or went stale upstream: drop and re-resolve once.
		r.deleteCandidates(fk)
	}

	sources, err := r.resolveItemSourcesForSource(ctx, rec, lib, targetItem, effectivePart, sourceID, accept)
	if err != nil {
		return nil, err
	}
	src, matched := matchPlayableSource(sources, targetItem.Title, sourceID, r.now().Unix())
	if !matched {
		return nil, core.ErrNotAvailable
	}
	km, err := r.openSingleSource(ctx, rec, lib, targetItem, effectivePart, src)
	if err != nil {
		return nil, err
	}
	r.storeCandidates(fk, []core.PlayableSource{src})
	return km, nil
}

// KindVersions lists the playable version menu of a plugin kind item in the
// same W152 shape catalogue VersionSlots uses: index 0 is the synthetic Auto
// slot (backed by the viewer's remembered choice when matched, else the
// plugin's first source), followed by the remaining distinct /resolve sources.
// When allowResolve is false or ctx carries LocalResponse, only cached
// candidates are inspected and the plugin's /resolve endpoint is never called.
func (r *KindResolver) KindVersions(ctx context.Context, viewer *core.Viewer, key core.RatingKey, accept []string, allowResolve bool) ([]core.ReleaseVariant, error) {
	if r == nil || r.items == nil || r.libs == nil || viewer == nil || !key.IsPluginKey() {
		return nil, core.ErrNotFound
	}
	var forcedPart *int
	if parentKey, pIdx, isPart := key.PluginPart(); isPart {
		key = parentKey
		idx := pIdx
		forcedPart = &idx
	}
	item, lib, rec, err := r.loadAuthorized(ctx, viewer, key)
	if err != nil {
		return nil, err
	}
	if item.Unavailable != nil {
		return nil, nil
	}
	lv, _, ok := rec.Decl.Level(item.Level)
	if !ok || !lv.Playable || !item.Playable {
		return nil, nil
	}
	var partIdx *int
	if forcedPart != nil {
		parts := core.ExtractKindItemPartsFromItem(item)
		if len(parts) > 0 && *forcedPart >= len(parts) {
			return nil, core.ErrNotFound
		}
		partIdx = forcedPart
	} else {
		partIdx, ok = singlePartIndex(item)
		if !ok {
			return nil, nil
		}
	}
	var sources []core.PlayableSource
	if allowResolve && !core.LocalResponse(ctx) {
		sources, err = r.resolveCandidates(ctx, rec, lib, item, partIdx, accept)
	} else {
		var hit bool
		sources, hit, err = r.cachedCandidatesFor(ctx, item, partIdx)
		if !hit && err == nil {
			return nil, nil
		}
	}
	if err != nil {
		if errors.Is(err, core.ErrNotAvailable) || errors.Is(err, core.ErrPreparing) || errors.Is(err, core.ErrSourceUnavailable) {
			return nil, nil
		}
		return nil, err
	}
	itemDur := extractItemDuration(item, rec.Decl)
	nowUnix := r.now().Unix()
	type entry struct {
		rel     core.Release
		variant core.ReleaseVariant
	}
	entries := make([]entry, 0, len(sources))
	seen := make(map[string]bool, len(sources))
	for _, src := range sources {
		if src.ExpiresAt > 0 && nowUnix >= src.ExpiresAt {
			continue
		}
		rel := playableSourceRelease(src, item.Title)
		if rel.ID == "" || seen[rel.ID] {
			continue
		}
		seen[rel.ID] = true
		entries = append(entries, entry{
			rel:     rel,
			variant: r.kindVariantFor(src, rel, item.Title, itemDur),
		})
	}
	if len(entries) == 0 {
		return nil, nil
	}

	autoIdx := 0
	autoRemembered := false
	if r.resolver != nil {
		title := r.projectedTitle(ctx, item, rec, lib)
		rels := make([]core.Release, len(entries))
		for i, e := range entries {
			rels[i] = e.rel
		}
		if idx, _ := r.resolver.MatchViewerChoice(viewer.ID, title, rels); idx >= 0 {
			autoIdx = idx
			autoRemembered = true
		}
	}

	autoEntry := entries[autoIdx].variant
	autoLabel := "Auto"
	if lbl := strings.TrimSpace(autoEntry.Label); lbl != "" {
		autoLabel = "Auto · " + lbl
	}
	capLen := min(len(entries), core.MaxVariants)
	variants := make([]core.ReleaseVariant, 0, capLen)
	variants = append(variants, core.ReleaseVariant{
		ID:               core.KindAutoVersionID,
		Label:            autoLabel,
		Edition:          autoEntry.Edition,
		Auto:             true,
		Remembered:       autoRemembered,
		Availability:     autoEntry.Availability,
		AvailabilityNote: autoEntry.AvailabilityNote,
		Release:          autoEntry.Release,
		Info:             autoEntry.Info,
	})
	for i, e := range entries {
		if i == autoIdx {
			continue
		}
		if len(variants) >= core.MaxVariants {
			break
		}
		variants = append(variants, e.variant)
	}
	return variants, nil
}

// kindVariantFor projects one plugin /resolve source into a ReleaseVariant
// whose ID is the source's stable release id, carrying estimated media metadata
// until the source is probed.
func (r *KindResolver) kindVariantFor(src core.PlayableSource, rel core.Release, title string, itemDur time.Duration) core.ReleaseVariant {
	size := rel.Size
	if size <= 0 {
		size = src.Size
	}
	dur := itemDur
	if src.DurationSec > 0 {
		dur = time.Duration(src.DurationSec * float64(time.Second))
	}
	info := core.MediaInfo{
		Container: kindContainerFromMIME(src.MIME, firstNonEmpty(rel.Name, src.Path, src.URL)),
		Duration:  dur,
		Size:      size,
		Estimated: true,
	}
	if size > 0 && dur > 0 {
		if sec := int64(dur / time.Second); sec > 0 {
			info.BitrateKbps = int((size * 8) / sec / 1000)
		}
	}
	if rel.Info.Height > 0 || rel.Info.VideoCodec != "" {
		w := 0
		if rel.Info.Height > 0 {
			w = (rel.Info.Height * 16) / 9
		}
		vs := core.StreamInfo{
			Index:       0,
			Type:        "video",
			Codec:       firstNonEmpty(rel.Info.VideoCodec, "h264"),
			Width:       w,
			Height:      rel.Info.Height,
			DolbyVision: rel.Info.DolbyVision,
		}
		if rel.Info.HDR10 || rel.Info.DolbyVision {
			vs.ColorTransfer = "smpte2084"
		}
		info.Streams = append(info.Streams, vs)
	}
	if rel.Info.AudioCodec != "" {
		info.Streams = append(info.Streams, core.StreamInfo{
			Index:    len(info.Streams),
			Type:     "audio",
			Codec:    rel.Info.AudioCodec,
			Channels: kindChannelsFromString(rel.Info.Channels),
		})
	}
	avail := core.VersionFetch
	availNote := ""
	switch strings.ToLower(strings.TrimSpace(src.Pattern)) {
	case "file":
		avail = core.VersionInstant
	case "range", "direct", "download", "container":
		if src.BlobRef != "" {
			avail = core.VersionInstant
		} else {
			avail = core.VersionCached
		}
	case kindstream.PatternRelease:
		if src.Release != nil && src.Release.Candidate != nil && src.Release.Candidate.Cached {
			avail = core.VersionCached
		} else if rel.Protocol == core.ProtocolTorrent {
			avail = core.VersionFetch
			availNote = "May need to fetch before playback"
		}
	}
	return core.ReleaseVariant{
		ID:               rel.ID,
		Label:            firstNonEmpty(src.Label, rel.Name, title),
		Edition:          rel.Info.Edition,
		Availability:     avail,
		AvailabilityNote: availNote,
		Release:          rel,
		Info:             info,
	}
}

func kindContainerFromMIME(mime, name string) string {
	m := strings.ToLower(strings.TrimSpace(mime))
	if semi := strings.IndexByte(m, ';'); semi >= 0 {
		m = strings.TrimSpace(m[:semi])
	}
	switch m {
	case "video/x-matroska", "audio/x-matroska":
		return "mkv"
	case "video/mp4", "audio/mp4":
		return "mp4"
	case "video/webm", "audio/webm":
		return "webm"
	case "video/mp2t":
		return "mpegts"
	case "video/quicktime":
		return "mov"
	case "audio/mpeg":
		return "mp3"
	case "audio/flac":
		return "flac"
	}
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(name))) {
	case ".mkv":
		return "mkv"
	case ".mp4", ".m4v", ".m4a":
		return "mp4"
	case ".webm":
		return "webm"
	case ".ts", ".m2ts":
		return "mpegts"
	case ".mov":
		return "mov"
	}
	return ""
}

func kindChannelsFromString(ch string) int {
	switch strings.TrimSpace(ch) {
	case "7.1":
		return 8
	case "6.1":
		return 7
	case "5.1":
		return 6
	case "5.0":
		return 5
	case "2.0":
		return 2
	case "1.0":
		return 1
	default:
		return 2
	}
}

func kindReleaseSource(raw string) core.Source {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "remux":
		return core.SourceRemux
	case "bluray", "blu-ray", "bdrip", "disc":
		return core.SourceBluRay
	case "web-dl", "webdl", "web":
		return core.SourceWEBDL
	case "webrip":
		return core.SourceWEBRip
	case "hdtv":
		return core.SourceHDTV
	case "dvd", "dvdrip":
		return core.SourceDVD
	default:
		return core.SourceUnknown
	}
}

// OpenKindExcluding opens a plugin kind item while excluding already-tried
// candidate release IDs (for mid-stream playback failover). Replacement
// candidates come from the stored candidate list of the same kind resolve,
// falling back to a fresh plugin re-resolve when exhausted or expired, and
// respecting provider backoff.
func (r *KindResolver) OpenKindExcluding(ctx context.Context, viewer *core.Viewer, key core.RatingKey, accept, exclude []string) (*frontend.KindMedia, error) {
	if r == nil || r.items == nil || r.libs == nil || !key.IsPluginKey() {
		return nil, core.ErrNotFound
	}
	if parentKey, partIdx, isPart := key.PluginPart(); isPart {
		return r.OpenKindPartExcluding(ctx, viewer, parentKey, partIdx, accept, exclude)
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
		// Honor the viewer's remembered version choice on Auto open without
		// overwriting the shared default winner in plugin_resolutions.
		if len(exclude) == 0 && viewer != nil && r.resolver != nil {
			if partIdx, single := singlePartIndex(item); single {
				title := r.projectedTitle(ctx, item, rec, lib)
				_, hasTitle := r.resolver.ViewerChoiceFor(viewer.ID, item.Key)
				hasShow := false
				if !hasTitle && title.Type == core.TypeEpisode && title.ShowKey.Valid() {
					_, hasShow = r.resolver.ViewerChoiceFor(viewer.ID, title.ShowKey)
				}
				if hasTitle || hasShow {
					if sources, cerr := r.resolveCandidates(ctx, rec, lib, item, partIdx, accept); cerr == nil && len(sources) > 0 {
						nowUnix := r.now().Unix()
						live := make([]core.PlayableSource, 0, len(sources))
						rels := make([]core.Release, 0, len(sources))
						for _, src := range sources {
							if src.ExpiresAt > 0 && nowUnix >= src.ExpiresAt {
								continue
							}
							live = append(live, src)
							rels = append(rels, playableSourceRelease(src, item.Title))
						}
						if idx, _ := r.resolver.MatchViewerChoice(viewer.ID, title, rels); idx >= 0 {
							if media, oerr := r.openSingleSource(ctx, rec, lib, item, partIdx, live[idx]); oerr == nil {
								return media, nil
							}
						}
					}
				}
			}
		}
		return r.openPlayableItemMediaExcluding(ctx, rec, lib, item, accept, exclude)
	}

	synID := "kind:" + key.String()
	if slices.Contains(exclude, synID) {
		return nil, core.ErrNotAvailable
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
				bs, _, _, oerr := r.openPlayableItemByteSourceExcluding(lctx, rec, lib, leafItem, accept, exclude)
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
		Release: core.Release{
			ID:       synID,
			Name:     item.Title,
			Protocol: core.ProtocolHTTP,
			Size:     seq.Size(),
		},
	}, nil
}

// OpenKindPart implements frontend.KindPartStreams (#576). It opens one zero-based
// part of a multi-part plugin kind item, passing KindResolveRequest.PartIndex
// to the plugin's /resolve route.
func (r *KindResolver) OpenKindPart(ctx context.Context, viewer *core.Viewer, key core.RatingKey, partIndex int, accept []string) (*frontend.KindMedia, error) {
	return r.OpenKindPartExcluding(ctx, viewer, key, partIndex, accept, nil)
}

// OpenKindPartExcluding opens one zero-based part of a multi-part plugin kind
// item while skipping candidate release IDs listed in exclude.
func (r *KindResolver) OpenKindPartExcluding(ctx context.Context, viewer *core.Viewer, key core.RatingKey, partIndex int, accept, exclude []string) (*frontend.KindMedia, error) {
	if r == nil || r.items == nil || r.libs == nil || !key.IsPluginKey() || partIndex < 0 {
		return nil, core.ErrNotFound
	}
	if parentKey, _, isPart := key.PluginPart(); isPart {
		key = parentKey
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
		if len(parts) == 0 || partIndex >= len(parts) {
			return nil, core.ErrNotFound
		}
		idx := partIndex
		bs, winning, err := r.openItemByteSourceExcluding(ctx, rec, lib, item, &idx, accept, exclude)
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
		rel := playableSourceRelease(winning, firstNonEmpty(bs.Name(), item.Title))
		if rel.Size <= 0 {
			rel.Size = bs.Size()
		}
		if rel.Name == "" {
			rel.Name = firstNonEmpty(bs.Name(), item.Title)
		}
		return &frontend.KindMedia{
			ReaderAt:    bs,
			Closer:      bs,
			Size:        bs.Size(),
			MIME:        bs.MIME(),
			Duration:    dur,
			Name:        bs.Name(),
			Release:     rel,
			MediaSource: unwrapKindMediaSource(bs),
		}, nil
	}

	leaves, err := r.collectPlayableLeaves(ctx, rec, item)
	if err != nil {
		return nil, err
	}
	if partIndex >= len(leaves) {
		return nil, core.ErrNotFound
	}
	return r.openPlayableItemMediaExcluding(ctx, rec, lib, leaves[partIndex], accept, exclude)
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
	return r.openPlayableItemMediaExcluding(ctx, rec, lib, item, accept, nil)
}

func (r *KindResolver) openPlayableItemMediaExcluding(ctx context.Context, rec pluginitems.KindRecord, lib core.Library, item core.KindItem, accept, exclude []string) (*frontend.KindMedia, error) {
	bs, dur, rel, err := r.openPlayableItemByteSourceExcluding(ctx, rec, lib, item, accept, exclude)
	if err != nil {
		return nil, err
	}
	if rel.Size <= 0 {
		rel.Size = bs.Size()
	}
	if rel.Name == "" {
		rel.Name = firstNonEmpty(bs.Name(), item.Title)
	}
	return &frontend.KindMedia{
		ReaderAt:    bs,
		Closer:      bs,
		Size:        bs.Size(),
		MIME:        bs.MIME(),
		Duration:    dur,
		Name:        bs.Name(),
		Release:     rel,
		MediaSource: unwrapKindMediaSource(bs),
	}, nil
}

func (r *KindResolver) openPlayableItemByteSource(ctx context.Context, rec pluginitems.KindRecord, lib core.Library, item core.KindItem, accept []string) (kindstream.ByteSource, time.Duration, error) {
	bs, dur, _, err := r.openPlayableItemByteSourceExcluding(ctx, rec, lib, item, accept, nil)
	return bs, dur, err
}

func (r *KindResolver) openPlayableItemByteSourceExcluding(ctx context.Context, rec pluginitems.KindRecord, lib core.Library, item core.KindItem, accept, exclude []string) (kindstream.ByteSource, time.Duration, core.Release, error) {
	parts := core.ExtractKindItemPartsFromItem(item)
	if len(parts) == 0 {
		bs, winning, err := r.openItemByteSourceExcluding(ctx, rec, lib, item, nil, accept, exclude)
		if err != nil {
			return nil, 0, core.Release{}, err
		}
		return bs, extractItemDuration(item, rec.Decl), playableSourceRelease(winning, item.Title), nil
	}
	if len(parts) == 1 {
		idx := 0
		bs, winning, err := r.openItemByteSourceExcluding(ctx, rec, lib, item, &idx, accept, exclude)
		if err != nil {
			return nil, 0, core.Release{}, err
		}
		bs = withPartDefaults(bs, parts[0], item.Title)
		dur := extractItemDuration(item, rec.Decl)
		if dur == 0 && parts[0].DurationSec > 0 {
			dur = time.Duration(parts[0].DurationSec * float64(time.Second))
		}
		return bs, dur, playableSourceRelease(winning, item.Title), nil
	}

	// Multi-part item (#576): open part 0 up front so any pending/unavailable
	// status surfaces before HTTP headers are written, and assemble a Sequence
	// over all parts so whole-item readers stream every file in order.
	zero := 0
	firstRaw, winning0, err := r.openItemByteSourceExcluding(ctx, rec, lib, item, &zero, accept, exclude)
	if err != nil {
		return nil, 0, core.Release{}, err
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
			reopened, _, oerr := r.openItemByteSourceExcluding(lctx, rec, lib, item, &idx, accept, exclude)
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
			return nil, 0, core.Release{}, perr
		}
		seqLeaves[i] = kindstream.Leaf{
			Name: firstNonEmpty(partCopy.Name, fmt.Sprintf("%s (part %d)", item.Title, partIdx+1)),
			MIME: partMIME,
			Size: partSize,
			Open: func(lctx context.Context) (kindstream.ByteSource, error) {
				bs, _, oerr := r.openItemByteSourceExcluding(lctx, rec, lib, item, &partIdx, accept, exclude)
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
	rel := playableSourceRelease(winning0, item.Title)
	rel.Size = seq.Size()
	return &preopenedSequence{
		ByteSource: seq,
		name:       firstNonEmpty(item.Title, seq.Name()),
		cleanup:    releasePreopened,
	}, dur, rel, nil
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
		query := strings.TrimSpace(q.Query)
		category := strings.TrimSpace(q.Category)
		if query == "" && category == "" {
			return nil, &plugins.ValidationError{Fields: []plugins.FieldError{{
				Field:   "query",
				Message: "indexer_search needs a query or category",
			}}}
		}
		if _, err := core.SearchCategories(category); err != nil {
			return nil, &plugins.ValidationError{Fields: []plugins.FieldError{{
				Field:   "category",
				Message: err.Error(),
			}}}
		}
		if _, err := resolver.ScoreSourceCandidates(ctx, r.resolver, q.Profile, nil); err != nil {
			if errors.Is(err, resolver.ErrUnknownProfile) {
				return nil, &plugins.ValidationError{Fields: []plugins.FieldError{{
					Field:   "profile",
					Message: err.Error(),
				}}}
			}
			return nil, err
		}
		if r.indexer == nil {
			return []core.SourceCandidate{}, nil
		}
		rels, err := r.indexer.Search(ctx, core.SearchRequest{
			Title:    query,
			Category: category,
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
		if query == "" {
			rels = append([]core.Release(nil), rels...)
			sort.SliceStable(rels, func(i, j int) bool {
				pi, pj := rels[i].PubDate, rels[j].PubDate
				if pi.IsZero() != pj.IsZero() {
					return !pi.IsZero()
				}
				return pi.After(pj)
			})
			if len(rels) > maxRecentSourceCandidates {
				rels = rels[:maxRecentSourceCandidates]
			}
		}
		scored, err := resolver.ScoreSourceCandidates(ctx, r.resolver, q.Profile, rels)
		if err != nil {
			return nil, err
		}
		if query != "" && r.isUsenetBackingOff() && len(scored) > 1 {
			nonUsenet := make([]core.SourceCandidate, 0, len(scored))
			usenetOnly := make([]core.SourceCandidate, 0, len(scored))
			for _, c := range scored {
				if c.NZBURL != "" && c.InfoHash == "" && c.Magnet == "" && c.TorrentURL == "" {
					usenetOnly = append(usenetOnly, c)
				} else {
					nonUsenet = append(nonUsenet, c)
				}
			}
			if len(nonUsenet) > 0 && len(usenetOnly) > 0 {
				scored = append(nonUsenet, usenetOnly...)
			}
		}
		return scored, nil

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

	case "release_files":
		cand := core.SourceCandidate{
			InfoHash:   strings.ToLower(strings.TrimSpace(q.InfoHash)),
			Magnet:     strings.TrimSpace(q.Magnet),
			TorrentURL: strings.TrimSpace(q.TorrentURL),
		}
		if q.Candidate != nil {
			cand = *q.Candidate
			cand.InfoHash = strings.ToLower(strings.TrimSpace(cand.InfoHash))
			cand.Magnet = strings.TrimSpace(cand.Magnet)
			cand.TorrentURL = strings.TrimSpace(cand.TorrentURL)
			cand.NZBURL = strings.TrimSpace(cand.NZBURL)
			if q.InfoHash != "" {
				cand.InfoHash = strings.ToLower(strings.TrimSpace(q.InfoHash))
			}
			if q.Magnet != "" {
				cand.Magnet = strings.TrimSpace(q.Magnet)
			}
			if q.TorrentURL != "" {
				cand.TorrentURL = strings.TrimSpace(q.TorrentURL)
			}
		}
		// The listing answers for the whole release; any file choice the
		// candidate already carries does not narrow it.
		listCand := cand
		listCand.FileIndex = nil
		listCand.Files = nil
		rel, err := releaseFromHint(core.ReleaseHint{Candidate: &listCand}, "")
		if err != nil {
			return nil, err
		}
		if rel.Protocol == core.ProtocolUsenet {
			if r.indexer == nil {
				return []core.SourceCandidate{cand}, nil
			}
			// Fetching through the originating indexer is the same call
			// playback makes, so the NZB lands in the vault and playback
			// does not fetch it again.
			data, err := r.indexer.FetchNZB(ctx, rel)
			if err != nil {
				return nil, err
			}
			files, err := r.listNZBFiles(ctx, rel.ID, data)
			if err != nil {
				return nil, err
			}
			cand.Files = files
			return []core.SourceCandidate{cand}, nil
		}
		if rel.Protocol != core.ProtocolTorrent {
			return []core.SourceCandidate{cand}, nil
		}
		if rel.InfoHash == "" && rel.MagnetURL == "" && rel.NZBURL != "" {
			resolved, err := r.resolveTorrent(ctx, rel)
			if err != nil {
				return nil, err
			}
			rel = resolved
			if rel.InfoHash != "" {
				cand.InfoHash = rel.InfoHash
			}
			if rel.MagnetURL != "" {
				cand.Magnet = rel.MagnetURL
			}
		}
		if r.debrid != nil {
			files, err := r.debrid.ListReleaseFiles(ctx, rel)
			if err == nil {
				cand.Cached = true
				cand.Files = files
				return []core.SourceCandidate{cand}, nil
			}
			if !errors.Is(err, core.ErrNotCached) && !errors.Is(err, debrid.ErrNoProvider) {
				return nil, err
			}
			cand.Cached = false
		}
		if len(rel.TorrentFiles) > 0 {
			cand.Files = slices.Clone(rel.TorrentFiles)
		}
		return []core.SourceCandidate{cand}, nil

	case "usenet_check":
		cand := core.SourceCandidate{
			NZBURL: strings.TrimSpace(q.NZBURL),
		}
		if q.Candidate != nil {
			cand = *q.Candidate
			cand.NZBURL = strings.TrimSpace(cand.NZBURL)
			if strings.TrimSpace(q.NZBURL) != "" {
				cand.NZBURL = strings.TrimSpace(q.NZBURL)
			}
		}
		res, err := r.checkUsenetCandidate(ctx, cand)
		if err != nil {
			return nil, err
		}
		cand.UsenetCheck = &res
		return []core.SourceCandidate{cand}, nil

	case "debrid_acquire":
		if r.debrid == nil {
			return nil, core.ErrNotAvailable
		}
		cand := core.SourceCandidate{
			InfoHash:   strings.ToLower(strings.TrimSpace(q.InfoHash)),
			Magnet:     strings.TrimSpace(q.Magnet),
			TorrentURL: strings.TrimSpace(q.TorrentURL),
		}
		if q.Candidate != nil {
			cand = *q.Candidate
			cand.InfoHash = strings.ToLower(strings.TrimSpace(cand.InfoHash))
			cand.Magnet = strings.TrimSpace(cand.Magnet)
			cand.TorrentURL = strings.TrimSpace(cand.TorrentURL)
			if q.InfoHash != "" {
				cand.InfoHash = strings.ToLower(strings.TrimSpace(q.InfoHash))
			}
			if q.Magnet != "" {
				cand.Magnet = strings.TrimSpace(q.Magnet)
			}
			if q.TorrentURL != "" {
				cand.TorrentURL = strings.TrimSpace(q.TorrentURL)
			}
		}
		rel, err := releaseFromHint(core.ReleaseHint{Candidate: &cand}, "plugin-kind-acquire")
		if err != nil {
			return nil, err
		}
		rel.Protocol = core.ProtocolTorrent
		if rel.InfoHash == "" && rel.MagnetURL == "" && rel.NZBURL != "" {
			resolved, err := r.resolveTorrent(ctx, rel)
			if err != nil {
				return nil, err
			}
			rel = resolved
		}
		if _, err := r.debrid.Acquire(ctx, rel); err != nil {
			return nil, err
		}
		return []core.SourceCandidate{{
			InfoHash:   rel.InfoHash,
			Magnet:     rel.MagnetURL,
			TorrentURL: cand.TorrentURL,
			Cached:     true,
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
		proj := pluginitems.Project(item, rec.Decl, lib.Present, lib.CardShape)
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

// skippedSources collects candidates that failed before a later one opened
// during one item open, so the resolver can decline to save a fallback as
// the item's resolution when an earlier candidate failed because the
// plugin's answer was wrong (#641).
type skippedSources struct {
	failures []kindstream.SourceFailure
}

func (s *skippedSources) pluginAnswerFailed() bool {
	for _, f := range s.failures {
		if f.PluginAnswer() {
			return true
		}
	}
	return false
}

func (r *KindResolver) kindstreamDeps(ctx context.Context, rec pluginitems.KindRecord, lib core.Library, item core.KindItem, partIdx *int, skipped *skippedSources) kindstream.Deps {
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
		// The byte source outlives the prepare request (the client fetches
		// the media later), so the allowlist check must not use the
		// request's cancellable context.
		allowCtx := context.WithoutCancel(ctx)
		allowHost = func(host string) bool {
			hctx, cancel := context.WithTimeout(allowCtx, 5*time.Second)
			defer cancel()
			return r.mgr.AllowNetworkHost(hctx, rec.PluginID, host)
		}
		host := r.mgr.HostFor(rec.PluginID)
		secretFn = func(sctx context.Context, name string) (string, error) {
			return host.SecretGet(sctx, name)
		}
	}
	defaultMIME := kindDefaultMIME(rec.Decl)
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
		HTTP:        r.http,
		Cache:       ns,
		OpenRelease: r.openRelease,
		OpenReleaseSource: func(octx context.Context, src core.PlayableSource) (core.MediaSource, error) {
			return r.openReleaseForSource(octx, src, defaultMIME, secretFn)
		},
		DefaultReleaseMIME: defaultMIME,
		Secret:             secretFn,
		AllowHost:          allowHost,
		MaxDownloadBytes:   quota.EffectiveMaxBytes(),
		ItemIndex:          effectiveItemIndex(item.Index, partIdx),
		Backoff:            r.backoff,
		OnSourceSkipped: func(failures []kindstream.SourceFailure, winning core.PlayableSource) {
			if skipped != nil {
				skipped.failures = append(skipped.failures, failures...)
			}
			for _, f := range failures {
				if r.log == nil {
					break
				}
				r.log.Warn("kind source failed to open; serving a later source",
					"plugin", rec.PluginID,
					"library", lib.Key,
					"item", item.ID,
					"source", f.Source.ID,
					"pattern", f.Source.Pattern,
					"winning", winning.ID,
					"err", f.Err,
				)
			}
		},
	}
}

func (r *KindResolver) openItemByteSource(ctx context.Context, rec pluginitems.KindRecord, lib core.Library, item core.KindItem, partIdx *int, accept []string) (kindstream.ByteSource, error) {
	bs, _, err := r.openItemByteSourceExcluding(ctx, rec, lib, item, partIdx, accept, nil)
	return bs, err
}

func flightKeyForItem(ref core.ItemRef, partIdx *int) resolveFlightKey {
	return flightKeyForItemSource(ref, partIdx, "")
}

func flightKeyForItemSource(ref core.ItemRef, partIdx *int, source string) resolveFlightKey {
	fk := resolveFlightKey{ref: ref, part: -1, source: source}
	if partIdx != nil {
		fk.part = *partIdx
	}
	return fk
}

func (r *KindResolver) openItemByteSourceExcluding(ctx context.Context, rec pluginitems.KindRecord, lib core.Library, item core.KindItem, partIdx *int, accept, exclude []string) (kindstream.ByteSource, core.PlayableSource, error) {
	if item.Unavailable != nil {
		return nil, core.PlayableSource{}, core.ErrNotAvailable
	}
	skipped := &skippedSources{}
	ksDeps := r.kindstreamDeps(ctx, rec, lib, item, partIdx, skipped)
	fk := flightKeyForItem(item.Ref, partIdx)

	saveAndReturn := func(bs kindstream.ByteSource, winning core.PlayableSource) (kindstream.ByteSource, core.PlayableSource, error) {
		if winning.Size <= 0 && bs.Size() > 0 {
			winning.Size = bs.Size()
		}
		if winning.MIME == "" && bs.MIME() != "" {
			winning.MIME = bs.MIME()
		}
		// A fallback that opened only because an earlier candidate failed
		// on the plugin's own answer is not saved as the item's
		// resolution: the plugin's first choice is broken, and saving
		// the fallback would hide that on every later play. Nor is a
		// source whose declared digest is still verifying (#641, #642).
		save := !skipped.pluginAnswerFailed()
		if dr, ok := bs.(kindstream.DigestReporter); ok {
			if declared, verified := dr.DigestState(); declared && !verified {
				save = false
			}
		}
		if save {
			_ = r.items.SavePartResolution(ctx, item.Ref, partIdx, winning)
		}
		return bs, winning, nil
	}

	// 1. Check persisted positive/negative resolution cache first so repeat
	// opens never call into the plugin runtime while the resolution is fresh,
	// unless the cached candidate was excluded by failover or its provider is
	// currently backing off.
	var backingOffCached *core.PlayableSource
	cachedSrc, ok, err := r.items.GetPartResolution(ctx, item.Ref, partIdx)
	if err != nil {
		return nil, core.PlayableSource{}, err
	}
	if ok {
		cachedID := playableSourceRelease(cachedSrc, item.Title).ID
		if slices.Contains(exclude, cachedID) {
			_ = r.items.InvalidatePartResolution(ctx, item.Ref, partIdx)
		} else if r.isSourceBackingOff(cachedSrc) {
			c := cachedSrc
			backingOffCached = &c
		} else {
			bs, oerr := kindstream.Open(ctx, cachedSrc, ksDeps)
			if oerr == nil {
				if cachedSrc.Size <= 0 && bs.Size() > 0 {
					cachedSrc.Size = bs.Size()
					if cachedSrc.MIME == "" {
						cachedSrc.MIME = bs.MIME()
					}
					_ = r.items.SavePartResolution(ctx, item.Ref, partIdx, cachedSrc)
				}
				return bs, cachedSrc, nil
			}
			// Cached source went stale upstream: invalidate and fall through.
			_ = r.items.InvalidatePartResolution(ctx, item.Ref, partIdx)
			r.deleteCandidates(fk)
		}
	}

	// 2. On failover or provider backoff, try remaining candidates from the
	// stored candidate list of the original kind resolve before re-calling
	// the plugin runtime.
	if len(exclude) > 0 || backingOffCached != nil {
		if stored, hasStored, cerr := r.cachedCandidatesFor(ctx, item, partIdx); cerr == nil && hasStored {
			cands := r.filterAndOrderCandidates(stored, item.Title, exclude)
			hasReady := slices.ContainsFunc(cands, func(s core.PlayableSource) bool { return !r.isSourceBackingOff(s) })
			if len(cands) > 0 && (backingOffCached == nil || hasReady) {
				if bs, winning, oerr := kindstream.OpenSources(ctx, cands, ksDeps); oerr == nil {
					return saveAndReturn(bs, winning)
				}
			}
		}
	}

	// 3. Resolve (or re-resolve excluding failed releases) via the plugin.
	// On a normal (non-failover) open, reuse warm cached candidates first so
	// listing versions and then pressing Play only calls /resolve once.
	var sources []core.PlayableSource
	if len(exclude) > 0 {
		sources, err = r.resolveItemSourcesExcluding(ctx, rec, lib, item, partIdx, accept, exclude)
	} else {
		sources, err = r.resolveCandidates(ctx, rec, lib, item, partIdx, accept)
	}
	if err != nil {
		if backingOffCached != nil {
			if bs, oerr := kindstream.Open(ctx, *backingOffCached, ksDeps); oerr == nil {
				return saveAndReturn(bs, *backingOffCached)
			}
		}
		return nil, core.PlayableSource{}, err
	}

	cands := r.filterAndOrderCandidates(sources, item.Title, exclude)
	if len(cands) == 0 {
		if backingOffCached != nil {
			if bs, oerr := kindstream.Open(ctx, *backingOffCached, ksDeps); oerr == nil {
				return saveAndReturn(bs, *backingOffCached)
			}
		}
		return nil, core.PlayableSource{}, core.ErrNotAvailable
	}
	bs, winning, err := kindstream.OpenSources(ctx, cands, ksDeps)
	if err != nil {
		if errors.Is(err, core.ErrArchiveUnstreamable) && r.log != nil {
			r.log.Warn("this archive format or compression method cannot be streamed",
				"plugin", rec.PluginID,
				"library", lib.Key,
				"item", item.ID,
				"err", err,
			)
		}
		return nil, core.PlayableSource{}, err
	}
	return saveAndReturn(bs, winning)
}

func (r *KindResolver) storeCandidates(fk resolveFlightKey, sources []core.PlayableSource) {
	if r == nil || len(sources) == 0 {
		return
	}
	now := r.now()
	exp := now.Add(defaultKindCandidateCacheTTL)
	for _, s := range sources {
		if s.ExpiresAt > 0 {
			sExp := time.Unix(s.ExpiresAt, 0)
			if sExp.After(now) && sExp.Before(exp) {
				exp = sExp
			}
		}
	}
	cloned := slices.Clone(sources)
	r.candMu.Lock()
	defer r.candMu.Unlock()
	for k, v := range r.candCache {
		if !now.Before(v.expiresAt) {
			delete(r.candCache, k)
		}
	}
	if _, exists := r.candCache[fk]; !exists && len(r.candCache) >= maxKindCandidateCacheEntries {
		var oldestKey resolveFlightKey
		var oldestAt time.Time
		first := true
		for k, v := range r.candCache {
			if first || v.storedAt.Before(oldestAt) {
				oldestKey = k
				oldestAt = v.storedAt
				first = false
			}
		}
		if !first {
			delete(r.candCache, oldestKey)
		}
	}
	r.candCache[fk] = cachedKindCandidates{
		sources:   cloned,
		expiresAt: exp,
		storedAt:  now,
	}
}

func (r *KindResolver) cachedCandidates(fk resolveFlightKey) ([]core.PlayableSource, bool) {
	if r == nil {
		return nil, false
	}
	now := r.now()
	r.candMu.Lock()
	defer r.candMu.Unlock()
	entry, ok := r.candCache[fk]
	if !ok {
		return nil, false
	}
	if !now.Before(entry.expiresAt) {
		delete(r.candCache, fk)
		return nil, false
	}
	return slices.Clone(entry.sources), true
}

func (r *KindResolver) deleteCandidates(fk resolveFlightKey) {
	if r == nil {
		return
	}
	r.candMu.Lock()
	delete(r.candCache, fk)
	r.candMu.Unlock()
}

// cachedCandidatesFor checks the in-memory candidate cache first and falls
// back to the persisted plugin_resolutions.candidates column. Expired sources
// are filtered against r.now() so tests with fake clocks expire cleanly.
func (r *KindResolver) cachedCandidatesFor(ctx context.Context, item core.KindItem, partIdx *int) ([]core.PlayableSource, bool, error) {
	fk := flightKeyForItem(item.Ref, partIdx)
	nowUnix := r.now().Unix()
	filterLive := func(srcs []core.PlayableSource) []core.PlayableSource {
		live := make([]core.PlayableSource, 0, len(srcs))
		for _, s := range srcs {
			if s.ExpiresAt > 0 && nowUnix >= s.ExpiresAt {
				continue
			}
			// A Usenet release that already failed in playback is not
			// offered again from the cached candidate list (#665): the
			// check and real playback must agree.
			if r.usenetPlaybackFailed(s) {
				continue
			}
			live = append(live, s)
		}
		return live
	}
	if cached, ok := r.cachedCandidates(fk); ok {
		if live := filterLive(cached); len(live) > 0 {
			return live, true, nil
		}
		r.deleteCandidates(fk)
	}
	if r.items != nil {
		stored, ok, err := r.items.GetPartCandidates(ctx, item.Ref, partIdx)
		if err != nil {
			return nil, ok, err
		}
		if ok {
			if live := filterLive(stored); len(live) > 0 {
				r.storeCandidates(fk, live)
				return live, true, nil
			}
		}
	}
	return nil, false, nil
}

// resolveCandidates returns the cached candidate list when fresh (or an active
// negative cache error), calling the plugin's /resolve only on a cache miss.
func (r *KindResolver) resolveCandidates(ctx context.Context, rec pluginitems.KindRecord, lib core.Library, item core.KindItem, partIdx *int, accept []string) ([]core.PlayableSource, error) {
	if sources, ok, err := r.cachedCandidatesFor(ctx, item, partIdx); ok {
		return sources, err
	}
	return r.resolveItemSources(ctx, rec, lib, item, partIdx, accept)
}

func (r *KindResolver) isUsenetBackingOff() bool {
	if r == nil {
		return false
	}
	if r.usenetServing != nil && !r.usenetServing() {
		return true
	}
	return r.resolver != nil && r.resolver.UsenetBackingOff()
}

func (r *KindResolver) isSourceBackingOff(src core.PlayableSource) bool {
	if r == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(src.Pattern), kindstream.PatternRelease) && src.Release != nil {
		rel, err := releaseFromHint(*src.Release, "")
		if err == nil && rel.Protocol == core.ProtocolUsenet && rel.InfoHash == "" && rel.MagnetURL == "" {
			return r.isUsenetBackingOff()
		}
		return false
	}
	if src.URL != "" && r.backoff != nil {
		return r.backoff.IsBackingOff(src.URL)
	}
	return false
}

func (r *KindResolver) filterAndOrderCandidates(sources []core.PlayableSource, defaultTitle string, exclude []string) []core.PlayableSource {
	if len(sources) == 0 {
		return nil
	}
	nowUnix := r.now().Unix()
	ready := make([]core.PlayableSource, 0, len(sources))
	backingOff := make([]core.PlayableSource, 0)
	for _, src := range sources {
		if src.ExpiresAt > 0 && nowUnix >= src.ExpiresAt {
			continue
		}
		id := playableSourceRelease(src, defaultTitle).ID
		if len(exclude) > 0 && slices.Contains(exclude, id) {
			continue
		}
		if r.isSourceBackingOff(src) {
			backingOff = append(backingOff, src)
		} else {
			ready = append(ready, src)
		}
	}
	return append(ready, backingOff...)
}

func (r *KindResolver) resolveItemSourcesExcluding(ctx context.Context, rec pluginitems.KindRecord, lib core.Library, item core.KindItem, partIdx *int, accept, exclude []string) ([]core.PlayableSource, error) {
	if item.Unavailable != nil {
		return nil, core.ErrNotAvailable
	}
	if r.mgr == nil {
		return nil, core.ErrSourceUnavailable
	}
	fk := flightKeyForItem(item.Ref, partIdx)
	var partCopy *int
	if partIdx != nil {
		v := *partIdx
		partCopy = &v
	}
	sources, err := r.callPluginResolve(ctx, rec, lib, item, partCopy, "", accept, exclude)
	if err == nil && len(sources) > 0 {
		r.storeCandidates(fk, sources)
	}
	return sources, err
}

func (r *KindResolver) resolveItemSourcesForSource(ctx context.Context, rec pluginitems.KindRecord, lib core.Library, item core.KindItem, partIdx *int, sourceID string, accept []string) ([]core.PlayableSource, error) {
	if item.Unavailable != nil {
		return nil, core.ErrNotAvailable
	}
	if r.mgr == nil {
		return nil, core.ErrSourceUnavailable
	}
	fk := flightKeyForItemSource(item.Ref, partIdx, sourceID)
	var partCopy *int
	if partIdx != nil {
		v := *partIdx
		partCopy = &v
	}

	r.flightMu.Lock()
	fl, ok := r.flights[fk]
	if !ok {
		fl = &resolveFlight{done: make(chan struct{})}
		r.flights[fk] = fl
		go func() {
			defer func() {
				r.flightMu.Lock()
				delete(r.flights, fk)
				r.flightMu.Unlock()
				close(fl.done)
			}()
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), plugins.ResolverTotal+5*time.Second)
			defer cancel()
			fl.sources, fl.err = r.callPluginResolve(rctx, rec, lib, item, partCopy, sourceID, accept, nil)
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

func (r *KindResolver) resolveItemSources(ctx context.Context, rec pluginitems.KindRecord, lib core.Library, item core.KindItem, partIdx *int, accept []string) ([]core.PlayableSource, error) {
	if item.Unavailable != nil {
		return nil, core.ErrNotAvailable
	}
	if r.mgr == nil {
		return nil, core.ErrSourceUnavailable
	}
	fk := flightKeyForItem(item.Ref, partIdx)
	var partCopy *int
	if partIdx != nil {
		v := *partIdx
		partCopy = &v
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
			fl.sources, fl.err = r.callPluginResolve(rctx, rec, lib, item, partCopy, "", accept, nil)
			if fl.err == nil && len(fl.sources) > 0 {
				r.storeCandidates(fk, fl.sources)
				if r.items != nil {
					_ = r.items.SavePartCandidates(rctx, item.Ref, partCopy, fl.sources)
				}
			}
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

func (r *KindResolver) callPluginResolve(ctx context.Context, rec pluginitems.KindRecord, lib core.Library, item core.KindItem, partIdx *int, source string, accept, exclude []string) ([]core.PlayableSource, error) {
	pr, err := r.mgr.Get(ctx, rec.PluginID)
	if err != nil || !pr.Enabled || !pr.Manifest.HasCapability(plugins.CapKindResolver) {
		return nil, core.ErrSourceUnavailable
	}
	var clientMap map[string]any
	if len(accept) > 0 || len(exclude) > 0 {
		clientMap = make(map[string]any, 2)
		if len(accept) > 0 {
			clientMap["accept"] = accept
		}
		if len(exclude) > 0 {
			clientMap["exclude"] = exclude
		}
	}
	body, err := json.Marshal(core.KindResolveRequest{
		LibraryKey: lib.Key,
		Item:       item,
		PartIndex:  partIdx,
		Source:     source,
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
		if errors.Is(err, core.ErrJobsBusy) {
			return nil, err
		}
		if errors.Is(err, plugins.ErrBusy) {
			return nil, fmt.Errorf("%w: %w", core.ErrSourceUnavailable, err)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, core.ErrNotAvailable
		}
		return nil, fmt.Errorf("%w: %v", core.ErrSourceUnavailable, err)
	}
	if resp.Status == http.StatusAccepted {
		return nil, parsePreparingReply(resp.Body)
	}
	if resp.Status == http.StatusNotFound {
		if source == "" {
			_ = r.items.SaveNegativePartResolution(ctx, item.Ref, partIdx)
		}
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
		if source == "" {
			_ = r.items.SaveNegativePartResolution(ctx, item.Ref, partIdx)
		}
		return nil, core.ErrNotAvailable
	}
	if err := r.validateNoRawSecretsInHeaders(ctx, pr.ID, pr.Manifest.Ether.Permissions.Secrets, resolved.Sources); err != nil {
		return nil, err
	}
	if resolved.TTLSec > 0 {
		exp := r.now().Add(time.Duration(resolved.TTLSec) * time.Second).Unix()
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

func releaseFromHint(hint core.ReleaseHint, defaultTitle string) (core.Release, error) {
	var rel core.Release
	fileIndex := hint.FileIndex
	if fileIndex == nil && hint.Candidate != nil {
		fileIndex = hint.Candidate.FileIndex
	}
	if fileIndex != nil && *fileIndex < 0 {
		return core.Release{}, fmt.Errorf("release fileIndex must be non-negative")
	}
	if fileIndex != nil || strings.TrimSpace(hint.File) != "" {
		rel.Target = &core.ReleaseTarget{
			FileIndex:     fileIndex,
			ExactFile:     strings.TrimSpace(hint.File),
			AllowNonVideo: true,
		}
	}
	var torrentURL string
	if hint.Candidate != nil {
		rel.InfoHash = strings.ToLower(strings.TrimSpace(hint.Candidate.InfoHash))
		rel.MagnetURL = strings.TrimSpace(hint.Candidate.Magnet)
		torrentURL = strings.TrimSpace(hint.Candidate.TorrentURL)
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
	if t := strings.TrimSpace(hint.TorrentURL); t != "" {
		torrentURL = t
	}
	if u := strings.TrimSpace(hint.NZBURL); u != "" {
		rel.NZBURL = u
	}
	if strings.HasPrefix(strings.ToLower(torrentURL), "magnet:") {
		if rel.MagnetURL == "" {
			rel.MagnetURL = torrentURL
		}
		torrentURL = ""
	}
	if rel.InfoHash == "" && rel.MagnetURL != "" {
		if h := newznab.InfoHashFromMagnet(rel.MagnetURL); h != "" {
			rel.InfoHash = h
		}
	}
	switch {
	case rel.InfoHash != "" || rel.MagnetURL != "" || torrentURL != "":
		rel.Protocol = core.ProtocolTorrent
		if torrentURL != "" {
			// Core's torrent resolvers (newznab/cardigann) read the torrent
			// download/page link from Release.NZBURL while Protocol is Torrent.
			rel.NZBURL = torrentURL
		} else {
			rel.NZBURL = ""
		}
	case rel.NZBURL != "":
		rel.Protocol = core.ProtocolUsenet
	}
	rel.ID = releaseIDWithTarget(rel)
	if rel.Name == "" {
		rel.Name = firstNonEmpty(hint.FileHint, defaultTitle, rel.InfoHash, "kind-release")
	}
	return rel, nil
}

func releaseIDWithTarget(rel core.Release) string {
	id := "kind:" + firstNonEmpty(rel.InfoHash, rel.NZBURL, rel.MagnetURL)
	if rel.Target != nil {
		selection, _ := json.Marshal(rel.Target)
		id += fmt.Sprintf(":file:%x", sha256.Sum256(selection))
	}
	return id
}

func kindDefaultMIME(decl plugins.MediaKindMeta) string {
	// The kind-level media class is the source of truth; per-app
	// presentations are only consulted when a manifest does not declare one.
	switch strings.ToLower(strings.TrimSpace(string(decl.Media))) {
	case "music", "audiobook", "podcast":
		return "audio/*"
	case "book":
		return "application/epub+zip"
	case "photo":
		return "image/*"
	case "video":
		return ""
	}
	if decl.Present.MediaBrowser != nil {
		switch strings.ToLower(strings.TrimSpace(decl.Present.MediaBrowser.CollectionType)) {
		case "music", "audiobooks":
			return "audio/*"
		case "books":
			return "application/epub+zip"
		case "photos":
			return "image/*"
		}
	}
	if decl.Present.Plex != nil {
		switch strings.ToLower(strings.TrimSpace(decl.Present.Plex.Base)) {
		case "artist":
			return "audio/*"
		case "photo":
			return "image/*"
		}
	}
	return ""
}

func releaseFromSource(src core.PlayableSource, defaultTitle, fallbackMIME string) (core.Release, error) {
	if src.Release == nil {
		return core.Release{}, core.ErrSourceUnavailable
	}
	rel, err := releaseFromHint(*src.Release, defaultTitle)
	if err != nil {
		return core.Release{}, err
	}
	effMIME := nzb.NormalizeMIME(src.MIME)
	if effMIME == "" {
		effMIME = nzb.NormalizeMIME(fallbackMIME)
	}
	var member string
	var memberIdx *int
	if src.Container != nil {
		member = strings.TrimSpace(src.Container.Member)
		memberIdx = src.Container.MemberIndex
	} else {
		member = strings.TrimSpace(src.Release.FileHint)
	}
	if !strings.HasPrefix(effMIME, "video/") {
		if rel.Target != nil {
			if effMIME != "" {
				rel.Target.MIME = effMIME
			}
			if member != "" {
				rel.Target.Member = member
			}
			if memberIdx != nil {
				rel.Target.MemberIndex = memberIdx
			}
		} else if nzb.IsNonVideoMIME(effMIME) || member != "" || memberIdx != nil {
			rel.Target = &core.ReleaseTarget{
				AllowNonVideo: true,
				Member:        member,
				MemberIndex:   memberIdx,
				MIME:          effMIME,
			}
		}
	}
	if rel.Target != nil {
		rel.ID = releaseIDWithTarget(rel)
	}
	return rel, nil
}

func playableSourceRelease(src core.PlayableSource, defaultTitle string) core.Release {
	if strings.EqualFold(strings.TrimSpace(src.Pattern), kindstream.PatternRelease) && src.Release != nil {
		if rel, err := releaseFromSource(src, defaultTitle, ""); err == nil {
			if rel.Size <= 0 && src.Size > 0 {
				rel.Size = src.Size
			}
			rel.Info = releaseparse.Parse(firstNonEmpty(rel.Name, src.Release.FileHint, src.Label))
			if cand := src.Release.Candidate; cand != nil {
				if cand.Resolution > 0 {
					rel.Info.Height = cand.Resolution
				}
				if cand.Codec != "" && rel.Info.VideoCodec == "" {
					rel.Info.VideoCodec = strings.ToLower(strings.TrimSpace(cand.Codec))
				}
				if cand.Source != "" && rel.Info.Source == core.SourceUnknown {
					rel.Info.Source = kindReleaseSource(cand.Source)
				}
			}
			return rel
		}
	}
	containerSeed, _ := json.Marshal(src.Container)
	seed := fmt.Sprintf("%s|%s|%s|%s",
		strings.ToLower(strings.TrimSpace(src.Pattern)),
		strings.TrimSpace(src.URL),
		strings.TrimSpace(src.Path),
		string(containerSeed),
	)
	if blob := strings.TrimSpace(src.BlobRef); blob != "" {
		seed += "|blob:" + blob
	} else if strings.TrimSpace(src.URL) == "" && strings.TrimSpace(src.Path) == "" && src.Container == nil && strings.TrimSpace(src.ID) != "" {
		seed += "|id:" + strings.TrimSpace(src.ID)
	}
	sum := sha256.Sum256([]byte(seed))
	name := firstNonEmpty(src.Label, defaultTitle, "kind-stream")
	return core.Release{
		ID:       "kind:src:" + hex.EncodeToString(sum[:8]),
		Name:     name,
		Protocol: core.ProtocolHTTP,
		Size:     src.Size,
		Info:     releaseparse.Parse(firstNonEmpty(src.Label, src.Path, src.URL)),
	}
}

func (r *KindResolver) openRelease(ctx context.Context, hint core.ReleaseHint) (core.MediaSource, error) {
	if r.engine == nil {
		return nil, core.ErrSourceUnavailable
	}
	rel, err := releaseFromHint(hint, "")
	if err != nil {
		return nil, err
	}
	return r.openPreparedRelease(ctx, rel)
}

func (r *KindResolver) openReleaseForSource(ctx context.Context, src core.PlayableSource, fallbackMIME string, secretFn func(context.Context, string) (string, error)) (core.MediaSource, error) {
	if r.engine == nil {
		return nil, core.ErrSourceUnavailable
	}
	rel, err := releaseFromSource(src, "", fallbackMIME)
	if err != nil {
		return nil, err
	}
	if rel.Protocol == core.ProtocolUsenet && rel.Target != nil && rel.Target.AllowNonVideo && secretFn != nil {
		pwSecret := ""
		if src.Container != nil && strings.TrimSpace(src.Container.PasswordSecret) != "" {
			pwSecret = strings.TrimSpace(src.Container.PasswordSecret)
		} else if src.Release != nil {
			pwSecret = strings.TrimSpace(src.Release.PasswordSecret)
		}
		if pwSecret != "" {
			pw, err := secretFn(ctx, pwSecret)
			if err != nil {
				return nil, err
			}
			rel.Target.ArchivePassword = pw
		}
	}
	return r.openPreparedRelease(ctx, rel)
}

func (r *KindResolver) listNZBFiles(ctx context.Context, releaseID string, data []byte) ([]core.ReleaseFile, error) {
	if lister, ok := r.engine.(interface {
		ListFiles(context.Context, string, []byte) ([]core.ReleaseFile, error)
	}); ok {
		files, err := lister.ListFiles(ctx, releaseID, data)
		if err == nil {
			return files, nil
		}
		if !errors.Is(err, core.ErrNotAvailable) {
			return nil, err
		}
	}
	doc, err := nzb.Parse(data)
	if err != nil {
		return nil, err
	}
	files := make([]core.ReleaseFile, len(doc.Files))
	for i, f := range doc.Files {
		files[i] = core.ReleaseFile{
			Index:           i,
			Path:            f.Name,
			Size:            f.EstimatedDecodedBytes(),
			SizeApproximate: true,
		}
	}
	return files, nil
}

func (r *KindResolver) resolveTorrent(ctx context.Context, rel core.Release) (core.Release, error) {
	if r.indexer == nil {
		return rel, core.ErrSourceUnavailable
	}
	tr, ok := r.indexer.(debrid.TorrentResolver)
	if !ok {
		return rel, fmt.Errorf("kindresolver: torrent %q has no info hash: %w", rel.Name, core.ErrReleaseRejected)
	}
	resolved, err := tr.ResolveTorrent(ctx, rel)
	if err != nil {
		return rel, err
	}
	resolved.Protocol = core.ProtocolTorrent
	if resolved.InfoHash != "" {
		resolved.ID = releaseIDWithTarget(resolved)
	}
	return resolved, nil
}

func (r *KindResolver) openPreparedRelease(ctx context.Context, rel core.Release) (core.MediaSource, error) {
	if rel.Protocol == core.ProtocolTorrent || rel.InfoHash != "" || rel.MagnetURL != "" {
		if r.debrid == nil {
			return nil, core.ErrSourceUnavailable
		}
		rel.Protocol = core.ProtocolTorrent
		if rel.InfoHash == "" && rel.MagnetURL == "" && rel.NZBURL != "" {
			resolved, err := r.resolveTorrent(ctx, rel)
			if err != nil {
				return nil, err
			}
			rel = resolved
		}
		payload, err := r.debrid.Acquire(ctx, rel)
		if err != nil {
			return nil, err
		}
		return r.engine.Open(ctx, rel.ID, payload)
	}
	if rel.NZBURL != "" {
		if r.indexer == nil {
			return nil, core.ErrSourceUnavailable
		}
		rel.Protocol = core.ProtocolUsenet
		releaseKey := normalizeNZBKey(rel.NZBURL)
		nzbData, err := r.indexer.FetchNZB(ctx, rel)
		if err != nil {
			return nil, err
		}
		var nzbHash string
		if rel.Target == nil {
			nzbHash = hashNZBBytes(nzbData)
		}
		if rel.Target != nil && rel.Target.AllowNonVideo {
			nzbData, err = ensureNonVideoNZB(nzbData, rel.Target)
			if err != nil {
				return nil, err
			}
		}
		ms, err := r.engine.Open(ctx, rel.ID, nzbData)
		if err != nil {
			r.recordUsenetPlaybackFailure(releaseKey, nzbHash, err)
			return nil, err
		}
		return r.wrapUsenetPlaybackSource(ms, releaseKey, nzbHash), nil
	}
	return nil, core.ErrSourceUnavailable
}

// ensureNonVideoNZB attaches ether-nonvideo metadata (and narrows explicit file
// targets) when the underlying core.Indexer returned a raw NZB directly instead
// of passing through debrid.Indexer.FetchNZB.
func ensureNonVideoNZB(data []byte, target *core.ReleaseTarget) ([]byte, error) {
	if target == nil || !target.AllowNonVideo {
		return data, nil
	}
	if bytes.Contains(data, []byte(nzb.MetaNonVideo)) {
		return data, nil
	}
	if !bytes.Contains(bytes.ToLower(data), []byte("<nzb")) {
		return data, nil
	}
	return debrid.EpisodeNZB(data, target)
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

func (p *partDefaultByteSource) ReadAtContext(ctx context.Context, buf []byte, off int64) (int, error) {
	if cr, ok := p.ByteSource.(core.ContextReaderAt); ok {
		return cr.ReadAtContext(ctx, buf, off)
	}
	return p.ByteSource.ReadAt(buf, off)
}

func unwrapKindMediaSource(bs kindstream.ByteSource) core.MediaSource {
	if p, ok := bs.(*partDefaultByteSource); ok {
		bs = p.ByteSource
	}
	return kindstream.UnwrapMediaSource(bs)
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

func (p *preopenedSequence) ReadAtContext(ctx context.Context, buf []byte, off int64) (int, error) {
	if cr, ok := p.ByteSource.(core.ContextReaderAt); ok {
		return cr.ReadAtContext(ctx, buf, off)
	}
	return p.ByteSource.ReadAt(buf, off)
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
