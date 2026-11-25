package sessions

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
)

const (
	// versionSwitchWait bounds how long an in-session version switch waits
	// for the target source to open before refusing with ErrPreparing so the
	// current stream continues uninterrupted.
	versionSwitchWait       = 4 * time.Second
	versionSwitchFastWait   = 250 * time.Millisecond
	versionSwitchRetryAfter = 5 * time.Second
)

var _ frontend.VersionSwitcher = (*Broker)(nil)

// SwitchVersion swaps the underlying media source of an active VOD session to
// another offered version at position, keeping the same session ID.
//
// When the target version requires a long fetch (uncached torrent or slow
// preparation exceeding versionSwitchWait), the active session is left
// completely untouched and a PreparingError is returned so the client can show
// "preparing" and retry.
func (b *Broker) SwitchVersion(ctx context.Context, a frontend.SessionAccess, sessionID, versionHandle string, position time.Duration) (frontend.PlayPlan, error) {
	if versionHandle == "" || position < 0 || b.deps.DB == nil {
		return frontend.PlayPlan{}, core.ErrNotFound
	}
	s, _, err := b.access(ctx, a, sessionID)
	if err != nil {
		return frontend.PlayPlan{}, err
	}
	if s.live || s.restored || s.public.Item == 0 || (s.public.Item.IsPluginKey() && b.deps.KindSource == nil) {
		return frontend.PlayPlan{}, core.ErrNotAvailable
	}
	isPlugin := s.public.Item.IsPluginKey()
	unlock, err := b.gates.Acquire(ctx, fmt.Sprintf("broker/owner/%d/%s", s.public.ViewerID, s.clientID))
	if err != nil {
		return frontend.PlayPlan{}, err
	}
	defer unlock()
	s, viewer, err := b.access(ctx, a, sessionID)
	if err != nil {
		return frontend.PlayPlan{}, err
	}
	if viewer.ID != s.public.ViewerID {
		viewer, err = b.deps.ActiveViewer(ctx, s.public.ViewerID)
	}
	if err != nil || !b.currentMediaPolicy(ctx, viewer, s) {
		return frontend.PlayPlan{}, core.ErrUnauthorized
	}
	if s.live || s.restored {
		return frontend.PlayPlan{}, core.ErrNotAvailable
	}
	caps := s.caps
	b.stampViewerTranscoding(viewer, &caps)
	viewer, err = b.confirm(ctx, viewer, caps)
	if err != nil {
		return frontend.PlayPlan{}, err
	}
	var title *core.Title
	if isPlugin {
		if in, ierr := b.deps.KindSource(core.WithLiveApp(ctx, string(s.public.Protocol)), viewer, s.public.Item); ierr == nil && in != nil && in.Title != nil && in.Title.Key == s.public.Item {
			title = in.Title
		} else {
			return frontend.PlayPlan{}, core.ErrNotFound
		}
	} else {
		title, err = b.deps.Lookup(core.WithLiveApp(ctx, string(s.public.Protocol)), viewer, s.public.Device, s.public.Item)
		if err != nil {
			return frontend.PlayPlan{}, err
		}
	}
	if title == nil || title.Key != s.public.Item || (!title.Released.IsZero() && title.Released.After(b.deps.Now())) {
		return frontend.PlayPlan{}, core.ErrNotFound
	}
	if title.Type == core.TypeShow || title.Type == core.TypeSeason {
		return frontend.PlayPlan{}, core.ErrNotFound
	}

	h := &versionHandles{db: b.deps.DB, broker: b, protocol: s.public.Protocol}
	scope := frontend.VersionScope{Viewer: viewer, Device: s.public.Device, Protocol: s.public.Protocol, Caps: caps}
	item, targetSelector, err := h.resolveRecord(ctx, scope, versionHandle)
	if errors.Is(err, core.ErrNotFound) {
		if parent, _, lerr := h.lookupRow(ctx, scope, versionHandle); lerr == nil && parent == s.public.Item {
			return frontend.PlayPlan{}, core.ErrVersionUnavailable
		}
	}
	if err != nil {
		return frontend.PlayPlan{}, err
	}
	if item != s.public.Item || targetSelector == "" {
		return frontend.PlayPlan{}, core.ErrNotFound
	}

	var chosen *core.ReleaseVariant
	if isPlugin {
		if b.deps.KindVersions != nil {
			if variants, verr := b.deps.KindVersions(core.WithLiveApp(ctx, string(s.public.Protocol)), viewer, s.public.Item); verr == nil {
				for j := range variants {
					if variants[j].ID == targetSelector || variants[j].Release.ID == targetSelector {
						chosen = &variants[j]
						break
					}
				}
			}
		}
	} else if b.deps.Variants != nil {
		if variants, verr := b.deps.Variants(ctx, viewer, title, caps); verr == nil {
			for j := range variants {
				if variants[j].ID == targetSelector || variants[j].Release.ID == targetSelector {
					chosen = &variants[j]
					break
				}
			}
		}
	}
	slotSelector := core.IsExplicitVersionSlotSelector(targetSelector)
	if chosen != nil && chosen.Availability == core.VersionUnavailable && !slotSelector {
		return frontend.PlayPlan{}, core.ErrVersionUnavailable
	}
	isAuto := targetSelector == core.KindAutoVersionID || (chosen != nil && chosen.Auto && chosen.ID == targetSelector)
	exact := !isAuto && !slotSelector
	if isAuto {
		targetSelector = ""
	}

	var previous *core.PlaySession
	var existed bool
	var ps *core.PlaySession
	if isPlugin {
		kp, ok := b.deps.Playback.(core.KindPlayback)
		if !ok {
			return frontend.PlayPlan{}, core.ErrNotAvailable
		}
		if scoped, ok := b.deps.Playback.(core.SurfaceScopedPlayback); ok {
			previous, existed = scoped.SessionForSurface(string(s.public.Protocol), viewer.ID, caps.ClientID, s.public.Item, targetSelector, exact)
		} else {
			previous, existed = kp.SessionForKind(viewer.ID, caps.ClientID, s.public.Item)
		}
		in, ierr := b.deps.KindSource(core.WithLiveApp(ctx, string(s.public.Protocol)), viewer, s.public.Item)
		if ierr != nil || in == nil {
			return frontend.PlayPlan{}, core.ErrNotAvailable
		}
		if targetSelector != "" && targetSelector != core.KindAutoVersionID {
			in.PreferSourceID = targetSelector
		}
		in.Exact = exact
		in.DeferSupersession = true
		ps, err = kp.PrepareKind(ctx, viewer, *in, caps)
	} else {
		playback, ok := b.deps.Playback.(core.SurfaceScopedPlayback)
		if !ok {
			return frontend.PlayPlan{}, core.ErrNotAvailable
		}
		previous, existed = playback.SessionForSurface(string(s.public.Protocol), viewer.ID, caps.ClientID, s.public.Item, targetSelector, exact)
		if within, ok := b.deps.Playback.(interface {
			PrepareWithin(context.Context, time.Duration, *core.User, core.RatingKey, core.ClientCaps, string, bool) (*core.PlaySession, error)
		}); ok {
			wait := versionSwitchWait
			if chosen != nil && chosen.Availability == core.VersionFetch && chosen.Release.Protocol == core.ProtocolTorrent {
				wait = versionSwitchFastWait
			}
			ps, err = within.PrepareWithin(ctx, wait, viewer, s.public.Item, caps, targetSelector, exact)
		} else {
			if chosen != nil && chosen.Availability == core.VersionFetch {
				return frontend.PlayPlan{}, &core.PreparingError{RetryAfter: versionSwitchRetryAfter}
			}
			ps, err = playback.PrepareSurface(ctx, viewer, s.public.Item, caps, targetSelector, exact)
		}
	}
	if err != nil {
		switch {
		case errors.Is(err, core.ErrPreparing):
			return frontend.PlayPlan{}, err
		case errors.Is(err, core.ErrDownloadQueued):
			return frontend.PlayPlan{}, &core.PreparingError{RetryAfter: versionSwitchRetryAfter, Err: err}
		case errors.Is(err, core.ErrNotCached), errors.Is(err, core.ErrPrepareSlow):
			return frontend.PlayPlan{}, &core.PreparingError{RetryAfter: versionSwitchRetryAfter}
		case errors.Is(err, core.ErrVideoTranscodingDisabled):
			return frontend.PlayPlan{}, &frontend.NoCompatiblePathError{
				RejectedPaths: []frontend.RejectedPlaybackPath{{Path: "video", Reasons: []string{reasonVideoTranscodingOff}}},
				Cause:         core.ErrVideoTranscodingDisabled,
			}
		case errors.As(err, new(*core.RequiredAudioLanguageError)):
			return frontend.PlayPlan{}, err
		case errors.Is(err, core.ErrNotAvailable), errors.Is(err, core.ErrReleaseRejected), errors.Is(err, core.ErrArticleMissing), errors.Is(err, core.ErrArchiveUnstreamable):
			return frontend.PlayPlan{}, core.ErrVersionUnavailable
		default:
			return frontend.PlayPlan{}, err
		}
	}
	if ps == nil {
		return frontend.PlayPlan{}, core.ErrVersionUnavailable
	}
	if exact && (ps.Resolution == nil || !ps.Resolution.MatchesVersion(targetSelector)) {
		if !existed || previous == nil || previous.ID != ps.ID {
			if ps.ID != s.playID {
				b.deps.Playback.Stop(ps.ID)
			}
		}
		return frontend.PlayPlan{}, core.ErrVersionUnavailable
	}

	sameSource := ps.ID == s.playID
	existed = existed && previous != nil && previous.ID == ps.ID
	if !sameSource {
		b.acquirePlanSource(ps.ID, !existed)
	}
	committed := false
	pinned := false
	defer func() {
		if committed {
			return
		}
		if !sameSource {
			b.releasePlanSource(ps.ID, pinned)
		} else if pinned {
			if pins, ok := b.deps.Playback.(core.PlaybackRevisionPin); ok {
				pins.UnpinSourceRevision(ps.ID)
			}
		}
	}()

	// The new file is admitted like a fresh start: from the session's initial
	// options and original-file declaration, not the options a track, quality
	// or re-encode restart of the old file left behind.
	opts := s.opts
	var original *frontend.OriginalPlaybackCapabilities
	if entry, ok := b.bindings.Get(sessionID); ok {
		opts = entry.Requested.Options
		original = entry.Requested.OriginalPlayback
	}
	opts.AudioStream = -1
	opts.SubtitleStream = -1
	planReq := frontend.PlanRequest{
		Protocol: s.public.Protocol,
		Viewer:   viewer,
		Device:   s.public.Device,
		Item:     s.public.Item,
		Caps:     caps,
		Intent: frontend.PlayIntent{
			Options:          opts,
			Position:         position,
			AllowTranscoding: s.allowTranscoding,
			DeclaredCaps:     true,
			ExactVersion:     !isAuto,
			OriginalPlayback: original,
		},
	}
	if b.deps.SelectScopedTracks != nil || b.deps.SelectTracks != nil {
		var selected core.DecideOptions
		var terr error
		if b.deps.SelectScopedTracks != nil {
			selected, terr = b.deps.SelectScopedTracks(ctx, viewer, s.public.Device, title, ps.Info, planReq.Intent.Options, false, false)
		} else {
			selected, terr = b.deps.SelectTracks(ctx, viewer, title, ps.Info, planReq.Intent.Options, false, false)
		}
		if terr != nil {
			return frontend.PlayPlan{}, terr
		}
		planReq.Intent.Options = selected
	}
	sourceChosenAt := b.deps.Now()
	dec, originalChoice, err := b.admitWithClientCap(ps, &planReq, s.clientCap, b.admitPlanDecision)
	decisionAt := b.deps.Now()
	if err != nil {
		return frontend.PlayPlan{}, err
	}
	// Original playback of the current source keeps the session's pin while
	// the file is still the pinned revision, or takes one for this revision.
	if sameSource && s.sourcePinned {
		if s.sourceETag == "" || sourceRevision(ps) != s.sourceETag {
			return frontend.PlayPlan{}, frontend.ErrSourceGone
		}
	} else {
		var pinErr error
		ps, pinned, pinErr = b.pinOriginalRuntime(ctx, &planReq, ps, dec, pinned)
		if pinErr != nil {
			return frontend.PlayPlan{}, pinErr
		}
	}

	etag := sourceRevision(ps)
	relID := ""
	if ps.Resolution != nil {
		relID = ps.Resolution.Release.ID
	}
	normHandle := normalizeVersionHandle(versionHandle)

	if !sameSource {
		if err := b.retireAudio(ctx, s); err != nil {
			return frontend.PlayPlan{}, err
		}
		if b.deps.Transcoder != nil {
			b.transcodeStop(sessionID)
		}
		if scoped, ok := b.deps.Playback.(core.SurfaceScopedPlayback); !ok || !scoped.MarkSurfaceOpened(ps.ID) {
			return frontend.PlayPlan{}, core.ErrNotAvailable
		}
		if binding, ok := b.deps.Playback.(interface {
			BindClientSession(context.Context, string, string) error
		}); ok {
			if err := binding.BindClientSession(ctx, sessionID, ps.ID); err != nil {
				return frontend.PlayPlan{}, err
			}
		}
	}

	b.mu.Lock()
	live := b.sessions[sessionID]
	if live == nil {
		b.mu.Unlock()
		return frontend.PlayPlan{}, core.ErrNotFound
	}
	oldPlayID := live.playID
	oldPinned := live.sourcePinned
	oldHold := live.trickplay
	stopOld := false
	if !sameSource {
		b.transferPlanSourceLocked(ps.ID)
		stopOld = b.releaseSessionSourceLocked(oldPlayID)
		live.trickplay = nil
		live.trickplayStarted = false
		live.format = ""
		live.originalFailure, live.originalReplaced, live.originalAwaitingProgress = false, false, false
		live.originalPosition, live.originalAudio, live.originalSubtitle = nil, nil, nil
		live.originalResumeAt = 0
		// A previous original grant may already be in the decoder.
		if oldPinned || s.originalReplaced {
			live.grant = rand.Text()
			live.public.MediaPath = "/sessions/media/" + sessionID + "/" + live.grant + "/file"
		}
	}
	live.playID = ps.ID
	live.caps.NoVideoTranscode = caps.NoVideoTranscode
	live.opts = planReq.Intent.Options
	live.decision = dec
	live.versionHandle = normHandle
	live.exactRelease = relID
	live.autoRelease = !exact
	if !sameSource || pinned {
		live.sourcePinned = pinned
	}
	live.sourceETag = etag
	live.streamPosition = position
	live.public.PlayMethod = playMethod(dec.Kind)
	committed = true
	b.mu.Unlock()

	if !sameSource {
		b.bindings.Put(&Binding[frontend.PlayIntent]{ID: sessionID, UserID: viewer.ID, ClientID: live.clientID, Key: live.public.Item, PlayID: ps.ID, PlaybackID: live.playbackID, Requested: planReq.Intent, Decision: dec, Info: ps.Info, Caps: live.caps, Opts: live.opts, UpdatedAt: b.deps.Now()})
		b.owners.BindFor("broker", viewer.ID, live.clientID, live.public.Item, live.playbackID, ps.ID, true, b.deps.Now())
		if oldHold != nil {
			oldHold.Close(true)
		}
		if oldPinned && oldPlayID != ps.ID {
			if pins, ok := b.deps.Playback.(core.PlaybackRevisionPin); ok {
				pins.UnpinSourceRevision(oldPlayID)
			}
		}
		if stopOld && oldPlayID != "" && oldPlayID != ps.ID {
			b.deps.Playback.Stop(oldPlayID)
		}
	}
	if noter, ok := b.deps.Playback.(interface{ NoteStartPosition(string, time.Duration) }); ok {
		noter.NoteStartPosition(ps.ID, position)
	}

	direct, remux, transcoding := b.planAlternatives(ps, planReq)
	hls, hlsContainer := b.hlsPlanOutput(ps, planReq, dec)
	httpOK, httpContainer := b.httpPlanOutput(ps, planReq, dec)
	return frontend.PlayPlan{
		SourceChosenAt:        sourceChosenAt,
		DecisionAt:            decisionAt,
		ID:                    sessionID,
		Media:                 publicMedia(ps.Info),
		Decision:              dec,
		SelectedSubtitle:      sideSubtitleStream(ps, planReq, dec),
		DirectPlay:            direct,
		DirectStream:          remux,
		Transcoding:           transcoding,
		VideoTranscodingOff:   b.videoOff(planReq.Caps),
		HLS:                   hls,
		HLSContainer:          hlsContainer,
		HTTP:                  httpOK,
		HTTPContainer:         httpContainer,
		ETag:                  etag,
		VersionHandle:         normHandle,
		ReleaseID:             relID,
		OriginalOverClientCap: originalChoice,
	}, nil
}
