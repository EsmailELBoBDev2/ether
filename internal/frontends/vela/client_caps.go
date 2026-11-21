package vela

import (
	"net/http"
	"strings"

	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/frontend/kit/caps"
)

// Detail and Play build the same declaration with current network and owner rules.
func (i *instance) clientCapabilities(r *http.Request, v *frontend.Viewer, c client) (frontend.CapabilityResult, error) {
	audioCodecs := strings.Join(c.AudioCodecs, ",")
	audioContainers := velaAudioDirectContainers(c.Containers, c.AudioCodecs)
	profile := caps.DeviceProfile{
		MaxStreamingBitrate: c.MaxBitrate,
		DirectPlayProfiles: []caps.DirectPlayProfile{
			{Type: "Video", Container: strings.Join(c.Containers, ","), VideoCodec: strings.Join(c.VideoCodecs, ","), AudioCodec: audioCodecs},
			{Type: "Audio", Container: audioContainers, AudioCodec: audioCodecs},
		},
	}
	audioTranscodeCodecs := audioCodecs
	if audioTranscodeCodecs == "" {
		audioTranscodeCodecs = "aac,mp3,opus"
	}
	profile.TranscodingProfiles = append(profile.TranscodingProfiles, caps.TranscodingProfile{
		Type:       "Audio",
		Protocol:   "http",
		Container:  "mp4,aac,mp3,ogg,webm",
		AudioCodec: audioTranscodeCodecs,
	})
	if c.HLS || c.MSE {
		container := "mp4"
		profile.TranscodingProfiles = append(profile.TranscodingProfiles,
			caps.TranscodingProfile{Type: "Video", Protocol: "hls", Container: container, VideoCodec: strings.Join(c.VideoCodecs, ","), AudioCodec: audioCodecs},
			caps.TranscodingProfile{Type: "Audio", Protocol: "hls", Container: "mp4,ts", AudioCodec: audioTranscodeCodecs},
		)
	}
	for _, format := range c.SubtitleFormats {
		if (c.HLS || c.MSE) && format == "webvtt" {
			profile.SubtitleProfiles = append(profile.SubtitleProfiles, caps.SubtitleProfile{Format: "ether-vtt-window", Method: "External"})
		}
		profile.SubtitleProfiles = append(profile.SubtitleProfiles, caps.SubtitleProfile{Format: format, Method: "External"})
	}
	hdr, dv := false, false
	for _, format := range c.HDR {
		switch format {
		case "hdr10", "hlg":
			hdr = true
		case "dv":
			dv = true
		}
	}
	result, err := i.host.Capabilities.Build(i.identityContext(r), frontend.CapabilityRequest{Viewer: v, Device: caps.DeviceInfo{Protocol: "vela", Product: "Vela", Platform: c.Device.Platform, Version: c.Device.AppVersion, ClientID: c.Device.ID, DeviceName: c.Device.Name, HDR10: hdr, DolbyVision: dv, VideoCodecs: c.VideoCodecs, AudioCodecs: c.AudioCodecs, Containers: c.Containers, SubtitleCodecs: c.SubtitleFormats, MaxHeight: c.MaxHeight, MaxBitrateKbps: c.MaxBitrate / 1000}, Profile: profile, UserAgent: r.UserAgent()})
	// Preserve exact declarations, after owner rules have denied any ranges.
	for _, format := range c.HDR {
		if ((format == "hdr10" || format == "hlg") && result.Client.HDR10) || (format == "dv" && result.Client.DolbyVision) {
			result.Client.HDRFormats = append(result.Client.HDRFormats, format)
		}
	}
	return result, err
}

func velaAudioDirectContainers(containers, audioCodecs []string) string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, c := range containers {
		add(c)
		if strings.EqualFold(strings.TrimSpace(c), "mp4") {
			add("m4a")
			add("m4b")
		}
	}
	for _, codec := range audioCodecs {
		switch strings.ToLower(strings.TrimSpace(codec)) {
		case "mp3":
			add("mp3")
		case "flac":
			add("flac")
		case "aac":
			add("aac")
			add("mp4")
			add("m4a")
			add("m4b")
		case "opus", "vorbis":
			add("ogg")
			add("opus")
			add("webm")
		case "alac":
			add("mp4")
			add("m4a")
			add("m4b")
		case "pcm", "pcm_s16le", "wav":
			add("wav")
		}
	}
	return strings.Join(out, ",")
}
