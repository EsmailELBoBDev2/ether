package livetv

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/livetv/dvr"
)

// growingLiveRelay is an encoder whose live playlist gains a segment on
// every poll.
type growingLiveRelay struct {
	core.HLSSession
	mu    sync.Mutex
	polls int
}

func (f *growingLiveRelay) ServePlaylist(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.polls++
	n := f.polls
	f.mu.Unlock()
	fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:1\n")
	for i := max(0, n-4); i < n; i++ {
		fmt.Fprintf(w, "#EXTINF:1,\n%05d.ts\n", i)
	}
}
func (f *growingLiveRelay) ServeSegment(w http.ResponseWriter, r *http.Request, n int) {
	fmt.Fprintf(w, "segment-%d|", n)
}
func (f *growingLiveRelay) Close() error { return nil }

func TestDVRRecorderWritesSessionSegments(t *testing.T) {
	dir := t.TempDir()
	owner := &core.User{ID: 1, IsOwner: true}
	s := testService(t, Options{
		DVR:       func() DVRSettings { return DVRSettings{Dir: dir} },
		InputUser: func(context.Context) (*core.User, error) { return owner, nil },
	})
	if s.dvr == nil {
		t.Fatal("dvr runtime missing")
	}
	tunes := 0
	s.dvr.tune = func(ctx context.Context, u *core.User, channel string, caps core.ClientCaps, client string) (SessionInfo, error) {
		tunes++
		if u.ID != owner.ID || caps.Product != "Ether DVR" || !strings.HasPrefix(client, "ether-dvr-") {
			t.Errorf("tune for %+v with %+v", u, caps)
		}
		ss := &session{info: SessionInfo{ID: "dvr-fixture", UserID: owner.ID, Channel: Channel{ID: channel}}, user: *owner, ready: true}
		ownedRelayFixture(t, s, ss, &growingLiveRelay{}, core.Decision{Container: "mpegts"}, relaySegment)
		s.mu.Lock()
		s.sessions[ss.info.ID] = ss
		s.mu.Unlock()
		return ss.info, nil
	}
	c, err := s.SaveChannel(t.Context(), Channel{Name: "News", Number: "1", Kind: "iptv", Enabled: true, StreamURL: "http://example.invalid/live.ts"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	p := dvr.Programme{ID: "c-1", ChannelID: c.ID, Start: now.Add(-time.Second), End: now.Add(6 * time.Second), Title: "Evening News", EpisodeTitle: "Friday"}
	// Saving a channel starts the service's own recorder loop. The job is
	// marked active first so only this test's recorder runs it.
	job := dvr.Job{ID: "job-under-test", UserID: owner.ID, Programme: p}
	rec := &dvrRecorder{d: s.dvr, job: job}
	s.dvr.mu.Lock()
	s.dvr.active[job.ID] = rec
	s.dvr.mu.Unlock()
	if err := s.dvr.store.InsertJob(t.Context(), &rec.job); err != nil {
		t.Fatal(err)
	}
	job = rec.job
	rec.run(t.Context())

	got, err := s.dvr.store.Job(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != dvr.JobCompleted && got.Status != dvr.JobPartial {
		t.Fatalf("job = %+v", got)
	}
	recs, err := s.dvr.store.Recordings(t.Context(), owner.ID)
	if err != nil || len(recs) != 1 {
		t.Fatalf("recordings = %+v, %v", recs, err)
	}
	r := recs[0]
	if !dvrInside(dir, r.Path) || filepath.Ext(r.Path) != ".ts" || !strings.Contains(r.Path, filepath.Join("TV", "Evening News")) {
		t.Fatalf("path = %s", r.Path)
	}
	data, err := os.ReadFile(r.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "segment-") || int64(len(data)) != r.SizeBytes {
		t.Fatalf("file %q, size %d", data, r.SizeBytes)
	}
	// Each session segment is written once.
	seen := map[string]bool{}
	for _, part := range strings.Split(strings.TrimSuffix(string(data), "|"), "|") {
		if seen[part] {
			t.Fatalf("segment written twice: %s in %q", part, data)
		}
		seen[part] = true
	}
	if _, err := os.Stat(r.Path + dvrPartialSuffix); !os.IsNotExist(err) {
		t.Fatalf("partial left behind: %v", err)
	}
	if tunes != 1 {
		t.Fatalf("tuned %d times", tunes)
	}
}

func TestDVRRecorderFailsWithoutMedia(t *testing.T) {
	owner := &core.User{ID: 1, IsOwner: true}
	s := testService(t, Options{
		DVR:       func() DVRSettings { return DVRSettings{Dir: t.TempDir()} },
		InputUser: func(context.Context) (*core.User, error) { return owner, nil },
	})
	s.dvr.tune = func(context.Context, *core.User, string, core.ClientCaps, string) (SessionInfo, error) {
		return SessionInfo{}, core.ErrNotAvailable
	}
	now := time.Now()
	job, err := s.dvr.store.ScheduleOnce(t.Context(), owner.ID, dvr.Programme{ID: "x", ChannelID: "c", Start: now, End: now.Add(1500 * time.Millisecond), Title: "Nothing"}, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	(&dvrRecorder{d: s.dvr, job: job}).run(t.Context())
	got, _ := s.dvr.store.Job(t.Context(), job.ID)
	if got.Status != dvr.JobFailed || !strings.Contains(got.Reason, "tuning failed") {
		t.Fatalf("job = %+v", got)
	}
	if recs, _ := s.dvr.store.Recordings(t.Context(), 0); len(recs) != 0 {
		t.Fatalf("empty recording kept: %+v", recs)
	}
}

func TestParseDVRPlaylist(t *testing.T) {
	media := parseDVRPlaylist("#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXT-X-MAP:URI=\"/livetv/sessions/a/0/resource/init%2Emp4?g=1\"\n#EXTINF:5.5,\n/livetv/sessions/a/0/relay/7.ts?grant=x\n#EXTINF:6,\nrelay/8.ts\n")
	if media.target != 6*time.Second || media.init != "init.mp4" || len(media.segments) != 2 || media.segments[0].n != 7 || media.segments[0].duration != 5500*time.Millisecond || media.segments[1].n != 8 {
		t.Fatalf("media = %+v", media)
	}
	master := parseDVRPlaylist("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\n/livetv/sessions/a/0/resource/video.m3u8?g=1\n#EXT-X-STREAM-INF:BANDWIDTH=2\n/livetv/sessions/a/0/resource/other.m3u8\n")
	if master.variant != "video.m3u8" || len(master.segments) != 0 {
		t.Fatalf("master = %+v", master)
	}
}

func TestDVRNames(t *testing.T) {
	at := time.Date(2026, 11, 27, 21, 0, 0, 0, time.Local)
	ep := dvrRelPath(dvr.Programme{Title: "Who/What?", EpisodeTitle: "Pilot: Part 1", Season: 1, Episode: 2, Start: at})
	if want := filepath.Join("TV", "Who What", "Season 01", "Who What - S01E02 - Pilot Part 1 (2026-11-27 2100).ts"); ep != want {
		t.Fatalf("episode = %q want %q", ep, want)
	}
	daily := dvrRelPath(dvr.Programme{Title: "News", Start: at})
	if want := filepath.Join("TV", "News", "News - 2026-11-27 2100.ts"); daily != want {
		t.Fatalf("daily = %q", daily)
	}
	movie := dvrRelPath(dvr.Programme{Title: "Film", MediaType: "movie", ReleasedMS: time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli(), Start: at})
	if want := filepath.Join("Movies", "Film (1999)", "Film (1999).ts"); movie != want {
		t.Fatalf("movie = %q", movie)
	}
	if got := dvrSafeName(" .. ", "x"); got != "x" {
		t.Fatalf("empty name = %q", got)
	}
	dir := t.TempDir()
	if !dvrInside(dir, filepath.Join(dir, "TV", "a.ts")) || dvrInside(dir, filepath.Join(dir, "..", "a.ts")) || dvrInside(dir, dir) {
		t.Fatal("dvrInside")
	}
	first := filepath.Join(dir, "a.ts")
	if err := os.WriteFile(first, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := dvrUniquePath(first); got != filepath.Join(dir, "a (2).ts") {
		t.Fatalf("unique = %q", got)
	}
}

func TestDVRCleanStorageEvictsOldestForSpace(t *testing.T) {
	dir := t.TempDir()
	owner := &core.User{ID: 1, IsOwner: true}
	s := testService(t, Options{
		DVR:       func() DVRSettings { return DVRSettings{Dir: dir, MaxBytes: 25} },
		InputUser: func(context.Context) (*core.User, error) { return owner, nil },
	})
	free := int64(1 << 40)
	old := dvrDiskUsage
	dvrDiskUsage = func(string) (int64, int64, error) { return free, free, nil }
	t.Cleanup(func() { dvrDiskUsage = old })
	var paths []string
	for i, age := range []time.Duration{3 * time.Hour, 2 * time.Hour, time.Hour} {
		path := filepath.Join(dir, "TV", "Show", fmt.Sprintf("e%d.ts", i))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, 10), 0o644); err != nil {
			t.Fatal(err)
		}
		rec := dvr.Recording{UserID: 1, JobID: "j", MatchKey: path, Path: path, Status: dvr.JobCompleted, SizeBytes: 10, Created: time.Now().Add(-age)}
		if err := s.dvr.store.SaveRecording(t.Context(), &rec); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	outside := dvr.Recording{UserID: 1, JobID: "j", MatchKey: "x", Path: "/etc/hostname", Status: dvr.JobCompleted, SizeBytes: 10, Created: time.Now().Add(-10 * time.Hour)}
	if err := s.dvr.store.SaveRecording(t.Context(), &outside); err != nil {
		t.Fatal(err)
	}
	sp, err := s.dvr.cleanStorage(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	// 40 bytes stored against a 25 byte cap: the oldest file inside the
	// folder goes first, then the next; the file outside is never touched.
	if _, err := os.Stat(paths[0]); !os.IsNotExist(err) {
		t.Fatalf("oldest kept: %v", err)
	}
	if _, err := os.Stat(paths[1]); !os.IsNotExist(err) {
		t.Fatalf("second oldest kept: %v", err)
	}
	if _, err := os.Stat(paths[2]); err != nil {
		t.Fatalf("newest deleted: %v", err)
	}
	if sp.Used != 20 {
		t.Fatalf("space = %+v", sp)
	}
	recs, _ := s.dvr.store.Recordings(t.Context(), 0)
	if len(recs) != 2 {
		t.Fatalf("recordings left = %+v", recs)
	}
	// The disk is nearly full: a job does not start.
	free = dvrDiskFloor / 2
	if reason := s.dvr.preflight(t.Context(), dvr.Job{Programme: dvr.Programme{Start: time.Now(), End: time.Now().Add(time.Minute)}}); reason == "" {
		t.Fatal("job allowed on a full disk")
	}
}
