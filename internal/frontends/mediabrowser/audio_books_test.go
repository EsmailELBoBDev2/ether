package mediabrowser

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/frontend/frontendtest"
	"github.com/braedonsaunders/ether/internal/frontend/kit/ids"
)

func TestMediaBrowserBookRefusalAudioTranscodeUniversalAndReadingPercentage(t *testing.T) {
	viewer := &frontend.Viewer{ID: 1}
	bookKey := core.PluginKey(401)
	trackKey := core.PluginKey(402)
	var planCalls atomic.Int32
	var startedFormat frontend.StreamFormat

	flacInfo := frontend.MediaInfo{
		Container:   "flac",
		Duration:    5 * time.Minute,
		Size:        30 << 20,
		BitrateKbps: 1000,
		Streams:     []frontend.StreamInfo{{Index: 0, Type: "audio", Codec: "flac", Channels: 2, Default: true}},
		Chapters:    []core.Chapter{{Title: "Part 1", Start: 0, End: 5 * time.Minute}},
	}

	exp := frontendtest.Experience{
		ItemFunc: func(_ context.Context, req frontend.ItemRequest) (frontend.ItemDetail, error) {
			if req.Key == bookKey {
				return frontend.ItemDetail{
					Title: frontend.Title{Key: bookKey, Type: frontend.TypeMovie, Title: "Dune", MediaClass: core.MediaClassBook, MBItemType: "Book"},
				}, nil
			}
			return frontend.ItemDetail{
				Title: frontend.Title{Key: trackKey, Type: frontend.TypeMovie, Title: "You Must Burn!", MediaClass: core.MediaClassMusic, MBItemType: "Audio"},
			}, nil
		},
	}

	sessions := frontendtest.Sessions{
		PlanFunc: func(_ context.Context, req frontend.PlanRequest) (frontend.PlayPlan, error) {
			planCalls.Add(1)
			return frontend.PlayPlan{
				ID:            "plan-mb-audio",
				Media:         flacInfo,
				Decision:      frontend.Decision{Kind: core.DecisionTranscode, AudioCodec: "mp3", Container: "mp3", AudioBitrateKbps: 192},
				HTTP:          true,
				HTTPContainer: "mp3",
				HLS:           true,
			}, nil
		},
		OpenFunc: func(context.Context, frontend.OpenRequest) (frontend.Session, error) {
			return frontend.Session{ID: "sess-mb-audio"}, nil
		},
		StartStreamFunc: func(_ context.Context, _ frontend.SessionAccess, _ string, req frontend.StreamRequest) (string, error) {
			startedFormat = req.Format
			return "/sessions/media/sess-mb-audio/g/stream.mp3", nil
		},
		ServeMediaFunc: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "audio/mpeg")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ID3-mp3-audio"))
		},
	}

	ident := frontendtest.Identity{ResolveFunc: func(context.Context, string) (*frontend.Viewer, error) { return viewer, nil }}
	inst, err := (Jellyfin{MachineID: "test"}).Mount(context.Background(), frontend.Host{
		Identity:     ident,
		Experience:   exp,
		Sessions:     sessions,
		Capabilities: frontendtest.Capabilities{},
		SurfaceURL:   func(context.Context, *http.Request) (string, error) { return "https://ether.example/jellyfin", nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. PlaybackInfo on a Book returns ErrorCode: NoCompatibleStream without touching Sessions.Plan.
	bookPath := "/jellyfin/Items/" + ids.RatingKeyGUID(bookKey) + "/PlaybackInfo"
	req := httptest.NewRequest(http.MethodPost, bookPath, strings.NewReader(`{"IsPlayback":true}`))
	req.Header.Set("X-Emby-Token", "grant")
	rec := httptest.NewRecorder()
	inst.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ErrorCode":"NoCompatibleStream"`) {
		t.Fatalf("book PlaybackInfo = (%d) %s", rec.Code, rec.Body.String())
	}
	if planCalls.Load() != 0 {
		t.Fatalf("book PlaybackInfo invoked Sessions.Plan %d times, want 0", planCalls.Load())
	}

	// 2. PlaybackInfo on audio-only FLAC needing transcode selects StreamHTTP + TranscodingSubProtocol="http".
	trackPath := "/jellyfin/Items/" + ids.RatingKeyGUID(trackKey) + "/PlaybackInfo"
	req2 := httptest.NewRequest(http.MethodPost, trackPath, strings.NewReader(`{"IsPlayback":true,"EnableDirectPlay":true,"EnableTranscoding":true}`))
	req2.Header.Set("X-Emby-Token", "grant")
	rec2 := httptest.NewRecorder()
	inst.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("audio PlaybackInfo = (%d) %s", rec2.Code, rec2.Body.String())
	}
	var pbResp struct {
		MediaSources []map[string]any `json:"MediaSources"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &pbResp); err != nil {
		t.Fatal(err)
	}
	if len(pbResp.MediaSources) != 1 {
		t.Fatalf("MediaSources = %+v", pbResp.MediaSources)
	}
	ms := pbResp.MediaSources[0]
	if startedFormat != frontend.StreamHTTP {
		t.Fatalf("startedFormat = %v, want http", startedFormat)
	}
	if ms["TranscodingSubProtocol"] != "http" || !strings.Contains(ms["TranscodingUrl"].(string), "/stream.mp3") {
		t.Fatalf("audio MediaSource = %+v", ms)
	}

	// 3. Standalone GET /Audio/{id}/universal streams audio without prior PlaybackInfo!
	univPath := "/jellyfin/Audio/" + ids.RatingKeyGUID(trackKey) + "/universal?api_key=grant&DeviceId=finamp-1&transcodingContainer=mp3&audioCodec=mp3&maxStreamingBitrate=192000"
	uReq := httptest.NewRequest(http.MethodGet, univPath, nil)
	uRec := httptest.NewRecorder()
	inst.Handler().ServeHTTP(uRec, uReq)
	if uRec.Code != http.StatusOK || uRec.Header().Get("Content-Type") != "audio/mpeg" || uRec.Body.String() != "ID3-mp3-audio" {
		t.Fatalf("GET /Audio/{id}/universal = (%d, %q, %q)", uRec.Code, uRec.Header().Get("Content-Type"), uRec.Body.String())
	}

	// 4. userDataDTO projects Reading.Fraction into PlayedPercentage (0..100).
	ud := (&instance{}).userDataDTO(ids.RatingKeyGUID(bookKey), frontend.WatchStatus{
		Reading: &core.ReadingProgress{Locator: "epubcfi(/6/4)", Fraction: 0.42},
	})
	if pct, ok := ud["PlayedPercentage"].(float64); !ok || pct < 41.9 || pct > 42.1 {
		t.Fatalf("userDataDTO PlayedPercentage = %v, want 42", ud["PlayedPercentage"])
	}
}
