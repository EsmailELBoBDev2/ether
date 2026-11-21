package pluginitems

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plugins"
)

func TestProjectFactsResolveI18nFieldLabelsAndKeepCleanSummary(t *testing.T) {
	decl := plugins.MediaKindMeta{
		Key:   "book",
		Label: "Books",
		Media: string(core.MediaClassBook),
		Levels: []plugins.KindLevel{
			{Key: "book", Label: "Book", Role: string(core.MediaRoleBook), Playable: true},
		},
		Fields: []plugins.KindField{
			{Key: "authors", Type: "people", Label: "$t:field.authors"},
			{Key: "publisher", Type: "string", Label: "$t:field.publisher"},
			{Key: "imprint", Type: "string", Label: "$t:field.missing"},
			{Key: "language", Type: "string", Label: "$t:field.recursive"},
			{Key: "pages", Type: "int", Label: "Pages"},
			{Key: "isbn", Type: "string", Label: ""},
		},
	}
	item := core.KindItem{
		ID:      "b1",
		Level:   "book",
		Title:   "The Left Hand of Darkness",
		Summary: "A classic novel.",
		Fields: map[string]json.RawMessage{
			"authors":   json.RawMessage(`["Ursula K. Le Guin"]`),
			"publisher": json.RawMessage(`"Ace Books"`),
			"imprint":   json.RawMessage(`"Ace Science Fiction"`),
			"language":  json.RawMessage(`"English"`),
			"pages":     json.RawMessage(`304`),
			"isbn":      json.RawMessage(`"978-0441478125"`),
		},
	}

	strs := map[string]string{
		"field.authors":   "Authors",
		"field.publisher": "Publisher",
		"field.recursive": "$t:should_never_leak",
	}
	resolve := func(key string) (string, bool) {
		v, ok := strs[key]
		return v, ok
	}

	got := ProjectWithText(item, decl, core.Presentations{}, resolve)
	if got.Summary != "A classic novel." {
		t.Fatalf("Summary should remain clean prose, got %q", got.Summary)
	}
	if len(got.MediaCredits) != 1 || got.MediaCredits[0].Name != "Ursula K. Le Guin" || got.MediaCredits[0].Role != "author" {
		t.Fatalf("MediaCredits = %+v", got.MediaCredits)
	}

	facts := map[string]string{}
	for _, f := range got.MediaFacts {
		if strings.Contains(f.Label, "$t:") || strings.Contains(f.Value, "$t:") {
			t.Fatalf("MediaFacts leaked raw $t: token: %+v", f)
		}
		facts[f.Label] = f.Value
	}
	wantFacts := map[string]string{
		"Publisher": "Ace Books",
		"imprint":   "Ace Science Fiction",
		"language":  "English",
		"Pages":     "304",
		"isbn":      "978-0441478125",
	}
	for k, want := range wantFacts {
		if facts[k] != want {
			t.Errorf("MediaFacts[%q] = %q, want %q (all=%+v)", k, facts[k], want, got.MediaFacts)
		}
	}
	if _, hasAuthors := facts["Authors"]; hasAuthors {
		t.Errorf("people credit field should not be duplicated in MediaFacts: %+v", got.MediaFacts)
	}

	// Calling Project without a resolver must still never emit "$t:" and must
	// fall back to the field key.
	noResolver := Project(item, decl, core.Presentations{})
	if noResolver.Summary != "A classic novel." {
		t.Fatalf("Project(nil resolver) summary = %q", noResolver.Summary)
	}
	foundPublisherFallback := false
	for _, f := range noResolver.MediaFacts {
		if strings.Contains(f.Label, "$t:") {
			t.Fatalf("Project(nil resolver) leaked raw $t: token: %+v", f)
		}
		if f.Label == "publisher" && f.Value == "Ace Books" {
			foundPublisherFallback = true
		}
	}
	if !foundPublisherFallback {
		t.Fatalf("Project(nil resolver) must fall back to field key for publisher, got %+v", noResolver.MediaFacts)
	}
}
