package sessions

import (
	"context"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
)

type qualityTranscoder struct{ fakeMediaTranscoder }

func (*qualityTranscoder) ProactiveFallbackAvailable() bool { return true }

func TestQualityPolicyInvariants(t *testing.T) {
	info := core.MediaInfo{Container: "mp4", Duration: time.Hour, BitrateKbps: 15000, Streams: []core.StreamInfo{{Index: 0, Type: "video", Codec: "h264", Width: 1920, Height: 1080}, {Index: 1, Type: "audio", Codec: "aac", BitrateKbps: 192}}}
	playback := &fakePlayback{sessions: map[string]*core.PlaySession{"source": {ID: "source", Info: info}}}
	b := &Broker{deps: BrokerDeps{Playback: playback, Transcoder: &qualityTranscoder{}, Now: time.Now, Lookup: func(_ context.Context, _ *core.Viewer, _ core.Device, k core.RatingKey) (*core.Title, error) {
		return &core.Title{Key: k}, nil
	}}}
	s := &brokerSession{public: frontend.Session{Protocol: "vela", ViewerID: 1, Item: core.MovieKey(1)}, playID: "source", caps: core.ClientCaps{VideoCodecs: []string{"h264"}, MaxHeight: 720, MaxBitrateKbps: 2000}, opts: core.DecideOptions{DirectPlay: true, DirectStream: true}, decision: core.Decision{Kind: core.DecisionRemux, Container: "fmp4", VideoCopy: true, AudioCopy: true, AudioStream: 1}}
	viewer := &core.Viewer{ID: 1}
	rungs, err := b.qualityRungs(context.Background(), s, viewer)
	if err != nil || len(rungs) == 0 {
		t.Fatal(rungs, err)
	}
	for _, r := range rungs {
		if r.Height > 720 || r.Bandwidth > 2000000 {
			t.Fatal("session caps escaped", r)
		}
	}
	denied := false
	s.allowTranscoding = &denied
	if r, err := b.qualityRungs(context.Background(), s, viewer); err != nil || len(r) != 0 {
		t.Fatal("allowTranscode=false", r, err)
	}
	s.allowTranscoding = nil
	for _, protocol := range []frontend.Protocol{"plex", "jellyfin"} {
		s.public.Protocol = protocol
		if r, err := b.qualityRungs(context.Background(), s, viewer); err != nil || len(r) != 0 {
			t.Fatal("changed another surface", r, err)
		}
	}
	s.public.Protocol = "vela"
	s.live = true
	if r, err := b.qualityRungs(context.Background(), s, viewer); err != nil || len(r) != 0 {
		t.Fatal("file ladder on live", r, err)
	}
	if playback.prepares != 0 {
		t.Fatal("quality planning resolved a source")
	}
}

// TestQualityPlanDirectPlayReDecidesForHLSLadder locks the fix for a plain
// direct-play decision (reason "") re-deciding for the HLS ladder. Its empty
// transcode codecs would otherwise fail profileDeliveryAllowed and surface a
// broken Height-0 limitation instead of the HLS rungs.
func TestQualityPlanDirectPlayReDecidesForHLSLadder(t *testing.T) {
	info := core.MediaInfo{Container: "mp4", Duration: time.Hour, BitrateKbps: 15000, Streams: []core.StreamInfo{{Index: 0, Type: "video", Codec: "h264", Width: 1920, Height: 1080}, {Index: 1, Type: "audio", Codec: "aac", BitrateKbps: 192}}}
	playback := &fakePlayback{sessions: map[string]*core.PlaySession{"source": {ID: "source", Info: info}}}
	b := &Broker{deps: BrokerDeps{Playback: playback, Transcoder: &qualityTranscoder{}, Now: time.Now, Lookup: func(_ context.Context, _ *core.Viewer, _ core.Device, k core.RatingKey) (*core.Title, error) {
		return &core.Title{Key: k}, nil
	}}}
	s := &brokerSession{public: frontend.Session{Protocol: "vela", ViewerID: 1, Item: core.MovieKey(1)}, playID: "source", caps: core.ClientCaps{VideoCodecs: []string{"h264"}, MaxHeight: 720, MaxBitrateKbps: 2000}, opts: core.DecideOptions{DirectPlay: true, DirectStream: true}, decision: core.Decision{Kind: core.DecisionDirectPlay, VideoCopy: true, AudioCopy: true, Container: "mp4", AudioStream: 1}}
	viewer := &core.Viewer{ID: 1}

	variants, limits, err := b.qualityPlan(context.Background(), s, viewer)
	if err != nil {
		t.Fatal(err)
	}
	// A plain direct-play decision must build the HLS ladder.
	if len(variants) == 0 {
		t.Fatalf("direct-play qualityPlan yielded no variants; limits=%+v", limits)
	}
	for _, v := range variants {
		if v.Rung.Height <= 0 {
			t.Fatalf("direct-play qualityPlan returned a Height-0 rung: %+v", v)
		}
	}
	for _, l := range limits {
		if l.Height == 0 {
			t.Fatalf("direct-play qualityPlan emitted a Height-0 limitation: %+v", l)
		}
	}
	if playback.prepares != 0 {
		t.Fatal("quality planning resolved a source")
	}
}
