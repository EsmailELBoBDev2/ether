package vela

import (
	"net/http"

	"github.com/braedonsaunders/ether/internal/frontend"
)

// pageLoudness looks up measured loudness for the page's audio titles in one
// batch; other titles never ask.
func (i *instance) pageLoudness(r *http.Request, ts []frontend.Title) map[frontend.RatingKey]frontend.Loudness {
	if i.host.Loudness == nil {
		return nil
	}
	var refs []frontend.LoudnessRef
	for _, t := range ts {
		switch t.MediaClass {
		case frontend.MediaClassMusic, frontend.MediaClassAudiobook, frontend.MediaClassPodcast:
			refs = append(refs, frontend.LoudnessRef{Key: t.Key, Parent: t.ParentKey})
		}
	}
	if len(refs) == 0 {
		return nil
	}
	return i.host.Loudness.PageLoudness(r.Context(), refs)
}

// loudnessWire is media.loudness / PlaybackPlan.loudness.
func loudnessWire(l frontend.Loudness) object {
	out := object{
		"trackGainDb": l.TrackGainDB,
		"reference":   -18,
		"source":      l.Source,
	}
	if l.Source == frontend.LoudnessMeasured {
		out["trackPeak"] = l.TrackPeak
		out["lufs"] = l.LUFS
	}
	if l.AlbumGainDB != nil {
		out["albumGainDb"] = *l.AlbumGainDB
	}
	if l.AlbumPeak != nil {
		out["albumPeak"] = *l.AlbumPeak
	}
	if l.Trim != nil {
		out["trim"] = object{
			"startMs":        l.Trim.StartMs,
			"endMs":          l.Trim.EndMs,
			"fadeInEndMs":    l.Trim.FadeInEndMs,
			"fadeOutStartMs": l.Trim.FadeOutStartMs,
		}
	}
	return out
}
