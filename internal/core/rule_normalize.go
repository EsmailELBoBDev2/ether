package core

import (
	"bytes"
	"encoding/json"
	"strings"
)

// NormalizeRuleMediaType accepts historical labels while preserving unknown
// values so validation can report them instead of silently changing meaning.
func NormalizeRuleMediaType(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "movie", "movies", "film", "films":
		return "movie"
	case "show", "shows", "tv", "tv shows", "series":
		return "show"
	case "mixed":
		return "mixed"
	}
	return s
}

// NormalizeRuleDocument repairs only media-type fields, never names or search
// text. Unknown fields and number precision survive the data migration.
func NormalizeRuleDocument(raw []byte) ([]byte, bool) {
	var doc any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if !json.Valid(raw) || dec.Decode(&doc) != nil {
		return raw, false
	}
	changed := false
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case []any:
			for _, child := range v {
				walk(child)
			}
		case map[string]any:
			for k, child := range v {
				if s, ok := child.(string); ok && (k == "mediaType" || (k == "value" && v["field"] == "browse:type")) {
					if canonical := NormalizeRuleMediaType(s); canonical != s {
						v[k] = canonical
						changed = true
					}
				}
				walk(child)
			}
		}
	}
	walk(doc)
	if !changed {
		return raw, false
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return raw, false
	}
	return out, true
}

// NormalizeLibraryRules returns canonical rules, retaining malformed values
// unchanged so the compiler can reject them.
func NormalizeLibraryRules(rules []LibraryExclusion) []LibraryExclusion {
	raw, err := json.Marshal(rules)
	if err != nil {
		return rules
	}
	raw, _ = NormalizeRuleDocument(raw)
	var out []LibraryExclusion
	_ = json.Unmarshal(raw, &out)
	return out
}
