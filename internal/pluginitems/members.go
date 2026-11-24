package pluginitems

import (
	"context"
	"database/sql"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

// A list item (a playlist, a chart: a level declared members: true) orders
// other items of its kind. Its members are stored by position and read in
// pages joined to the items, so a member not stored yet is simply skipped.
func init() {
	store.Register("pluginitems", 20, `
CREATE TABLE IF NOT EXISTS plugin_item_members (
  item_ref TEXT NOT NULL,
  position INTEGER NOT NULL,
  member_ref TEXT NOT NULL,
  PRIMARY KEY (item_ref, position)
);`)
}

// An item's recording identity (the same song on an album, a single and a
// compilation), indexed per library for duplicate lookups.
func init() {
	store.Register("pluginitems", 21, `
ALTER TABLE plugin_items ADD COLUMN recording TEXT NOT NULL DEFAULT '';
CREATE INDEX plugin_items_recording ON plugin_items(library_id, recording) WHERE recording <> '';`)
}

func replaceMembersTx(ctx context.Context, tx *sql.Tx, pluginID, kindKey string, it core.KindItem) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_item_members WHERE item_ref = ?`, string(it.Ref)); err != nil {
		return err
	}
	for i, m := range it.Members {
		if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_item_members(item_ref, position, member_ref) VALUES(?, ?, ?)`,
			string(it.Ref), i, string(core.NewItemRef(pluginID, kindKey, strings.TrimSpace(m)))); err != nil {
			return err
		}
	}
	return nil
}

// MembersPage lists a list item's stored members in order from offset, at
// most limit, and how many members it names. Members not stored (yet) are
// skipped; the count is of named members.
func (s *Service) MembersPage(ctx context.Context, listRef core.ItemRef, offset, limit int) ([]core.KindItem, int, error) {
	if s == nil || s.db == nil || listRef == "" {
		return nil, 0, nil
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_item_members WHERE item_ref = ?`, string(listRef)).Scan(&total); err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT m.member_ref FROM plugin_item_members m JOIN plugin_items i ON i.item_ref = m.member_ref
		WHERE m.item_ref = ? ORDER BY m.position LIMIT ? OFFSET ?`, string(listRef), limit, max(offset, 0))
	if err != nil {
		return nil, 0, err
	}
	var refs []core.ItemRef
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			rows.Close()
			return nil, 0, err
		}
		refs = append(refs, core.ItemRef(ref))
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	out := make([]core.KindItem, 0, len(refs))
	for _, ref := range refs {
		if it, _, err := s.GetByRef(ctx, ref); err == nil {
			out = append(out, it)
		}
	}
	return out, total, nil
}
