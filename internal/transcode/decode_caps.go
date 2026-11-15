package transcode

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

// decodeCapabilities is independent of encoder support. Unknown means software
// decode, while keeping the selected hardware encoder.
type decodeCapabilities interface {
	supports(core.HWAccel, *core.StreamInfo) bool
	reject(core.HWAccel, *core.StreamInfo)
}

type decodeKey struct {
	accel, device, codec, profile, pixel string
	depth                                int
}

func sourceDecodeKey(hw core.HWAccel, v *core.StreamInfo) decodeKey {
	device := hw.Device
	if hw.Name == AccelVAAPI && device == "" {
		device = DefaultVAAPIDevice
	}
	return decodeKey{hw.Name, device, strings.ToLower(v.Codec), strings.ToLower(v.Profile), strings.ToLower(v.PixelFormat), v.BitDepth}
}

func (s runSpec) canGPUDecode() bool {
	switch s.Accel.Name {
	case AccelVAAPI, AccelQSV, AccelNVENC, AccelVideoToolbox:
	default:
		return false
	}
	v := s.Info.Video()
	return !s.SoftwareDecode && v != nil && s.Features.decode != nil && s.Features.decode.supports(s.Accel, v)
}

// A cache owns at most one short-lived worker. No work or processes are created
// at startup; first use enqueues a key and immediately uses software decode.
// Queue and cache are bounded independently of the size of the library.
type decodeProbeCache struct {
	mu                   sync.Mutex
	results              map[decodeKey]bool
	pending              map[decodeKey]bool
	queue                []decodeKey
	running              bool
	ctx                  context.Context
	cancel               context.CancelFunc
	wg                   sync.WaitGroup
	runner               Runner
	ffmpeg, ffprobe, dir string
	encoders             map[string]bool
}

func newDecodeProbeCache(r Runner, ffmpeg, ffprobe, dir string, encoders map[string]bool) *decodeProbeCache {
	ctx, cancel := context.WithCancel(context.Background())
	return &decodeProbeCache{results: make(map[decodeKey]bool), pending: make(map[decodeKey]bool), ctx: ctx, cancel: cancel, runner: r, ffmpeg: ffmpeg, ffprobe: ffprobe, dir: dir, encoders: encoders}
}

func (c *decodeProbeCache) supports(hw core.HWAccel, v *core.StreamInfo) bool {
	k := sourceDecodeKey(hw, v)
	c.mu.Lock()
	defer c.mu.Unlock()
	if ok, known := c.results[k]; known {
		return ok
	}
	if c.ctx.Err() != nil || c.pending[k] || len(c.results)+len(c.pending) >= 128 || len(c.queue) >= 16 {
		return false
	}
	c.pending[k] = true
	c.queue = append(c.queue, k)
	if !c.running {
		c.running = true
		c.wg.Add(1)
		go c.work()
	}
	return false
}

func (c *decodeProbeCache) reject(hw core.HWAccel, v *core.StreamInfo) {
	if v == nil {
		return
	}
	c.mu.Lock()
	k := sourceDecodeKey(hw, v)
	if _, exists := c.results[k]; exists || len(c.results)+len(c.pending) < 128 || c.pending[k] {
		c.results[k] = false
	}
	c.mu.Unlock()
}

func (c *decodeProbeCache) close() {
	c.mu.Lock()
	c.cancel()
	c.mu.Unlock()
	c.wg.Wait()
}

func (c *decodeProbeCache) work() {
	defer c.wg.Done()
	for {
		c.mu.Lock()
		if len(c.queue) == 0 || c.ctx.Err() != nil {
			c.running = false
			c.mu.Unlock()
			return
		}
		k := c.queue[0]
		c.queue = c.queue[1:]
		c.mu.Unlock()
		ok := c.probe(k)
		c.mu.Lock()
		// A runtime rejection wins over an in-flight synthetic probe.
		if _, exists := c.results[k]; !exists {
			c.results[k] = ok
		}
		delete(c.pending, k)
		c.mu.Unlock()
	}
}

// Only profiles whose synthetic encoding we can reproduce are admitted. Missing
// profile/pixel facts and unavailable software encoders conservatively stay on
// software decode. In particular, never test 4:2:0 and reuse it for 4:2:2/4:4:4.
func decodeSample(k decodeKey) (encoder, pixel, profile string) {
	depth := k.depth
	if depth == 0 {
		switch k.pixel {
		case "yuv420p", "nv12", "yuvj420p":
			depth = 8
		case "yuv420p10le", "p010le":
			depth = 10
		default:
			return
		}
	}
	if depth == 8 && (k.pixel == "yuv420p" || k.pixel == "nv12" || k.pixel == "yuvj420p") {
		pixel = "yuv420p"
	}
	if depth == 10 && (k.pixel == "yuv420p10le" || k.pixel == "p010le") {
		pixel = "yuv420p10le"
	}
	if pixel == "" {
		return
	}
	switch k.codec {
	case "h264":
		encoder = "libx264"
		switch k.profile {
		case "baseline", "constrained baseline":
			profile = "baseline"
		case "main":
			profile = "main"
		case "high":
			profile = "high"
		case "high 10":
			profile = "high10"
		default:
			return "", "", ""
		}
		if (depth > 8) != (profile == "high10") {
			return "", "", ""
		}
	case "hevc":
		encoder = "libx265"
		if k.profile == "main" && depth == 8 {
			profile = "main"
		} else if k.profile == "main 10" && depth == 10 {
			profile = "main10"
		} else {
			return "", "", ""
		}
	case "vp9":
		encoder = "libvpx-vp9"
		if k.profile == "profile 0" && depth == 8 {
			profile = "0"
		} else if k.profile == "profile 2" && depth == 10 {
			profile = "2"
		} else {
			return "", "", ""
		}
	case "av1":
		if k.profile != "main" {
			return "", "", ""
		}
		encoder = "libaom-av1"
		profile = "0"
	case "mpeg4":
		if depth != 8 {
			return "", "", ""
		}
		encoder = "mpeg4"
		switch k.profile {
		case "simple profile":
			profile = "0"
		case "advanced simple profile":
			profile = "15"
		default:
			return "", "", ""
		}
	case "mpeg2video":
		if depth != 8 {
			return "", "", ""
		}
		encoder = "mpeg2video"
		switch k.profile {
		case "main":
			profile = "4"
		case "simple":
			profile = "5"
		default:
			return "", "", ""
		}
	default:
		return "", "", ""
	}
	return
}

func (c *decodeProbeCache) probe(k decodeKey) bool {
	enc, pixel, profile := decodeSample(k)
	if enc == "" || !c.encoders[enc] {
		return false
	}
	dir, err := os.MkdirTemp(c.dir, "decode-probe-")
	if err != nil {
		return false
	}
	defer os.RemoveAll(dir)
	sample := filepath.Join(dir, "sample.mkv")
	ctx, cancel := context.WithTimeout(c.ctx, 8*time.Second)
	defer cancel()
	run := func(args []string) bool {
		var stderr bytes.Buffer
		err := c.runner.Run(ctx, Command{Path: c.ffmpeg, Args: append([]string{"-hide_banner", "-nostdin", "-loglevel", "error", "-xerror", "-threads", "1", "-filter_threads", "1"}, args...), Stderr: &stderr, Background: true})
		return err == nil && strings.TrimSpace(stderr.String()) == ""
	}
	args := []string{"-f", "lavfi", "-i", "color=size=128x128:rate=25,format=" + pixel, "-frames:v", "2", "-an", "-c:v", enc, "-threads", "1", "-pix_fmt", pixel, "-profile:v", profile}
	switch enc {
	case "libx264":
		args = append(args, "-preset", "ultrafast")
		if profile == "main" {
			args = append(args, "-x264-params", "cabac=1")
		}
		if profile == "high" || profile == "high10" {
			args = append(args, "-x264-params", "cabac=1:8x8dct=1")
		}
	case "libx265":
		args = append(args, "-preset", "ultrafast", "-x265-params", "pools=none:frame-threads=1:log-level=error")
	case "libaom-av1":
		args = append(args, "-cpu-used", "8")
	case "libvpx-vp9":
		args = append(args, "-deadline", "realtime", "-cpu-used", "8")
	}
	if !run(append(args, "-y", sample)) {
		return false
	}
	// Verify what the software encoder actually emitted; -profile is often a
	// ceiling (x264 can emit Baseline when asked for High).
	var facts bytes.Buffer
	if err := c.runner.Run(ctx, Command{Path: c.ffprobe, Args: []string{"-v", "error", "-select_streams", "v:0", "-show_entries", "stream=codec_name,profile,pix_fmt", "-of", "json", sample}, Stdout: &facts, Background: true}); err != nil {
		return false
	}
	var generated struct {
		Streams []struct {
			Codec   string `json:"codec_name"`
			Profile string `json:"profile"`
			Pixel   string `json:"pix_fmt"`
		} `json:"streams"`
	}
	if json.Unmarshal(facts.Bytes(), &generated) != nil || len(generated.Streams) != 1 {
		return false
	}
	actual := generated.Streams[0]
	expectedProfile := k.profile
	if expectedProfile == "baseline" {
		expectedProfile = "constrained baseline"
	}
	if actual.Codec != k.codec || strings.ToLower(actual.Profile) != expectedProfile || actual.Pixel != pixel {
		return false
	}
	accel, frame := k.accel, k.accel
	switch k.accel {
	case AccelNVENC:
		accel, frame = "cuda", "cuda"
	case AccelVideoToolbox:
		frame = "videotoolbox_vld"
	case AccelVAAPI, AccelQSV:
	default:
		return false
	}
	args = []string{"-hwaccel", accel, "-hwaccel_output_format", frame}
	if k.device != "" {
		args = append(args, "-hwaccel_device", k.device)
	}
	download := "nv12"
	if pixel == "yuv420p10le" {
		download = "p010le"
	}
	// hwdownload requires actual hardware frames: a successful software fallback
	// into the null muxer alone would otherwise falsely report support.
	args = append(args, "-i", sample, "-frames:v", "2", "-an", "-vf", "hwdownload,format="+download, "-f", "null", "-")
	return run(args)
}

func decodeInitFailure(stderr string, spec runSpec) bool {
	if spec.SoftwareDecode || !spec.usesHW() {
		return false
	}
	l := strings.ToLower(stderr)
	for _, marker := range []string{"no support for codec", "failed setup for format", "impossible to convert between the formats", "failed to initialise vaapi", "failed to initialize vaapi", "hardware accelerator failed to decode", "failed to get number of decode surfaces"} {
		if strings.Contains(l, marker) {
			return true
		}
	}
	return false
}
