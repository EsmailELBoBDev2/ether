package plex

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/frontend"
)

const (
	// bedrockPlayProbeWait matches plezyPlayProbeWait: explicit play intent
	// waits bounded on the shared foreground resolve+probe flight so the
	// pre-play queue/metadata row carries real probed tracks and the viewer's
	// selected audio/subtitle track before direct play begins (#670).
	bedrockPlayProbeWait = 12 * time.Second
	// bedrockActiveQueueWindow bounds how long a client's active PlayQueue
	// classifies a lean pre-play /library/metadata/<id> fetch as play intent
	// for the selected or upcoming queue item.
	bedrockActiveQueueWindow = 30 * time.Minute
	// bedrockSelectedJoinWindow bounds how long a full metadata fetch for the
	// currently selected queue item joins an in-flight play probe right after
	// POST /playQueues without turning later detail-page browsing into a play
	// probe.
	bedrockSelectedJoinWindow = 30 * time.Second
)

// bedrockPlayProbeClient identifies official Plex mobile, Bedrock TV, and
// smart-TV HTML players that populate direct-play track selection and pickers
// from the pre-play /playQueues or /library/metadata response rather than
// re-fetching metadata after /decision.
func bedrockPlayProbeClient(pr *Request) bool {
	if pr == nil {
		return false
	}
	product := strings.TrimSpace(pr.Product)
	return plexMobileOrTVBedrockClient(pr) || plexSmartTVClient(pr) ||
		strings.EqualFold(product, "Plex for Amazon FireTV") ||
		strings.EqualFold(product, "Plex for Fire TV")
}

// probeBedrockPlay runs or joins the shared foreground resolve+probe flight
// (and concurrent free-source non-Auto version header probes) for an explicit
// Bedrock play-intent request (POST /playQueues, play-queue re-centering, or a
// pre-play /library/metadata fetch for an item in the client's active queue).
// It returns true when handled via PlayMetadataProber (or exact probed tracks
// are already available), and false when the playback backend does not
// implement PlayMetadataProber so callers can fall back to warmDetail.
func (s *Server) probeBedrockPlay(ctx context.Context, pr *Request, t *frontend.Title) bool {
	if pr == nil || pr.User == nil || t == nil || !isPlayable(t.Type) || t.Key.IsPluginKey() || s.playback() == nil || libraryToolClient(pr) {
		return false
	}
	_, _, exact := s.mediaInfoWithSource(ctx, pr, t)
	prober, ok := s.playback().(frontend.PlayMetadataProber)
	if !exact && !ok {
		return false
	}
	var autoProbe func(context.Context) error
	if !exact {
		caps := s.clientCapsForKey(pr, t.Key)
		autoProbe = func(c context.Context) error {
			return prober.ProbePlayMetadata(c, pr.User, t.Key, caps)
		}
	}
	s.runPlayIntentProbes(ctx, pr, t, exact, bedrockPlayProbeWait, plezyVersionProbeWait, autoProbe)
	return true
}

// bedrockFirstPlay handles a Bedrock player's pre-play /library/metadata/<id>
// fetch for the selected item of an active PlayQueue (joining any in-flight
// play probe started by POST /playQueues) or a lean pre-play fetch for an
// upcoming item in that queue (e.g. advancing S01E01 -> S01E02 without a new
// POST /playQueues, #670). Ordinary detail browsing returns false immediately
// without starting a foreground probe.
func (s *Server) bedrockFirstPlay(ctx, reqCtx context.Context, pr *Request, t *frontend.Title, q url.Values) bool {
	if pr == nil || pr.User == nil || t == nil || !bedrockPlayProbeClient(pr) || libraryToolClient(pr) || t.Key.IsPluginKey() || s.queues == nil {
		return false
	}
	if _, _, exact := s.mediaInfoWithSource(ctx, pr, t); exact {
		return false
	}
	detailPage := metadataOptionOn(q.Get("includeRelated")) || metadataOptionOn(q.Get("includeExtras"))
	maxAge := bedrockActiveQueueWindow
	if detailPage {
		maxAge = bedrockSelectedJoinWindow
	}
	role := s.queues.ActiveQueueRole(pr.User.ID, pr.ClientID, t.Key, maxAge)
	if role == 0 || (role == 2 && detailPage) {
		return false
	}
	if reqCtx == nil {
		reqCtx = ctx
	}
	return s.probeBedrockPlay(reqCtx, pr, t)
}
