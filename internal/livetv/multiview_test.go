package livetv

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/transcode"
)

func TestMultiviewPlayoutFourTilesSupersedeAndStreamLimit(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4,\nseg.ts\n")
	}))
	defer up.Close()

	s := testService(t, Options{HTTPClient: up.Client()})
	ctx := context.Background()

	var channels [5]Channel
	for i := range channels {
		c, err := s.SaveChannel(ctx, Channel{
			Name:      fmt.Sprintf("Ch %d", i+1),
			Number:    fmt.Sprintf("%d", i+1),
			Kind:      "iptv",
			Enabled:   true,
			StreamURL: fmt.Sprintf("%s/ch%d.m3u8", up.URL, i),
		})
		if err != nil {
			t.Fatal(err)
		}
		channels[i] = c
	}

	u := &core.User{ID: 1, StreamLimit: 1}
	groupID := "mv_group_one"
	var tileSess [4]SessionInfo
	for tile := 0; tile < 4; tile++ {
		info, err := s.Tune(ctx, u, channels[tile].ID, core.ClientCaps{
			ClientID:       "vela/apple-tv",
			MultiviewGroup: groupID,
			MultiviewTile:  tile,
		})
		if err != nil {
			t.Fatalf("tile %d failed with StreamLimit=1: %v", tile, err)
		}
		tileSess[tile] = info
	}
	if got := len(s.Sessions(ctx, u)); got != 4 {
		t.Fatalf("expected 4 concurrent multiview sessions, got %d", got)
	}

	// StreamLimit=1 counts the 4-tile group as 1, so another device is rejected.
	if _, err := s.Tune(ctx, u, channels[4].ID, core.ClientCaps{ClientID: "vela/iphone"}); !errors.Is(err, core.ErrStreamLimit) {
		t.Fatalf("second device bypassed StreamLimit=1: got %v, want ErrStreamLimit", err)
	}

	// With StreamLimit=2, a second device is admitted (group counts as 1, not 4),
	// and a third device is rejected.
	u2 := &core.User{ID: 1, StreamLimit: 2}
	otherDev, err := s.Tune(ctx, u2, channels[4].ID, core.ClientCaps{ClientID: "vela/iphone"})
	if err != nil {
		t.Fatalf("second device rejected with StreamLimit=2 (group should count as 1): %v", err)
	}
	if _, err := s.Tune(ctx, u2, channels[0].ID, core.ClientCaps{ClientID: "vela/ipad"}); !errors.Is(err, core.ErrStreamLimit) {
		t.Fatalf("third device bypassed StreamLimit=2: got %v, want ErrStreamLimit", err)
	}
	_ = s.Stop(ctx, otherDev.ID, u2)

	// Re-tuning tile 1 replaces ONLY tile 1; tiles 0, 2, 3 remain alive.
	replaced, err := s.Tune(ctx, u, channels[4].ID, core.ClientCaps{
		ClientID:       "vela/apple-tv",
		MultiviewGroup: groupID,
		MultiviewTile:  1,
	})
	if err != nil {
		t.Fatalf("replace tile 1: %v", err)
	}
	if _, err := s.Session(ctx, tileSess[1].ID, u); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("old tile 1 survived replacement: %v", err)
	}
	for _, keep := range []SessionInfo{tileSess[0], replaced, tileSess[2], tileSess[3]} {
		if _, err := s.Session(ctx, keep.ID, u); err != nil {
			t.Fatalf("expected tile session %q to survive: %v", keep.ID, err)
		}
	}

	// Non-group tune on the same device supersedes the entire 4-tile group.
	solo, err := s.Tune(ctx, u, channels[0].ID, core.ClientCaps{ClientID: "vela/apple-tv"})
	if err != nil {
		t.Fatalf("non-group tune: %v", err)
	}
	active := s.Sessions(ctx, u)
	if len(active) != 1 || active[0].ID != solo.ID {
		t.Fatalf("non-group tune did not supersede whole group: active=%d", len(active))
	}
}

func TestMultiviewProviderConnectionCapTypedErrorAndSameChannelSharing(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4,\nseg.ts\n")
	}))
	defer up.Close()

	s := testService(t, Options{HTTPClient: up.Client()})
	ctx := context.Background()

	src, err := s.ImportSource(ctx, Source{
		ID:             "prov-acme",
		Name:           "Acme IPTV",
		MaxConnections: 2,
		Playlist:       fmt.Sprintf("#EXTM3U\n#EXTINF:-1,Seed\n%s/seed.m3u8\n", up.URL),
	})
	if err != nil {
		t.Fatal(err)
	}
	var chs [3]Channel
	for i := range chs {
		c, err := s.SaveChannel(ctx, Channel{
			Name:      fmt.Sprintf("Sports %d", i+1),
			Number:    fmt.Sprintf("%d", i+1),
			Kind:      "iptv",
			SourceID:  src.ID,
			Enabled:   true,
			StreamURL: fmt.Sprintf("%s/sport%d.m3u8", up.URL, i),
		})
		if err != nil {
			t.Fatal(err)
		}
		chs[i] = c
	}

	u := &core.User{ID: 3, StreamLimit: 4}
	// Tile 0 on Ch 0 and Tile 1 on Ch 1 consume both provider connections.
	if _, err := s.Tune(ctx, u, chs[0].ID, core.ClientCaps{ClientID: "vela/tv", MultiviewGroup: "mv_cap", MultiviewTile: 0}); err != nil {
		t.Fatalf("tile 0: %v", err)
	}
	if _, err := s.Tune(ctx, u, chs[1].ID, core.ClientCaps{ClientID: "vela/tv", MultiviewGroup: "mv_cap", MultiviewTile: 1}); err != nil {
		t.Fatalf("tile 1: %v", err)
	}
	// Tile 2 on Ch 0 shares the existing upstream connection and succeeds.
	if _, err := s.Tune(ctx, u, chs[0].ID, core.ClientCaps{ClientID: "vela/tv", MultiviewGroup: "mv_cap", MultiviewTile: 2}); err != nil {
		t.Fatalf("tile 2 same channel should share upstream: %v", err)
	}

	// Tile 3 on a 3rd distinct channel exceeds MaxConnections=2 with a typed error.
	_, err = s.Tune(ctx, u, chs[2].ID, core.ClientCaps{ClientID: "vela/tv", MultiviewGroup: "mv_cap", MultiviewTile: 3})
	var capErr *core.ProviderConnectionCapError
	if !errors.As(err, &capErr) {
		t.Fatalf("expected *core.ProviderConnectionCapError, got %T: %v", err, err)
	}
	if !errors.Is(err, ErrProviderBusy) || !errors.Is(err, core.ErrStreamLimit) {
		t.Fatalf("ProviderConnectionCapError should preserve ErrProviderBusy/ErrStreamLimit wrapping: %v", err)
	}
	if capErr.Provider != "Acme IPTV" || capErr.Max != 2 || capErr.InUse != 2 {
		t.Fatalf("capErr = %+v, want Provider='Acme IPTV' Max=2 InUse=2", capErr)
	}
	if capErr.Error() != "Acme IPTV allows 2 connections" {
		t.Fatalf("capErr.Error() = %q", capErr.Error())
	}
	// Existing 3 tiles remain active after tile 3 rejection.
	if got := len(s.Sessions(ctx, u)); got != 3 {
		t.Fatalf("expected 3 tiles to remain active after cap rejection, got %d", got)
	}
}

func TestMultiviewTileMaxHeightCopiesWhenSourceFitsAndScalesWhenLarger(t *testing.T) {
	info720 := core.MediaInfo{
		Container: "mpegts",
		Streams: []core.StreamInfo{
			{Index: 0, Type: "video", Codec: "h264", Width: 1280, Height: 720, Profile: "high", Level: 40, PixelFormat: "yuv420p"},
			{Index: 1, Type: "audio", Codec: "aac", Channels: 2, SampleRate: 48000},
		},
	}
	info1080 := core.MediaInfo{
		Container: "mpegts",
		Streams: []core.StreamInfo{
			{Index: 0, Type: "video", Codec: "h264", Width: 1920, Height: 1080, Profile: "high", Level: 41, PixelFormat: "yuv420p"},
			{Index: 1, Type: "audio", Codec: "aac", Channels: 2, SampleRate: 48000},
		},
	}
	opts := core.DecideOptions{DirectStream: true, AudioStream: -1, SubtitleStream: -1}
	tile720 := core.ClientCaps{
		VideoCodecs: []string{"h264"}, AudioCodecs: []string{"aac"},
		MaxHeight: 720, ExplicitMaxHeight: true,
		MultiviewGroup: "mv_q", MultiviewTile: 2,
	}
	// Source fits 720p tile cap -> VideoCopy=true (no video transcode).
	dFit := transcode.Decide(info720, tile720, opts, transcode.Policy{})
	if !dFit.VideoCopy {
		t.Fatalf("expected video copy when 720p source fits 720p tile cap, got %+v", dFit)
	}
	// Source exceeds 720p tile cap -> scales down to 720p.
	dScale := transcode.Decide(info1080, tile720, opts, transcode.Policy{})
	if dScale.VideoCopy || dScale.MaxHeight != 720 {
		t.Fatalf("expected 1080p source to scale to 720p under 720p tile cap, got %+v", dScale)
	}
	// Focused-tile upgrade to 1080p -> 1080p source now copies.
	tile1080 := tile720
	tile1080.MaxHeight = 1080
	dUpgrade := transcode.Decide(info1080, tile1080, opts, transcode.Policy{})
	if !dUpgrade.VideoCopy {
		t.Fatalf("expected video copy after upgrading tile cap to 1080p, got %+v", dUpgrade)
	}
}
