package availability

import (
	"context"
	"database/sql"

	"github.com/braedonsaunders/ether/internal/core"
)

// RebindTitleReleases updates the sealed payloads before the match transaction
// moves their rows. The owner has identified the content these releases bind
// to, so their old title IDs must not veto the corrected assignment.
func (s *Service) RebindTitleReleases(ctx context.Context, tx *sql.Tx, first, last int64, title *core.Title) error {
	rows, err := tx.QueryContext(ctx, `SELECT rating_key,id,source_id,payload FROM availability_releases WHERE rating_key BETWEEN ? AND ?`, first, last)
	if err != nil {
		return err
	}
	defer rows.Close()
	type entry struct {
		key             int64
		id, source, raw string
	}
	var changes []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.key, &e.id, &e.source, &e.raw); err != nil {
			return err
		}
		r, err := s.decode(e.raw)
		if err != nil {
			return err
		}
		r.TitleIDs, r.TitleIDsEchoed = title.IDs, false
		e.raw, err = s.encode(r)
		if err != nil {
			return err
		}
		changes = append(changes, e)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, e := range changes {
		if _, err := tx.ExecContext(ctx, `UPDATE availability_releases SET payload=?,title=?,revision=revision+1 WHERE rating_key=? AND id=? AND source_id=?`, e.raw, title.Title, e.key, e.id, e.source); err != nil {
			return err
		}
	}
	return nil
}
