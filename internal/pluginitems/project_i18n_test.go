package pluginitems

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plugins"
)

func TestProjectSummaryResolvesI18nFieldLabelsAndNeverLeaksTKeys(t *testing.T) {
	decl := plugins.MediaKindMeta{
		Key:   "book",
		Label: "Books",
		Levels: []plugins.KindLevel{
			{Key: "book", Label: "Book", Playable: true},
		},
		Fields: []plugins.KindField{
			{Key: "authors", Type: "people", Label: "$t:field.authors"},
			{Key: "publisher", Type: "string", Label: "$t:field.missing"},
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
			"language":  json.RawMessage(`"English"`),
			"pages":     json.RawMessage(`304`),
			"isbn":      json.RawMessage(`"978-0441478125"`),
		},
	}

	strs := map[string]string{
		"field.authors":   "Authors",
		"field.recursive": "$t:should_never_leak",
	}
	resolve := func(key string) (string, bool) {
		v, ok := strs[key]
		return v, ok
	}

	got := ProjectWithText(item, decl, core.Presentations{}, resolve)
	if strings.Contains(got.Summary, "$t:") {
		t.Fatalf("Summary leaked raw $t: token: %q", got.Summary)
	}
	wantParts := []string{
		"A classic novel.",
		"Authors: Ursula K. Le Guin",
		"publisher: Ace Books",
		"language: English",
		"Pages: 304",
		"isbn: 978-0441478125",
	}
	for _, part := range wantParts {
		if !strings.Contains(got.Summary, part) {
			t.Errorf("Summary missing %q; got %q", part, got.Summary)
		}
	}

	// Calling Project without a resolver must still never emit "$t:" and must
	// fall back to the field key.
	noResolver := Project(item, decl, core.Presentations{})
	if strings.Contains(noResolver.Summary, "$t:") {
		t.Fatalf("Project(nil resolver) leaked raw $t: token: %q", noResolver.Summary)
	}
	if !strings.Contains(noResolver.Summary, "authors: Ursula K. Le Guin") {
		t.Fatalf("Project(nil resolver) must fall back to field key, got %q", noResolver.Summary)
	}
}
