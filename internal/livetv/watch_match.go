package livetv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

func init() {
	// Time-leading index so watch-target matching (A5) probes only programmes
	// overlapping an event's window without scanning the whole guide.
	store.Register("livetv", 26, `CREATE INDEX livetv_programmes_time ON livetv_programmes(start_ms, end_ms);`)
}

// Bounds for time-indexed watch candidate queries. Each request inspects at
// most a small fixed slice of programmes overlapping the event window.
const (
	watchPrimaryWindowLimit   = 2000
	watchCarryoverWindowLimit = 500
	watchCarryoverLookback    = 6 * time.Hour
	watchTargetedChannelCap   = 128
)

// WatchGuideEntry is one viewer-visible channel + overlapping guide programme
// (or a callsign-matched channel without guide data) returned by a bounded
// lookup for A5/A6 live-event matching.
type WatchGuideEntry struct {
	ChannelID     string
	ChannelName   string
	ChannelNumber string
	ChannelLogo   string
	TvgName       string
	GuideID       string
	IsEventAddon  bool
	ChannelOnly   bool
	Programme     Programme
	Live          bool
}

// GuideRevision returns a compact, cluster-safe revision token covering the
// guide/channel epoch and the viewer's effective Live TV access policy.
func (s *Service) GuideRevision(ctx context.Context, u *core.User) (string, error) {
	if s == nil || s.o.DB == nil {
		return "none", nil
	}
	if u == nil {
		return "denied", nil
	}
	var gen int64
	var rawPol string
	err := s.o.DB.QueryRowContext(ctx,
		`SELECT generation, COALESCE((SELECT data FROM livetv_policies WHERE user_id=?),'') FROM livetv_channel_epoch WHERE id=1`,
		u.ID,
	).Scan(&gen, &rawPol)
	if err != nil {
		return "", err
	}
	sum := sha256.New()
	_, _ = fmt.Fprintf(sum, "g=%d|u=%d|owner=%t|rat=%s|pol=%s",
		gen, u.ID, u.IsOwner, u.MaxContentRating, rawPol)
	return fmt.Sprintf("g%d:%s", gen, hex.EncodeToString(sum.Sum(nil)[:8])), nil
}

// WatchCandidates returns a bounded set of viewer-visible channel+programme
// pairs whose airing window overlaps [from, until), along with the current
// viewer guide revision for caching.
//
// Cheapest-first bounded lookup:
//  1. Targeted lookup on viewer-visible channels matching callsign hints and
//     0.3.18 Stremio event channels via the (source_id, channel_id, end_ms, start_ms)
//     index (ensures large lineups never drop a broadcast or event channel).
//  2. Bounded time-range lookup via livetv_programmes_time(start_ms, end_ms)
//     covering [from, until) plus recent carry-over programmes overlapping from.
func (s *Service) WatchCandidates(ctx context.Context, u *core.User, from, until time.Time, callsigns []string) ([]WatchGuideEntry, string, error) {
	if s == nil || s.o.DB == nil {
		return nil, "none", nil
	}
	if u == nil {
		return nil, "denied", nil
	}
	if !until.After(from) {
		rev, err := s.GuideRevision(ctx, u)
		return nil, rev, err
	}

	rev, err := s.GuideRevision(ctx, u)
	if err != nil {
		return nil, "", err
	}

	ctx, pol, err := s.withPolicy(ctx, u)
	if err != nil {
		return nil, "", err
	}
	if !u.IsOwner && pol.Disabled {
		return nil, rev, nil
	}

	all, _, err := s.channelSnapshot(ctx)
	if err != nil {
		return nil, "", err
	}

	normCallsigns := normalizeCallsignHints(callsigns)
	byGuideKey := make(map[guideKey][]Channel)
	targeted := make(map[guideKey][]Channel)
	var callsignMatched []Channel
	targetedCount := 0

	for _, c := range all {
		if c.Kind == "virtual" {
			continue
		}
		if !s.audienceAllowed(ctx, u, c) {
			continue
		}
		matchesCallsign := channelMatchesAnyCallsignHint(c, normCallsigns)
		if matchesCallsign && len(callsignMatched) < watchTargetedChannelCap {
			callsignMatched = append(callsignMatched, c)
		}
		if c.GuideID == "" {
			continue
		}
		gk := guideKey{source: guideSource(c), guide: c.GuideID}
		byGuideKey[gk] = append(byGuideKey[gk], c)
		if targetedCount < watchTargetedChannelCap && (isStremioEventChannel(c) || matchesCallsign) {
			if _, exists := targeted[gk]; !exists {
				targetedCount++
			}
			targeted[gk] = append(targeted[gk], c)
		}
	}
	if len(byGuideKey) == 0 && len(callsignMatched) == 0 {
		return nil, rev, nil
	}

	type progDedupKey struct {
		source  string
		guide   string
		startMS int64
	}
	seen := make(map[progDedupKey]struct{})
	channelsWithProg := make(map[string]bool)
	var out []WatchGuideEntry
	now := s.o.Now()

	appendCandidate := func(gk guideKey, p Programme) {
		dk := progDedupKey{source: gk.source, guide: gk.guide, startMS: p.Start.UnixMilli()}
		if _, dup := seen[dk]; dup {
			return
		}
		seen[dk] = struct{}{}
		chans, ok := byGuideKey[gk]
		if !ok {
			return
		}
		live := !now.Before(p.Start) && now.Before(p.End)
		for _, c := range chans {
			// Record that this channel has real guide data in [from, until) even
			// if policy hides the programme, so callsign-only fallback never
			// overrides known EPG schedules or policy-blocked programmes.
			channelsWithProg[c.ID] = true
			cp := p
			cp.ChannelID = c.ID
			if cp.ID == "" {
				cp.ID = fmt.Sprintf("%s-%d", c.ID, cp.Start.UnixMilli())
			}
			if !s.allowedProgramme(ctx, u, c, &cp) {
				continue
			}
			out = append(out, WatchGuideEntry{
				ChannelID:     c.ID,
				ChannelName:   c.Name,
				ChannelNumber: c.Number,
				ChannelLogo:   c.Logo,
				TvgName:       c.TvgName,
				GuideID:       c.GuideID,
				IsEventAddon:  len(cp.EventStreams) > 0 || isStremioEventChannel(c),
				Programme:     cp,
				Live:          live,
			})
		}
	}

	// Step 1: Targeted broadcast callsign + Stremio event channels.
	if len(targeted) > 0 {
		progs, err := s.programmesForKeys(ctx, targeted, from, until)
		if err != nil {
			return nil, "", err
		}
		for gk, ps := range progs {
			for _, p := range ps {
				appendCandidate(gk, p)
			}
		}
	}

	// Step 2a: Primary time-indexed slice [from, until) via livetv_programmes_time.
	if len(byGuideKey) > 0 {
		fromMS := from.UnixMilli()
		untilMS := until.UnixMilli()
		if err := s.scanWatchTimeSlice(ctx,
			`SELECT source_id, channel_id, data FROM livetv_programmes WHERE start_ms>=? AND start_ms<? AND end_ms>? ORDER BY start_ms ASC LIMIT ?`,
			[]any{fromMS, untilMS, fromMS, watchPrimaryWindowLimit},
			byGuideKey,
			appendCandidate,
		); err != nil {
			return nil, "", err
		}

		// Step 2b: Carry-over slice for programmes that started shortly before from
		// and are still airing across from.
		carryFromMS := from.Add(-watchCarryoverLookback).UnixMilli()
		if err := s.scanWatchTimeSlice(ctx,
			`SELECT source_id, channel_id, data FROM livetv_programmes WHERE start_ms>=? AND start_ms<? AND end_ms>? ORDER BY start_ms DESC LIMIT ?`,
			[]any{carryFromMS, fromMS, fromMS, watchCarryoverWindowLimit},
			byGuideKey,
			appendCandidate,
		); err != nil {
			return nil, "", err
		}
	}

	// Step 3: Callsign-only fallback for viewer-visible channels matching an
	// event's broadcast callsign that have no EPG guide data in [from, until)
	// (e.g. IPTV playlists without XMLTV guide data).
	for _, c := range callsignMatched {
		if channelsWithProg[c.ID] {
			continue
		}
		out = append(out, WatchGuideEntry{
			ChannelID:     c.ID,
			ChannelName:   c.Name,
			ChannelNumber: c.Number,
			ChannelLogo:   c.Logo,
			TvgName:       c.TvgName,
			GuideID:       c.GuideID,
			IsEventAddon:  isStremioEventChannel(c),
			ChannelOnly:   true,
		})
	}

	return out, rev, nil
}

func (s *Service) scanWatchTimeSlice(
	ctx context.Context,
	query string,
	args []any,
	byGuideKey map[guideKey][]Channel,
	emit func(guideKey, Programme),
) error {
	rows, err := s.o.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var gk guideKey
		var raw []byte
		if err := rows.Scan(&gk.source, &gk.guide, &raw); err != nil {
			return err
		}
		if _, ok := byGuideKey[gk]; !ok {
			continue
		}
		var p Programme
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		emit(gk, p)
	}
	return rows.Err()
}

// CurrentProgrammeForWatch returns the programme currently airing on channelID
// for viewer u at time at, along with a stable programme key suitable for
// caching reverse live-event matches (A6) and detecting programme rollovers.
// Returns (nil, "", nil) when no programme is on air or channel is unavailable.
func (s *Service) CurrentProgrammeForWatch(ctx context.Context, u *core.User, channelID string, at time.Time) (*WatchGuideEntry, string, error) {
	if s == nil || s.o.DB == nil || u == nil || strings.TrimSpace(channelID) == "" {
		return nil, "", nil
	}
	if at.IsZero() {
		at = s.o.Now()
	}
	ctx, pol, err := s.withPolicy(ctx, u)
	if err != nil {
		return nil, "", err
	}
	if !u.IsOwner && pol.Disabled {
		return nil, "", nil
	}
	all, snap, err := s.channelSnapshot(ctx)
	if err != nil {
		return nil, "", err
	}
	snap.mu.Lock()
	idx, ok := snap.byID[channelID]
	snap.mu.Unlock()
	if !ok || idx < 0 || idx >= len(all) {
		return nil, "", nil
	}
	c := all[idx]
	if !s.audienceAllowed(ctx, u, c) {
		return nil, "", nil
	}
	p, found, err := s.programmeAt(ctx, c, at)
	if err != nil {
		return nil, "", err
	}
	if !found {
		// Guide-free fallback (A6): channel is visible to the viewer but has
		// no current EPG programme; expose its metadata so the matcher can
		// link it to an unambiguously matching live event.
		entry := &WatchGuideEntry{
			ChannelID:     c.ID,
			ChannelName:   c.Name,
			ChannelNumber: c.Number,
			ChannelLogo:   c.Logo,
			TvgName:       c.TvgName,
			GuideID:       c.GuideID,
			IsEventAddon:  isStremioEventChannel(c),
			ChannelOnly:   true,
			Live:          true,
		}
		progKey := fmt.Sprintf("%s|channel-only|%s|%s", c.ID, c.Name, c.TvgName)
		return entry, progKey, nil
	}
	if !s.allowedProgramme(ctx, u, c, &p) {
		return nil, "", nil
	}
	entry := &WatchGuideEntry{
		ChannelID:     c.ID,
		ChannelName:   c.Name,
		ChannelNumber: c.Number,
		ChannelLogo:   c.Logo,
		TvgName:       c.TvgName,
		GuideID:       c.GuideID,
		IsEventAddon:  len(p.EventStreams) > 0 || isStremioEventChannel(c),
		Programme:     p,
		Live:          true,
	}
	progKey := fmt.Sprintf("%s|%d|%d|%s", c.ID, p.Start.UnixMilli(), p.End.UnixMilli(), p.Item.Title)
	return entry, progKey, nil
}

// isStremioEventChannel identifies 0.3.18 Stremio event channels synthesized by
// internal/plugins/adapters/livetv_events.go.
func isStremioEventChannel(c Channel) bool {
	if strings.HasPrefix(c.GuideID, "event:") {
		return true
	}
	if isEventPluginURL(c.StreamURL) {
		return true
	}
	for _, src := range c.StreamSources {
		if isEventPluginURL(src.URL) {
			return true
		}
	}
	return false
}

func isEventPluginURL(u string) bool {
	return strings.HasPrefix(u, "plugin://") && strings.Contains(u, "/event/")
}

func normalizeCallsignHints(callsigns []string) []string {
	if len(callsigns) == 0 {
		return nil
	}
	out := make([]string, 0, len(callsigns))
	add := func(norm string) {
		if len(norm) >= 2 && !slices.Contains(out, norm) {
			out = append(out, norm)
		}
	}
	for _, raw := range callsigns {
		norm := compactAlnumLower(raw)
		add(norm)
		// Strip common network/channel suffixes so "USA Network" matches
		// channel "USA" and "Golf Chnl" matches "Golf Channel".
		for _, suffix := range []string{"network", "channel", "chnl", "net"} {
			if len(norm) > len(suffix)+1 && strings.HasSuffix(norm, suffix) {
				add(strings.TrimSuffix(norm, suffix))
			}
		}
		switch norm {
		case "fs1":
			add("foxsports1")
		case "foxsports1":
			add("fs1")
		case "fs2":
			add("foxsports2")
		case "foxsports2":
			add("fs2")
		case "cbssn":
			add("cbssports")
		}
	}
	return out
}

func channelMatchesAnyCallsignHint(c Channel, normCallsigns []string) bool {
	if len(normCallsigns) == 0 {
		return false
	}
	fields := []string{compactAlnumLower(c.Name), compactAlnumLower(c.TvgName), compactAlnumLower(c.GuideID)}
	for _, hint := range normCallsigns {
		for _, f := range fields {
			if f != "" && strings.Contains(f, hint) {
				return true
			}
		}
	}
	return false
}

func compactAlnumLower(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		switch {
		case r == '+':
			b.WriteString("plus")
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		}
	}
	return b.String()
}
