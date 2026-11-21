package plex

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/frontends/plex/model"
)

// Plezy (a Flutter/mpv Plex client) builds its track selection from the
// /library/metadata row it already holds: getVideoPlaybackData reuses a
// fresh (5 minute) cached row whenever every Part carries Stream, exists
// and accessible, and its subtitle selection trusts only the server's
// selected stream. A cold episode's detail row is estimated — invented
// audio, no subtitles — so first play started with subtitles off, and the
// real tracks the playback probe found seconds later were never re-read
// (#618). Two changes, both scoped to Plezy:
//
//   - A pre-probe (estimated) Part does not claim completeness: file
//     availability leaves exists/accessible unset for it, exactly like
//     PMS without a file check, so Plezy rejects its cached row at Play
//     and re-fetches. Missing flags still read as playable in Plezy
//     (isPlayable is accessible != false && exists != false).
//   - That re-fetch is play intent. The detail-screen fetch and the
//     play-time fetch are identical on the wire (same endpoint, same
//     includeChapters/includeMarkers/checkFiles/includeStreams), so the
//     only reliable signal is state: this client was already served an
//     incomplete row for this title and is asking again. Then the
//     request waits, bounded, on the shared resolve+probe flight — the
//     same warm entry the following file request opens — and answers
//     with the real streams and the viewer's rule-selected subtitle.
//
// The first (browsing) fetch never waits and starts nothing beyond the
// detail warm-up scheduleDetailWarm already runs for every client.

const (
	// plezyPlayProbeWait bounds the play-time re-fetch's wait for the
	// shared resolve+probe flight. Cold remux resolves routinely take
	// 4-6 s; moving that wait onto the metadata re-ask costs no time to
	// first frame because the following file.mkv joins the same flight.
	// The wait stays bounded by the request context.
	plezyPlayProbeWait = 12 * time.Second
	// plezyReaskMinAge separates Plezy's detail-open burst (three
	// /library/metadata/<id> fetches within ~1 s while browsing) from
	// the later play-time re-ask. Burst fetches inside this window are
	// browsing and never wait, and the arm survives the burst.
	plezyReaskMinAge = 2500 * time.Millisecond
	// plezyReaskTTL is how long a served incomplete row arms the
	// play-intent re-fetch. It comfortably covers Plezy's 5-minute
	// fresh-cache window.
	plezyReaskTTL     = 15 * time.Minute
	plezyReaskMaxRows = 512
)

// plezyClient identifies Plezy by its X-Plex-Product. The change is
// scoped to it: other clients re-read metadata around playback, or are
// already served a probed detail page (Bedrock), and keep today's
// wire shape untouched.
func plezyClient(pr *Request) bool {
	return pr != nil && strings.EqualFold(strings.TrimSpace(pr.Product), "Plezy")
}

type plezyReaskKey struct {
	userID   int64
	clientID string
	key      frontend.RatingKey
}

// plezyReaskStore remembers which Plezy clients were served an
// incomplete (pre-probe) detail row, so their later identical fetch at
// Play can be recognised as the play-time re-ask without catching the
// detail screen's initial burst.
type plezyReaskStore struct {
	mu   sync.Mutex
	rows map[plezyReaskKey]time.Time
}

// checkOrArm records an arming fetch for k when none is active, or
// classifies a follow-up fetch against the existing arm. It returns
// true only when k was armed at least plezyReaskMinAge ago (and within
// plezyReaskTTL): that follow-up is the play-time re-ask. Burst fetches
// inside plezyReaskMinAge return false without consuming or resetting
// the arm, so the arm survives the detail-open burst. An armed entry
// stays armed until it expires or forget is called, so a re-ask whose
// probe misses the wait budget still arms the next retry.
func (st *plezyReaskStore) checkOrArm(k plezyReaskKey, now time.Time) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.rows == nil {
		st.rows = map[plezyReaskKey]time.Time{}
	}
	if at, ok := st.rows[k]; ok {
		age := now.Sub(at)
		if age >= 0 && age <= plezyReaskTTL {
			return age >= plezyReaskMinAge
		}
		delete(st.rows, k)
	}
	if len(st.rows) >= plezyReaskMaxRows {
		for row, at := range st.rows {
			if age := now.Sub(at); age < 0 || age > plezyReaskTTL {
				delete(st.rows, row)
			}
		}
		if len(st.rows) >= plezyReaskMaxRows {
			return false // full of fresh rows: skip arming rather than grow
		}
	}
	st.rows[k] = now
	return false
}

func (st *plezyReaskStore) forget(k plezyReaskKey) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.rows, k)
}

// plezyFirstPlay runs on a single-item metadata read for a playable
// title. Nothing happens unless the client is Plezy asking with file
// checks (its detail and playback fetches both do) for a title with no
// exact tracks yet. The first fetch arms the title and follow-up burst
// fetches inside plezyReaskMinAge return immediately without waiting or
// disarming; once at least plezyReaskMinAge has elapsed, the re-ask is
// the play tap and waits (bounded by reqCtx and plezyPlayProbeWait) on
// the shared foreground resolve+probe flight.
func (s *Server) plezyFirstPlay(ctx, reqCtx context.Context, pr *Request, t *frontend.Title, q url.Values) {
	if pr == nil || pr.User == nil || t == nil || !plezyClient(pr) || libraryToolClient(pr) {
		return
	}
	if !metadataOptionOn(q.Get("checkFiles")) && !metadataOptionOn(q.Get("asyncCheckFiles")) && !metadataOptionOn(q.Get("checkFileAvailability")) {
		return
	}
	k := plezyReaskKey{userID: pr.User.ID, clientID: pr.ClientID, key: t.Key}
	if _, _, exact := s.mediaInfoWithSource(ctx, pr, t); exact {
		s.plezyReasks.forget(k)
		return
	}
	if !s.plezyReasks.checkOrArm(k, s.now()) {
		// Initial detail open or follow-up burst fetch inside
		// plezyReaskMinAge: never wait; this response goes out
		// incomplete (see projectFileAvailability) while keeping
		// the arm intact for the play-time re-ask.
		return
	}
	if reqCtx == nil {
		reqCtx = ctx
	}
	// Join (or start) the one foreground resolve+probe flight for this
	// title and wait bounded by the request context and
	// plezyPlayProbeWait. Using reqCtx (without NoProviderWait's
	// LocalResponse) and PlayMetadataProber ensures the play re-ask is
	// never short-circuited by local-only browsing gates, speculation
	// toggles, or a prior speculative warm miss, while publishing the
	// warm entry the following file.mkv request adopts directly.
	probeCtx, cancel := context.WithTimeout(reqCtx, plezyPlayProbeWait)
	defer cancel()
	_ = s.probePlezyPlay(probeCtx, pr, t)
	if _, _, exact := s.mediaInfoWithSource(ctx, pr, t); exact {
		s.plezyReasks.forget(k)
	}
}

func (s *Server) probePlezyPlay(ctx context.Context, pr *Request, t *frontend.Title) error {
	if pr == nil || pr.User == nil || s.playback() == nil {
		return nil
	}
	caps := s.clientCapsForKey(pr, t.Key)
	if prober, ok := s.playback().(frontend.PlayMetadataProber); ok {
		return prober.ProbePlayMetadata(ctx, pr.User, t.Key, caps)
	}
	if prober, ok := s.playback().(frontend.MetadataProber); ok {
		return prober.ProbeMetadata(ctx, pr.User, t.Key, caps)
	}
	return nil
}

// plezyPartProbed reports whether a rendered Part's streams come from a
// real probe rather than the pre-probe estimate. Stream bindings carry
// the estimate flag of the info they were rendered from; when no
// binding resolves (a render without issued streams), the title's
// session/resumed/warm probe decides.
func (s *Server) plezyPartProbed(ctx context.Context, pr *Request, part *model.Part) bool {
	resolved := false
	for _, st := range part.Stream {
		if st.ID == 0 {
			continue
		}
		binding, err := s.resolveIssuedStream(ctx, pr.User, pr.ClientID, st.ID)
		if err != nil {
			continue
		}
		if binding.Estimated {
			return false
		}
		resolved = true
	}
	if resolved {
		return true
	}
	key, _, ok := s.partTitle(part.ID)
	if !ok || pr.User == nil || s.playback() == nil {
		return false
	}
	if ps, ok := s.playback().SessionFor(pr.User.ID, pr.ClientID, key); ok && ps != nil && !ps.Info.Estimated && len(ps.Info.Streams) > 0 {
		return true
	}
	caps := s.clientCapsForKey(pr, key)
	if src, ok := s.playback().(resumedTrackSource); ok {
		if info, releaseID, ok := src.ResumedTracks(ctx, pr.User, key, caps); ok && releaseID != "" && !info.Estimated && len(info.Streams) > 0 {
			return true
		}
	}
	if wp, ok := s.playback().(frontend.WarmProber); ok {
		if _, warm := wp.WarmProbeInfo(pr.User, caps, key); warm {
			return true
		}
	}
	return false
}
