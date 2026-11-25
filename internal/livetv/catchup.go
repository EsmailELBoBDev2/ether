package livetv

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// CatchUpRequest selects either a guide programme or an explicit archive range.
type CatchUpRequest struct {
	ChannelID, ProgrammeID string
	Start                  time.Time
	Duration               time.Duration
	StartOver              bool
	ClampStart             bool
}

type CatchUpInfo struct {
	ID, ChannelID, ProgrammeID, Title string
	Start, End, ProgrammeEnd          time.Time
	StartOver                         bool
	Origin                            time.Duration
	MediaGeneration                   string
	Decision                          core.Decision
}

var ErrInvalidCatchUpRange = errors.New("invalid catch-up range")

type CatchUpError struct {
	Code        string
	ArchiveDays int
	EarliestAt  time.Time
}

func (e *CatchUpError) Error() string {
	switch e.Code {
	case "catchup_not_supported":
		return "This channel has no catch-up archive."
	case "catchup_outside_archive":
		return "The requested range starts outside the channel archive."
	default:
		return "The provider has no playable archive for this range."
	}
}

func archiveUnavailable() error { return &CatchUpError{Code: "catchup_unavailable"} }

type catchUpSession struct {
	info  CatchUpInfo
	hls   core.HLSSession
	input *session
}

func (s *Service) catchUpSelection(ctx context.Context, u *core.User, c Channel, req CatchUpRequest, now time.Time) (CatchUpInfo, error) {
	info := CatchUpInfo{ChannelID: c.ID, Title: c.Name, Start: req.Start, StartOver: req.StartOver}
	if !s.allowedProgramme(ctx, u, c, nil) {
		return info, core.ErrUnauthorized
	}
	if c.Archive == nil || c.Archive.Days <= 0 {
		return info, &CatchUpError{Code: "catchup_not_supported"}
	}
	days := min(c.Archive.Days, 30)
	earliest := now.Add(-time.Duration(days) * 24 * time.Hour)
	if req.ProgrammeID != "" {
		ps, err := s.programmes(ctx, c, earliest, now.Add(time.Second))
		if err != nil {
			return info, err
		}
		for _, p := range ps {
			if p.ID != req.ProgrammeID {
				continue
			}
			if !s.allowedProgramme(ctx, u, c, &p) {
				return info, core.ErrUnauthorized
			}
			info.ProgrammeID, info.Title, info.Start, info.ProgrammeEnd = p.ID, p.Item.Title, p.Start, p.End
			break
		}
		if info.ProgrammeID == "" {
			return info, core.ErrNotFound
		}
		if info.ProgrammeEnd.After(now) != req.StartOver {
			return info, ErrInvalidCatchUpRange
		}
		info.End = info.ProgrammeEnd
	} else {
		if req.Start.IsZero() || req.Duration <= 0 || req.StartOver {
			return info, ErrInvalidCatchUpRange
		}
		info.End = req.Start.Add(req.Duration)
		if req.ClampStart && info.Start.Before(earliest) && info.End.After(earliest) {
			info.Start = earliest
		}
		// Explicit padding still obeys the programme policy at the selected time.
		p, ok, err := s.programmeAt(ctx, c, info.Start)
		if err != nil {
			return info, err
		}
		if ok && !s.allowedProgramme(ctx, u, c, &p) {
			return info, core.ErrUnauthorized
		}
		if ok && p.Item.Title != "" {
			info.Title = p.Item.Title
		}
	}
	if info.Start.Before(earliest) || info.Start.After(now) {
		return info, &CatchUpError{Code: "catchup_outside_archive", ArchiveDays: days, EarliestAt: earliest.UTC()}
	}
	if info.End.After(now) {
		info.End = now
	}
	if !info.End.After(info.Start) {
		return info, archiveUnavailable()
	}
	return info, nil
}

// StartCatchUp opens a provider archive through the IPTV input relay and
// returns a session ID for ServePlaylist, ServeRelaySegment, SeekCatchUp and Stop.
func (s *Service) StartCatchUp(ctx context.Context, u *core.User, req CatchUpRequest, caps core.ClientCaps, clientSessionID string) (CatchUpInfo, error) {
	if !s.Enabled() {
		return CatchUpInfo{}, core.ErrNotAvailable
	}
	now := s.clockNow()
	if u == nil || s.o.CanPlay != nil && !s.o.CanPlay(u, caps.Remote, now) {
		return CatchUpInfo{}, core.ErrUnauthorized
	}
	if s.o.ConfirmAdmission != nil {
		var err error
		u, err = s.o.ConfirmAdmission(ctx, u, caps.Remote, now)
		if err != nil {
			return CatchUpInfo{}, err
		}
	}
	ctx, policy, err := s.withPolicy(ctx, u)
	if err != nil {
		return CatchUpInfo{}, err
	}
	c, err := s.Channel(ctx, req.ChannelID)
	if err != nil {
		return CatchUpInfo{}, err
	}
	info, err := s.catchUpSelection(ctx, u, c, req, now)
	if err != nil {
		return CatchUpInfo{}, err
	}
	c.Transcode = c.Transcode || policy.Transcode
	info.ID = liveSessionID()
	if clientSessionID == "" {
		clientSessionID = caps.ClientID
	}
	handover := core.LiveHandover(ctx)
	previous := s.supersedeBeforeTune(u.ID, caps, info.ID, handover)
	var playing []*core.PlaySession
	if s.o.Playback != nil {
		playing = s.o.Playback.Sessions()
	}
	s.mu.Lock()
	limit := u.StreamLimit
	if s.o.StreamLimit != nil {
		limit = s.o.StreamLimit(u)
	}
	if limit > 0 && s.activeStreamCountLocked(u.ID, caps.MultiviewGroup, caps.ClientID, playing, previous) >= limit {
		s.mu.Unlock()
		return CatchUpInfo{}, core.ErrStreamLimit
	}
	s.nextSubscription++
	ss := &session{
		ephemeral: true, streamGrant: randomID(), user: *u, caps: caps, last: now,
		info: SessionInfo{ID: info.ID, SubscriptionID: s.nextSubscription, ActivityID: liveSessionID(),
			ClientSessionID: clientSessionID, UserID: u.ID, Channel: c, StartedAt: now},
		view: core.PlaySession{ID: info.ID, PlaybackGroup: info.ID, UserID: u.ID, Username: u.Username,
			ClientID: caps.ClientID, Client: caps, TitleName: info.Title, StartedAt: now, Origin: "Catch-up"},
	}
	ss.allowCheck = func(ctx context.Context, viewer *core.User) error {
		ch, err := s.Channel(ctx, c.ID)
		if err != nil {
			return err
		}
		ctx, _, err = s.withPolicy(ctx, viewer)
		if err != nil {
			return err
		}
		p, found, err := s.programmeAt(ctx, ch, info.Start)
		if err != nil {
			return err
		}
		var programme *Programme
		if found {
			programme = &p
		}
		if !s.allowedProgramme(ctx, viewer, ch, programme) {
			return core.ErrUnauthorized
		}
		return nil
	}
	ss.ctx, ss.cancel = context.WithCancel(s.serviceContext())
	s.sessions[info.ID] = ss
	s.mu.Unlock()
	s.notifySessionsChanged()
	started := false
	defer func() {
		if !started {
			_ = s.Stop(ctx, info.ID, u)
		}
	}()
	if limiter, ok := s.o.Playback.(channelLimiter); ok {
		if err := limiter.BeginChannel(u, caps, info.ID); err != nil {
			return CatchUpInfo{}, err
		}
		ss.mu.Lock()
		if ss.closed {
			ss.mu.Unlock()
			limiter.EndChannel(info.ID)
			return CatchUpInfo{}, core.ErrNotFound
		}
		ss.reserved = true
		ss.mu.Unlock()
	}
	opened, err := s.openCatchUp(ctx, ss, info)
	if err != nil {
		return CatchUpInfo{}, err
	}
	ss.mu.Lock()
	if ss.closed {
		ss.mu.Unlock()
		opened.close(s)
		return CatchUpInfo{}, core.ErrNotFound
	}
	ss.catchUp, ss.ready = opened, true
	ss.view.Decision = opened.info.Decision.Kind
	ss.view.DecisionVideoCopy = opened.info.Decision.VideoCopy
	ss.view.DecisionAudioCopy = opened.info.Decision.AudioCopy
	ss.view.Info.Duration = info.End.Sub(info.Start)
	ss.mu.Unlock()
	started = true
	if !handover {
		s.finishHandover(previous, info.ID)
	}
	return opened.info, nil
}

func (s *Service) openCatchUp(ctx context.Context, ss *session, info CatchUpInfo) (*catchUpSession, error) {
	ctx, cancel := ss.bindContext(ctx)
	defer cancel()
	ctx, timeout := context.WithTimeout(ctx, 20*time.Second)
	defer timeout()
	tc := s.transcoder()
	if tc == nil {
		return nil, core.ErrNotAvailable
	}
	c := ss.info.Channel
	// W-A1 supplies this method. The optional seam lets both worker branches
	// compile while they are landing independently.
	builder, ok := any(s).(interface {
		ArchiveURL(context.Context, Channel, time.Time, time.Duration) (string, error)
	})
	if !ok {
		return nil, archiveUnavailable()
	}
	duration := info.End.Sub(info.Start) - info.Origin
	raw, err := builder.ArchiveURL(ctx, c, info.Start.Add(info.Origin), duration)
	if err != nil {
		return nil, archiveUnavailable()
	}
	src := StreamSource{ID: stableID(c.ID, "playlist"), AuthSourceID: c.SourceID, URL: raw}
	headers, err := s.upstreamHeaders(ctx, c, src)
	if err != nil {
		return nil, archiveUnavailable()
	}
	stage := &session{archiveInput: true, info: ss.info, caps: ss.caps, ctx: ss.ctx, inputSource: src, inputURL: raw, inputHeaders: headers}
	opened := &catchUpSession{info: info, input: stage}
	ready := false
	defer func() {
		if !ready {
			opened.close(s)
		}
	}()
	if err := s.grantDirectInput(stage, src, headers); err != nil {
		return nil, s.catchUpSourceError(ctx, c.SourceID, err)
	}
	prefix, _, err := s.sniffInput(ctx, stage, 7)
	if err != nil || len(prefix) == 0 {
		return nil, s.catchUpSourceError(ctx, c.SourceID, err)
	}
	input, err := s.relayInput(stage)
	if err != nil {
		return nil, s.catchUpSourceError(ctx, c.SourceID, err)
	}
	probe, err := s.probeSessionInput(ctx, stage)
	if err != nil || probe == nil || !probe.HasPlayableMedia() {
		return nil, s.catchUpSourceError(ctx, c.SourceID, err)
	}
	probe.Duration = duration
	probe.Keyframes = nil
	dec, cut, _ := s.relayPlan(stage, *probe)
	info.MediaGeneration = randomID()
	info.Decision = dec
	opened.info = info
	opened.hls, err = tc.StartHLS(ss.ctx, core.HLSRequest{
		SessionID: info.ID + "-" + info.MediaGeneration, MediaGeneration: info.MediaGeneration, InputURL: input,
		Info: *probe, Decision: dec, SegmentDuration: cut,
	})
	if err != nil {
		return nil, Diagnose("catchup_output", err)
	}
	ready = true
	return opened, nil
}

func (s *Service) catchUpSourceError(ctx context.Context, source string, err error) error {
	if errors.Is(err, ErrProviderBusy) {
		return s.wrapProviderConnectionCap(source, err)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return archiveUnavailable()
}

func (c *catchUpSession) close(s *Service) {
	if c == nil {
		return
	}
	s.revokeInput(c.input)
	if c.hls != nil {
		c.hls.Close()
	}
}

// SeekCatchUp returns the current output when the target segment is buffered.
// A seek outside it opens the provider at the requested programme offset.
func (s *Service) SeekCatchUp(ctx context.Context, u *core.User, id string, offset time.Duration) (CatchUpInfo, error) {
	ss, err := s.getSession(ctx, id, u)
	if err != nil {
		return CatchUpInfo{}, err
	}
	ss.prepareMu.Lock()
	defer ss.prepareMu.Unlock()
	ss.mu.Lock()
	current := ss.catchUp
	if ss.closed || current == nil {
		ss.mu.Unlock()
		return CatchUpInfo{}, core.ErrNotFound
	}
	info := current.info
	if info.StartOver {
		info.End = minTime(s.clockNow(), info.ProgrammeEnd)
	}
	if offset < 0 || offset > info.End.Sub(info.Start) {
		ss.mu.Unlock()
		return CatchUpInfo{}, ErrInvalidCatchUpRange
	}
	if offset == info.End.Sub(info.Start) || offset == info.Origin {
		ss.mu.Unlock()
		return info, nil
	}
	buffer, ok := current.hls.(interface{ HasBufferedPosition(time.Duration) bool })
	if offset >= info.Origin && ok && buffer.HasBufferedPosition(offset-info.Origin) {
		ss.mu.Unlock()
		return info, nil
	}
	ss.mu.Unlock()
	// Release the current provider connection before opening a new range;
	// even single-connection providers must support seeking.
	s.revokeInput(current.input)
	info.Origin = offset
	next, err := s.openCatchUp(ctx, ss, info)
	if err != nil {
		return CatchUpInfo{}, err
	}
	ss.mu.Lock()
	if ss.closed {
		ss.mu.Unlock()
		next.close(s)
		return CatchUpInfo{}, core.ErrNotFound
	}
	ss.catchUp = next
	ss.mu.Unlock()
	current.close(s)
	return next.info, nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (s *Service) serveCatchUp(w http.ResponseWriter, r *http.Request, ss *session, archive *catchUpSession, n *int) {
	if generation := r.URL.Query().Get("archive"); generation != "" && generation != archive.info.MediaGeneration {
		http.NotFound(w, r)
		return
	}
	if n == nil {
		archive.hls.ServePlaylist(w, r)
		return
	}
	s.touchMedia(ss)
	s.activeCatchUpRead(w, r, ss, archive, *n)
}

func (s *Service) activeCatchUpRead(w http.ResponseWriter, r *http.Request, ss *session, archive *catchUpSession, n int) {
	ss.activeReads.Add(1)
	defer ss.activeReads.Add(-1)
	observed := s.observeContinuousMedia(w, ss)
	defer observed.done()
	archive.hls.ServeSegment(observed, r, n)
}
