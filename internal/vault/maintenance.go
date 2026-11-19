package vault

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/nzb"
)

func (v *library) MigrateDir(ctx context.Context, dir string) error {
	entries, e := os.ReadDir(dir)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	type imported struct {
		path, id string
		digest   [32]byte
		k        Key
	}
	var imports []imported
	for _, f := range entries {
		if !f.Type().IsRegular() || !strings.EqualFold(filepath.Ext(f.Name()), ".nzb") {
			continue
		}
		path := filepath.Join(dir, f.Name())
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		id := strings.TrimSuffix(f.Name(), filepath.Ext(f.Name()))
		k, e := v.Put(ctx, PutRequest{NZB: b, Source: "legacy:nzb", Release: core.Release{IndexerID: LegacyAliasIndexer, ID: id}})
		if e != nil {
			return e
		}
		imports = append(imports, imported{path, id, sha256.Sum256(b), k})
	}
	// Validate all files in one serialized transaction. Cap eviction or a
	// concurrent purge cannot invalidate the verified rows during deletion.
	return v.write(ctx, func(tx *sql.Tx) error {
		for _, f := range imports {
			b, e := os.ReadFile(f.path)
			if e != nil {
				return e
			}
			if sha256.Sum256(b) != f.digest {
				return errors.New("vault: migration input changed")
			}
			doc, e := nzb.Parse(b)
			if e != nil {
				return e
			}
			want, _ := doc.Subset(func(*nzb.File) bool { return true })
			var alias Key
			if e := tx.QueryRowContext(ctx, "SELECT key FROM vault_aliases WHERE indexer_id=? AND release_id=?", LegacyAliasIndexer, f.id).Scan(&alias); e != nil {
				return e
			}
			if alias != f.k {
				return invalid
			}
			l, e := v.loc(ctx, tx, f.k)
			if e != nil {
				return e
			}
			b, e = v.read(l)
			if e != nil {
				return e
			}
			r, e := Decode(b[12:])
			if e != nil {
				return e
			}
			if e = v.passwords(r, false); e != nil {
				return e
			}
			if r.NZB == nil {
				return invalid
			}
			got, _ := r.NZB.Subset(func(*nzb.File) bool { return true })
			if !bytes.Equal(want, got) {
				return errors.New("vault: migration verification failed")
			}
		}
		if e := ctx.Err(); e != nil {
			return e
		}
		for _, f := range imports {
			if e := os.Remove(f.path); e != nil {
				return e
			}
		}
		left, e := os.ReadDir(dir)
		if e == nil && len(left) == 0 {
			return os.Remove(dir)
		}
		return e
	})
}

type liveFrame struct {
	location
	k    Key
	copy bool
}

// Compact serializes relocation with every node's writer. Old packs are
// unlinked only after the new pointers commit, with a second reference check.
func (v *library) Compact(ctx context.Context) error {
	base := filepath.Join(v.Dir, "packs")
	paths, e := filepath.Glob(filepath.Join(base, "*", "*.pack"))
	if e != nil {
		return e
	}
	for _, path := range paths {
		rel, e := filepath.Rel(base, path)
		if e != nil {
			return e
		}
		pack := filepath.ToSlash(rel)
		rewrite := false
		e = v.write(ctx, func(tx *sql.Tx) error {
			st, e := os.Stat(path)
			if errors.Is(e, os.ErrNotExist) {
				return nil
			}
			if e != nil {
				return e
			}
			rows, e := tx.QueryContext(ctx, `SELECT key,off,len,crc,0 FROM vault_releases WHERE pack=? AND dead=0 AND holders=? UNION ALL SELECT c.key,c.off,c.len,c.crc,1 FROM vault_copies c JOIN vault_releases r ON r.key=c.key AND r.crc=c.crc AND r.dead=0 WHERE c.pack=? AND c.node=?`, pack, v.node(), pack, v.node())
			if e != nil {
				return e
			}
			var frames []liveFrame
			var live int64
			for rows.Next() {
				x := liveFrame{}
				var replica int
				if e = rows.Scan(&x.k, &x.off, &x.length, &x.crc, &replica); e != nil {
					rows.Close()
					return e
				}
				x.pack = pack
				x.copy = replica != 0
				frames = append(frames, x)
				live += int64(x.length)
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
			if live*2 >= st.Size() {
				return nil
			}
			rewrite = true
			if filepath.ToSlash(v.writer) == pack {
				v.writer = ""
			}
			for _, x := range frames {
				b, e := v.read(x.location)
				if e != nil {
					return e
				}
				l, e := v.appendFrame(b)
				if e != nil {
					return e
				}
				if x.copy {
					_, e = tx.ExecContext(ctx, "UPDATE vault_copies SET pack=?,off=? WHERE key=? AND node=?", l.pack, l.off, x.k, v.node())
				} else {
					_, e = tx.ExecContext(ctx, "UPDATE vault_releases SET pack=?,off=? WHERE key=?", l.pack, l.off, x.k)
				}
				if e != nil {
					return e
				}
			}
			return nil
		})
		if e != nil {
			return e
		}
		if rewrite {
			e = v.write(ctx, func(tx *sql.Tx) error {
				var n int
				e := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM vault_releases WHERE pack=? AND dead=0 AND holders=?) + (SELECT count(*) FROM vault_copies c JOIN vault_releases r ON r.key=c.key AND r.crc=c.crc AND r.dead=0 WHERE c.pack=? AND c.node=?)`, pack, v.node(), pack, v.node()).Scan(&n)
				if e != nil || n != 0 {
					return e
				}
				e = os.Remove(path)
				if errors.Is(e, os.ErrNotExist) {
					return nil
				}
				return e
			})
			if e != nil {
				return e
			}
		}
	}
	return nil
}
