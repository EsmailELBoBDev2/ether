package newznab

import (
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
)

func TestQueryTitlesDropPunctuationAndFoldAccents(t *testing.T) {
	got := queryTitles(core.SearchRequest{Title: "O Senhor dos Anéis: A Sociedade do Anel", OriginalTitle: "The Lord of the Rings: The Fellowship of the Ring"})
	want := map[string]bool{"O Senhor dos Anéis A Sociedade do Anel": true, "O Senhor dos Aneis A Sociedade do Anel": true, "The Lord of the Rings The Fellowship of the Ring": true}
	for w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
			}
		}
		if !found {
			t.Fatalf("queryTitles = %q, missing %q", got, w)
		}
	}
	for _, g := range got {
		for _, r := range g {
			if r == ':' {
				t.Fatalf("query %q keeps a colon", g)
			}
		}
	}
}
