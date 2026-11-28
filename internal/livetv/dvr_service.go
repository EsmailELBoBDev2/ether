package livetv

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/livetv/dvr"
)

// DVRSettings is the recording configuration the app supplies.
type DVRSettings struct {
	// Dir is the recordings folder.
	Dir string
	// PrePad and PostPad are the defaults for new rules and one-offs.
	PrePad, PostPad time.Duration
	// MaxBytes caps the recordings folder; 0 is no cap.
	MaxBytes int64
	// MinFreeBytes is the free space kept on the disk; 0 keeps none.
	MinFreeBytes int64
}

const (
	dvrPlanEvery  = 30 * time.Minute
	dvrTick       = 15 * time.Second
	dvrLeaseFor   = 2 * time.Minute
	dvrStartEarly = 30 * time.Second
)

// dvrRuntime is the Live TV service's recorder: the rule planner and the
// jobs recording on this node.
type dvrRuntime struct {
	s       *Service
	store   *dvr.Store
	planner *dvr.Planner
	holder  string
	wake    chan struct{}
	replan  chan struct{}

	mu     sync.Mutex
	active map[string]*dvrRecorder

	// tune opens the live session a job records; tests replace it.
	tune func(context.Context, *core.User, string, core.ClientCaps, string) (SessionInfo, error)
	// watched reports a recording's watch state for retention; nil keeps
	// "until watched" recordings.
	watched dvr.WatchedFunc
	// changed is told when a recording finishes or is deleted, so the
	// recordings library follows.
	changed atomic.Pointer[func()]
}

// SetDVRRecordingsChanged registers the callback the recordings library
// uses to follow finished and deleted recordings.
func (s *Service) SetDVRRecordingsChanged(fn func()) {
	if s == nil || s.recorder == nil {
		return
	}
	s.recorder.changed.Store(&fn)
}

func (d *dvrRuntime) recordingsChanged() {
	if fn := d.changed.Load(); fn != nil && *fn != nil {
		(*fn)()
	}
}

func newDVRRuntime(s *Service) *dvrRuntime {
	if s.o.DVR == nil {
		return nil
	}
	st := dvr.NewStore(s.o.DB, s.o.Now)
	d := &dvrRuntime{s: s, store: st, holder: randomID(), wake: make(chan struct{}, 1), replan: make(chan struct{}, 1), active: map[string]*dvrRecorder{}}
	d.planner = &dvr.Planner{Store: st, Guide: dvrGuide{s}, Location: time.Local, Now: s.o.Now}
	d.tune = s.TuneWithSession
	return d
}

func (d *dvrRuntime) settings() DVRSettings {
	cfg := d.s.o.DVR()
	if cfg.PrePad <= 0 {
		cfg.PrePad = dvr.DefaultPrePad
	}
	if cfg.PostPad <= 0 {
		cfg.PostPad = dvr.DefaultPostPad
	}
	return cfg
}

func dvrSignal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// Replan asks the recorder to expand rules again soon (a rule changed).
func (d *dvrRuntime) Replan() { dvrSignal(d.replan) }

// Wake asks the recorder to look for due jobs now (a job changed).
func (d *dvrRuntime) Wake() { dvrSignal(d.wake) }

// dvrLoop plans rules and starts due jobs while Live TV runs. Recordings
// still in progress stop when the service context ends and are finished as
// partial; the next start resumes jobs whose airing has not ended.
func (s *Service) dvrLoop() {
	defer s.wg.Done()
	d := s.recorder
	steps := s.maintenanceSteps()
	ctx := s.serviceContext()
	var planned, cleaned time.Time
	timer := s.newTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			d.stopAll()
			return
		case <-timer.C:
		case <-d.wake:
		case <-d.replan:
			planned = time.Time{}
		}
		now := s.o.Now()
		if planned.IsZero() || now.Sub(planned) >= dvrPlanEvery {
			planned = now
			steps("dvrPlan", func() {
				if _, err := d.planner.Run(ctx); err != nil && ctx.Err() == nil {
					s.logger().Warn("dvr plan incomplete", "error", err)
				}
			})
		}
		steps("dvrStartDue", func() { d.startDue(ctx) })
		if now.Sub(cleaned) >= dvrStorageEvery {
			cleaned = now
			steps("dvrStorage", func() {
				if _, err := d.cleanStorage(ctx, 0); err != nil && ctx.Err() == nil {
					s.logger().Warn("dvr storage check failed", "error", err)
				}
			})
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(dvrTick)
	}
}

// startDue starts every pending or interrupted job whose padded window has
// begun, and finishes jobs a stopped node left recording.
func (d *dvrRuntime) startDue(ctx context.Context) {
	now := d.s.o.Now()
	jobs, err := d.store.Jobs(ctx, dvr.JobQuery{Statuses: []dvr.JobStatus{dvr.JobScheduled, dvr.JobConflict, dvr.JobRecording}, From: now.Add(-24 * time.Hour), Until: now.Add(4 * time.Hour)})
	if err != nil {
		d.s.logger().Warn("dvr jobs unavailable", "error", err)
		return
	}
	for _, j := range jobs {
		start, end := j.Window()
		if start.After(now.Add(dvrStartEarly)) {
			continue
		}
		d.mu.Lock()
		_, running := d.active[j.ID]
		d.mu.Unlock()
		if running {
			continue
		}
		if !end.After(now) {
			// Missed entirely, or left recording by a node that stopped.
			d.finishOrphan(ctx, j)
			continue
		}
		ok, err := d.store.AcquireLease(ctx, j.ID, d.holder, now.Add(dvrLeaseFor))
		if err != nil || !ok {
			continue
		}
		rec := &dvrRecorder{d: d, job: j}
		d.mu.Lock()
		d.active[j.ID] = rec
		d.mu.Unlock()
		d.s.wg.Add(1)
		go func() {
			defer d.s.wg.Done()
			rec.run(ctx)
			d.mu.Lock()
			delete(d.active, j.ID)
			d.mu.Unlock()
			_ = d.store.ReleaseLease(context.WithoutCancel(ctx), j.ID, d.holder)
		}()
	}
}

// finishOrphan closes a job whose window ended without a recorder here: a
// job that never started fails; a job another process was recording keeps
// what it wrote.
func (d *dvrRuntime) finishOrphan(ctx context.Context, j dvr.Job) {
	ok, err := d.store.AcquireLease(ctx, j.ID, d.holder, d.s.o.Now().Add(dvrLeaseFor))
	if err != nil || !ok {
		return
	}
	defer d.store.ReleaseLease(context.WithoutCancel(ctx), j.ID, d.holder)
	if j.Status == dvr.JobRecording && j.RecordingID != "" {
		rec := &dvrRecorder{d: d, job: j}
		rec.finishInterrupted(ctx)
		return
	}
	j.Status, j.Reason = dvr.JobFailed, "missed: the server was not running"
	if err := d.store.UpdateJob(ctx, &j); err != nil {
		d.s.logger().Warn("dvr job update failed", "job", j.ID, "error", err)
	}
}

// stop ends a running job early. It reports whether the job was recording
// on this node.
func (d *dvrRuntime) stop(id string) bool {
	d.mu.Lock()
	rec := d.active[id]
	d.mu.Unlock()
	if rec == nil {
		return false
	}
	rec.requestStop()
	return true
}

func (d *dvrRuntime) stopAll() {
	d.mu.Lock()
	list := make([]*dvrRecorder, 0, len(d.active))
	for _, r := range d.active {
		list = append(list, r)
	}
	d.mu.Unlock()
	for _, r := range list {
		r.requestStop()
	}
}

// dvrUser finds the account a job records for. Recording uses that
// user's channel access, parental rules and stream limit.
func (s *Service) dvrUser(ctx context.Context, id int64) (*core.User, error) {
	if s.o.InputUser != nil {
		if u, err := s.o.InputUser(ctx); err == nil && u != nil && u.ID == id {
			return u, nil
		}
	}
	if s.o.Users == nil {
		return nil, core.ErrNotFound
	}
	users, err := s.o.Users.Users(ctx)
	if err != nil {
		return nil, err
	}
	for _, u := range users {
		if u.ID == id {
			copy := u
			return &copy, nil
		}
	}
	return nil, core.ErrNotFound
}

// dvrOwner is the account the owner console schedules for.
func (s *Service) dvrOwner(ctx context.Context) (*core.User, error) {
	if s.o.InputUser == nil {
		return nil, core.ErrNotAvailable
	}
	u, err := s.o.InputUser(ctx)
	if err != nil {
		return nil, err
	}
	if u == nil || u.ID == 0 {
		return nil, core.ErrNotFound
	}
	return u, nil
}

// dvrGuide reads a user's guide for the planner.
type dvrGuide struct{ s *Service }

func (g dvrGuide) DVRGuide(ctx context.Context, userID int64, from, until time.Time) ([]dvr.Programme, error) {
	u, err := g.s.dvrUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	channels, err := g.s.AllowedChannels(ctx, u)
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(channels))
	for _, c := range channels {
		names[c.ID] = c.Name
	}
	ps, err := g.s.Guide(ctx, u, from, until)
	if err != nil {
		return nil, err
	}
	out := make([]dvr.Programme, 0, len(ps))
	for _, p := range ps {
		name, ok := names[p.ChannelID]
		if !ok || p.Item.Slate != "" {
			continue
		}
		out = append(out, dvrProgramme(p, name))
	}
	return out, nil
}

func dvrProgramme(p Programme, channelName string) dvr.Programme {
	it := p.Item
	return dvr.Programme{
		ID: p.ID, ChannelID: p.ChannelID, ChannelName: channelName, Start: p.Start, End: p.End,
		Title: it.Title, ShowTitle: it.ShowTitle, EpisodeTitle: it.EpisodeTitle, Summary: it.Summary,
		GUID: it.GUID, ShowGUID: it.ShowGUID, MediaType: it.MediaType, Thumb: it.Thumb, Rating: it.Rating,
		Genres: append([]string(nil), it.Genres...), Season: it.Season, Episode: it.Episode, Premiere: it.Premiere, ReleasedMS: it.ReleasedMS,
	}
}

// errDVROff is returned by the owner API when the app supplies no settings.
var errDVROff = errors.New("recording is not available")
