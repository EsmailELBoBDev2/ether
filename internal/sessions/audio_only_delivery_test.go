package sessions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
)

func TestAudioOnlyFLACOnVelaWebGetsHTTPAndHLSNot422(t *testing.T) {
	flacInfo := core.MediaInfo{
		Container:   "flac",
		Duration:    4 * time.Minute,
		Size:        30 << 20,
		BitrateKbps: 1000,
		Streams: []core.StreamInfo{{
			Index:       0,
			Type:        "audio",
			Codec:       "flac",
			Channels:    2,
			SampleRate:  44100,
			BitrateKbps: 1000,
			Default:     true,
		}},
	}

	// Vela Web declares aac + opus (no flac) for audio direct play and http/hls audio transcode.
	caps := core.ClientCaps{
		Product:              "Vela Web",
		Containers:           []string{"mp4", "mp3", "ogg", "webm"},
		AudioCodecs:          []string{"aac", "opus", "mp3"},
		TranscodeAudioCodecs: []string{"aac", "opus"},
		HLSContainers:        []string{"mp4"},
		Profiles: &core.ClientCapabilityProfiles{
			DirectPlay: []core.ClientDirectPlayProfile{
				{MediaType: "audio", Containers: []string{"mp4", "m4a", "mp3", "ogg", "webm"}, AudioCodecs: []string{"aac", "opus", "mp3"}},
			},
			Transcoding: []core.ClientTranscodeProfile{
				{MediaType: "audio", Delivery: "http", Containers: []string{"mp4", "aac", "mp3", "ogg"}, AudioCodecs: []string{"aac", "opus", "mp3"}},
				{MediaType: "audio", Delivery: "hls", Containers: []string{"mp4", "mpegts"}, AudioCodecs: []string{"aac"}},
			},
		},
	}

	source := &core.PlaySession{ID: "play-1", Info: flacInfo}
	pb := &fakePlayback{sessions: map[string]*core.PlaySession{"play-1": source}}
	b := &Broker{
		deps: BrokerDeps{
			SurfacePolicy: testSessionPolicy,
			Playback:      pb,
			Transcoder:    profileTranscoder{},
		},
		bindings: NewBindingTable[frontend.PlayIntent](),
	}
	req := frontend.PlanRequest{
		Caps: caps,
		Intent: frontend.PlayIntent{
			Options: core.DecideOptions{DirectPlay: true, DirectStream: true},
		},
	}

	// 1. decideProfile succeeds (does NOT fail with unsupported transport / 422).
	dec, err := b.decideProfile(source, req)
	if err != nil {
		t.Fatalf("decideProfile(FLAC on Vela Web) = %v, want valid transcode decision", err)
	}
	if dec.Kind != core.DecisionTranscode || dec.AudioCodec != "aac" || dec.VideoCodec != "" {
		t.Fatalf("decideProfile = %+v", dec)
	}

	// 2. Progressive HTTP audio transcode is admitted and uses audio AAC in mp4/fmp4 (.m4a).
	httpOK, httpContainer := b.httpPlanOutput(source, req, dec)
	if !httpOK || httpStreamExt(httpContainer, dec.AudioCodec) != "m4a" {
		t.Fatalf("httpPlanOutput = (%v, %q, ext=%q), want (true, mp4/fmp4, m4a)", httpOK, httpContainer, httpStreamExt(httpContainer, dec.AudioCodec))
	}

	// 3. On-demand HLS audio transcode is ALSO admitted (knownHLSStream allows probed audio-only).
	hlsOK, _ := b.hlsPlanOutput(source, req, dec)
	if !hlsOK {
		t.Fatal("hlsPlanOutput returned false for probed audio-only FLAC on Vela Web")
	}

	// 4. DirectPlay is NOT allowed for FLAC when client only supports aac/opus/mp3.
	if profileDirectAllowed(caps.Profiles, flacInfo, core.Decision{Kind: core.DecisionDirectPlay}) {
		t.Fatal("FLAC should not direct-play on aac/opus-only audio profile")
	}

	// 5. Master playlist for audio-only HLS uses decision bitrate for BANDWIDTH and emits valid M3U8.
	hs := &fakeMediaSession{}
	b.bindings.Put(&Binding[frontend.PlayIntent]{ID: "sess1", Decision: dec})
	sess := &brokerSession{
		playID: "play-1",
		public: frontend.Session{ID: "sess1", Protocol: "vela"},
	}
	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodGet, "/sessions/media/sess1/grant1/master.m3u8", nil)
	b.serveMaster(rec, httpReq, sess, hs)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "#EXT-X-STREAM-INF:BANDWIDTH=192000") || !strings.Contains(body, "base/index.m3u8") {
		t.Fatalf("audio-only master playlist = (%d) %q", rec.Code, body)
	}
	_ = context.Background()
}
