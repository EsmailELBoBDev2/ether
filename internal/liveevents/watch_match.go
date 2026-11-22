package liveevents

import (
	"math"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"
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
// channel + guide programme (or a callsign-only channel without guide data)
// used by the A5/A6 matcher.
type GuideCandidate struct {
	ChannelID     string
	ChannelName   string
	ChannelNumber string
	ChannelLogo   string
	TvgName       string
	GuideID       string
	IsEventAddon  bool
	ChannelOnly   bool
	Unhealthy     bool
	Title         string
	EpisodeTitle  string
	Summary       string
	Genres        []string
	Start         time.Time
	End           time.Time
	Live          bool
}

type competitorTerms struct {
	phrases [][]string // normalized multi-token or single-token terms for this competitor
}

type eventMatchProfile struct {
	state           string            // event lifecycle state (pre|in|post|delayed|...)
	competitors     []competitorTerms // per-competitor phrases (2 for head-to-head)
	extraTerms      [][]string        // matchHints.terms + event title phrases
	callsigns       [][]string        // normalized broadcast names + matchHints.callsigns
	linearCallsigns [][]string        // non-OTT linear broadcast callsigns for guide-free ChannelOnly fallback
	category        []string          // normalized category/league words for genre fallback
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

// normalizeCallsignTokens strips common regional prefixes and quality/network
// suffixes from channel or broadcast labels so "US: Network 2 HD" matches
// "Network 2", "USA Net" matches "USA Network", and "Golf Chnl" matches
// "Golf Channel" without confusing numbered variants or brand names.
func normalizeCallsignTokens(s string) []string {
	raw := normalizeTokens(s)
	if len(raw) == 0 {
		return nil
	}
	isQualityOrSuffix := func(tok string) bool {
		switch tok {
		case "hd", "fhd", "uhd", "sd", "4k", "hevc", "raw", "east", "west", "tv", "network", "channel", "chnl", "ch":
			return true
		default:
			return false
		}
	}
	isRegionTag := func(tok string) bool {
		switch tok {
		case "us", "usa", "uk", "gb", "ca", "can", "au", "ie":
			return true
		default:
			return false
		}
	}

	// Strip a leading country prefix (e.g. "US: Net 2 HD" or "UK: Sky Sports")
	// while preserving "USA" / "USA Network" / "USA Net" as a network brand.
	start := 0
	if len(raw) >= 2 && isRegionTag(raw[0]) {
		next := raw[1]
		isUSABrand := raw[0] == "usa" && (next == "net" || isQualityOrSuffix(next))
		isSingleRegionSuffix := len(raw) == 2 && isQualityOrSuffix(next)
		if !isUSABrand && !isSingleRegionSuffix {
			start = 1
		}
	}

	out := make([]string, 0, len(raw)-start)
	for i := start; i < len(raw); i++ {
		tok := raw[i]
		if isQualityOrSuffix(tok) {
			continue
		}
		if tok == "net" && len(out) > 0 {
			continue
		}
		out = append(out, tok)
	}
	if len(out) == 0 {
		return raw
	}
	return out
}

func buildEventMatchProfile(ev core.LiveEvent) eventMatchProfile {
	prof := eventMatchProfile{state: ev.Status.State}

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

	appendUniqueTokens := func(dst *[][]string, toks []string) {
		if len(toks) == 0 {
			return
		}
		for _, existing := range *dst {
			if slices.Equal(existing, toks) {
				return
			}
		}
		*dst = append(*dst, toks)
	}
	addCallsign := func(raw string, allowLinear bool) {
		toks := normalizeCallsignTokens(raw)
		if len(toks) == 0 {
			return
		}
		variants := expandCallsignAliases(toks)
		for _, v := range variants {
			appendUniqueTokens(&prof.callsigns, v)
			if allowLinear && !isStreamingOnlyCallsign(v) {
				appendUniqueTokens(&prof.linearCallsigns, v)
			}
		}
	}
	for _, b := range ev.Broadcasts {
		if b.Kind == "radio" {
			continue
		}
		addCallsign(b.Name, b.Kind != "stream")
	}
	if ev.MatchHints != nil {
		for _, cs := range ev.MatchHints.Callsigns {
			addCallsign(cs, true)
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
// the channel's name, tvgName, or guideId without confusing numbered channels
// or unrelated sister networks (e.g. FOX vs Fox News, TNT vs TNT Sports 1).
func callsignMatchesChannel(callsigns [][]string, cand GuideCandidate) (matched bool, exact bool) {
	if len(callsigns) == 0 {
		return false, false
	}
	fields := [][]string{
		normalizeCallsignTokens(cand.ChannelName),
		normalizeCallsignTokens(cand.TvgName),
		normalizeCallsignTokens(cand.GuideID),
	}
	for _, cs := range callsigns {
		for _, field := range fields {
			ok, isExact := callsignMatchesField(cs, field)
			if isExact {
				return true, true
			}
			if ok {
				matched = true
			}
		}
	}
	return matched, false
}

func callsignMatchesField(callsign, field []string) (matched bool, exact bool) {
	if len(callsign) == 0 || len(field) == 0 || len(callsign) > len(field) {
		return false, false
	}
	if slices.Equal(callsign, field) {
		return true, true
	}
	callsignHasSports := slices.Contains(callsign, "sports") || slices.Contains(callsign, "sport")
	for i := 0; i <= len(field)-len(callsign); i++ {
		if !slices.Equal(field[i:i+len(callsign)], callsign) {
			continue
		}
		// Single-token callsigns (e.g. "fox", "usa", "golf") must anchor at the
		// start of the channel name so they do not falsely match mid-name brands
		// like "Silver Fox Movies" or "beIN Sports USA".
		if i > 0 && len(callsign) == 1 {
			continue
		}
		// Prevent matching distinct sister networks or numbered sub-channels
		// when the callsign did not name them (e.g. FOX vs Fox News / FOX Sports 1,
		// TNT vs TNT Sports 1, ESPN vs ESPN+, Net vs Net 2, ABC vs ABC Australia).
		nextIdx := i + len(callsign)
		if nextIdx < len(field) {
			next := field[nextIdx]
			switch next {
			case "plus", "news", "business", "movies", "movie", "cinema",
				"kids", "junior", "jr", "music", "radio", "deportes", "espanol",
				"australia", "uk", "canada", "ireland", "extra", "alt", "alternate":
				continue
			case "sports", "sport":
				if !callsignHasSports {
					continue
				}
			}
			if isAllDigits(next) && nextIdx == len(field)-1 && len(next) == 1 {
				// e.g., callsign "alpha" vs channel "alpha 2" -> distinct sub-channel
				continue
			}
		}
		return true, false
	}
	return false, false
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

func isGenericLiveSportsTitle(tokens []string) bool {
	return slices.Contains(tokens, "live") && (slices.Contains(tokens, "sports") || slices.Contains(tokens, "sport"))
}

// scoreGuideCandidate evaluates a single Live TV or Stremio event channel
// programme (or callsign-only channel without EPG) against an event profile.
// Returns (confidence in [0, 1], ok).
func scoreGuideCandidate(prof eventMatchProfile, cand GuideCandidate) (float64, bool) {
	// Callsign-only fallback for channels that have no EPG guide programmes
	// covering the event window (e.g. IPTV playlists without XMLTV).
	if cand.ChannelOnly {
		if cand.IsEventAddon {
			return 0, false
		}
		linearMatch, linearExact := callsignMatchesChannel(prof.linearCallsigns, cand)
		if !linearMatch {
			return 0, false
		}
		switch prof.state {
		case core.LiveEventStatePost, core.LiveEventStateCanceled, core.LiveEventStatePostponed:
			return 0, false
		}
		if linearExact {
			return 0.60, true
		}
		return 0.52, true
	}

	callsignMatch, _ := callsignMatchesChannel(prof.callsigns, cand)

	titleTokens := normalizeTokens(cand.Title)
	if cand.EpisodeTitle != "" {
		titleTokens = append(titleTokens, normalizeTokens(cand.EpisodeTitle)...)
	}
	summaryTokens := normalizeTokens(cand.Summary)
	allTokens := make([]string, 0, len(titleTokens)+len(summaryTokens))
	allTokens = append(allTokens, titleTokens...)
	allTokens = append(allTokens, summaryTokens...)

	sportsGenre := hasSportsGenre(cand.Genres, prof.category)

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

		catInTitle := matchesCategoryInTokens(titleTokens, prof.category)
		liveSportsTitle := isGenericLiveSportsTitle(titleTokens)
		if callsignMatch {
			if score > 0 {
				score += 0.24
			} else if sportsGenre || catInTitle || liveSportsTitle {
				// Broadcast channel matches AND programme is live sports / matches
				// league/category during the event window even if EPG omitted teams.
				score = 0.36
			}
		}

		if sportsGenre && score > 0 {
			score += 0.12
		} else if (catInTitle || liveSportsTitle) && score > 0 {
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
	if hasStrongTitleOverlap(titleTokens, prof.extraTerms) {
		extraTitleHits++
		extraAnyHits++
	}
	catInTitle := matchesCategoryInTokens(titleTokens, prof.category)
	liveSportsTitle := isGenericLiveSportsTitle(titleTokens)

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
		} else if sportsGenre || catInTitle || liveSportsTitle {
			score = 0.36
		}
	}
	if sportsGenre && score > 0 {
		score += 0.12
	} else if (catInTitle || liveSportsTitle) && score > 0 {
		score += 0.10
	}

	if score < minGuideMatchConfidence {
		return 0, false
	}
	return clampConfidence(score), true
}

func isShortDistinctiveAlnum(tok string) bool {
	if len(tok) != 2 {
		return false
	}
	hasLetter := false
	hasDigit := false
	for _, r := range tok {
		if unicode.IsLetter(r) {
			hasLetter = true
		} else if unicode.IsDigit(r) {
			hasDigit = true
		}
	}
	return hasLetter && hasDigit
}

func countDistinctPhraseHits(tokens []string, phrases [][]string) int {
	hits := 0
	for _, p := range phrases {
		if len(p) == 1 && len(p[0]) < 3 && !isShortDistinctiveAlnum(p[0]) {
			continue
		}
		if containsPhrase(tokens, p) {
			hits++
		}
	}
	return hits
}

// hasStrongTitleOverlap reports whether tokens cover most distinctive words of
// any multi-word event title phrase (handles title-sponsor prefixes like
// "Heineken Las Vegas Grand Prix" against "Formula 1: Las Vegas Grand Prix").
func hasStrongTitleOverlap(tokens []string, phrases [][]string) bool {
	if len(tokens) == 0 {
		return false
	}
	for _, p := range phrases {
		if len(p) < 3 {
			continue
		}
		distinctive := 0
		matched := 0
		for _, word := range p {
			if len(word) < 3 {
				continue
			}
			distinctive++
			if slices.Contains(tokens, word) {
				matched++
			}
		}
		if distinctive >= 3 && matched >= 3 && matched*4 >= distinctive*3 {
			return true
		}
	}
	return false
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

// isPlayableStreamURL reports whether rawURL is a directly playable stream
// reference (plugin:// or streaming media scheme/manifest) rather than an
// informational web page such as an ESPN Gamecast/Commentary/Odds URL.
func isPlayableStreamURL(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme == "" {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "plugin", "rtmp", "rtmps", "rtsp", "rtsps", "udp", "rtp":
		return true
	case "http", "https":
		p := strings.ToLower(u.Path)
		return strings.HasSuffix(p, ".m3u8") ||
			strings.HasSuffix(p, ".mpd") ||
			strings.HasSuffix(p, ".ts") ||
			strings.HasSuffix(p, ".mp4")
	default:
		return false
	}
}

func boolPtr(v bool) *bool { return &v }

// matchEventToCandidates scores guide candidates + playable plugin broadcast
// streams for ev, returning deduplicated, deterministically sorted WatchTargets.
// Unhealthy channels are excluded whenever any healthy target exists; when only
// unhealthy channels match, they are returned with available=false so clients
// can render "Unavailable right now" instead of a Watch button.
// Informational reference links (detail.Links) are excluded per spec A5.
func matchEventToCandidates(ev core.LiveEvent, candidates []GuideCandidate) []core.WatchTarget {
	prof := buildEventMatchProfile(ev)
	eventLive := ev.Status.State == core.LiveEventStateIn

	// Deduplicate by channelId, retaining the best-scoring programme per channel
	// (preferring healthy over unhealthy if both appear for the same channel).
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
		live := cand.Live
		if cand.ChannelOnly {
			live = eventLive
		}
		t := core.WatchTarget{
			Kind:       kind,
			ChannelID:  cand.ChannelID,
			Name:       cand.ChannelName,
			Number:     cand.ChannelNumber,
			Logo:       cand.ChannelLogo,
			Live:       live,
			Confidence: conf,
		}
		if cand.Unhealthy {
			t.Available = boolPtr(false)
		}
		prev, exists := bestByChannel[cand.ChannelID]
		if !exists ||
			(t.IsAvailable() && !prev.IsAvailable()) ||
			(t.IsAvailable() == prev.IsAvailable() && (t.Confidence > prev.Confidence || (t.Confidence == prev.Confidence && t.Live && !prev.Live))) {
			bestByChannel[cand.ChannelID] = t
		}
	}

	healthy := make([]core.WatchTarget, 0, len(bestByChannel)+len(ev.Broadcasts))
	var unhealthy []core.WatchTarget
	for _, t := range bestByChannel {
		if t.IsAvailable() {
			healthy = append(healthy, t)
		} else {
			unhealthy = append(unhealthy, t)
		}
	}

	// Source #3: Plugin-provided directly playable stream URLs (never web pages).
	seenURL := make(map[string]struct{})
	for _, b := range ev.Broadcasts {
		u := strings.TrimSpace(b.URL)
		if !isPlayableStreamURL(u) {
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
		healthy = append(healthy, core.WatchTarget{
			Kind:       core.WatchTargetKindPlugin,
			Name:       name,
			URL:        u,
			Live:       eventLive,
			Confidence: 0.55,
		})
	}

	out := healthy
	if len(out) == 0 {
		out = unhealthy
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
		if avA, avB := a.IsAvailable(), b.IsAvailable(); avA != avB {
			return avA
		}
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

// expandCallsignAliases returns normalized token variants for common broadcast
// abbreviations (e.g. FS1 <-> FOX Sports 1, ESPN2 <-> ESPN 2) without blurring
// distinct sibling channels (FOX vs FS1, ESPN vs ESPN2, TNT vs TNT Sports 1).
func expandCallsignAliases(toks []string) [][]string {
	out := [][]string{toks}
	key := strings.Join(toks, " ")
	switch key {
	case "fs1":
		out = append(out, []string{"fox", "sports", "1"})
	case "fox sports 1":
		out = append(out, []string{"fs1"})
	case "fs2":
		out = append(out, []string{"fox", "sports", "2"})
	case "fox sports 2":
		out = append(out, []string{"fs2"})
	case "espn2":
		out = append(out, []string{"espn", "2"})
	case "espn 2":
		out = append(out, []string{"espn2"})
	case "nbatv":
		out = append(out, []string{"nba"})
	case "cbssn":
		out = append(out, []string{"cbs", "sports"})
	case "cbs sports":
		out = append(out, []string{"cbssn"})
	case "tru":
		out = append(out, []string{"trutv"})
	case "trutv":
		out = append(out, []string{"tru"})
	}
	return out
}

// isStreamingOnlyCallsign identifies OTT streaming services (like Peacock or
// ESPN+) that are not linear TV channels and must not produce guide-free
// ChannelOnly watch or channel-link matches.
func isStreamingOnlyCallsign(toks []string) bool {
	switch strings.Join(toks, " ") {
	case "peacock", "espn plus", "paramount plus", "apple", "apple plus",
		"prime video", "amazon prime", "amazon", "netflix", "hulu", "max",
		"fubo", "fandango", "fandango home", "youtube", "nba league pass",
		"mls season pass", "nfl plus", "discovery plus", "stan sport",
		"viaplay", "vict plus":
		return true
	default:
		return false
	}
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

// matchChannelOnlyToEvents performs the A6 guide-free fallback when a live
// channel has no current EPG programme: link it to the single live event whose
// linear broadcasts match that channel. If several live events match the same
// network, return nil unless exactly one features a viewer-favourited entity.
func matchChannelOnlyToEvents(cand GuideCandidate, events []core.LiveEvent, favKeys []string) *core.LiveEventRef {
	if cand.IsEventAddon {
		return nil
	}
	var exactMatches []core.LiveEvent
	var prefixMatches []core.LiveEvent
	for _, ev := range events {
		if ev.ID == "" || ev.Status.State != core.LiveEventStateIn {
			continue
		}
		prof := buildEventMatchProfile(ev)
		matched, exact := callsignMatchesChannel(prof.linearCallsigns, cand)
		if !matched {
			continue
		}
		if exact {
			exactMatches = append(exactMatches, ev)
		} else {
			prefixMatches = append(prefixMatches, ev)
		}
	}
	matches := exactMatches
	if len(matches) == 0 {
		matches = prefixMatches
	}
	if len(matches) == 0 {
		return nil
	}
	if len(matches) == 1 {
		return &core.LiveEventRef{ID: matches[0].ID, Feed: matches[0].Feed}
	}
	// Multiple live events match the same network: only link when exactly one
	// features a viewer-favourited competitor or ranking entity.
	if len(favKeys) == 0 {
		return nil
	}
	var favMatches []core.LiveEvent
	for _, ev := range matches {
		if eventMatchesAnyFavoriteKey(ev, favKeys) {
			favMatches = append(favMatches, ev)
		}
	}
	if len(favMatches) == 1 {
		return &core.LiveEventRef{ID: favMatches[0].ID, Feed: favMatches[0].Feed}
	}
	return nil
}

func eventMatchesAnyFavoriteKey(ev core.LiveEvent, favKeys []string) bool {
	pluginID, feedID, _, ok := core.ParseLiveEventID(ev.ID)
	if !ok {
		return false
	}
	for _, fk := range favKeys {
		fp, ff, _, fEntityID, fok := core.ParseLiveEntityKey(fk)
		if !fok || fp != pluginID || ff != feedID {
			continue
		}
		for _, c := range ev.Competitors {
			if c.EntityID != "" && c.EntityID == fEntityID {
				return true
			}
		}
		for _, r := range ev.Ranking {
			if r.EntityID != "" && r.EntityID == fEntityID {
				return true
			}
		}
	}
	return false
}
