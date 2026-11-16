package libraryfilter

import (
	"math"
	"sort"
	"strconv"
	"strings"
)

func envelopeFacet(key string) bool { return key == "year" || key == "rating" || key == "released" }

// AppendPluginFacetClauses appends ` AND EXISTS (...)` subqueries on plugin_item_fields
// for each facet key to where.
func AppendPluginFacetClauses(where *strings.Builder, args *[]any, facets map[string][]string, types map[string]string) {
	if len(facets) == 0 {
		return
	}
	keys := make([]string, 0, len(facets))
	for k := range facets {
		if strings.TrimSpace(k) != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	for _, key := range keys {
		rawVals := facets[key]
		var cleanVals []string
		for _, v := range rawVals {
			v = strings.TrimSpace(v)
			if v != "" {
				cleanVals = append(cleanVals, v)
			}
		}
		if len(cleanVals) == 0 {
			continue
		}

		if envelopeFacet(key) {
			if len(cleanVals) == 1 && strings.Contains(cleanVals[0], "..") {
				parts := strings.SplitN(cleanVals[0], "..", 2)
				lo, e1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
				hi, e2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
				if e1 == nil && e2 == nil {
					where.WriteString(` AND i.` + key + ` BETWEEN ? AND ?`)
					*args = append(*args, lo, hi)
					continue
				}
			}
			ph := make([]string, len(cleanVals))
			for n, value := range cleanVals {
				ph[n] = "?"
				number, err := strconv.ParseFloat(value, 64)
				if err != nil {
					where.WriteString(` AND 0=1`)
					number = 0
				}
				*args = append(*args, number)
			}
			where.WriteString(` AND i.` + key + ` IN (` + strings.Join(ph, ",") + `)`)
			continue
		}

		// Support integer range filters encoded as "min..max".
		if (types[key] == "" || types[key] == "int" || types[key] == "date" || types[key] == "duration") && len(cleanVals) == 1 && strings.Contains(cleanVals[0], "..") {
			parts := strings.SplitN(cleanVals[0], "..", 2)
			minVal, err1 := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
			maxVal, err2 := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
			if err1 == nil && err2 == nil {
				where.WriteString(` AND EXISTS (SELECT 1 FROM plugin_item_fields f WHERE f.item_ref = i.item_ref AND f.field = ? AND f.v_int BETWEEN ? AND ?)`)
				*args = append(*args, key, minVal, maxVal)
				continue
			}
		}

		column := "v_text"
		switch types[key] {
		case "int", "date", "duration":
			column = "v_int"
		case "float":
			column = "v_real"
		}
		placeholders := make([]string, len(cleanVals))
		*args = append(*args, key)
		for idx, v := range cleanVals {
			placeholders[idx] = "?"
			var value any = v
			if column == "v_int" {
				n, err := strconv.ParseInt(v, 10, 64)
				if err != nil {
					where.WriteString(" AND 0=1")
				}
				value = n
			} else if column == "v_real" {
				n, err := strconv.ParseFloat(v, 64)
				if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
					where.WriteString(" AND 0=1")
					n = 0
				}
				value = n
			}
			*args = append(*args, value)
		}
		where.WriteString(` AND EXISTS (SELECT 1 FROM plugin_item_fields f WHERE f.item_ref = i.item_ref AND f.field = ? AND f.` + column + ` IN (` + strings.Join(placeholders, ", ") + `))`)
	}
}
