package mediabrowser

import (
	"net/http"

	"github.com/braedonsaunders/ether/internal/frontend"
)

// jellyfinMediaSourceDTO adds MediaSourceInfo fields Jellyfin always serializes.
// VideoType is withheld for estimates, audio-only sources and live streams.
func (i *instance) jellyfinMediaSourceDTO(source map[string]any, media frontend.MediaInfo, live bool) {
	source["UseMostCompatibleTranscodingProfile"] = false
	if !media.Estimated {
		jellyfinDefaultStreamIndexes(source, media)
	}
	if live || media.Estimated || media.Video() == nil {
		return
	}
	source["VideoType"] = "VideoFile"
}

// jellyfinViewerStreamDefaults replaces the container's default flags on an
// item detail source with the tracks this viewer's language rules select.
// Jellyfin computes DefaultAudioStreamIndex and DefaultSubtitleStreamIndex per
// user; its web client copies the detail's values into the play request, so a
// container-only -1 was sent back as an explicit "subtitles off" even when the
// viewer's subtitle mode would have picked a track.
func (i *instance) jellyfinViewerStreamDefaults(r *http.Request, v *frontend.Viewer, key frontend.RatingKey, original string, source map[string]any, media frontend.MediaInfo) {
	if i.protocol != "jellyfin" || i.host.TrackPreferences == nil || r == nil || v == nil || media.Estimated || len(media.Streams) == 0 {
		return
	}
	selection, err := i.host.TrackPreferences.Select(r.Context(), v.ID, key, media, original)
	if err != nil {
		return
	}
	if selection.Audio >= 0 && selection.Audio < len(media.Streams) && media.Streams[selection.Audio].Type == "audio" {
		source["DefaultAudioStreamIndex"] = media.Streams[selection.Audio].Index
	}
	if selection.Subtitle >= 0 && selection.Subtitle < len(media.Streams) && media.Streams[selection.Subtitle].Type == "subtitle" {
		source["DefaultSubtitleStreamIndex"] = media.Streams[selection.Subtitle].Index
		return
	}
	for _, stream := range media.Streams {
		if stream.Type == "subtitle" {
			source["DefaultSubtitleStreamIndex"] = -1
			return
		}
	}
}

// jellyfinDefaultStreamIndexes follows Jellyfin when the container does not
// mark a default: the first audio stream, and -1 for subtitles so none is
// selected. A marked default wins, and the earliest mark wins when several
// streams are marked.
func jellyfinDefaultStreamIndexes(source map[string]any, media frontend.MediaInfo) {
	audioIndex, haveAudio, audioDefault := 0, false, false
	subtitleIndex, subtitleDefault, subtitleCount := 0, false, 0
	for _, stream := range media.Streams {
		selected := stream.Default
		if stream.DefaultDisposition != nil {
			selected = *stream.DefaultDisposition
		}
		switch stream.Type {
		case "audio":
			if !haveAudio {
				audioIndex = stream.Index
				haveAudio = true
			}
			if selected && !audioDefault {
				audioIndex = stream.Index
				audioDefault = true
			}
		case "subtitle":
			subtitleCount++
			if selected && !subtitleDefault {
				subtitleIndex = stream.Index
				subtitleDefault = true
			}
		}
	}
	if haveAudio {
		source["DefaultAudioStreamIndex"] = audioIndex
	}
	if subtitleDefault {
		source["DefaultSubtitleStreamIndex"] = subtitleIndex
	} else if subtitleCount > 0 {
		source["DefaultSubtitleStreamIndex"] = -1
	}
}

// applyPlannedStreamIndexes publishes the stream playback actually selected.
// The container default flag is not that choice: a default-flagged dub must
// not stay selected after the language chain picked another track.
func applyPlannedStreamIndexes(source map[string]any, media frontend.MediaInfo, decision frontend.Decision) {
	if decision.AudioStream >= 0 {
		source["DefaultAudioStreamIndex"] = decision.AudioStream
	}
	if decision.SubtitleStream >= 0 {
		source["DefaultSubtitleStreamIndex"] = decision.SubtitleStream
		return
	}
	for _, stream := range media.Streams {
		if stream.Type == "subtitle" {
			source["DefaultSubtitleStreamIndex"] = -1
			return
		}
	}
}

// jellyfinSideSubtitleDefault restores the subtitle choice on a PlaybackInfo
// source when the track is rendered beside the video. The plan clears a text
// track the client fetches itself (the progressive web reader, an HLS
// rendition) so the video is never asked to burn it in; publishing that -1
// told Jellyfin Web the viewer wanted subtitles off. An explicit -1 from the
// client stays off; with no choice in the request the viewer's rules pick.
func (i *instance) jellyfinSideSubtitleDefault(r *http.Request, v *frontend.Viewer, key frontend.RatingKey, source map[string]any, media frontend.MediaInfo, asked int, askedSet bool) {
	if current, ok := source["DefaultSubtitleStreamIndex"].(int); ok && current >= 0 {
		return
	}
	if media.Estimated || (askedSet && asked < 0) {
		return
	}
	want := -1
	if askedSet {
		want = asked
	} else if i.host.TrackPreferences != nil && r != nil && v != nil {
		if selection, err := i.host.TrackPreferences.Select(r.Context(), v.ID, key, media, ""); err == nil && selection.Subtitle >= 0 && selection.Subtitle < len(media.Streams) {
			want = media.Streams[selection.Subtitle].Index
		}
	}
	if want < 0 {
		return
	}
	for _, stream := range media.Streams {
		if stream.Type == "subtitle" && stream.Index == want && frontend.IsTextSubtitle(stream.Codec) {
			source["DefaultSubtitleStreamIndex"] = want
			return
		}
	}
}

// jellyfinResumeSubtitle records the lab viewer's in-progress choice. Jellyfin
// stores -1 on a partially watched item when no subtitle stream is selected,
// and omits the field once the item is unplayed or finished.
func jellyfinResumeSubtitle(item map[string]any) {
	data, _ := item["UserData"].(map[string]any)
	if data == nil {
		return
	}
	if played, _ := data["Played"].(bool); played {
		return
	}
	ticks, ok := data["PlaybackPositionTicks"].(int64)
	if !ok {
		if wide, wideOK := data["PlaybackPositionTicks"].(float64); wideOK {
			ticks, ok = int64(wide), wide != 0
		}
	}
	if !ok || ticks <= 0 {
		return
	}
	sources, _ := item["MediaSources"].([]any)
	for _, source := range sources {
		dto, ok := source.(map[string]any)
		if !ok {
			continue
		}
		if _, exists := dto["DefaultSubtitleStreamIndex"]; !exists {
			dto["DefaultSubtitleStreamIndex"] = -1
		}
	}
}
