package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/store"
)

// A series match moves the same episode coordinate across independently
// keyed tables. Existing destination state must survive and a movie with
// the same TMDB number must retain its separate vault entry.
func TestTitleMatchMovesCanonicalBindings(t *testing.T) {
	ctx := context.Background()
	db := store.OpenTest(t)
	a := &App{db: db}
	from := &core.Title{Key: core.ShowKey(81), IDs: core.IDs{TMDB: 81}, Type: core.TypeShow, Title: "Old"}
	to := &core.Title{Key: core.ShowKey(92), IDs: core.IDs{TMDB: 92}, Type: core.TypeShow, Title: "New", Year: 2024}
	ep, dest := core.EpisodeKey(81, 2, 3), core.EpisodeKey(92, 2, 3)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO state_watch(user_id,rating_key,view_count,view_offset_ns,watch_at)
		VALUES(1,?,2,100,2),(1,?,1,200,3),(2,?,1,300,4)`, ep, dest, ep)
	exec(`INSERT INTO favorites_items(viewer_id,kind,subject_id,added_at) VALUES(1,'title',?,1),(1,'title',?,2)`, ep.String(), dest.String())
	exec(`INSERT INTO collections_specs(id,spec_json) VALUES (10,?)`,
		`{"sources":[{"kind":"titles","keys":["`+from.Key.String()+`","`+to.Key.String()+`"]}]}`)
	exec(`INSERT INTO collections_specs(id,spec_json) VALUES (11,'{}')`)
	exec(`INSERT INTO collections_members(spec_id,pos,key) VALUES(11,1,?)`, from.Key)
	exec(`INSERT INTO availability_releases(id,rating_key,source_id,generation,payload,title,name,protocol,first_seen,last_seen)
		VALUES('r',?,'test','1','{}','old','test','torrent',1,1)`, ep)
	exec(`INSERT INTO vault_releases(key,kind,name,title_key,tmdb,year,season,episode,size,segments,pack,off,len,crc,holders,source,added,last_used,private,profile_fp,info)
		VALUES ('show','torrent','show','old',81,0,2,3,0,0,'',0,0,0,'','',0,0,0,'','{}'),
		       ('movie','torrent','movie','old',81,0,0,0,0,0,'',0,0,0,'','',0,0,0,'','{}')`)
	if err := a.moveTitleBindings(ctx, from, to); err != nil {
		t.Fatal(err)
	}
	assertInt := func(want int64, query string, args ...any) {
		t.Helper()
		var got int64
		if err := db.QueryRowContext(ctx, query, args...).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: got %d, want %d, error %v", query, got, want, err)
		}
	}
	assertInt(0, `SELECT COUNT(*) FROM state_watch WHERE rating_key=?`, ep)
	assertInt(2, `SELECT view_count FROM state_watch WHERE user_id=1 AND rating_key=?`, dest)
	assertInt(200, `SELECT view_offset_ns FROM state_watch WHERE user_id=1 AND rating_key=?`, dest)
	assertInt(300, `SELECT view_offset_ns FROM state_watch WHERE user_id=2 AND rating_key=?`, dest)
	assertInt(2, `SELECT added_at FROM favorites_items WHERE viewer_id=1 AND kind='title' AND subject_id=?`, dest.String())
	assertInt(int64(to.Key), `SELECT key FROM collections_members WHERE spec_id=11 AND pos=1`)
	assertInt(int64(dest), `SELECT rating_key FROM availability_releases WHERE id='r'`)
	assertInt(92, `SELECT tmdb FROM vault_releases WHERE key='show' AND owner_match=1`)
	assertInt(81, `SELECT tmdb FROM vault_releases WHERE key='movie' AND owner_match=0`)
	var raw string
	if err := db.QueryRowContext(ctx, `SELECT spec_json FROM collections_specs WHERE id=10`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var spec core.CollectionSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		t.Fatal(err)
	}
	if keys := spec.Sources[0].Keys; len(keys) != 1 || keys[0] != to.Key.String() {
		t.Fatalf("explicit collection source = %v", keys)
	}
}
