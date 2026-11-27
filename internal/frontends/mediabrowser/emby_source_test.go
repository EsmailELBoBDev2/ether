package mediabrowser

import (
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
)

func TestEmbyMediaSourceKnownIdentityAndMIME(t *testing.T) {
	i := &instance{}
	source := map[string]any{}
	i.embyMediaSourceDTO(source, "item", frontend.MediaInfo{Container: "mp4"})
	if source["ItemId"] != "item" {
		t.Fatal(source)
	}
	if _, ok := source["MimeType"]; ok {
		t.Fatal("MIME type inferred without a video stream")
	}
	i.embyMediaSourceDTO(source, "item", frontend.MediaInfo{Container: "mp4", Streams: []core.StreamInfo{{Type: "video"}}})
	if source["MimeType"] != "video/mp4" {
		t.Fatal(source)
	}
}

func TestEmbyDirectURLNeverRequiresAccountCredential(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"https://ether.example/emby/sessions/lazy/token_1/file", true},
		{"https://ether.example/emby/sessions/media/session/token/file", true},
		{"/emby/sessions/media/session/token/file", true},
		{"/Videos/item/stream", false},
		{"https://ether.example/emby/sessions/lazy/token/file?api_key=secret", false},
		{"https://other.example/sessions/media/../token/file", false},
		{"https://ether.example/emby/sessions/media/session/token/index.m3u8", false},
	} {
		source := map[string]any{"Path": tc.path}
		(&instance{}).embyMediaSourceDTO(source, "item", frontend.MediaInfo{})
		got, present := source["AddApiKeyToDirectStreamUrl"]
		if present != tc.want || present && got != false {
			t.Errorf("%s: %#v", tc.path, source)
		}
	}
}

func TestEmbyDirectStreamURLIsAPathUnderTheMount(t *testing.T) {
	// Emby clients prefix the server address and /emby to DirectStreamUrl.
	for raw, want := range map[string]string{
		"https://ether.example/emby/sessions/media/session/token/file": "/sessions/media/session/token/file",
		"https://ether.example/sessions/media/session/token/file":      "/sessions/media/session/token/file",
		"/emby/sessions/lazy/token_1/file":                             "/sessions/lazy/token_1/file",
	} {
		source := map[string]any{"Path": raw}
		(&instance{}).embyMediaSourceDTO(source, "item", frontend.MediaInfo{})
		if source["DirectStreamUrl"] != want {
			t.Errorf("%s: DirectStreamUrl %v, want %s", raw, source["DirectStreamUrl"], want)
		}
		if source["Path"] != raw {
			t.Errorf("%s: Path changed to %v", raw, source["Path"])
		}
	}
}

func TestEmbyTranscodingURLHasNoMountPrefix(t *testing.T) {
	for raw, want := range map[string]string{
		"/emby/sessions/media/s/g/master.m3u8":       "/sessions/media/s/g/master.m3u8",
		"/emby/sessions/media/s/g/master.m3u8?a=b":   "/sessions/media/s/g/master.m3u8?a=b",
		"/sessions/media/s/g/master.m3u8":            "/sessions/media/s/g/master.m3u8",
		"https://other.example/live/stream.m3u8?x=1": "https://other.example/live/stream.m3u8?x=1",
	} {
		source := map[string]any{"TranscodingUrl": raw}
		(&instance{}).embyMediaSourceDTO(source, "item", frontend.MediaInfo{})
		if source["TranscodingUrl"] != want {
			t.Errorf("%s: %v, want %s", raw, source["TranscodingUrl"], want)
		}
	}
}

func TestEmbySourceDefaultAudioUsesAuthoritativeDisposition(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name      string
		estimated bool
		streams   []core.StreamInfo
		want      int
		present   bool
	}{
		{"explicit default", false, []core.StreamInfo{{Type: "video", Index: 0}, {Type: "audio", Index: 1, DefaultDisposition: &yes}}, 1, true},
		{"legacy default", false, []core.StreamInfo{{Type: "audio", Index: 0, Default: true}}, 0, true},
		{"explicit false overrides", false, []core.StreamInfo{{Type: "audio", Index: 1, Default: true, DefaultDisposition: &no}}, 0, false},
		{"multiple defaults", false, []core.StreamInfo{{Type: "audio", Index: 2, DefaultDisposition: &yes}, {Type: "audio", Index: 1, DefaultDisposition: &yes}}, 1, true},
		{"unknown", false, []core.StreamInfo{{Type: "audio", Index: 1}}, 0, false},
		{"estimated", true, []core.StreamInfo{{Type: "audio", Index: 1, DefaultDisposition: &yes}}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := map[string]any{"DefaultAudioStreamIndex": 999}
			(&instance{}).embyMediaSourceDTO(source, "item", frontend.MediaInfo{Estimated: tc.estimated, Streams: tc.streams})
			got, ok := source["DefaultAudioStreamIndex"]
			if ok != tc.present || ok && got != tc.want {
				t.Fatalf("got %#v, want %d present=%v", got, tc.want, tc.present)
			}
		})
	}
}

func TestEmbySubtitleDeliveryForEmbyClients(t *testing.T) {
	source := map[string]any{"MediaStreams": []any{
		map[string]any{"Type": "Video", "Index": 0},
		map[string]any{"Type": "Subtitle", "Index": 2, "Codec": "pgs"},
		map[string]any{"Type": "Subtitle", "Index": 3, "Codec": "subrip", "DeliveryMethod": "External", "DeliveryUrl": "/emby/sessions/media/s/g/subtitles/3.vtt"},
		map[string]any{"Type": "Subtitle", "Index": 4, "Codec": "subrip", "DeliveryMethod": "Embed"},
	}}
	(&instance{}).embyMediaSourceDTO(source, "item", frontend.MediaInfo{})
	streams := source["MediaStreams"].([]any)
	if got := streams[1].(map[string]any)["DeliveryMethod"]; got != "Encode" {
		t.Fatalf("image subtitle method %v, want Encode", got)
	}
	if got := streams[2].(map[string]any)["DeliveryUrl"]; got != "/sessions/media/s/g/subtitles/3.vtt" {
		t.Fatalf("text subtitle url %v", got)
	}
	if got := streams[3].(map[string]any)["DeliveryMethod"]; got != "Embed" {
		t.Fatalf("embedded subtitle method changed to %v", got)
	}
}
