package core

import (
	"testing"
	"time"
)

func TestParseLRC(t *testing.T) {
	got := ParseLRC("[ar:Daft Punk]\n[00:12.50] There are so many things\n[00:20.1][01:05.25]Chorus\nno stamp\n[00:15.00]\n")
	want := []LyricLine{
		{12*time.Second + 500*time.Millisecond, "There are so many things"},
		{15 * time.Second, ""},
		{20*time.Second + 100*time.Millisecond, "Chorus"},
		{65*time.Second + 250*time.Millisecond, "Chorus"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d: got %+v want %+v", i, got[i], want[i])
		}
	}
}
