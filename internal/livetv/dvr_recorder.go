package livetv

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/livetv/dvr"
	"github.com/braedonsaunders/ether/internal/transcode"
)

const (
	dvrPartialSuffix = ".partial"
	// dvrStall re-tunes a session that has produced no new segment.
	dvrStall = time.Minute
	// dvrLateBy marks a recording partial when its first media arrived
	// this long after the programme started.
	dvrLateBy = 2 * time.Minute
)

// dvrCaps asks a live session for MPEG-TS segments in the codecs IPTV
// carries, so the session copies the stream instead of transcoding it.
var dvrCaps = core.ClientCaps{
	Product:       "Ether DVR",
	Platform:      "Server",
	Device:        "Recorder",
	DeviceName:    "Ether DVR",
	VideoCodecs:   []string{"h264", "hevc", "mpeg2video"},
	AudioCodecs:   []string{"aac", "ac3", "eac3", "mp2", "mp3"},
	Containers:    []string{"mpegts"},
	HLSContainers: []string{"mpegts"},
}

// dvrRecorder records one job. It is an in-process client of an ordinary
// live session: the session does source selection, failover and provider
// connection limits exactly as for a viewer, and the recorder appends the
// session's segments to the file. Segment numbers are the session's own,
// so a segment is never written twice within one session.
type dvrRecorder struct {
	d   *dvrRuntime
	job dvr.Job

	mu      sync.Mutex
	cancel  context.CancelFunc
	stopped bool
}

func (r *dvrRecorder) requestStop() {
	r.mu.Lock()
	r.stopped = true
	cancel := r.cancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (r *dvrRecorder) stopRequested() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopped
}

type dvrProgress struct {
	bytes     int64
	duration  time.Duration
	gaps      int
	firstData time.Time
	lastErr   string
}

func (r *dvrRecorder) run(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	r.mu.Lock()
	r.cancel = cancel
	stopped := r.stopped
	r.mu.Unlock()
	defer cancel()
	if stopped {
		cancel()
	}
	s := r.d.s
	bg := context.WithoutCancel(parent)
	log := s.logger().With("dvrJob", r.job.ID, "channel", r.job.Programme.ChannelID)
	user, err := s.dvrUser(ctx, r.job.UserID)
	if err != nil {
		r.fail(bg, "the account that scheduled this recording is gone")
		return
	}
	rec, f, err := r.open(bg)
	if err != nil {
		log.Warn("dvr cannot write recording", "error", err)
		r.fail(bg, "cannot write the recording: "+err.Error())
		return
	}
	r.job.Status, r.job.Reason, r.job.RecordingID, r.job.Node = dvr.JobRecording, "", rec.ID, r.d.holder
	if err := r.d.store.UpdateJob(bg, &r.job); err != nil {
		log.Warn("dvr job update failed", "error", err)
	}
	log.Info("dvr recording started", "path", rec.Path)
	prog := dvrProgress{bytes: rec.SizeBytes, duration: time.Duration(rec.DurationMS) * time.Millisecond, gaps: rec.Gaps}
	if prog.bytes > 0 {
		prog.firstData = rec.Created
	}
	writeErr := r.pull(ctx, user, f, &prog, log)
	closeErr := f.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	_, end := r.job.Window()
	if writeErr == nil && parent.Err() != nil && !r.stopRequested() && s.o.Now().Before(end) {
		// The service is stopping mid-airing: keep the job recording so
		// the next start resumes it into the same file.
		rec.SizeBytes, rec.DurationMS, rec.Gaps = prog.bytes, prog.duration.Milliseconds(), prog.gaps
		if err := r.d.store.SaveRecording(bg, &rec); err != nil {
			log.Warn("dvr progress save failed", "error", err)
		}
		return
	}
	if writeErr != nil {
		prog.lastErr = "writing the file failed: " + writeErr.Error()
	}
	r.finish(bg, rec, prog)
}

// open resumes the job's recording, or creates its file and row.
func (r *dvrRecorder) open(ctx context.Context) (dvr.Recording, *os.File, error) {
	dir := r.d.settings().Dir
	if dir == "" || !filepath.IsAbs(dir) {
		return dvr.Recording{}, nil, fmt.Errorf("no recordings folder is set")
	}
	var rec dvr.Recording
	if r.job.RecordingID != "" {
		var err error
		if rec, err = r.d.store.Recording(ctx, r.job.RecordingID); err != nil && !errors.Is(err, core.ErrNotFound) {
			return dvr.Recording{}, nil, err
		}
	}
	if rec.ID == "" || !dvrInside(dir, rec.Path) {
		path := dvrUniquePath(filepath.Join(dir, dvrRelPath(r.job.Programme)))
		rec = dvr.Recording{UserID: r.job.UserID, JobID: r.job.ID, RuleID: r.job.RuleID, MatchKey: r.job.MatchKey, Path: path, Programme: r.job.Programme}
	}
	rec.Status = dvr.JobRecording
	if err := os.MkdirAll(filepath.Dir(rec.Path), 0o755); err != nil {
		return dvr.Recording{}, nil, err
	}
	f, err := os.OpenFile(rec.Path+dvrPartialSuffix, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return dvr.Recording{}, nil, err
	}
	if info, err := f.Stat(); err == nil {
		rec.SizeBytes = info.Size()
	}
	if err := r.d.store.SaveRecording(ctx, &rec); err != nil {
		f.Close()
		return dvr.Recording{}, nil, err
	}
	return rec, f, nil
}

// pull copies segments until the window ends or the job is stopped. Only
// a write error is returned; stream trouble is retried and counted.
func (r *dvrRecorder) pull(ctx context.Context, user *core.User, f *os.File, prog *dvrProgress, log interface {
	Warn(string, ...any)
}) error {
	s := r.d.s
	bg := context.WithoutCancel(ctx)
	_, end := r.job.Window()
	w := bufio.NewWriterSize(f, 1<<20)
	defer w.Flush()
	sessionID, variant := "", ""
	last := int64(-1)
	lastNew := s.o.Now()
	failures := 0
	leaseAt := s.o.Now()
	closeSession := func() {
		if sessionID != "" {
			_ = s.Stop(bg, sessionID, user)
			sessionID = ""
		}
	}
	defer closeSession()
	for ctx.Err() == nil {
		now := s.o.Now()
		if !now.Before(end) {
			return nil
		}
		if now.Sub(leaseAt) >= dvrLeaseFor/4 {
			ok, err := r.d.store.AcquireLease(bg, r.job.ID, r.d.holder, now.Add(dvrLeaseFor))
			if err == nil && !ok {
				prog.lastErr = "another server took over this recording"
				return nil
			}
			leaseAt = now
		}
		if sessionID == "" {
			caps := dvrCaps
			caps.ClientID = "ether-dvr-" + r.job.ID
			info, err := r.d.tune(ctx, user, r.job.Programme.ChannelID, caps, caps.ClientID)
			if err != nil {
				if ctx.Err() == nil {
					prog.lastErr = "tuning failed: " + err.Error()
					log.Warn("dvr tune failed", "error", err)
				}
				r.wait(ctx, 10*time.Second, end)
				continue
			}
			sessionID, variant, last, lastNew, failures = info.ID, "", -1, now, 0
			if prog.bytes > 0 {
				prog.gaps++
			}
		}
		pl, err := r.playlist(ctx, user, sessionID, &variant)
		if err != nil {
			failures++
			if failures >= 3 || now.Sub(lastNew) > dvrStall {
				if ctx.Err() == nil {
					prog.lastErr = "the stream stopped: " + err.Error()
				}
				closeSession()
			}
			r.wait(ctx, 2*time.Second, end)
			continue
		}
		if pl.init != "" && prog.bytes == 0 {
			data, err := r.resource(ctx, user, sessionID, pl.init)
			if err != nil {
				r.wait(ctx, time.Second, end)
				continue
			}
			if _, err := w.Write(data); err != nil {
				return err
			}
			prog.bytes += int64(len(data))
		}
		for _, seg := range pl.segments {
			if seg.n <= last {
				continue
			}
			data, err := r.segment(ctx, user, sessionID, seg.n)
			if err != nil {
				break
			}
			if _, err := w.Write(data); err != nil {
				return err
			}
			if prog.firstData.IsZero() {
				prog.firstData = s.o.Now()
			}
			prog.bytes += int64(len(data))
			prog.duration += seg.duration
			last, lastNew, failures = seg.n, s.o.Now(), 0
		}
		if err := w.Flush(); err != nil {
			return err
		}
		if s.o.Now().Sub(lastNew) > dvrStall {
			prog.lastErr = "the stream stalled"
			closeSession()
			continue
		}
		pause := pl.target / 2
		if pause < time.Second {
			pause = time.Second
		}
		if pause > 3*time.Second {
			pause = 3 * time.Second
		}
		r.wait(ctx, pause, end)
	}
	return nil
}

func (r *dvrRecorder) wait(ctx context.Context, d time.Duration, end time.Time) {
	if left := end.Sub(r.d.s.o.Now()); left < d {
		d = max(left, 0)
	}
	t := r.d.s.newTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// finish renames the file and closes the job.
func (r *dvrRecorder) finish(ctx context.Context, rec dvr.Recording, prog dvrProgress) {
	log := r.d.s.logger().With("dvrJob", r.job.ID)
	if prog.bytes == 0 {
		_ = os.Remove(rec.Path + dvrPartialSuffix)
		_ = r.d.store.DeleteRecording(ctx, rec.ID)
		reason := prog.lastErr
		if reason == "" {
			reason = "no video was received"
		}
		r.job.RecordingID = ""
		r.fail(ctx, reason)
		return
	}
	if err := os.Rename(rec.Path+dvrPartialSuffix, rec.Path); err != nil {
		log.Warn("dvr rename failed", "error", err)
		r.fail(ctx, "cannot finish the file: "+err.Error())
		return
	}
	status, reason := dvr.JobCompleted, ""
	now := r.d.s.o.Now()
	switch {
	case prog.lastErr != "" && prog.gaps > 0:
		status, reason = dvr.JobPartial, prog.lastErr
	case prog.gaps > 0:
		status, reason = dvr.JobPartial, "the stream dropped and was tuned again"
	case r.stopRequested() && now.Before(r.job.Programme.End.Add(-time.Minute)):
		status, reason = dvr.JobPartial, "stopped early"
	case !prog.firstData.IsZero() && prog.firstData.After(r.job.Programme.Start.Add(dvrLateBy)):
		status, reason = dvr.JobPartial, "started late"
	}
	rec.Status, rec.SizeBytes, rec.DurationMS, rec.Gaps = status, prog.bytes, prog.duration.Milliseconds(), prog.gaps
	if err := r.d.store.SaveRecording(ctx, &rec); err != nil {
		log.Warn("dvr recording save failed", "error", err)
	}
	r.job.Status, r.job.Reason, r.job.RecordingID = status, reason, rec.ID
	if err := r.d.store.UpdateJob(ctx, &r.job); err != nil {
		log.Warn("dvr job update failed", "error", err)
	}
	log.Info("dvr recording finished", "status", status, "bytes", prog.bytes, "gaps", prog.gaps, "path", rec.Path)
}

// finishInterrupted closes a job left recording by a process that stopped
// before its window ended, keeping what was written.
func (r *dvrRecorder) finishInterrupted(ctx context.Context) {
	rec, err := r.d.store.Recording(ctx, r.job.RecordingID)
	if err != nil {
		r.fail(ctx, "the recording was interrupted")
		return
	}
	prog := dvrProgress{bytes: rec.SizeBytes, duration: time.Duration(rec.DurationMS) * time.Millisecond, gaps: rec.Gaps + 1, lastErr: "the server stopped during the recording"}
	if info, err := os.Stat(rec.Path + dvrPartialSuffix); err == nil {
		prog.bytes = info.Size()
	} else {
		prog.bytes = 0
	}
	r.finish(ctx, rec, prog)
}

func (r *dvrRecorder) fail(ctx context.Context, reason string) {
	r.job.Status, r.job.Reason = dvr.JobFailed, reason
	if err := r.d.store.UpdateJob(ctx, &r.job); err != nil {
		r.d.s.logger().Warn("dvr job update failed", "dvrJob", r.job.ID, "error", err)
	}
	r.d.s.logger().Warn("dvr recording failed", "dvrJob", r.job.ID, "reason", reason)
}

// dvrResponse captures an in-process session response.
type dvrResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *dvrResponse) Header() http.Header { return w.header }
func (w *dvrResponse) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}
func (w *dvrResponse) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(p)
}

func (r *dvrRecorder) call(ctx context.Context, path string, serve func(http.ResponseWriter, *http.Request)) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://ether-dvr.invalid"+path, nil)
	if err != nil {
		return nil, err
	}
	req.RemoteAddr = "127.0.0.1:0"
	w := &dvrResponse{header: http.Header{}}
	serve(w, req)
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.status != http.StatusOK {
		return nil, fmt.Errorf("session answered %d", w.status)
	}
	return w.body.Bytes(), nil
}

func (r *dvrRecorder) playlist(ctx context.Context, user *core.User, id string, variant *string) (dvrPlaylist, error) {
	s := r.d.s
	var body []byte
	var err error
	if *variant != "" {
		body, err = r.resource(ctx, user, id, *variant)
	} else {
		body, err = r.call(ctx, "/livetv/sessions/"+id+"/0/index.m3u8", func(w http.ResponseWriter, req *http.Request) { s.ServePlaylist(w, req, id, user) })
	}
	if err != nil {
		return dvrPlaylist{}, err
	}
	pl := parseDVRPlaylist(string(body))
	if pl.variant != "" && *variant == "" {
		*variant = pl.variant
		return r.playlist(ctx, user, id, variant)
	}
	return pl, nil
}

func (r *dvrRecorder) resource(ctx context.Context, user *core.User, id, name string) ([]byte, error) {
	s := r.d.s
	return r.call(ctx, "/livetv/sessions/"+id+"/0/resource/"+name, func(w http.ResponseWriter, req *http.Request) { s.ServeResource(w, req, id, name, user) })
}

func (r *dvrRecorder) segment(ctx context.Context, user *core.User, id string, n int64) ([]byte, error) {
	s := r.d.s
	return r.call(ctx, "/livetv/sessions/"+id+"/0/relay/"+strconv.FormatInt(n, 10)+".ts", func(w http.ResponseWriter, req *http.Request) { s.ServeRelaySegment(w, req, id, int(n), user) })
}

type dvrSegment struct {
	n        int64
	duration time.Duration
}

type dvrPlaylist struct {
	target   time.Duration
	variant  string // first variant of a master playlist (a resource name)
	init     string // EXT-X-MAP resource name
	segments []dvrSegment
}

// parseDVRPlaylist reads the playlists a live session serves: media
// playlists of relay segments, or a master playlist whose variants are
// session resources.
func parseDVRPlaylist(body string) dvrPlaylist {
	var pl dvrPlaylist
	var dur time.Duration
	variantNext := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			if v, err := strconv.ParseFloat(strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:"), 64); err == nil && v > 0 {
				pl.target = time.Duration(v * float64(time.Second))
			}
		case strings.HasPrefix(line, "#EXTINF:"):
			v, _, _ := strings.Cut(strings.TrimPrefix(line, "#EXTINF:"), ",")
			if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
				dur = time.Duration(f * float64(time.Second))
			}
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF"):
			variantNext = true
		case strings.HasPrefix(line, "#EXT-X-MAP:"):
			if _, rest, ok := strings.Cut(line, `URI="`); ok {
				uri, _, _ := strings.Cut(rest, `"`)
				pl.init = dvrResourceName(uri)
			}
		case strings.HasPrefix(line, "#"):
		default:
			if variantNext {
				variantNext = false
				if pl.variant == "" {
					pl.variant = dvrResourceName(line)
				}
				continue
			}
			if n, ok := dvrSegmentNumber(line); ok {
				pl.segments = append(pl.segments, dvrSegment{n: n, duration: dur})
			}
			dur = 0
		}
	}
	return pl
}

func dvrStripQuery(uri string) string {
	if i := strings.IndexAny(uri, "?#"); i >= 0 {
		uri = uri[:i]
	}
	return uri
}

func dvrSegmentNumber(uri string) (int64, bool) {
	uri = dvrStripQuery(uri)
	i := strings.LastIndex(uri, "relay/")
	if i < 0 {
		return 0, false
	}
	n, ok := transcode.ParseSegmentName(uri[i+len("relay/"):])
	if !ok || n < 0 {
		return 0, false
	}
	return int64(n), true
}

func dvrResourceName(uri string) string {
	uri = dvrStripQuery(uri)
	i := strings.LastIndex(uri, "resource/")
	if i < 0 {
		return ""
	}
	name := uri[i+len("resource/"):]
	if un, err := url.PathUnescape(name); err == nil {
		name = un
	}
	return name
}
