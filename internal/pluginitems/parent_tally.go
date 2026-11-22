package pluginitems

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plugins"
)

// ParentCounts holds the indexed child and descendant leaf counts for one
// container item in a multi-level plugin kind, read from
// plugin_item_parent_tally in O(1) without walking descendants.
type ParentCounts struct {
	ChildCount int
	LeafCount  int
}

type parentTallyDelta struct {
	child int64
	leaf  int64
}

// ParentCountsMany returns the stored direct-child and descendant-leaf counts
// for each ref in a single library from plugin_item_parent_tally. Missing refs
// have no stored children and are omitted from the returned map.
func (s *Service) ParentCountsMany(ctx context.Context, libraryID core.LibraryID, refs []core.ItemRef) (map[core.ItemRef]ParentCounts, error) {
	if s == nil || s.db == nil || libraryID <= 0 || len(refs) == 0 {
		return nil, nil
	}
	unique := make([]core.ItemRef, 0, len(refs))
	seen := make(map[core.ItemRef]struct{}, len(refs))
	for _, r := range refs {
		if r == "" {
			continue
		}
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		unique = append(unique, r)
	}
	if len(unique) == 0 {
		return nil, nil
	}
	out := make(map[core.ItemRef]ParentCounts, len(unique))
	const chunkSize = 250
	for start := 0; start < len(unique); start += chunkSize {
		end := min(start+chunkSize, len(unique))
		chunk := unique[start:end]
		placeholders := make([]string, len(chunk))
		args := make([]any, 0, 1+len(chunk))
		args = append(args, int64(libraryID))
		for i, r := range chunk {
			placeholders[i] = "?"
			args = append(args, string(r))
		}
		rows, err := s.db.QueryContext(ctx,
			`SELECT parent_ref, child_count, leaf_count FROM plugin_item_parent_tally WHERE library_id = ? AND parent_ref IN (`+strings.Join(placeholders, ",")+`)`,
			args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var refStr string
			var childCount, leafCount int
			if err := rows.Scan(&refStr, &childCount, &leafCount); err != nil {
				rows.Close()
				return nil, err
			}
			out[core.ItemRef(refStr)] = ParentCounts{ChildCount: childCount, LeafCount: leafCount}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// parentTallyTracker computes exact plugin_item_parent_tally deltas for a
// multi-level upsert chunk while the transaction still reflects the pre-chunk
// state of plugin_items and plugin_item_parent_tally. Tracking in-tx parent
// links and leaf counts handles out-of-order inserts, reparenting, and level
// changes within a single chunk identically to sequential writes.
type parentTallyTracker struct {
	tx        *sql.Tx
	libraryID int64
	leafLevel string
	parentOf  map[core.ItemRef]core.ItemRef
	leafOf    map[core.ItemRef]int64
	delta     map[core.ItemRef]parentTallyDelta
}

func newParentTallyTracker(tx *sql.Tx, libraryID core.LibraryID, leafLevel string, existing map[core.ItemRef]existingItemMeta) *parentTallyTracker {
	parentOf := make(map[core.ItemRef]core.ItemRef, len(existing))
	for ref, ex := range existing {
		parentOf[ref] = ex.parentRef
	}
	return &parentTallyTracker{
		tx:        tx,
		libraryID: int64(libraryID),
		leafLevel: leafLevel,
		parentOf:  parentOf,
		leafOf:    make(map[core.ItemRef]int64),
		delta:     make(map[core.ItemRef]parentTallyDelta),
	}
}

func (t *parentTallyTracker) ownLeaf(level string) int64 {
	if level != "" && level == t.leafLevel {
		return 1
	}
	return 0
}

func (t *parentTallyTracker) primeParents(ctx context.Context, refs []core.ItemRef) error {
	missing := make([]core.ItemRef, 0, len(refs))
	seen := make(map[core.ItemRef]struct{}, len(refs))
	for _, r := range refs {
		if r == "" {
			continue
		}
		if _, ok := t.parentOf[r]; ok {
			continue
		}
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		missing = append(missing, r)
	}
	if len(missing) == 0 {
		return nil
	}
	const chunkSize = 250
	for start := 0; start < len(missing); start += chunkSize {
		end := min(start+chunkSize, len(missing))
		chunk := missing[start:end]
		placeholders := make([]string, len(chunk))
		args := make([]any, len(chunk))
		for i, r := range chunk {
			placeholders[i] = "?"
			args[i] = string(r)
			t.parentOf[r] = ""
		}
		rows, err := t.tx.QueryContext(ctx,
			`SELECT item_ref, parent_ref FROM plugin_items WHERE item_ref IN (`+strings.Join(placeholders, ",")+`)`,
			args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var refStr, parentStr string
			if err := rows.Scan(&refStr, &parentStr); err != nil {
				rows.Close()
				return err
			}
			t.parentOf[core.ItemRef(refStr)] = core.ItemRef(parentStr)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	return nil
}

func (t *parentTallyTracker) primeLeaves(ctx context.Context, refs []core.ItemRef) error {
	missing := make([]core.ItemRef, 0, len(refs))
	seen := make(map[core.ItemRef]struct{}, len(refs))
	for _, r := range refs {
		if r == "" {
			continue
		}
		if _, ok := t.leafOf[r]; ok {
			continue
		}
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		missing = append(missing, r)
	}
	if len(missing) == 0 {
		return nil
	}
	const chunkSize = 250
	for start := 0; start < len(missing); start += chunkSize {
		end := min(start+chunkSize, len(missing))
		chunk := missing[start:end]
		placeholders := make([]string, len(chunk))
		args := make([]any, 0, 1+len(chunk))
		args = append(args, t.libraryID)
		for i, r := range chunk {
			placeholders[i] = "?"
			args = append(args, string(r))
			t.leafOf[r] = 0
		}
		rows, err := t.tx.QueryContext(ctx,
			`SELECT parent_ref, leaf_count FROM plugin_item_parent_tally WHERE library_id = ? AND parent_ref IN (`+strings.Join(placeholders, ",")+`)`,
			args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var refStr string
			var leafCount int64
			if err := rows.Scan(&refStr, &leafCount); err != nil {
				rows.Close()
				return err
			}
			t.leafOf[core.ItemRef(refStr)] = leafCount
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	return nil
}

func (t *parentTallyTracker) parent(ctx context.Context, ref core.ItemRef) (core.ItemRef, error) {
	if ref == "" {
		return "", nil
	}
	if p, ok := t.parentOf[ref]; ok {
		return p, nil
	}
	var parentStr string
	err := t.tx.QueryRowContext(ctx, `SELECT parent_ref FROM plugin_items WHERE item_ref = ?`, string(ref)).Scan(&parentStr)
	if errors.Is(err, sql.ErrNoRows) {
		t.parentOf[ref] = ""
		return "", nil
	}
	if err != nil {
		return "", err
	}
	p := core.ItemRef(parentStr)
	t.parentOf[ref] = p
	return p, nil
}

func (t *parentTallyTracker) leafCount(ctx context.Context, ref core.ItemRef) (int64, error) {
	if ref == "" {
		return 0, nil
	}
	if n, ok := t.leafOf[ref]; ok {
		return n, nil
	}
	var leafCount int64
	err := t.tx.QueryRowContext(ctx,
		`SELECT leaf_count FROM plugin_item_parent_tally WHERE library_id = ? AND parent_ref = ?`,
		t.libraryID, string(ref)).Scan(&leafCount)
	if errors.Is(err, sql.ErrNoRows) {
		t.leafOf[ref] = 0
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	t.leafOf[ref] = leafCount
	return leafCount, nil
}

func (t *parentTallyTracker) addChild(parent core.ItemRef, d int64) {
	if parent == "" || d == 0 {
		return
	}
	cur := t.delta[parent]
	cur.child += d
	t.delta[parent] = cur
}

func (t *parentTallyTracker) addLeafAncestors(ctx context.Context, startParent, originRef core.ItemRef, d int64, skip map[core.ItemRef]struct{}) error {
	if startParent == "" || d == 0 {
		return nil
	}
	const maxParentHops = 8
	visited := make(map[core.ItemRef]struct{}, 4)
	if originRef != "" {
		visited[originRef] = struct{}{}
	}
	cur := startParent
	for hops := 0; hops < maxParentHops && cur != ""; hops++ {
		if _, seen := visited[cur]; seen {
			break
		}
		visited[cur] = struct{}{}
		if _, skipped := skip[cur]; !skipped {
			base, err := t.leafCount(ctx, cur)
			if err != nil {
				return err
			}
			t.leafOf[cur] = base + d
			curDelta := t.delta[cur]
			curDelta.leaf += d
			t.delta[cur] = curDelta
		}
		next, err := t.parent(ctx, cur)
		if err != nil {
			return err
		}
		cur = next
	}
	return nil
}

// apply records one item insert or structural update (parent or level change).
func (t *parentTallyTracker) apply(ctx context.Context, ref core.ItemRef, existed bool, oldParent, newParent core.ItemRef, oldLevel, newLevel string) error {
	if !existed {
		var childLeaves int64
		if newLevel != t.leafLevel {
			var err error
			childLeaves, err = t.leafCount(ctx, ref)
			if err != nil {
				return err
			}
		}
		sub := t.ownLeaf(newLevel) + childLeaves
		t.parentOf[ref] = newParent
		if newParent != "" {
			t.addChild(newParent, 1)
			if sub != 0 {
				if err := t.addLeafAncestors(ctx, newParent, ref, sub, nil); err != nil {
					return err
				}
			}
		}
		return nil
	}

	if oldParent == newParent && oldLevel == newLevel {
		return nil
	}
	var childLeaves int64
	if oldLevel != t.leafLevel || newLevel != t.leafLevel {
		var err error
		childLeaves, err = t.leafCount(ctx, ref)
		if err != nil {
			return err
		}
	}
	oldSub := t.ownLeaf(oldLevel) + childLeaves
	newSub := t.ownLeaf(newLevel) + childLeaves
	if oldParent == newParent {
		if newParent != "" && newSub != oldSub {
			return t.addLeafAncestors(ctx, newParent, ref, newSub-oldSub, nil)
		}
		return nil
	}
	if oldParent != "" {
		t.addChild(oldParent, -1)
		if oldSub != 0 {
			if err := t.addLeafAncestors(ctx, oldParent, ref, -oldSub, nil); err != nil {
				return err
			}
		}
	}
	t.parentOf[ref] = newParent
	if newParent != "" {
		t.addChild(newParent, 1)
		if newSub != 0 {
			if err := t.addLeafAncestors(ctx, newParent, ref, newSub, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func applyParentTallyDeltaTx(ctx context.Context, tx *sql.Tx, libraryID core.LibraryID, delta map[core.ItemRef]parentTallyDelta) error {
	if len(delta) == 0 || libraryID <= 0 {
		return nil
	}
	refs := make([]core.ItemRef, 0, len(delta))
	for r, d := range delta {
		if d.child != 0 || d.leaf != 0 {
			refs = append(refs, r)
		}
	}
	if len(refs) == 0 {
		return nil
	}
	slices.Sort(refs)
	libID := int64(libraryID)
	for _, r := range refs {
		d := delta[r]
		refStr := string(r)
		if d.child > 0 || d.leaf > 0 {
			initChild := max(d.child, 0)
			initLeaf := max(d.leaf, 0)
			if _, err := tx.ExecContext(ctx, `
INSERT INTO plugin_item_parent_tally(library_id, parent_ref, child_count, leaf_count)
VALUES(?, ?, ?, ?)
ON CONFLICT(library_id, parent_ref) DO UPDATE SET
	child_count = CASE WHEN plugin_item_parent_tally.child_count + ? < 0 THEN 0 ELSE plugin_item_parent_tally.child_count + ? END,
	leaf_count  = CASE WHEN plugin_item_parent_tally.leaf_count  + ? < 0 THEN 0 ELSE plugin_item_parent_tally.leaf_count  + ? END`,
				libID, refStr, initChild, initLeaf, d.child, d.child, d.leaf, d.leaf); err != nil {
				return err
			}
		} else {
			if _, err := tx.ExecContext(ctx, `
UPDATE plugin_item_parent_tally
SET child_count = CASE WHEN child_count + ? < 0 THEN 0 ELSE child_count + ? END,
    leaf_count  = CASE WHEN leaf_count  + ? < 0 THEN 0 ELSE leaf_count  + ? END
WHERE library_id = ? AND parent_ref = ?`,
				d.child, d.child, d.leaf, d.leaf, libID, refStr); err != nil {
				return err
			}
		}
		if d.child < 0 || d.leaf < 0 {
			if _, err := tx.ExecContext(ctx, `
DELETE FROM plugin_item_parent_tally
WHERE library_id = ? AND parent_ref = ? AND child_count <= 0 AND leaf_count <= 0`,
				libID, refStr); err != nil {
				return err
			}
		}
	}
	return nil
}

func deleteParentTallyRefsTx(ctx context.Context, tx *sql.Tx, libraryID core.LibraryID, refs []core.ItemRef) error {
	if len(refs) == 0 || libraryID <= 0 {
		return nil
	}
	libID := int64(libraryID)
	const chunkSize = 250
	for start := 0; start < len(refs); start += chunkSize {
		end := min(start+chunkSize, len(refs))
		chunk := refs[start:end]
		placeholders := make([]string, len(chunk))
		args := make([]any, 0, 1+len(chunk))
		args = append(args, libID)
		for i, r := range chunk {
			placeholders[i] = "?"
			args = append(args, string(r))
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM plugin_item_parent_tally WHERE library_id = ? AND parent_ref IN (`+strings.Join(placeholders, ",")+`)`,
			args...); err != nil {
			return err
		}
	}
	return nil
}

func rebuildKindParentTallyTx(ctx context.Context, tx *sql.Tx, libraryID core.LibraryID, decl plugins.MediaKindMeta) error {
	if libraryID <= 0 {
		return nil
	}
	libID := int64(libraryID)
	if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_item_parent_tally WHERE library_id = ?`, libID); err != nil {
		return err
	}
	if len(decl.Levels) < 2 {
		return nil
	}
	leafLevel := decl.Levels[len(decl.Levels)-1].Key
	if _, err := tx.ExecContext(ctx, `
INSERT INTO plugin_item_parent_tally(library_id, parent_ref, child_count, leaf_count)
SELECT library_id, parent_ref, COUNT(*), CAST(SUM(CASE WHEN level = ? THEN 1 ELSE 0 END) AS BIGINT)
FROM plugin_items
WHERE library_id = ? AND parent_ref <> ''
GROUP BY library_id, parent_ref`,
		leafLevel, libID); err != nil {
		return err
	}
	for d := len(decl.Levels) - 2; d >= 1; d-- {
		lvl := decl.Levels[d].Key
		if lvl == "" || lvl == leafLevel {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO plugin_item_parent_tally(library_id, parent_ref, child_count, leaf_count)
SELECT i.library_id, i.parent_ref, 0, CAST(SUM(c.leaf_count) AS BIGINT)
FROM plugin_items i
JOIN plugin_item_parent_tally c
  ON c.library_id = i.library_id AND c.parent_ref = i.item_ref
WHERE i.library_id = ? AND i.level = ? AND i.parent_ref <> '' AND c.leaf_count > 0
GROUP BY i.library_id, i.parent_ref
ON CONFLICT(library_id, parent_ref) DO UPDATE SET
	leaf_count = plugin_item_parent_tally.leaf_count + excluded.leaf_count`,
			libID, lvl); err != nil {
			return err
		}
	}
	return nil
}

const (
	migration15SQLiteDDL = `
CREATE TABLE plugin_item_parent_tally (
	library_id  INTEGER NOT NULL,
	parent_ref  TEXT    NOT NULL,
	child_count INTEGER NOT NULL DEFAULT 0,
	leaf_count  INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (library_id, parent_ref)
);
`
	migration15PostgresDDL = `
CREATE TABLE plugin_item_parent_tally (
	library_id  BIGINT           NOT NULL,
	parent_ref  TEXT COLLATE "C" NOT NULL,
	child_count BIGINT           NOT NULL DEFAULT 0,
	leaf_count  BIGINT           NOT NULL DEFAULT 0,
	PRIMARY KEY (library_id, parent_ref)
);
`
)

func applyMigration15SQLite(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, migration15SQLiteDDL); err != nil {
		return err
	}
	return backfillParentTallyTx(ctx, tx)
}

func applyMigration15Postgres(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, migration15PostgresDDL); err != nil {
		return err
	}
	return backfillParentTallyTx(ctx, tx)
}

func backfillParentTallyTx(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT library_id, decl FROM plugin_kinds WHERE library_id > 0`)
	if err != nil {
		return err
	}
	type kindRow struct {
		libraryID core.LibraryID
		declJSON  string
	}
	var kinds []kindRow
	for rows.Next() {
		var libID int64
		var declJSON string
		if err := rows.Scan(&libID, &declJSON); err != nil {
			rows.Close()
			return err
		}
		kinds = append(kinds, kindRow{libraryID: core.LibraryID(libID), declJSON: declJSON})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, k := range kinds {
		var decl plugins.MediaKindMeta
		if err := json.Unmarshal([]byte(k.declJSON), &decl); err != nil || len(decl.Levels) < 2 {
			continue
		}
		if err := rebuildKindParentTallyTx(ctx, tx, k.libraryID, decl); err != nil {
			return err
		}
	}
	return nil
}
