package transcode

import (
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

type fakeDecodeCapabilities struct {
	allow func(core.HWAccel, *core.StreamInfo) bool
}

func (f fakeDecodeCapabilities) supports(hw core.HWAccel, v *core.StreamInfo) bool {
	return f.allow(hw, v)
}
func (fakeDecodeCapabilities) reject(core.HWAccel, *core.StreamInfo) {}

// Each accelerator must independently admit the source's profile/depth/chroma,
// retain its encoder on rejection, and honour the explicit runtime retry flag.
func TestDecodeCapabilityGraphs(t *testing.T) {
	for _, tc := range []struct{ accel, encoder, filter string }{
		{AccelVAAPI, "h264_vaapi", "scale_vaapi="},
		{AccelQSV, "h264_qsv", "vpp_qsv="},
		{AccelNVENC, "h264_nvenc", "scale_cuda="},
		{AccelVideoToolbox, "h264_videotoolbox", "scale_vt="},
	} {
		t.Run(tc.accel, func(t *testing.T) {
			info := mustParse(t, "mp4_h264_aac.json")
			opts := allow()
			opts.MaxHeight = 720
			s := baseSpec(info, Decide(info, appleTV, opts, Policy{}))
			s.Accel = core.HWAccel{Name: tc.accel, Device: "test-device", Encoders: []string{tc.encoder}}
			s.Features.hwaccels = map[string]bool{"cuda": true, "qsv": true, "videotoolbox": true}
			for _, filter := range []string{"scale_cuda", "vpp_qsv", "scale_vt"} {
				s.Features.filters[filter] = true
			}
			allowed := sourceDecodeKey(s.Accel, s.Info.Video())
			s.Features.decode = fakeDecodeCapabilities{allow: func(hw core.HWAccel, v *core.StreamInfo) bool { return sourceDecodeKey(hw, v) == allowed }}
			mustContain(t, buildArgs(s), "-hwaccel_output_format", tc.filter, "-c:v "+tc.encoder)
			original := *s.Info.Video()
			for _, change := range []func(*core.StreamInfo){
				func(v *core.StreamInfo) { v.Codec = "mpeg4"; v.Profile = "Simple Profile" },
				func(v *core.StreamInfo) { v.Profile = "High 10"; v.BitDepth = 10; v.PixelFormat = "yuv420p10le" },
				func(v *core.StreamInfo) { v.PixelFormat = "yuv422p" },
			} {
				*s.Info.Video() = original
				change(s.Info.Video())
				args := buildArgs(s)
				mustNotContain(t, args, "-hwaccel ", "-hwaccel_output_format", tc.filter)
				mustContain(t, args, "-c:v "+tc.encoder, "scale=1280:720")
				if tc.accel == AccelVAAPI {
					mustContain(t, args, "-vaapi_device test-device", "format=nv12,hwupload")
				}
			}
			*s.Info.Video() = original
			s.SoftwareDecode = true
			mustNotContain(t, buildArgs(s), "-hwaccel ", tc.filter)
			mustContain(t, buildArgs(s), "-c:v "+tc.encoder)
			s.SoftwareDecode = false
			s.Features.decode = nil
			mustNotContain(t, buildArgs(s), "-hwaccel ", tc.filter)
		})
	}
}
