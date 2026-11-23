package pluginitems

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/reqmemo"
)

const (
	migration17SQLiteDDL = `
ALTER TABLE plugin_item_field_tally ADD COLUMN v_lower TEXT NOT NULL DEFAULT '';
ALTER TABLE plugin_item_field_tally ADD COLUMN v_fold  TEXT NOT NULL DEFAULT '';
CREATE TABLE plugin_item_person_tally (
	library_id  INTEGER NOT NULL,
	level       TEXT    NOT NULL,
	field       TEXT    NOT NULL,
	v_int       INTEGER NOT NULL,
	label       TEXT    NOT NULL,
	label_lower TEXT    NOT NULL DEFAULT '',
	label_fold  TEXT    NOT NULL DEFAULT '',
	cnt         INTEGER NOT NULL,
	PRIMARY KEY (library_id, level, field, v_int)
);
`
	migration17PostgresDDL = `
ALTER TABLE plugin_item_field_tally ADD COLUMN v_lower TEXT COLLATE "C" NOT NULL DEFAULT '';
ALTER TABLE plugin_item_field_tally ADD COLUMN v_fold  TEXT COLLATE "C" NOT NULL DEFAULT '';
CREATE TABLE plugin_item_person_tally (
	library_id  BIGINT           NOT NULL,
	level       TEXT COLLATE "C" NOT NULL,
	field       TEXT COLLATE "C" NOT NULL,
	v_int       BIGINT           NOT NULL,
	label       TEXT COLLATE "C" NOT NULL,
	label_lower TEXT COLLATE "C" NOT NULL DEFAULT '',
	label_fold  TEXT COLLATE "C" NOT NULL DEFAULT '',
	cnt         BIGINT           NOT NULL,
	PRIMARY KEY (library_id, level, field, v_int)
);
`
	migration17IndexesDDL = `
DROP INDEX IF EXISTS plugin_item_field_tally_count;
CREATE INDEX plugin_item_field_tally_count
	ON plugin_item_field_tally(library_id, level, field, cnt DESC, v_lower, v_text, v_fold);
CREATE INDEX plugin_item_field_tally_name
	ON plugin_item_field_tally(library_id, level, field, v_lower, v_text, cnt, v_fold);
CREATE INDEX plugin_item_person_tally_name
	ON plugin_item_person_tally(library_id, level, field, label_lower, label, v_int, cnt, label_fold);
CREATE INDEX plugin_item_person_tally_count
	ON plugin_item_person_tally(library_id, level, field, cnt DESC, label_lower, label, v_int, label_fold);
`
)

func applyMigration17SQLite(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, migration17SQLiteDDL); err != nil {
		return err
	}
	if err := backfillFacetTalliesTx(ctx, tx, false); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, migration17IndexesDDL)
	return err
}

func applyMigration17Postgres(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, migration17PostgresDDL); err != nil {
		return err
	}
	if err := backfillFacetTalliesTx(ctx, tx, true); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, migration17IndexesDDL)
	return err
}

func backfillFacetTalliesTx(ctx context.Context, tx *sql.Tx, isPG bool) error {
	if err := backfillFieldTallyCaseTx(ctx, tx, 0); err != nil {
		return err
	}
	return backfillPersonTallyTx(ctx, tx, isPG, 0)
}

// backfillFieldTallyCaseTx populates v_lower and v_fold on plugin_item_field_tally
// in bounded keyset batches. libraryID == 0 backfills all libraries.
func backfillFieldTallyCaseTx(ctx context.Context, tx *sql.Tx, libraryID int64) error {
	const batchSize = 2000
	type rowKey struct {
		libID int64
		level string
		field string
		text  string
	}
	var (
		hasCursor bool
		curLib    int64
		curLevel  string
		curField  string
		curText   string
	)
	for {
		var (
			rows *sql.Rows
			err  error
		)
		if libraryID > 0 {
			if !hasCursor {
				rows, err = tx.QueryContext(ctx, `
SELECT library_id, level, field, v_text
FROM plugin_item_field_tally
WHERE library_id = ?
ORDER BY level ASC, field ASC, v_text ASC
LIMIT ?`, libraryID, batchSize)
			} else {
				rows, err = tx.QueryContext(ctx, `
SELECT library_id, level, field, v_text
FROM plugin_item_field_tally
WHERE library_id = ? AND (level, field, v_text) > (?, ?, ?)
ORDER BY level ASC, field ASC, v_text ASC
LIMIT ?`, libraryID, curLevel, curField, curText, batchSize)
			}
		} else {
			if !hasCursor {
				rows, err = tx.QueryContext(ctx, `
SELECT library_id, level, field, v_text
FROM plugin_item_field_tally
ORDER BY library_id ASC, level ASC, field ASC, v_text ASC
LIMIT ?`, batchSize)
			} else {
				rows, err = tx.QueryContext(ctx, `
SELECT library_id, level, field, v_text
FROM plugin_item_field_tally
WHERE (library_id, level, field, v_text) > (?, ?, ?, ?)
ORDER BY library_id ASC, level ASC, field ASC, v_text ASC
LIMIT ?`, curLib, curLevel, curField, curText, batchSize)
			}
		}
		if err != nil {
			return err
		}
		batch := make([]rowKey, 0, batchSize)
		for rows.Next() {
			var k rowKey
			if err := rows.Scan(&k.libID, &k.level, &k.field, &k.text); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, k)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(batch) == 0 {
			break
		}
		for _, k := range batch {
			if _, err := tx.ExecContext(ctx, `
UPDATE plugin_item_field_tally
SET v_lower = ?, v_fold = ?
WHERE library_id = ? AND level = ? AND field = ? AND v_text = ?`,
				strings.ToLower(k.text), FoldFieldText(k.text),
				k.libID, k.level, k.field, k.text); err != nil {
				return err
			}
		}
		last := batch[len(batch)-1]
		hasCursor = true
		curLib, curLevel, curField, curText = last.libID, last.level, last.field, last.text
		if len(batch) < batchSize {
			break
		}
	}
	return nil
}

// backfillPersonTallyTx rebuilds plugin_item_person_tally from plugin_item_fields
// joined with plugin_items. libraryID == 0 backfills all libraries.
func backfillPersonTallyTx(ctx context.Context, tx *sql.Tx, isPG bool, libraryID int64) error {
	minPID := core.PluginPersonIDBase
	maxPID := core.PluginPersonIDBase + core.PluginPersonIDSpan
	minExpr := `MIN(f.v_text)`
	if isPG {
		minExpr = `MIN(f.v_text COLLATE "C")`
	}

	var (
		where strings.Builder
		args  []any
	)
	where.WriteString(`f.v_int <> 0 AND f.v_int > ? AND f.v_int < ? AND f.v_text <> '' AND i.level <> ''`)
	args = append(args, minPID, maxPID)
	if libraryID > 0 {
		where.WriteString(` AND i.library_id = ?`)
		args = append(args, libraryID)
	}

	rows, err := tx.QueryContext(ctx, `
SELECT i.library_id, i.level, f.field, f.v_int, `+minExpr+`, COUNT(*)
FROM plugin_item_fields f
JOIN plugin_items i ON i.item_ref = f.item_ref
WHERE `+where.String()+`
GROUP BY i.library_id, i.level, f.field, f.v_int`, args...)
	if err != nil {
		return err
	}

	type perLevelRow struct {
		libID int64
		level string
		field string
		vInt  int64
		label string
		cnt   int64
	}
	type aggKey struct {
		libID int64
		field string
		vInt  int64
	}
	type aggVal struct {
		label string
		cnt   int64
	}
	var perLevel []perLevelRow
	agg := map[aggKey]*aggVal{}
	for rows.Next() {
		var r perLevelRow
		if err := rows.Scan(&r.libID, &r.level, &r.field, &r.vInt, &r.label, &r.cnt); err != nil {
			rows.Close()
			return err
		}
		if r.label == "" || r.cnt <= 0 {
			continue
		}
		perLevel = append(perLevel, r)
		ak := aggKey{libID: r.libID, field: r.field, vInt: r.vInt}
		if cur, ok := agg[ak]; ok {
			cur.cnt += r.cnt
			if r.label < cur.label {
				cur.label = r.label
			}
		} else {
			agg[ak] = &aggVal{label: r.label, cnt: r.cnt}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, r := range perLevel {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO plugin_item_person_tally(library_id, level, field, v_int, label, label_lower, label_fold, cnt)
VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
			r.libID, r.level, r.field, r.vInt, r.label, strings.ToLower(r.label), FoldFieldText(r.label), r.cnt); err != nil {
			return err
		}
	}
	if len(agg) == 0 {
		return nil
	}
	aggKeys := make([]aggKey, 0, len(agg))
	for k := range agg {
		aggKeys = append(aggKeys, k)
	}
	slices.SortFunc(aggKeys, func(a, b aggKey) int {
		if a.libID != b.libID {
			if a.libID < b.libID {
				return -1
			}
			return 1
		}
		if c := strings.Compare(a.field, b.field); c != 0 {
			return c
		}
		if a.vInt < b.vInt {
			return -1
		}
		if a.vInt > b.vInt {
			return 1
		}
		return 0
	})
	for _, k := range aggKeys {
		v := agg[k]
		if _, err := tx.ExecContext(ctx, `
INSERT INTO plugin_item_person_tally(library_id, level, field, v_int, label, label_lower, label_fold, cnt)
VALUES(?, '', ?, ?, ?, ?, ?, ?)`,
			k.libID, k.field, k.vInt, v.label, strings.ToLower(v.label), FoldFieldText(v.label), v.cnt); err != nil {
			return err
		}
	}
	return nil
}

type personTallyKey struct {
	level string
	field string
	vInt  int64
}

type personTallyChange struct {
	delta        int64
	minAdded     string
	removedTexts map[string]struct{}
}

func addPersonTallyDelta(delta map[personTallyKey]*personTallyChange, level, field, text string, vInt, d int64) {
	if level == "" || field == "" || text == "" || !core.IsPluginPersonID(vInt) || d == 0 {
		return
	}
	for _, lv := range [2]string{level, ""} {
		k := personTallyKey{level: lv, field: field, vInt: vInt}
		ch := delta[k]
		if ch == nil {
			ch = &personTallyChange{}
			delta[k] = ch
		}
		ch.delta += d
		if d > 0 {
			if ch.minAdded == "" || text < ch.minAdded {
				ch.minAdded = text
			}
		} else {
			if ch.removedTexts == nil {
				ch.removedTexts = map[string]struct{}{}
			}
			ch.removedTexts[text] = struct{}{}
		}
	}
}

func applyPersonTallyDeltaTx(ctx context.Context, tx *sql.Tx, isPG bool, libraryID core.LibraryID, delta map[personTallyKey]*personTallyChange, emptiedText map[fieldTallyKey]bool) error {
	if len(delta) == 0 || libraryID <= 0 {
		return nil
	}
	keys := make([]personTallyKey, 0, len(delta))
	for k, ch := range delta {
		if ch.delta != 0 || ch.minAdded != "" || len(ch.removedTexts) > 0 {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	slices.SortFunc(keys, func(a, b personTallyKey) int {
		if c := strings.Compare(a.level, b.level); c != 0 {
			return c
		}
		if c := strings.Compare(a.field, b.field); c != 0 {
			return c
		}
		if a.vInt < b.vInt {
			return -1
		}
		if a.vInt > b.vInt {
			return 1
		}
		return 0
	})
	libID := int64(libraryID)
	minExpr := `MIN(f.v_text)`
	libCheck := `i.library_id + 0 = ?`
	if isPG {
		minExpr = `MIN(f.v_text COLLATE "C")`
		libCheck = `i.library_id = ?`
	}

	for _, k := range keys {
		ch := delta[k]
		deleted := false
		if ch.delta > 0 {
			lbl := ch.minAdded
			if _, err := tx.ExecContext(ctx, `
INSERT INTO plugin_item_person_tally(library_id, level, field, v_int, label, label_lower, label_fold, cnt)
VALUES(?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(library_id, level, field, v_int) DO UPDATE SET
	cnt = plugin_item_person_tally.cnt + excluded.cnt,
	label = CASE WHEN excluded.label < plugin_item_person_tally.label THEN excluded.label ELSE plugin_item_person_tally.label END,
	label_lower = CASE WHEN excluded.label < plugin_item_person_tally.label THEN excluded.label_lower ELSE plugin_item_person_tally.label_lower END,
	label_fold = CASE WHEN excluded.label < plugin_item_person_tally.label THEN excluded.label_fold ELSE plugin_item_person_tally.label_fold END`,
				libID, k.level, k.field, k.vInt, lbl, strings.ToLower(lbl), FoldFieldText(lbl), ch.delta); err != nil {
				return err
			}
		} else if ch.delta < 0 {
			if _, err := tx.ExecContext(ctx, `
UPDATE plugin_item_person_tally
SET cnt = cnt + ?
WHERE library_id = ? AND level = ? AND field = ? AND v_int = ?`,
				ch.delta, libID, k.level, k.field, k.vInt); err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx, `
DELETE FROM plugin_item_person_tally
WHERE library_id = ? AND level = ? AND field = ? AND v_int = ? AND cnt <= 0`,
				libID, k.level, k.field, k.vInt)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				deleted = true
			}
		}
		if deleted {
			continue
		}

		needsRefresh := false
		for rt := range ch.removedTexts {
			if emptiedText[fieldTallyKey{level: k.level, field: k.field, text: rt}] {
				needsRefresh = true
				break
			}
		}
		if needsRefresh {
			var newLabel sql.NullString
			var err error
			if k.level != "" {
				err = tx.QueryRowContext(ctx, `
SELECT `+minExpr+`
FROM plugin_item_fields f
JOIN plugin_items i ON i.item_ref = f.item_ref
WHERE f.field = ? AND f.v_int <> 0 AND f.v_int = ? AND `+libCheck+` AND i.level = ? AND f.v_text <> ''`,
					k.field, k.vInt, libID, k.level).Scan(&newLabel)
			} else {
				err = tx.QueryRowContext(ctx, `
SELECT `+minExpr+`
FROM plugin_item_fields f
JOIN plugin_items i ON i.item_ref = f.item_ref
WHERE f.field = ? AND f.v_int <> 0 AND f.v_int = ? AND `+libCheck+` AND i.level <> '' AND f.v_text <> ''`,
					k.field, k.vInt, libID).Scan(&newLabel)
			}
			if err != nil {
				return err
			}
			if !newLabel.Valid || newLabel.String == "" {
				if _, err := tx.ExecContext(ctx, `
DELETE FROM plugin_item_person_tally
WHERE library_id = ? AND level = ? AND field = ? AND v_int = ?`,
					libID, k.level, k.field, k.vInt); err != nil {
					return err
				}
				continue
			}
			lbl := newLabel.String
			if _, err := tx.ExecContext(ctx, `
UPDATE plugin_item_person_tally
SET label = ?, label_lower = ?, label_fold = ?
WHERE library_id = ? AND level = ? AND field = ? AND v_int = ?`,
				lbl, strings.ToLower(lbl), FoldFieldText(lbl), libID, k.level, k.field, k.vInt); err != nil {
				return err
			}
		} else if ch.delta <= 0 && ch.minAdded != "" {
			lbl := ch.minAdded
			if _, err := tx.ExecContext(ctx, `
UPDATE plugin_item_person_tally
SET label = ?, label_lower = ?, label_fold = ?
WHERE library_id = ? AND level = ? AND field = ? AND v_int = ? AND ? < label`,
				lbl, strings.ToLower(lbl), FoldFieldText(lbl), libID, k.level, k.field, k.vInt, lbl); err != nil {
				return err
			}
		}
	}
	return nil
}

// libraryTallyState holds the per-level item counts read from plugin_item_tally
// for one library, along with whether the tally tables are populated for it.
// Active is false only for raw-SQL fixtures that seed plugin_items without
// going through Service write paths.
type libraryTallyState struct {
	active      bool
	libTotal    int
	levelCounts map[string]int
}

func (st libraryTallyState) countFor(level string) int {
	if level == "" {
		return st.libTotal
	}
	return st.levelCounts[level]
}

// isSingleLevel reports whether every indexed item in the library belongs to
// level, allowing sort-index scans on library_id to skip redundant level checks.
func (st libraryTallyState) isSingleLevel(level string) bool {
	return st.active && level != "" && st.libTotal > 0 && st.levelCounts[level] == st.libTotal
}

// libraryTally returns the cached per-request plugin_item_tally summary for
// libraryID. Results are memoized on ctx keyed by s.writeGen so repeated calls
// in the same request (e.g. across declared shelves) issue one query total.
func (s *Service) libraryTally(ctx context.Context, libraryID core.LibraryID) (libraryTallyState, error) {
	if s == nil || s.db == nil || libraryID <= 0 {
		return libraryTallyState{}, nil
	}
	key := fmt.Sprintf("pluginitems:libtally:%p:%d:%d", s, s.writeGen.Load(), libraryID)
	return reqmemo.Do(ctx, key, func() (libraryTallyState, error) {
		rows, err := s.db.QueryContext(ctx,
			`SELECT level, n FROM plugin_item_tally WHERE library_id = ?`,
			int64(libraryID))
		if err != nil {
			return libraryTallyState{}, err
		}
		st := libraryTallyState{levelCounts: map[string]int{}}
		for rows.Next() {
			var lv string
			var n int
			if err := rows.Scan(&lv, &n); err != nil {
				rows.Close()
				return libraryTallyState{}, err
			}
			if n > 0 {
				st.levelCounts[lv] = n
				st.libTotal += n
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return libraryTallyState{}, err
		}
		rows.Close()

		if st.libTotal > 0 {
			st.active = true
			return st, nil
		}
		var hasAny int
		if err := s.db.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM plugin_items WHERE library_id = ?)`,
			int64(libraryID)).Scan(&hasAny); err != nil {
			return libraryTallyState{}, err
		}
		st.active = hasAny == 0
		return st, nil
	})
}
