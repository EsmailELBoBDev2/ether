package vela

import (
	"errors"
	"fmt"

	"github.com/braedonsaunders/ether/internal/frontend"
	"net/http"
	"strings"
	"time"
)

func (i *instance) channels(w http.ResponseWriter, r *http.Request) {
	v, token, e := i.viewer(r)
	if e != nil {
		fail(w, e, r)
		return
	}
	if i.host.LiveTV == nil {
		i.unavailable(w, r)
		return
	}
	scope, e := i.livePageScope(r, v, token)
	if e != nil {
		fail(w, e, r)
		return
	}
	cursor, e := i.readLiveCursor(r, scope, hashJSON([]any{"channels", limit(r)}))
	if e != nil {
		fail(w, e, r)
		return
	}
	channels, e := i.host.LiveTV.Channels(r.Context(), v)
	if e != nil {
		fail(w, e, r)
		return
	}
	out := []object{}
	snapshot := hashJSON(channels)
	if cursor.Snapshot != "" && cursor.Snapshot != snapshot {
		fail(w, frontend.ErrLiveGuideStale)
		return
	}
	start := min(cursor.Offset, len(channels))
	end := start + min(limit(r), len(channels)-start)
	memo := newLiveArtCache()
	for _, c := range channels[start:end] {
		art, err := i.liveArtworkCached(r, token, 0, c.ID, c.Images, memo)
		if err != nil {
			fail(w, err, r)
			return
		}
		channel := object{"id": c.ID, "number": c.Number, "name": c.Name, "kind": c.Kind, "artwork": art, "favorite": c.Favorite, "rating": c.Rating}
		if c.Groups != nil {
			// The Vela protocol names an owner's channel groups "guides"
			// (Channel.guides, LiveGuide); Vela builds its guide tabs from it.
			guides := []object{}
			for _, group := range c.Groups {
				guides = append(guides, object{"id": group.ID, "name": group.Name})
			}
			channel["guides"] = guides
		}
		if c.Group != "" {
			channel["group"] = c.Group
		}
		out = append(out, channel)
	}
	page := object{"items": out, "continuation": "exhausted"}
	if end < len(channels) {
		next, err := i.nextLiveCursor(cursor, end, snapshot)
		if err != nil {
			fail(w, err, r)
			return
		}
		page["continuation"], page["nextCursor"] = "more", next
	}
	cache(w, r, page)
}
func (i *instance) guide(w http.ResponseWriter, r *http.Request) {
	v, token, e := i.viewer(r)
	if e != nil {
		fail(w, e, r)
		return
	}
	if i.host.LiveTV == nil {
		i.unavailable(w, r)
		return
	}
	q := r.URL.Query()
	from, e1 := time.Parse(time.RFC3339, q.Get("from"))
	until, e2 := time.Parse(time.RFC3339, q.Get("until"))
	if e1 != nil || e2 != nil || !until.After(from) || until.Sub(from) > 24*time.Hour {
		problem(w, 400, "request", "invalid_guide_window")
		return
	}
	channels := []string{}
	if s := q.Get("channelIds"); s != "" {
		channels = strings.Split(s, ",")
	}
	if len(channels) > 100 {
		problem(w, 400, "request", "too_many_channels")
		return
	}
	scope, e := i.livePageScope(r, v, token)
	if e != nil {
		fail(w, e, r)
		return
	}
	cursor, e := i.readLiveCursor(r, scope, hashJSON([]any{"guide", from.UTC(), until.UTC(), channels, limit(r)}))
	if e != nil {
		fail(w, e, r)
		return
	}
	pager, ok := i.host.LiveTV.(frontend.LiveGuidePager)
	if !ok {
		i.unavailable(w, r)
		return
	}
	programmes, e := pager.GuidePage(i.host.Experience.NoProviderWait(r.Context()), v, frontend.LiveGuidePageQuery{LiveGuideQuery: frontend.LiveGuideQuery{Channels: channels, From: from, Until: until, Limit: limit(r), Device: requestDevice(r)}, Offset: cursor.Offset, Snapshot: cursor.Snapshot})
	if e != nil {
		fail(w, e, r)
		return
	}
	out := []object{}
	memo := newLiveArtCache()
	for _, p := range programmes.Items {
		art := []object{}
		for _, img := range p.Images {
			item, channel := p.Item, ""
			if img.Subject == p.ID {
				item, channel = 0, p.ChannelID
			}
			images, err := i.liveArtworkCached(r, token, item, channel, []frontend.Image{img}, memo)
			if err != nil {
				fail(w, err, r)
				return
			}
			art = append(art, images...)
		}
		programme := object{"id": p.ID, "channelId": p.ChannelID, "name": p.Title, "summary": p.Summary, "start": p.Start.UTC().Format(time.RFC3339), "end": p.End.UTC().Format(time.RFC3339), "episodeTitle": p.EpisodeTitle, "seasonNumber": p.Season, "episodeNumber": p.Episode, "artwork": art, "rating": p.Rating}
		if len(p.Genres) > 0 {
			programme["genres"] = p.Genres
		}
		out = append(out, programme)
	}
	page := object{"items": out, "from": from.UTC().Format(time.RFC3339), "until": until.UTC().Format(time.RFC3339), "continuation": "exhausted"}
	if programmes.More {
		if len(out) == 0 || programmes.Snapshot == "" {
			fail(w, frontend.ErrLiveGuideStale)
			return
		}
		next, err := i.nextLiveCursor(cursor, cursor.Offset+len(out), programmes.Snapshot)
		if err != nil {
			fail(w, err, r)
			return
		}
		page["continuation"], page["nextCursor"] = "more", next
	}
	cache(w, r, page)
}

// Issue bounded, current-policy image capabilities for this response page.
// Source URLs and credentials never enter the Vela wire model.
//
// One response reuses a grant for an image it already issued (a channel's
// logo repeats on every programme), and a viewer whose image admission is
// full gets the page without the remaining artwork rather than no guide.
func (i *instance) liveArtwork(r *http.Request, token string, item frontend.RatingKey, channel string, images []frontend.Image) ([]object, error) {
	return i.liveArtworkCached(r, token, item, channel, images, nil)
}

type liveArtCache struct {
	issued map[string]object
	full   bool
}

func newLiveArtCache() *liveArtCache { return &liveArtCache{issued: map[string]object{}} }

func (i *instance) liveArtworkCached(r *http.Request, token string, item frontend.RatingKey, channel string, images []frontend.Image, memo *liveArtCache) ([]object, error) {
	out := []object{}
	if i.host.ImageGrants == nil || (item == 0 && channel == "") {
		return out, nil
	}
	for _, img := range images {
		key := fmt.Sprintf("%d\x00%s\x00%s\x00%s\x00%s", item, channel, img.Kind, img.Path, img.Tag)
		if memo != nil {
			if art, ok := memo.issued[key]; ok {
				out = append(out, art)
				continue
			}
			if memo.full {
				continue
			}
		}
		secret, err := i.host.ImageGrants.Issue(r.Context(), frontend.ImageGrantRequest{Credential: token, Device: requestDevice(r), Item: item, Channel: channel, Image: img})
		if err != nil {
			if memo != nil && errors.Is(err, frontend.ErrImageCapacity) {
				memo.full = true
				continue
			}
			return nil, err
		}
		art := object{"id": hash(img.Path), "kind": img.Kind, "tag": img.Tag, "sizes": []object{imageSize(img, secret)}}
		if memo != nil {
			memo.issued[key] = art
		}
		out = append(out, art)
	}
	return out, nil
}
