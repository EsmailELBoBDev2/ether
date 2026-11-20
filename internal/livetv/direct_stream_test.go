package livetv

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

func makeValidTSPacket() []byte {
	// 3 valid 188-byte MPEG-TS packets with 0x47 sync bytes.
	pkt := make([]byte, 3*tsPacket)
	for i := 0; i < 3; i++ {
		pkt[i*tsPacket] = 0x47
		pkt[i*tsPacket+1] = 0x40
	}
	return pkt
}

func TestDirectStreamSingleUseMasterMuxedTS(t *testing.T) {
	var masterHits atomic.Int32
	var variantHits atomic.Int32
	var sawHeader atomic.Bool
	tsSeg := makeValidTSPacket()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/master.m3u8":
			if r.Header.Get("X-Stream-Auth") == "secret-hdr" {
				sawHeader.Store(true)
			}
			if masterHits.Add(1) > 1 {
				http.Error(w, "token already used", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-STREAM-INF:BANDWIDTH=2500000,RESOLUTION=1280x720,CODECS=\"avc1.64001f,mp4a.40.2\"\n/variant.m3u8\n")
		case "/variant.m3u8":
			variantHits.Add(1)
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:1\n#EXTINF:2.0,\n/seg1.ts\n#EXTINF:2.0,\n/seg2.ts\n#EXTINF:2.0,\n/seg3.ts\n")
		case "/seg1.ts", "/seg2.ts", "/seg3.ts":
			w.Header().Set("Content-Type", "video/mp2t")
			_, _ = w.Write(tsSeg)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	// No Live TV channels configured: s.Enabled() is false, yet direct live
	// streams from plugin libraries must still tune and serve.
	s := testService(t, Options{HTTPClient: upstream.Client()})
	if s.Enabled() {
		t.Fatal("expected Live TV to be disabled with no channels configured")
	}

	var allowed atomic.Bool
	allowed.Store(true)
	u := &core.User{ID: 7, Username: "alice", StreamLimit: 2}
	caps := core.ClientCaps{
		Product:       "Vela",
		Protocol:      "vela",
		ClientID:      "vela-client-1",
		HLSContainers: []string{"mpegts"},
	}

	info, err := s.TuneDirectStream(context.Background(), u, DirectStream{
		ID:      "esports:ch-1",
		Title:   "Finals Stage A",
		URL:     upstream.URL + "/master.m3u8?token=once",
		Headers: map[string]string{"X-Stream-Auth": "secret-hdr"},
		AllowCheck: func(context.Context, *core.User) error {
			if !allowed.Load() {
				return core.ErrNotFound
			}
			return nil
		},
	}, caps, "vela-sess-1")
	if err != nil {
		t.Fatalf("TuneDirectStream failed: %v", err)
	}
	defer func() { _ = s.Stop(context.Background(), info.ID, u) }()

	if got := masterHits.Load(); got != 1 {
		t.Fatalf("master playlist fetched %d times, want exactly 1", got)
	}
	if !sawHeader.Load() {
		t.Fatal("custom header X-Stream-Auth was not forwarded to upstream")
	}
	if variantHits.Load() == 0 {
		t.Fatal("variant media playlist was never fetched")
	}

	// Verify ServePlaylist returns the owned playlist and ServeRelaySegment works.
	rec := httptest.NewRecorder()
	s.ServePlaylist(rec, httptest.NewRequest(http.MethodGet, "/index.m3u8", nil), info.ID, u)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "#EXTM3U") || !strings.Contains(rec.Body.String(), "/relay/") {
		t.Fatalf("ServePlaylist status=%d body=%q", rec.Code, rec.Body.String())
	}

	// Direct streams must never register as Live TV channels.
	if chs, err := s.Channels(context.Background()); err != nil || len(chs) != 0 {
		t.Fatalf("expected 0 Live TV channels, got %d (err=%v)", len(chs), err)
	}

	// Revoking access via AllowCheck immediately denies playlist reads.
	allowed.Store(false)
	denied := httptest.NewRecorder()
	s.ServePlaylist(denied, httptest.NewRequest(http.MethodGet, "/index.m3u8", nil), info.ID, u)
	if denied.Code != http.StatusNotFound {
		t.Fatalf("revoked ServePlaylist status=%d, want 404", denied.Code)
	}
	if got := masterHits.Load(); got != 1 {
		t.Fatalf("master playlist fetched %d times after serving, want 1", got)
	}
}

type singleUseRelayTranscoder struct {
	core.Transcoder
	client *http.Client

	mu             sync.Mutex
	probeManifest  string
	relayManifest  string
	probeErr       error
	relayErr       error
	startHLSCount  int
}

func (f *singleUseRelayTranscoder) Decide(_ core.MediaInfo, _ core.ClientCaps, _ core.DecideOptions) core.Decision {
	return core.Decision{
		Kind:       core.DecisionRemux,
		Container:  "mpegts",
		VideoCodec: "h264",
		AudioCodec: "aac",
		VideoCopy:  true,
		AudioCopy:  true,
	}
}

func (f *singleUseRelayTranscoder) Probe(ctx context.Context, input string) (*core.MediaInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, input, nil)
	if err != nil {
		return nil, err
	}
	res, err := f.client.Do(req)
	if err != nil {
		f.mu.Lock()
		f.probeErr = err
		f.mu.Unlock()
		return nil, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	f.mu.Lock()
	f.probeManifest = string(body)
	f.mu.Unlock()
	if res.StatusCode != http.StatusOK {
		return nil, providerResponseError(res.StatusCode)
	}
	return &core.MediaInfo{
		Container: "hls",
		Streams: []core.StreamInfo{
			{Index: 0, Type: "video", Codec: "h264", Width: 1280, Height: 720, BitDepth: 8},
			{Index: 1, Type: "audio", Codec: "aac", Channels: 2},
		},
	}, nil
}

func (f *singleUseRelayTranscoder) StartHLS(ctx context.Context, r core.HLSRequest) (core.HLSSession, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.InputURL, nil)
	if err != nil {
		return nil, err
	}
	res, err := f.client.Do(req)
	if err != nil {
		f.mu.Lock()
		f.relayErr = err
		f.mu.Unlock()
		return nil, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	f.mu.Lock()
	f.relayManifest = string(body)
	f.startHLSCount++
	f.mu.Unlock()
	if res.StatusCode != http.StatusOK {
		return nil, providerResponseError(res.StatusCode)
	}
	return &cmafRelayHLS{fakeHLS: fakeHLS{id: r.SessionID}, seg: makeValidTSPacket()}, nil
}

type cmafRelayHLS struct {
	fakeHLS
	seg []byte
}

func (h *cmafRelayHLS) ServePlaylist(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	_, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:2.0,\n00000.ts\n#EXTINF:2.0,\n00001.ts\n#EXTINF:2.0,\n00002.ts\n")
}

func (h *cmafRelayHLS) ServeSegment(w http.ResponseWriter, _ *http.Request, _ int) {
	w.Header().Set("Content-Type", "video/mp2t")
	_, _ = w.Write(h.seg)
}

func TestDirectStreamSingleUseMasterDemuxedCMAF(t *testing.T) {
	var masterHits atomic.Int32

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cmaf-master.m3u8":
			if masterHits.Add(1) > 1 {
				http.Error(w, "token already used", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(w, strings.Join([]string{
				"#EXTM3U",
				"#EXT-X-VERSION:7",
				`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="English",DEFAULT=YES,AUTOSELECT=YES,URI="/audio.m3u8"`,
				`#EXT-X-STREAM-INF:BANDWIDTH=3500000,RESOLUTION=1280x720,CODECS="avc1.64001f,mp4a.40.2",AUDIO="aud"`,
				"/video.m3u8",
				"",
			}, "\n"))
		case "/video.m3u8", "/audio.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:1\n#EXTINF:2.0,\n/chunk1.m4s\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	tc := &singleUseRelayTranscoder{client: &http.Client{Timeout: 5 * time.Second}}
	s := testService(t, Options{HTTPClient: upstream.Client(), Transcoder: tc})
	u := &core.User{ID: 8, Username: "bob", StreamLimit: 2}
	caps := core.ClientCaps{
		Product:       "Vela",
		Protocol:      "vela",
		ClientID:      "vela-client-cmaf",
		VideoCodecs:   []string{"h264"},
		AudioCodecs:   []string{"aac"},
		HLSContainers: []string{"mpegts"},
	}

	info, err := s.TuneDirectStream(context.Background(), u, DirectStream{
		ID:    "esports:cmaf-1",
		Title: "Arena CMAF Feed",
		URL:   upstream.URL + "/cmaf-master.m3u8?token=once",
	}, caps, "vela-sess-cmaf")
	if err != nil {
		t.Fatalf("TuneDirectStream (demuxed CMAF) failed: %v", err)
	}
	defer func() { _ = s.Stop(context.Background(), info.ID, u) }()

	if got := masterHits.Load(); got != 1 {
		t.Fatalf("demuxed CMAF master fetched %d times, want exactly 1", got)
	}

	tc.mu.Lock()
	probeMan := tc.probeManifest
	relayMan := tc.relayManifest
	startCount := tc.startHLSCount
	tc.mu.Unlock()

	if !strings.Contains(probeMan, "#EXTM3U") || !strings.Contains(probeMan, "#EXT-X-MEDIA:TYPE=AUDIO") {
		t.Fatalf("probe did not receive rewritten demuxed master: %q", probeMan)
	}
	if startCount != 1 || !strings.Contains(relayMan, "#EXTM3U") || !strings.Contains(relayMan, "#EXT-X-MEDIA:TYPE=AUDIO") {
		t.Fatalf("relay StartHLS (count=%d) did not receive cached demuxed master: %q", startCount, relayMan)
	}

	rec := httptest.NewRecorder()
	s.ServePlaylist(rec, httptest.NewRequest(http.MethodGet, "/index.m3u8", nil), info.ID, u)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "#EXTM3U") {
		t.Fatalf("ServePlaylist status=%d body=%q", rec.Code, rec.Body.String())
	}
	if got := masterHits.Load(); got != 1 {
		t.Fatalf("demuxed CMAF master fetched %d times after playlist serve, want 1", got)
	}
}
