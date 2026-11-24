package plex

import (
	"net/http"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// TestFirstPlayRuleSubtitleAcrossClients replays a first play: the client
// holds the pre-playback estimate (no subtitle tracks), then plays once the
// release is probed and has an English subtitle. The viewer's rules say
// English, always. It reports what each client is actually given.
func TestFirstPlayRuleSubtitleAcrossClients(t *testing.T) {
	android := []string{
		"X-Plex-Product", "Plex for Android (TV)", "X-Plex-Version", "2026.22.0",
		"X-Plex-Platform", "Android", "X-Plex-Device", "Chromecast", "X-Plex-Model", "sabrina",
		"X-Plex-Client-Identifier", "fp-android", "X-Plex-Session-Identifier", "fp-android-play",
		"X-Plex-Client-Profile-Extra", androidMKVProfile,
	}
	web := []string{"X-Plex-Product", "Plex Web", "X-Plex-Platform", "Chrome", "X-Plex-Client-Identifier", "fp-web"}
	srt := core.StreamInfo{Index: 2, Type: "subtitle", Codec: "subrip", Language: "en"}
	pgs := core.StreamInfo{Index: 2, Type: "subtitle", Codec: "hdmv_pgs_subtitle", Language: "en"}
	for _, tc := range []struct {
		name      string
		hdr       []string
		sub       core.StreamInfo
		query     string
		mustServe bool
	}{
		{"android-tv-srt", android, srt, "&protocol=hls&directPlay=0&directStream=1&hasMDE=1&subtitles=none", true},
		{"android-tv-pgs", android, pgs, "&protocol=hls&directPlay=0&directStream=1&hasMDE=1&subtitles=none", false},
		{"plex-web-srt-auto-burn", web, srt, "&protocol=hls&directPlay=0&directStream=1&subtitles=burn", false},
		{"plex-web-srt-no-burn", web, srt, "&protocol=hls&directPlay=0&directStream=1&subtitles=auto", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probed := core.MediaInfo{
				Container: "mkv", Duration: 100 * time.Minute, BitrateKbps: 12000,
				Streams: []core.StreamInfo{
					{Index: 0, Type: "video", Codec: "h264", Width: 1920, Height: 1080, Default: true},
					{Index: 1, Type: "audio", Codec: "aac", Language: "ko", Channels: 2, Default: true},
					tc.sub,
				},
			}
			f, _, _ := newIssue459Fixture(t, probed, "en")
			key := fightClub.Key.String()

			// The detail page the client rendered: the estimate, no subtitles.
			f.playback.info = core.MediaInfo{Container: "mkv", Duration: probed.Duration, Streams: probed.Streams[:2]}
			if meta := f.get(t, "/library/metadata/"+key, tc.hdr...); meta.Code != http.StatusOK {
				t.Fatalf("detail: %d", meta.Code)
			} else if subs := subtitleTags(meta.Body.String()); len(subs) != 0 {
				t.Fatalf("estimate listed subtitles: %v", subs)
			}

			// Play: the release is now probed.
			f.playback.info = probed
			dec := f.get(t, "/video/:/transcode/universal/decision?path=/library/metadata/"+key+tc.query+"&session=fp-"+tc.name, tc.hdr...)
			if dec.Code != http.StatusOK {
				t.Fatalf("decision: %d %s", dec.Code, dec.Body.String())
			}
			selected := selectedTag(subtitleTags(dec.Body.String()))
			start := f.get(t, "/video/:/transcode/universal/start.m3u8?path=/library/metadata/"+key+tc.query+"&session=fp-"+tc.name, tc.hdr...)
			if start.Code != http.StatusOK {
				t.Fatalf("start: %d %s", start.Code, start.Body.String())
			}
			hs := f.trans.sessions["fp-"+tc.name]
			if hs == nil {
				t.Fatal("no HLS session")
			}
			served := hs.req.Decision.SubtitleStream == tc.sub.Index
			t.Logf("RESULT %s: decision shows subtitle selected=%q; stream carries subtitle=%v burn=%v (stream index %d)",
				tc.name, streamAttr(selected, "languageCode"), served, hs.req.Decision.BurnSubtitles, hs.req.Decision.SubtitleStream)
			if tc.mustServe && (!served || streamAttr(selected, "languageCode") == "") {
				t.Fatalf("first play did not deliver the rule's English subtitle")
			}
		})
	}
}
