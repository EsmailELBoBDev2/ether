package pluginitems

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
)

func sqliteIndexFTS(ctx context.Context, tx *sql.Tx, seq int64, ref core.ItemRef, libraryID core.LibraryID, title, fieldsText, peopleText string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_item_fts WHERE rowid = ?`, seq); err != nil {
		return err
	}
	title = strings.TrimSpace(title)
	fieldsText = strings.TrimSpace(fieldsText)
	peopleText = strings.TrimSpace(peopleText)
	if title == "" && fieldsText == "" && peopleText == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO plugin_item_fts(rowid, item_ref, library_id, title, fields, people) VALUES(?, ?, ?, ?, ?, ?)`,
		seq, string(ref), int64(libraryID), title, fieldsText, peopleText)
	return err
}

// sqliteReindexFTSIfChanged rewrites an existing item's FTS row only when
// its stored text differs. Replacing an unchanged row deletes and re-adds
// every one of its tokens in the FTS index inside the write transaction.
func sqliteReindexFTSIfChanged(ctx context.Context, tx *sql.Tx, seq int64, ref core.ItemRef, libraryID core.LibraryID, title, fieldsText, peopleText string) error {
	var oldRef, oldTitle, oldFields, oldPeople string
	var oldLib int64
	err := tx.QueryRowContext(ctx, `SELECT item_ref, library_id, title, fields, people FROM plugin_item_fts WHERE rowid = ?`, seq).
		Scan(&oldRef, &oldLib, &oldTitle, &oldFields, &oldPeople)
	title, fieldsText, peopleText = strings.TrimSpace(title), strings.TrimSpace(fieldsText), strings.TrimSpace(peopleText)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if title == "" && fieldsText == "" && peopleText == "" {
			return nil
		}
	case err != nil:
		return err
	case oldRef == string(ref) && oldLib == int64(libraryID) && oldTitle == title && oldFields == fieldsText && oldPeople == peopleText:
		return nil
	}
	return sqliteIndexFTS(ctx, tx, seq, ref, libraryID, title, fieldsText, peopleText)
}

func sqliteDeleteFTS(ctx context.Context, tx *sql.Tx, seq int64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM plugin_item_fts WHERE rowid = ?`, seq)
	return err
}

func (s *Service) searchSQLite(ctx context.Context, q SearchQuery, tokens []string, limit int) (BrowsePage, error) {
	parts := make([]string, 0, len(tokens))
	for i, tok := range tokens {
		part := `"` + strings.ReplaceAll(tok, `"`, `""`) + `"`
		if searchTokenPrefix(tok, i, len(tokens)) {
			part += "*"
		}
		parts = append(parts, part)
	}
	matchExpr := strings.Join(parts, " AND ")

	var where strings.Builder
	args := []any{matchExpr, int64(q.LibraryID)}
	// The unary + keeps the planner from leading with the library's title
	// index and running the FTS match once per item, which stale planner
	// statistics (a library added after the last ANALYZE) make look cheap
	// (#621). The plan always starts from the FTS matches.
	where.WriteString(`f.plugin_item_fts MATCH ? AND +i.library_id = ?`)
	if len(q.Levels) > 0 {
		placeholders := make([]string, len(q.Levels))
		for idx, lv := range q.Levels {
			placeholders[idx] = "?"
			args = append(args, strings.TrimSpace(lv))
		}
		where.WriteString(` AND i.level IN (` + strings.Join(placeholders, ",") + `)`)
	} else if q.Level != "" {
		where.WriteString(` AND i.level = ?`)
		args = append(args, q.Level)
	}
	if q.PersonID > 0 {
		ok, err := s.appendPersonFilter(ctx, q.LibraryID, &where, &args, q.PersonID)
		if err != nil {
			return BrowsePage{}, err
		}
		if !ok {
			return BrowsePage{Total: 0, TotalExact: !(q.NoTotal && !q.CheapTotal)}, nil
		}
	}
	if err := s.appendFacets(ctx, q.LibraryID, &where, &args, q.Facets, q.AllFacets, q.ExcludeFacets, q.AnyGroups); err != nil {
		return BrowsePage{}, err
	}
	viewerID := q.ViewerID
	if viewerID <= 0 {
		viewerID = core.CatalogViewerID(ctx)
	}
	if err := s.appendVisibilityForViewer(ctx, &where, &args, q.LibraryID, q.Policy, q.Hidden, viewerID); err != nil {
		return BrowsePage{}, err
	}
	if err := s.appendVisibilityForViewer(ctx, &where, &args, q.LibraryID, q.TabPolicy, false, viewerID); err != nil {
		return BrowsePage{}, err
	}

	exactTitle, exactSort, prefixTitle, prefixSort := searchTitleTierArgs(q.Query)
	querySQL := `SELECT ` + prefixedItemCols("i.") + `
FROM plugin_item_fts f
JOIN plugin_items i ON i.seq = f.rowid
WHERE ` + where.String() + `
ORDER BY
	CASE
		WHEN LOWER(i.title) = ? OR i.sort_title = ? THEN 0
		WHEN LOWER(i.title) LIKE ? ESCAPE '\' OR i.sort_title LIKE ? ESCAPE '\' THEN 1
		ELSE 2
	END ASC,
	CASE
		WHEN LOWER(i.title) = ? OR i.sort_title = ? OR LOWER(i.title) LIKE ? ESCAPE '\' OR i.sort_title LIKE ? ESCAPE '\' THEN i.rating
		ELSE 0
	END DESC,
	bm25(f.plugin_item_fts, 10.0, 2.0, 1.0) ASC,
	i.rating DESC,
	i.sort_title ASC,
	i.item_ref ASC
LIMIT ?`
	qArgs := append(append([]any(nil), args...), exactTitle, exactSort, prefixTitle, prefixSort, exactTitle, exactSort, prefixTitle, prefixSort, limit+1)
	items, err := s.queryItems(ctx, querySQL, qArgs, true)
	if err != nil {
		return BrowsePage{}, err
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}

	total, totalExact, err := s.resolveSearchTotal(ctx, q,
		`SELECT COUNT(*) FROM plugin_item_fts f JOIN plugin_items i ON i.seq = f.rowid WHERE `+where.String(),
		where.String(), args, hasMore, len(items))
	if err != nil {
		return BrowsePage{}, err
	}
	return BrowsePage{Items: items, Total: total, TotalExact: totalExact}, nil
}
