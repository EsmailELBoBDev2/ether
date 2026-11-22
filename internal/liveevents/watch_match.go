package liveevents

import (
	"math"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/braedonsaunders/ether/internal/core"
)

const (
	// WatchWindowBefore and WatchWindowAfter define the A5 guide overlap window
	// around an event's scheduled start time (start - 30m to start + 4h).
	WatchWindowBefore = 30 * 60 // seconds (30m)
	WatchWindowAfter  = 4 * 3600

	minGuideMatchConfidence = 0.45
	maxWatchTargetsPerEvent = 16
)

// GuideCandidate is the package-neutral view of one viewer-visible Live TV
// channel + guide programme used by the A5/A6 matcher.
type GuideCandidate struct {
	ChannelID     string
	ChannelName   string
	ChannelNumber string
	ChannelLogo   string
	TvgName       string
	GuideID       string
	IsEventAddon  bool
	Title         string
	EpisodeTitle  string
	Summary       string
	Genres        []string
	Live          bool
}

type competitorTerms struct {
	phrases [][]string // normalized multi-token or single-token terms for this competitor
}

type eventMatchProfile struct {
	competitors []competitorTerms // per-competitor phrases (2 for head-to-head)
	extraTerms  [][]string        // matchHints.terms + event title phrases
	callsigns   [][]string        // normalized broadcast names + matchHints.callsigns
	category    []string          // normalized category/league words for genre fallback
}

// normalizeTokens lowercases text, splits on non-alphanumeric Unicode runs,
// preserves '+' as "plus", and strips connector words ("vs", "v", "versus", "at").
// Punctuation such as "@" is stripped as a separator automatically.
func normalizeTokens(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		tok := cur.String()
		cur.Reset()
		switch tok {
		case "vs", "v", "versus", "at":
			return
		}
		out = append(out, tok)
	}
	for _, r := range s {
		r = unicode.ToLower(r)
		switch {
		case r == '+':
			flush()
			out = append(out, "plus")
		case r == '\'' || r == '\u2019':
			// Join apostrophes inside names ("A's", "St. John's") without breaking token.
			continue
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			cur.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return out
}

// normalizeCallsignTokens strips common regional/quality tags from channel or
// broadcast labels so "US: Network 2 HD" matches "Network 2" cleanly without
// confusing numbered variants like "Network 1" vs "Network 2".
func normalizeCallsignTokens(s string) []string {
	raw := normalizeTokens(s)
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, tok := range raw {
		switch tok {
		case "hd", "fhd", "uhd", "sd", "4k", "hevc", "raw", "east", "west", "us", "usa", "uk", "ca", "can", "au", "tv", "network", "channel", "ch":
			continue
		default:
			out = append(out, tok)
		}
	}
	if len(out) == 0 {
		return raw
	}
	return out
}

func buildEventMatchProfile(ev core.LiveEvent) eventMatchProfile {
	var prof eventMatchProfile

	addPhrase := func(dst *[][]string, raw string) {
		toks := normalizeTokens(raw)
		if len(toks) == 0 {
			return
		}
		if len(toks) == 1 && len(toks[0]) < 2 {
			return
		}
		for _, existing := range *dst {
			if slices.Equal(existing, toks) {
				return
			}
		}
		*dst = append(*dst, toks)
	}

	for _, c := range ev.Competitors {
		var ct competitorTerms
		addPhrase(&ct.phrases, c.Name)
		addPhrase(&ct.phrases, c.Short)
		addPhrase(&ct.phrases, c.Abbr)
		// Also include the last distinctive word of multi-word team names
		// (e.g., "City Eagles" -> "eagles") when >= 3 characters so guides
		// listing nicknames match reliably.
		if nameToks := normalizeTokens(c.Name); len(nameToks) >= 2 {
			last := nameToks[len(nameToks)-1]
			if len(last) >= 3 {
				addPhrase(&ct.phrases, last)
			}
		}
		if len(ct.phrases) > 0 {
			prof.competitors = append(prof.competitors, ct)
		}
	}

	if ev.MatchHints != nil {
		for _, term := range ev.MatchHints.Terms {
			toks := normalizeTokens(term)
			if len(toks) == 0 {
				continue
			}
			// Associate hint terms that overlap a competitor with that competitor
			// too, so plugin-supplied abbreviations/nicknames count toward matching
			// both competitors.
			associated := false
			for i := range prof.competitors {
				if sharesTokenWithPhrases(toks, prof.competitors[i].phrases) {
					addPhrase(&prof.competitors[i].phrases, term)
					associated = true
				}
			}
			if !associated || len(prof.competitors) == 0 {
				addPhrase(&prof.extraTerms, term)
			} else {
				addPhrase(&prof.extraTerms, term)
			}
		}
	}

	if ev.Title != "" {
		addPhrase(&prof.extraTerms, ev.Title)
	}

	addCallsign := func(raw string) {
		toks := normalizeCallsignTokens(raw)
		if len(toks) == 0 {
			return
		}
		for _, existing := range prof.callsigns {
			if slices.Equal(existing, toks) {
				return
			}
		}
		prof.callsigns = append(prof.callsigns, toks)
	}
	for _, b := range ev.Broadcasts {
		if b.Kind == "radio" {
			continue
		}
		addCallsign(b.Name)
	}
	if ev.MatchHints != nil {
		for _, cs := range ev.MatchHints.Callsigns {
			addCallsign(cs)
		}
	}

	for _, src := range []string{ev.Category, ev.League.Name, ev.League.Abbr} {
		for _, tok := range normalizeTokens(src) {
			if len(tok) >= 2 && !slices.Contains(prof.category, tok) {
				prof.category = append(prof.category, tok)
			}
		}
	}

	return prof
}

func sharesTokenWithPhrases(toks []string, phrases [][]string) bool {
	for _, p := range phrases {
		for _, pt := range p {
			if len(pt) < 2 {
				continue
			}
			for _, t := range toks {
				if t == pt {
					return true
				}
			}
		}
	}
	return false
}

// containsPhrase reports whether needle occurs as a contiguous token sequence
// inside haystack.
func containsPhrase(haystack, needle []string) bool {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return false
	}
	for i := 0; i <= len(haystack)-len(needle); i++ {
		if slices.Equal(haystack[i:i+len(needle)], needle) {
			return true
		}
	}
	return false
}

// phraseMatched reports whether any phrase in phrases occurs in haystack, and
// whether a distinctive phrase (multi-word or >= 3 chars) matched.
func phraseMatched(haystack []string, phrases [][]string) (matched bool, distinctive bool) {
	for _, p := range phrases {
		if !containsPhrase(haystack, p) {
			continue
		}
		matched = true
		if len(p) >= 2 || (len(p) == 1 && len(p[0]) >= 3) {
			distinctive = true
			return true, true
		}
	}
	return matched, distinctive
}

// callsignMatchesChannel checks whether any normalized event callsign matches
// the channel's name, tvgName, or guideId without confusing numbered channels.
func callsignMatchesChannel(callsigns [][]string, cand GuideCandidate) bool {
	if len(callsigns) == 0 {
		return false
	}
	fields := [][]string{
		normalizeCallsignTokens(cand.ChannelName),
		normalizeCallsignTokens(cand.TvgName),
		normalizeCallsignTokens(cand.GuideID),
	}
	for _, cs := range callsigns {
		for _, field := range fields {
			if callsignMatchesField(cs, field) {
				return true
			}
		}
	}
	return false
}

func callsignMatchesField(callsign, field []string) bool {
	if len(callsign) == 0 || len(field) == 0 || len(callsign) > len(field) {
		return false
	}
	if slices.Equal(callsign, field) {
		return true
	}
	for i := 0; i <= len(field)-len(callsign); i++ {
		if !slices.Equal(field[i:i+len(callsign)], callsign) {
			continue
		}
		// Prevent "net" matching "net plus" or "net 2" when the callsign
		// itself did not specify "plus" or a channel variant number.
		// Affiliate numbers like "net 5 city" (followed by non-digit city)
		// remain allowed when the callsign has no trailing digit and is not
		// followed by a bare single-digit suffix at the end of the channel name.
		nextIdx := i + len(callsign)
		if nextIdx < len(field) {
			next := field[nextIdx]
			if next == "plus" {
				continue
			}
			if isAllDigits(next) && nextIdx == len(field)-1 && len(next) == 1 {
				// e.g., callsign "alpha" vs channel "alpha 2" -> distinct sub-channel
				continue
			}
		}
		return true
	}
	return false
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func hasSportsGenre(genres []string, categoryTokens []string) bool {
	for _, g := range genres {
		toks := normalizeTokens(g)
		for _, tok := range toks {
			switch tok {
			case "sport", "sports", "athletics", "racing", "motorsport", "motorsports", "esports", "esport":
				return true
			}
			if slices.Contains(categoryTokens, tok) {
				return true
			}
		}
	}
	return false
}

// scoreGuideCandidate evaluates a single Live TV or Stremio event channel
// programme against an event profile. Returns (confidence in [0, 1], ok).
func scoreGuideCandidate(prof eventMatchProfile, cand GuideCandidate) (float64, bool) {
	titleTokens := normalizeTokens(cand.Title)
	if cand.EpisodeTitle != "" {
		titleTokens = append(titleTokens, normalizeTokens(cand.EpisodeTitle)...)
	}
	summaryTokens := normalizeTokens(cand.Summary)
	allTokens := make([]string, 0, len(titleTokens)+len(summaryTokens))
	allTokens = append(allTokens, titleTokens...)
	allTokens = append(allTokens, summaryTokens...)

	sportsGenre := hasSportsGenre(cand.Genres, prof.category)
	callsignMatch := callsignMatchesChannel(prof.callsigns, cand)

	// Head-to-head competitor matching (2 competitors).
	if len(prof.competitors) == 2 {
		c0Title, c0TitleDist := phraseMatched(titleTokens, prof.competitors[0].phrases)
		c1Title, c1TitleDist := phraseMatched(titleTokens, prof.competitors[1].phrases)
		c0Any, c0AnyDist := phraseMatched(allTokens, prof.competitors[0].phrases)
		c1Any, c1AnyDist := phraseMatched(allTokens, prof.competitors[1].phrases)

		bothTitle := c0Title && c1Title
		bothAny := c0Any && c1Any
		oneTitle := (c0Title && c0TitleDist) || (c1Title && c1TitleDist)
		oneAny := (c0Any && c0AnyDist) || (c1Any && c1AnyDist)

		// 0.3.18 Stremio event channels (A5 source #2): require both competitors.
		if cand.IsEventAddon {
			if !bothAny {
				return 0, false
			}
			score := 0.78
			if bothTitle {
				score = 0.88
			}
			if c0TitleDist && c1TitleDist {
				score += 0.04
			}
			return clampConfidence(score), true
		}

		// Standard Live TV guide programme (A5 source #1).
		var score float64
		switch {
		case bothTitle:
			score = 0.68
			if c0TitleDist || c1TitleDist {
				score += 0.04
			}
		case bothAny:
			score = 0.56
		case oneTitle && (callsignMatch || sportsGenre):
			score = 0.32
		case oneAny && callsignMatch && sportsGenre:
			score = 0.24
		}

		if callsignMatch {
			if score > 0 {
				score += 0.24
			} else if sportsGenre || matchesCategoryInTokens(titleTokens, prof.category) {
				// Broadcast channel matches AND programme is live sports / matches
				// league/category during the event window even if EPG omitted teams.
				score = 0.36
			}
		}

		if sportsGenre && score > 0 {
			score += 0.12
		} else if matchesCategoryInTokens(titleTokens, prof.category) && score > 0 {
			score += 0.10
		}

		if score < minGuideMatchConfidence {
			return 0, false
		}
		return clampConfidence(score), true
	}

	// N-competitor or non-head-to-head event (e.g., motorsport, golf, single-title event).
	extraTitleHits := countDistinctPhraseHits(titleTokens, prof.extraTerms)
	extraAnyHits := countDistinctPhraseHits(allTokens, prof.extraTerms)
	catInTitle := matchesCategoryInTokens(titleTokens, prof.category)

	if cand.IsEventAddon {
		if extraTitleHits == 0 && extraAnyHits < 2 {
			return 0, false
		}
		score := 0.75
		if extraTitleHits >= 1 {
			score = 0.85
		}
		return clampConfidence(score), true
	}

	var score float64
	switch {
	case extraTitleHits >= 2:
		score = 0.68
	case extraTitleHits == 1:
		score = 0.50
	case extraAnyHits >= 2:
		score = 0.48
	}

	if callsignMatch {
		if score > 0 {
			score += 0.24
		} else if sportsGenre || catInTitle {
			score = 0.36
		}
	}
	if sportsGenre && score > 0 {
		score += 0.12
	} else if catInTitle && score > 0 {
		score += 0.10
	}

	if score < minGuideMatchConfidence {
		return 0, false
	}
	return clampConfidence(score), true
}

func countDistinctPhraseHits(tokens []string, phrases [][]string) int {
	hits := 0
	for _, p := range phrases {
		if len(p) == 1 && len(p[0]) < 3 {
			continue
		}
		if containsPhrase(tokens, p) {
			hits++
		}
	}
	return hits
}

func matchesCategoryInTokens(tokens, category []string) bool {
	if len(tokens) == 0 || len(category) == 0 {
		return false
	}
	for _, c := range category {
		if len(c) >= 2 && slices.Contains(tokens, c) {
			return true
		}
	}
	return false
}

func clampConfidence(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return math.Round(v*100) / 100
}

// matchEventToCandidates scores guide candidates + plugin stream links for ev,
// returning deduplicated, deterministically sorted WatchTargets.
func matchEventToCandidates(ev core.LiveEvent, candidates []GuideCandidate, pluginLinks []core.LiveEventLink) []core.WatchTarget {
	prof := buildEventMatchProfile(ev)

	// Deduplicate by channelId, retaining the best-scoring programme per channel.
	bestByChannel := make(map[string]core.WatchTarget)
	for _, cand := range candidates {
		conf, ok := scoreGuideCandidate(prof, cand)
		if !ok {
			continue
		}
		kind := core.WatchTargetKindChannel
		if cand.IsEventAddon {
			kind = core.WatchTargetKindAddon
		}
		t := core.WatchTarget{
			Kind:       kind,
			ChannelID:  cand.ChannelID,
			Name:       cand.ChannelName,
			Number:     cand.ChannelNumber,
			Logo:       cand.ChannelLogo,
			Live:       cand.Live,
			Confidence: conf,
		}
		prev, exists := bestByChannel[cand.ChannelID]
		if !exists || t.Confidence > prev.Confidence || (t.Confidence == prev.Confidence && t.Live && !prev.Live) {
			bestByChannel[cand.ChannelID] = t
		}
	}

	out := make([]core.WatchTarget, 0, len(bestByChannel)+len(pluginLinks))
	for _, t := range bestByChannel {
		out = append(out, t)
	}

	// Source #3: Plugin-provided stream links (from broadcasts with URL or detail links).
	seenURL := make(map[string]struct{})
	eventLive := ev.Status.State == core.LiveEventStateIn
	for _, b := range ev.Broadcasts {
		u := strings.TrimSpace(b.URL)
		if u == "" {
			continue
		}
		if _, dup := seenURL[u]; dup {
			continue
		}
		seenURL[u] = struct{}{}
		name := strings.TrimSpace(b.Name)
		if name == "" {
			name = "Stream"
		}
		out = append(out, core.WatchTarget{
			Kind:       core.WatchTargetKindPlugin,
			Name:       name,
			URL:        u,
			Live:       eventLive,
			Confidence: 0.55,
		})
	}
	for _, link := range pluginLinks {
		u := strings.TrimSpace(link.URL)
		if u == "" {
			continue
		}
		if _, dup := seenURL[u]; dup {
			continue
		}
		seenURL[u] = struct{}{}
		name := strings.TrimSpace(link.Label)
		if name == "" {
			name = "Stream"
		}
		out = append(out, core.WatchTarget{
			Kind:       core.WatchTargetKindPlugin,
			Name:       name,
			URL:        u,
			Live:       eventLive,
			Confidence: 0.50,
		})
	}

	sortWatchTargets(out)
	if len(out) > maxWatchTargetsPerEvent {
		out = out[:maxWatchTargetsPerEvent]
	}
	return out
}

func watchKindRank(kind string) int {
	switch kind {
	case core.WatchTargetKindChannel:
		return 0
	case core.WatchTargetKindAddon:
		return 1
	case core.WatchTargetKindPlugin:
		return 2
	default:
		return 3
	}
}

func sortWatchTargets(targets []core.WatchTarget) {
	sort.Slice(targets, func(i, j int) bool {
		a, b := targets[i], targets[j]
		if a.Confidence != b.Confidence {
			return a.Confidence > b.Confidence
		}
		if a.Live != b.Live {
			return a.Live
		}
		if ra, rb := watchKindRank(a.Kind), watchKindRank(b.Kind); ra != rb {
			return ra < rb
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ChannelID < b.ChannelID
	})
}

// matchProgrammeToEvents performs the reverse of A5 (for A6 Live TV overlay):
// given a currently airing channel programme and active/near-active events,
// pick the highest-confidence matching LiveEventRef, or nil if none meets the
// threshold.
func matchProgrammeToEvents(cand GuideCandidate, events []core.LiveEvent) *core.LiveEventRef {
	var bestRef *core.LiveEventRef
	bestScore := 0.0
	bestLive := false
	for _, ev := range events {
		if ev.ID == "" {
			continue
		}
		if ev.Status.State == core.LiveEventStateCanceled || ev.Status.State == core.LiveEventStatePostponed {
			continue
		}
		prof := buildEventMatchProfile(ev)
		conf, ok := scoreGuideCandidate(prof, cand)
		if !ok {
			continue
		}
		isLive := ev.Status.State == core.LiveEventStateIn
		if bestRef == nil || conf > bestScore || (conf == bestScore && isLive && !bestLive) || (conf == bestScore && isLive == bestLive && ev.ID < bestRef.ID) {
			ref := core.LiveEventRef{ID: ev.ID, Feed: ev.Feed}
			bestRef = &ref
			bestScore = conf
			bestLive = isLive
		}
	}
	return bestRef
}
