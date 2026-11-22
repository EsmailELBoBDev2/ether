package livetv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

	// watchConsecutiveOpenFailures is the consecutive failed-open threshold at
	// which a stream input is treated as failing for Sports watch targets even
	// after its short tune cooldown elapses, until an open succeeds or the
	// failure ages out of watchRecentFailureWindow.
	watchConsecutiveOpenFailures = 2
	watchRecentFailureWindow     = 30 * time.Minute
)

type watchChannelState int

const (
	watchChannelHealthy watchChannelState = iota
	watchChannelUnhealthy
	watchChannelPaused
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
	Unhealthy     bool
	Programme     Programme
	Live          bool
}

func (s *Service) bumpHealthRevision() {
	if s == nil {
		return
	}
	s.healthRev.Add(1)
}

func (s *Service) noteHealthExpiry(at time.Time) {
	if s == nil || at.IsZero() {
		return
	}
	target := at.UnixNano()
	if target <= 0 {
		return
	}
	for {
		cur := s.nextHealthExpiry.Load()
		if cur > 0 && cur <= target {
			return
		}
		if s.nextHealthExpiry.CompareAndSwap(cur, target) {
			return
		}
	}
}

func (s *Service) healthRevision() uint64 {
	if s == nil {
		return 0
	}
	nowNano := s.o.Now().UnixNano()
	if exp := s.nextHealthExpiry.Load(); exp > 0 && nowNano >= exp {
		if s.nextHealthExpiry.CompareAndSwap(exp, 0) {
			s.healthRev.Add(1)
		}
	}
	return s.healthRev.Load()
}

func sourceHistoryAvailabilityChanged(a, b SourceHistory) bool {
	return a.Failures != b.Failures ||
		!a.RetryAfter.Equal(b.RetryAfter) ||
		a.LastError != b.LastError ||
		!a.LastErrorAt.Equal(b.LastErrorAt) ||
		!a.LastPlayed.Equal(b.LastPlayed)
}

func (s *Service) noteSourceHistoryExpiry(h SourceHistory, now time.Time) {
	if s == nil {
		return
	}
	if now.IsZero() {
		now = s.o.Now()
	}
	var expiry time.Time
	if h.RetryAfter.After(now) {
		expiry = h.RetryAfter
	}
	if h.Failures >= watchConsecutiveOpenFailures {
		lastErr := h.LastErrorAt
		if lastErr.IsZero() {
			lastErr = h.Updated
		}
		if !lastErr.IsZero() && !h.LastPlayed.After(lastErr) {
			if winEnd := lastErr.Add(watchRecentFailureWindow); winEnd.After(now) && winEnd.After(expiry) {
				expiry = winEnd
			}
		}
	}
	if !expiry.IsZero() {
		s.noteHealthExpiry(expiry)
	}
}

func sameAccountHealth(a, b *ProviderAccount) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if !strings.EqualFold(a.Status, b.Status) {
		return false
	}
	if a.ExpiresAt == nil && b.ExpiresAt == nil {
		return true
	}
	if a.ExpiresAt == nil || b.ExpiresAt == nil {
		return false
	}
	return a.ExpiresAt.Equal(*b.ExpiresAt)
}

func accountUsableAt(a *ProviderAccount, now time.Time) bool {
	if a == nil {
		return true
	}
	if a.Status != "" && !strings.EqualFold(a.Status, "active") {
		return false
	}
	if a.ExpiresAt != nil && !now.Before(*a.ExpiresAt) {
		return false
	}
	return true
}

func (s *Service) sourceAccountHealthy(sourceID string, entry authEntry, now time.Time) bool {
	if entry.pool != nil {
		src := entry.pool
		candidates := append([]ProviderLogin{{ID: "primary", Account: src.Account}}, src.Logins...)
		p := &s.loginPool
		p.mu.Lock()
		var earliestRetry time.Time
		usable := false
		for _, cand := range candidates {
			if cand.Error != "" {
				continue
			}
			if until := p.failed[sourceID+"\x00"+cand.ID]; now.Before(until) {
				if earliestRetry.IsZero() || until.Before(earliestRetry) {
					earliestRetry = until
				}
				continue
			}
			if !accountUsableAt(cand.Account, now) {
				continue
			}
			usable = true
			break
		}
		p.mu.Unlock()
		if !usable {
			if !earliestRetry.IsZero() {
				s.noteHealthExpiry(earliestRetry)
			}
			return false
		}
		return true
	}
	if entry.account != nil {
		if !accountUsableAt(entry.account, now) {
			return false
		}
		if entry.account.ExpiresAt != nil && now.Before(*entry.account.ExpiresAt) {
			s.noteHealthExpiry(*entry.account.ExpiresAt)
		}
	}
	return true
}

func (s *Service) sourceInputHealthy(channelID string, src StreamSource, now time.Time) bool {
	st := s.sourceStatus(channelID, src)
	hist, hasHist := s.sourceHistory(channelID, src)

	var (
		failures   int
		retryAfter time.Time
		lastErrAt  time.Time
		lastPlayed time.Time
	)
	switch {
	case hasHist && hist.Updated.After(st.LastChecked):
		failures = hist.Failures
		retryAfter = hist.RetryAfter
		lastErrAt = hist.LastErrorAt
		if lastErrAt.IsZero() && failures > 0 {
			lastErrAt = hist.Updated
		}
		lastPlayed = hist.LastPlayed
	case !st.LastChecked.IsZero():
		failures = st.Failures
		retryAfter = st.RetryAfter
		if st.LastError != "" || failures > 0 {
			lastErrAt = st.LastChecked
			if hasHist && !hist.LastErrorAt.IsZero() && hist.LastErrorAt.After(lastErrAt) {
				lastErrAt = hist.LastErrorAt
			}
		}
		lastPlayed = st.LastPlayed
	case hasHist:
		failures = hist.Failures
		retryAfter = hist.RetryAfter
		lastErrAt = hist.LastErrorAt
		if lastErrAt.IsZero() && failures > 0 {
			lastErrAt = hist.Updated
		}
		lastPlayed = hist.LastPlayed
	default:
		return true
	}

	var expiry time.Time
	unhealthy := false
	if retryAfter.After(now) {
		unhealthy = true
		expiry = retryAfter
	}
	if failures >= watchConsecutiveOpenFailures && !lastErrAt.IsZero() && !lastPlayed.After(lastErrAt) {
		winEnd := lastErrAt.Add(watchRecentFailureWindow)
		if winEnd.After(now) {
			unhealthy = true
			if winEnd.After(expiry) {
				expiry = winEnd
			}
		}
	}
	if unhealthy {
		if !expiry.IsZero() {
			s.noteHealthExpiry(expiry)
		}
		return false
	}
	return true
}

func (s *Service) channelWatchHealth(ctx context.Context, c Channel, now time.Time) watchChannelState {
	if s.sourcePaused(ctx, c) {
		return watchChannelPaused
	}
	if c.Kind != "iptv" {
		return watchChannelHealthy
	}

	var single [1]StreamSource
	inputs := c.StreamSources
	if len(inputs) == 0 {
		rawURL := strings.TrimSpace(c.StreamURL)
		if !validStreamURL(rawURL) {
			return watchChannelUnhealthy
		}
		id := stableID(c.ID, "primary")
		if c.SourceID != "" {
			id = stableID(c.ID, "playlist")
		}
		single[0] = StreamSource{ID: id, URL: rawURL, Enabled: true}
		inputs = single[:]
	}

	activeCount := 0
	unpausedCount := 0
	healthyCount := 0
	sweptUnavailable := s.sweepState(c) == "unavailable"

	for _, src := range inputs {
		if !src.Enabled || !s.autoDonorActive(src.AutoFrom) {
			continue
		}
		rawURL := strings.TrimSpace(src.URL)
		if !validStreamURL(rawURL) {
			continue
		}
		activeCount++
		effSourceID := src.AuthSourceID
		if effSourceID == "" {
			effSourceID = c.SourceID
		}
		var entry authEntry
		if effSourceID != "" {
			var err error
			entry, err = s.sourceEntry(ctx, effSourceID)
			if errors.Is(err, core.ErrNotAvailable) {
				continue // this input's source is paused
			}
		}
		unpausedCount++
		if sweptUnavailable {
			continue // keep scanning only to see whether all active inputs are paused
		}
		if effSourceID != "" && !s.sourceAccountHealthy(effSourceID, entry, now) {
			continue
		}
		checkSrc := src
		checkSrc.URL = rawURL
		if checkSrc.ID == "" {
			checkSrc.ID = stableID(c.ID, rawURL)
		}
		if s.sourceInputHealthy(c.ID, checkSrc, now) {
			healthyCount++
		}
	}

	if activeCount > 0 && unpausedCount == 0 {
		return watchChannelPaused
	}
	if sweptUnavailable || activeCount == 0 || healthyCount == 0 {
		return watchChannelUnhealthy
	}
	return watchChannelHealthy
}

// GuideRevision returns a compact, cluster-safe revision token covering the
// guide/channel epoch, channel health revision, and the viewer's effective
// Live TV access policy.
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
	healthRev := s.healthRevision()
	sum := sha256.New()
	_, _ = fmt.Fprintf(sum, "g=%d|h=%d|u=%d|owner=%t|rat=%s|pol=%s",
		gen, healthRev, u.ID, u.IsOwner, u.MaxContentRating, rawPol)
	return fmt.Sprintf("g%d:h%d:%s", gen, healthRev, hex.EncodeToString(sum.Sum(nil)[:8])), nil
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

	now := s.o.Now()
	app := core.LiveApp(ctx)
	normCallsigns := normalizeCallsignHints(callsigns)
	byGuideKey := make(map[guideKey][]Channel)
	unhealthyByID := make(map[string]bool)

	var healthyCallsign, unhealthyCallsign []Channel
	var healthyTargetedKeys, unhealthyTargetedKeys []guideKey
	targetedHealthy := make(map[guideKey][]Channel)
	targetedUnhealthy := make(map[guideKey][]Channel)

	for _, c := range all {
		if c.Kind == "virtual" {
			continue
		}
		if app != "" && slices.Contains(c.HiddenApps, app) {
			continue
		}
		if !s.audiencePolicyAllowed(ctx, u, c) {
			continue
		}
		health := s.channelWatchHealth(ctx, c, now)
		if health == watchChannelPaused {
			continue
		}
		unhealthy := health == watchChannelUnhealthy
		if unhealthy {
			unhealthyByID[c.ID] = true
		}
		matchesCallsign := channelMatchesAnyCallsignHint(c, normCallsigns)
		if matchesCallsign {
			if !unhealthy {
				if len(healthyCallsign) < watchTargetedChannelCap {
					healthyCallsign = append(healthyCallsign, c)
				}
			} else if len(unhealthyCallsign) < watchTargetedChannelCap {
				unhealthyCallsign = append(unhealthyCallsign, c)
			}
		}
		if c.GuideID == "" {
			continue
		}
		gk := guideKey{source: guideSource(c), guide: c.GuideID}
		byGuideKey[gk] = append(byGuideKey[gk], c)
		if isStremioEventChannel(c) || matchesCallsign {
			if !unhealthy {
				if _, exists := targetedHealthy[gk]; !exists {
					if len(healthyTargetedKeys) < watchTargetedChannelCap {
						healthyTargetedKeys = append(healthyTargetedKeys, gk)
						targetedHealthy[gk] = append(targetedHealthy[gk], c)
					}
				} else {
					targetedHealthy[gk] = append(targetedHealthy[gk], c)
				}
			} else {
				if _, exists := targetedUnhealthy[gk]; !exists {
					if len(unhealthyTargetedKeys) < watchTargetedChannelCap {
						unhealthyTargetedKeys = append(unhealthyTargetedKeys, gk)
						targetedUnhealthy[gk] = append(targetedUnhealthy[gk], c)
					}
				} else {
					targetedUnhealthy[gk] = append(targetedUnhealthy[gk], c)
				}
			}
		}
	}

	callsignMatched := make([]Channel, 0, min(len(healthyCallsign)+len(unhealthyCallsign), watchTargetedChannelCap))
	callsignMatched = append(callsignMatched, healthyCallsign...)
	if rem := watchTargetedChannelCap - len(callsignMatched); rem > 0 && len(unhealthyCallsign) > 0 {
		callsignMatched = append(callsignMatched, unhealthyCallsign[:min(rem, len(unhealthyCallsign))]...)
	}

	targeted := make(map[guideKey][]Channel, min(len(healthyTargetedKeys)+len(unhealthyTargetedKeys), watchTargetedChannelCap))
	for _, gk := range healthyTargetedKeys {
		targeted[gk] = append(targeted[gk], targetedHealthy[gk]...)
	}
	for _, gk := range unhealthyTargetedKeys {
		if _, exists := targeted[gk]; exists {
			targeted[gk] = append(targeted[gk], targetedUnhealthy[gk]...)
			continue
		}
		if len(targeted) >= watchTargetedChannelCap {
			break
		}
		targeted[gk] = append(targeted[gk], targetedUnhealthy[gk]...)
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
			if !s.allowedProgrammeForWatch(ctx, u, c, &cp) {
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
				Unhealthy:     unhealthyByID[c.ID],
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
			Unhealthy:     unhealthyByID[c.ID],
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
	if app := core.LiveApp(ctx); app != "" && slices.Contains(c.HiddenApps, app) {
		return nil, "", nil
	}
	if !s.audienceAllowed(ctx, u, c) || s.channelWatchHealth(ctx, c, at) != watchChannelHealthy {
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
