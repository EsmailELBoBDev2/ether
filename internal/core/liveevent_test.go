package core

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestLiveEventNamespaceAndParse(t *testing.T) {
	id := NamespaceLiveEventID("community.acme.feed", "scores", "evt-42")
	if id != "community.acme.feed:scores:evt-42" {
		t.Fatalf("unexpected namespaced id: %q", id)
	}
	p, f, e, ok := ParseLiveEventID(id)
	if !ok || p != "community.acme.feed" || f != "scores" || e != "evt-42" {
		t.Fatalf("parse failed: ok=%v p=%q f=%q e=%q", ok, p, f, e)
	}
	// Plugin-scoped event IDs containing colons remain intact.
	p, f, e, ok = ParseLiveEventID("community.acme.feed:scores:league:99")
	if !ok || p != "community.acme.feed" || f != "scores" || e != "league:99" {
		t.Fatalf("parse with colon in event id failed: ok=%v p=%q f=%q e=%q", ok, p, f, e)
	}
	for _, bad := range []string{"", "a:b", ":feed:id", "plug::id", "plug:feed:", "a/b:feed:id", "plug:f/g:id"} {
		if _, _, _, ok := ParseLiveEventID(bad); ok {
			t.Fatalf("expected %q to be rejected", bad)
		}
	}
}

func TestLiveEntityKeyNamespaceAndParse(t *testing.T) {
	key := NamespaceLiveEntityKey("community.acme.feed", "scores", "team", "home-1")
	if key != "community.acme.feed:scores:team:home-1" {
		t.Fatalf("unexpected entity key: %q", key)
	}
	p, f, k, id, ok := ParseLiveEntityKey(key)
	if !ok || p != "community.acme.feed" || f != "scores" || k != "team" || id != "home-1" {
		t.Fatalf("parse entity key failed: ok=%v p=%q f=%q k=%q id=%q", ok, p, f, k, id)
	}
	for _, bad := range []string{"", "a:b:c", "a:b:c:", "a::c:d", "a:b::d", "a/x:b:c:d"} {
		if _, _, _, _, ok := ParseLiveEntityKey(bad); ok {
			t.Fatalf("expected %q to be rejected", bad)
		}
	}
}

func TestLiveEventDetailJSONAndClone(t *testing.T) {
	orig := LiveEventDetail{
		LiveEvent: LiveEvent{
			ID:       "community.acme.feed:main:101",
			Feed:     "main",
			Category: "ball",
			League:   LiveEventLeague{ID: "top", Name: "Top League", Abbr: "TL", Logo: "https://example.invalid/tl.png"},
			Title:    "Alpha vs Beta",
			Start:    "2026-11-22T18:00:00Z",
			Status:   LiveEventStatus{State: LiveEventStateIn, Detail: "P4 · 2:14", Period: 4, Clock: "2:14", Close: true},
			Competitors: []LiveEventCompetitor{
				{ID: "a", Name: "Alpha", Short: "ALP", Abbr: "ALP", Score: "24", Record: "8-3", Rank: 3, Home: false, EntityID: "team-a"},
				{ID: "b", Name: "Beta", Short: "BET", Abbr: "BET", Score: "27", Record: "9-2", Rank: 1, Home: true, Winner: false, EntityID: "team-b"},
			},
			Situation:   map[string]string{"State": "Active", "Turn": "BET"},
			Probability: &LiveEventProbability{Label: "Win", Value: 0.64, CompetitorID: "b"},
			Venue:       "Central Arena",
			Broadcasts:  []LiveEventBroadcast{{Name: "Net 1", Kind: LiveEventBroadcastTV, Region: "US"}},
			MatchHints:  &LiveEventMatchHints{Terms: []string{"Alpha", "Beta", "ALP", "BET"}, Callsigns: []string{"Net 1"}},
			UpdatedAt:   "2026-11-22T20:15:00Z",
		},
		Tables: []LiveEventTable{
			{Title: "Linescore", Columns: []string{"Side", "P1", "T"}, Rows: [][]string{{"ALP", "7", "24"}, {"BET", "10", "27"}}},
		},
		Plays: []LiveEventPlay{
			{Clock: "2:14", Period: 4, Text: "Goal scored", Emphasis: LiveEventPlayEmphasisScore, CompetitorID: "b"},
		},
		ProbabilitySeries: []LiveEventProbabilityPoint{
			{T: "2026-11-22T18:00:00Z", Value: 0.5},
			{T: "2026-11-22T20:15:00Z", Value: 0.64},
		},
		Links: []LiveEventLink{{Label: "Summary", URL: "https://example.invalid/101"}},
	}

	raw, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var decoded LiveEventDetail
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, orig) {
		t.Fatalf("round-trip mismatch:\ngot:  %+v\nwant: %+v", decoded, orig)
	}

	cloned := orig.Clone()
	cloned.Competitors[0].Score = "99"
	cloned.Situation["Turn"] = "ALP"
	cloned.Probability.Value = 0.01
	cloned.MatchHints.Terms[0] = "Mutated"
	cloned.Tables[0].Rows[0][2] = "99"
	if orig.Competitors[0].Score != "24" || orig.Situation["Turn"] != "BET" || orig.Probability.Value != 0.64 || orig.MatchHints.Terms[0] != "Alpha" || orig.Tables[0].Rows[0][2] != "24" {
		t.Fatal("Clone aliased mutable fields")
	}

	// Numeric t normalises cleanly to a string.
	var pt LiveEventProbabilityPoint
	if err := json.Unmarshal([]byte(`{"t":0.75,"value":0.62}`), &pt); err != nil {
		t.Fatal(err)
	}
	if pt.T != "0.75" || pt.Value != 0.62 {
		t.Fatalf("numeric t unmarshal: got %+v", pt)
	}
}
