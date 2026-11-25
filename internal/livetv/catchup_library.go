package livetv

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/frontend"
)

func (s *Service) catchUpChannels(ctx context.Context, u *core.User, first bool) (context.Context, []Channel, error) {
	ctx, pol, err := s.withPolicy(ctx, u)
	if err != nil || pol.Disabled {
		return ctx, nil, err
	}
	_, snap, err := s.channelSnapshot(ctx)
	if err != nil {
		return ctx, nil, err
	}
	snap.mu.Lock()
	archived := snap.archived
	snap.mu.Unlock()
	var out []Channel
	for _, ch := range archived {
		if s.allowedWithLookup(ctx, u, ch, nil, nil, false) {
			out = append(out, ch)
			if first {
				break
			}
		}
	}
	return ctx, out, nil
}

func (h liveTVHost) CatchUpLibraryVisible(ctx context.Context, u *frontend.Viewer) (bool, error) {
	_, channels, err := h.s.catchUpChannels(ctx, u, true)
	return len(channels) > 0, err
}

func catchUpLibraryItem(p Programme, kind frontend.MediaType) frontend.CatchUpItem {
	out := frontend.CatchUpItem{
		Type: kind, Programme: toFrontendProgramme(p),
		ShowID:   frontend.CatchUpGroupID(frontend.TypeShow, p.Item.Title),
		SeasonID: frontend.CatchUpGroupID(frontend.TypeSeason, p.Item.Title),
	}
	switch kind {
	case frontend.TypeShow:
		out.ID = out.ShowID
	case frontend.TypeSeason:
		out.ID = out.SeasonID
	case frontend.TypeEpisode:
		out.ID = frontend.CatchUpEpisodeID(p.ID)
	}
	return out
}

func (h liveTVHost) CatchUpItem(ctx context.Context, u *frontend.Viewer, id string) (frontend.CatchUpItem, error) {
	s := h.s
	if req, ok := frontend.CatchUpEpisodeRequest(id); ok {
		ctx, _, err := s.withPolicy(ctx, u)
		if err != nil {
			return frontend.CatchUpItem{}, err
		}
		ch, err := s.Channel(ctx, req.ChannelID)
		if err != nil {
			return frontend.CatchUpItem{}, err
		}
		stamp, _ := strconv.ParseInt(strings.TrimPrefix(req.ProgrammeID, ch.ID+"-"), 10, 64)
		now := s.clockNow()
		if ch.ArchiveDays() <= 0 || time.UnixMilli(stamp).Before(now.Add(-time.Duration(ch.ArchiveDays())*24*time.Hour)) ||
			!s.allowedWithLookup(ctx, u, ch, nil, nil, false) {
			return frontend.CatchUpItem{}, core.ErrNotFound
		}
		p, found, err := s.programmeAt(ctx, ch, time.UnixMilli(stamp))
		if err != nil {
			return frontend.CatchUpItem{}, err
		}
		eligible, _ := frontend.ArchiveFlags(ch.ArchiveDays(), p.Start, p.End, now)
		if !found || p.ID != req.ProgrammeID || !eligible || !s.allowedProgramme(ctx, u, ch, &p) {
			return frontend.CatchUpItem{}, core.ErrNotFound
		}
		return catchUpLibraryItem(p, frontend.TypeEpisode), nil
	}
	if !strings.HasPrefix(id, "catchup-show-") && !strings.HasPrefix(id, "catchup-season-") {
		return frontend.CatchUpItem{}, core.ErrNotFound
	}
	page, err := h.catchUpItems(ctx, u, frontend.CatchUpQuery{Limit: 1}, id)
	if err != nil {
		return frontend.CatchUpItem{}, err
	}
	if len(page.Items) == 0 {
		return frontend.CatchUpItem{}, core.ErrNotFound
	}
	return page.Items[0], nil
}

func (h liveTVHost) CatchUpItems(ctx context.Context, u *frontend.Viewer, q frontend.CatchUpQuery) (frontend.CatchUpPage, error) {
	return h.catchUpItems(ctx, u, q, "")
}

func (h liveTVHost) catchUpItems(ctx context.Context, u *core.User, q frontend.CatchUpQuery, exact string) (frontend.CatchUpPage, error) {
	var out frontend.CatchUpPage
	ctx, channels, err := h.s.catchUpChannels(ctx, u, false)
	if err != nil || len(channels) == 0 {
		return out, err
	}
	show := strings.HasPrefix(q.Parent, "catchup-show-")
	season := strings.HasPrefix(q.Parent, "catchup-season-")
	if q.Parent != "" && q.Parent != frontend.CatchUpLibraryID && !show && !season {
		return out, nil
	}
	kind := frontend.TypeShow
	switch {
	case q.Recursive || season:
		kind = frontend.TypeEpisode
	case show || strings.HasPrefix(exact, "catchup-season-"):
		kind = frontend.TypeSeason
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, 1000)
	now := h.s.clockNow()
	seen := map[string]bool{}
	// Each batch is a disjoint start-time interval. Process the newest
	// interval first so small pages do not read the entire archive.
	for until := now.Add(time.Millisecond); until.After(now.Add(-30 * 24 * time.Hour)); {
		from := until.Add(-12 * time.Hour)
		rows, err := h.s.catchUpWindow(ctx, u, channels, from, until, now)
		if err != nil {
			return frontend.CatchUpPage{}, err
		}
		for _, p := range rows {
			item := catchUpLibraryItem(p, kind)
			if exact != "" && item.ID != exact ||
				show && item.ShowID != q.Parent ||
				season && item.SeasonID != q.Parent {
				continue
			}
			if kind != frontend.TypeEpisode {
				if seen[item.ID] {
					continue
				}
				seen[item.ID] = true
			}
			if out.Total >= q.Offset && len(out.Items) < limit {
				out.Items = append(out.Items, item)
			}
			out.Total++
			if (exact != "" || kind == frontend.TypeSeason && q.Parent != "") || len(out.Items) == limit && out.Total-q.Offset > limit {
				return out, nil
			}
		}
		until = from
	}
	return out, nil
}

func (s *Service) catchUpWindow(ctx context.Context, u *core.User, channels []Channel, from, until, now time.Time) ([]Programme, error) {
	var out []Programme
	for batch := range slices.Chunk(channels, 100) {
		values := make([]string, 0, len(batch))
		args := make([]any, 0, 4*len(batch)+2)
		byID := make(map[string]Channel, len(batch))
		for _, ch := range batch {
			earliest := now.Add(-time.Duration(ch.ArchiveDays()) * 24 * time.Hour)
			if ch.GuideID == "" || !earliest.Before(until) {
				continue
			}
			values = append(values, "(CAST(? AS TEXT),CAST(? AS TEXT),CAST(? AS TEXT),CAST(? AS BIGINT))")
			args = append(args, ch.ID, guideSource(ch), ch.GuideID, max(from.UnixMilli(), earliest.UnixMilli()))
			byID[ch.ID] = ch
		}
		if len(values) == 0 {
			continue
		}
		args = append(args, until.UnixMilli(), now.UnixMilli())
		rows, err := s.o.DB.QueryContext(ctx, `WITH archive(id, source, guide, earliest) AS (VALUES `+strings.Join(values, ",")+`)
			SELECT archive.id, p.data FROM archive JOIN livetv_programmes p
			ON p.source_id=archive.source AND p.channel_id=archive.guide AND p.start_ms>=archive.earliest
			WHERE p.start_ms<? AND p.end_ms<=? AND p.end_ms>p.start_ms`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			var raw []byte
			var p Programme
			if err = rows.Scan(&id, &raw); err == nil {
				err = json.Unmarshal(raw, &p)
			}
			if err != nil {
				break
			}
			ch := byID[id]
			eligible, _ := frontend.ArchiveFlags(ch.ArchiveDays(), p.Start, p.End, now)
			if !eligible || p.Item.Title == "" {
				continue
			}
			p.ChannelID = id
			p.ID = fmt.Sprintf("%s-%d", id, p.Start.UnixMilli())
			out = append(out, p)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
		// Imported guide rows carry their own rating facts. A catalog-linked
		// row requires the normal policy check and is resolved separately.
		out = slices.DeleteFunc(out, func(p Programme) bool {
			ch, ok := byID[p.ChannelID]
			if !ok {
				return false
			}
			if p.Item.Key != 0 {
				return !s.allowedProgramme(ctx, u, ch, &p)
			}
			return !s.allowedWithLookup(ctx, u, ch, &p, nil, false)
		})
	}
	slices.SortFunc(out, func(a, b Programme) int {
		if n := b.Start.Compare(a.Start); n != 0 {
			return n
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return out, nil
}

func (h liveTVHost) FindCatchUp(ctx context.Context, viewer *frontend.Viewer, req CatchUpRequest, caps frontend.ClientCaps, clientSession string) (CatchUpInfo, bool) {
	if viewer == nil || clientSession == "" {
		return CatchUpInfo{}, false
	}
	h.s.mu.Lock()
	var candidate CatchUpInfo
	for _, ss := range h.s.sessions {
		ss.mu.Lock()
		if !ss.closed && ss.catchUp != nil && ss.info.UserID == viewer.ID &&
			ss.info.ClientSessionID == clientSession && ss.caps.ClientID == caps.ClientID && ss.caps.Protocol == caps.Protocol {
			info := ss.catchUp.info
			if info.ChannelID == req.ChannelID && info.ProgrammeID == req.ProgrammeID && !info.StartOver {
				candidate = info
			}
		}
		ss.mu.Unlock()
		if candidate.ID != "" {
			break
		}
	}
	h.s.mu.Unlock()
	if candidate.ID == "" {
		return CatchUpInfo{}, false
	}
	_, err := h.s.getSession(ctx, candidate.ID, viewer)
	return candidate, err == nil
}
