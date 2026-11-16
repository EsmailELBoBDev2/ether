package libraryfilter

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plugins"
)

// PluginSQLHidden compiles the same ordered policy against plugin_items AS i.
// fields maps the rule vocabulary onto declared, indexed plugin field keys.
func (f *Filter) PluginSQLHidden(typ core.MediaType, fields map[string]string) (string, []any) {
	q, args := f.SQLHiddenForType(typ)
	return pluginSQL(q, typ, fields), args
}

func pluginSQL(q string, typ core.MediaType, fields map[string]string) string {
	q = strings.ReplaceAll(q, "t.type", "'"+string(typ)+"'")
	for _, pair := range [][2]string{{"year", "year"}, {"rating", "rating"}} {
		q = strings.ReplaceAll(q, "t."+pair[0], "i."+pair[1])
	}
	for _, pair := range [][2]string{{"votes", "vote_count"}, {"popularity", "popularity"}, {"runtime", "runtime_sec"}, {"language", "language"}, {"contentRating", "content_rating_rank"}} {
		expr := "0"
		column := "v_int + pf.v_real"
		if pair[0] == "language" {
			expr = "''"
			column = "v_text"
		}
		if key := fields[pair[0]]; key != "" {
			expr = "COALESCE((SELECT pf." + column + " FROM plugin_item_fields pf WHERE pf.item_ref=i.item_ref AND pf.field='" + strings.ReplaceAll(key, "'", "''") + "' LIMIT 1)," + expr + ")"
		}
		q = strings.ReplaceAll(q, "t."+pair[1], expr)
	}
	for _, pair := range [][2]string{{"genre", "genres"}, {"studio", "studios"}, {"country", "countries"}, {"keyword", "keywords"}} {
		key := strings.ReplaceAll(fields[pair[0]], "'", "''")
		for _, alias := range []string{"lf", "co"} {
			q = strings.ReplaceAll(q, "FROM catalog_"+pair[1]+" "+alias+" WHERE "+alias+".key=t.key", "FROM plugin_item_fields "+alias+" WHERE "+alias+".item_ref=i.item_ref AND "+alias+".field='"+key+"'")
			q = strings.ReplaceAll(q, alias+"."+pair[0], alias+".v_text")
		}
	}
	return q
}

// PluginCatchCountsSQL counts the first matching rule for each plugin item.
func (f *Filter) PluginCatchCountsSQL(typ core.MediaType, fields map[string]string) (string, []any) {
	q, args := f.CatchCountsSQL()
	return pluginSQL(q, typ, fields), args
}

// CompilePluginPolicy rejects catalogue-only conditions instead of silently
// treating an unsupported plugin condition as a non-match.
func CompilePluginPolicy(specs []core.LibraryExclusion, fallback string, declarations ...plugins.MediaKindMeta) (*Filter, error) {
	var browse BrowseCompiler
	if len(declarations) > 0 {
		decl := declarations[0]
		types := map[string]string{}
		for _, field := range decl.Fields {
			if field.Indexed {
				types[field.Key] = field.Type
			}
		}
		browse = func(key string, raw json.RawMessage) (string, []any, error) {
			valid := envelopeFacet(key)
			for _, field := range decl.Fields {
				if field.Key == key && field.Indexed {
					valid = true
					break
				}
			}
			if !valid {
				return "", nil, fmt.Errorf("unknown browse condition %q", key)
			}
			values, err := textValues(raw)
			if err != nil { // Facet values travel as text, including numeric/bool fields.
				var scalar any
				if json.Unmarshal(raw, &scalar) != nil || scalar == nil {
					return "", nil, fmt.Errorf("%s needs a facet value", key)
				}
				switch scalar.(type) {
				case float64, bool:
					values = []string{fmt.Sprint(scalar)}
				default:
					return "", nil, err
				}
			}
			if len(values) == 0 || len(values) > 100 {
				return "", nil, fmt.Errorf("%s needs 1–100 values", key)
			}
			for _, value := range values {
				if strings.TrimSpace(value) == "" {
					return "", nil, fmt.Errorf("%s needs a facet value", key)
				}
			}
			var where strings.Builder
			var args []any
			AppendPluginFacetClauses(&where, &args, map[string][]string{key: values}, types)
			return strings.TrimPrefix(where.String(), " AND "), args, nil
		}
	}
	f, err := compilePolicy(specs, fallback, browse)
	if err != nil {
		return nil, err
	}
	var validate func(condition) error
	validate = func(c condition) error {
		if c.predicate != nil && c.predicateSQL == "" {
			return fmt.Errorf("condition %q is only available for catalogue sources", c.field)
		}
		for _, child := range c.children {
			if err := validate(child); err != nil {
				return err
			}
		}
		return nil
	}
	for _, g := range f.rules {
		for _, c := range g.rules {
			if err := validate(c); err != nil {
				return nil, err
			}
		}
	}
	return f, nil
}
