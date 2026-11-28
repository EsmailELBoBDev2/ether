package app

import (
	"testing"
	"time"

	"github.com/braedonsaunders/ether/internal/livetv/dvr"
	"github.com/braedonsaunders/ether/internal/pluginitems"
)

func TestDVRLibraryItems(t *testing.T) {
	at := time.Date(2026, 11, 27, 21, 0, 0, 0, time.Local)
	recs := []dvr.Recording{
		{ID: "a", Status: dvr.JobCompleted, Path: "/r/a.ts", DurationMS: 60000, Programme: dvr.Programme{Title: "Show", EpisodeTitle: "Pilot", Season: 1, Episode: 1, Start: at}},
		{ID: "b", Status: dvr.JobPartial, Path: "/r/b.ts", Programme: dvr.Programme{Title: "show!", Season: 1, Episode: 2, Start: at}},
		{ID: "c", Status: dvr.JobCompleted, Path: "/r/c.ts", Programme: dvr.Programme{Title: "News", Start: at}},
		{ID: "d", Status: dvr.JobCompleted, Path: "/r/d.ts", Programme: dvr.Programme{Title: "Film", MediaType: "movie", Start: at}},
		{ID: "e", Status: dvr.JobRecording, Path: "/r/e.ts", Programme: dvr.Programme{Title: "Live", Start: at}},
	}
	tv, movies := dvrLibraryItems(recs)
	if len(movies) != 1 || movies[0].ID != "rec-d" || movies[0].Level != "movie" || !movies[0].Playable {
		t.Fatalf("movies = %+v", movies)
	}
	levels := map[string]int{}
	ids := map[string]bool{}
	for i, it := range tv {
		levels[it.Level]++
		ids[it.ID] = true
		if it.ParentID != "" && !ids[it.ParentID] {
			t.Fatalf("item %d (%s) comes before its parent %s", i, it.ID, it.ParentID)
		}
	}
	// "Show" and "show!" are one show; News is grouped by its year.
	if levels["show"] != 2 || levels["season"] != 2 || levels["episode"] != 3 || ids["rec-e"] {
		t.Fatalf("tv = %+v", tv)
	}
	// A recording's length is its runtime in every app; unknown stays empty.
	kinds := dvrKinds()
	for _, it := range tv {
		if it.ID == "rec-a" {
			if got := pluginitems.Project(it, kinds[0], kinds[0].Present).Runtime; got != time.Minute {
				t.Fatalf("rec-a runtime = %v, want 1m", got)
			}
		}
		if it.ID == "rec-b" && it.Fields != nil {
			t.Fatalf("rec-b without a length has fields %v", it.Fields)
		}
	}
	srcs, err := dvrResolve(t.Context(), tv[len(tv)-1])
	if err == nil || len(srcs) != 0 {
		t.Fatalf("a missing file resolved: %+v", srcs)
	}
}
