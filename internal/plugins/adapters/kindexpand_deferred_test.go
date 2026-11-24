package adapters

import "testing"

func TestSearchNameFolding(t *testing.T) {
	for _, c := range []struct{ a, b string }{
		{"Dave Matthews Band", "dave matthews band"},
		{"The Beatles", "beatles"},
		{"Simon & Garfunkel", "Simon and Garfunkel"},
		{"AC/DC", "acdc"},
	} {
		if searchName(c.a) != searchName(c.b) {
			t.Fatalf("%q and %q should fold alike: %q vs %q", c.a, c.b, searchName(c.a), searchName(c.b))
		}
	}
	if searchName("Dave Matthews") == searchName("Dave Matthews Band") {
		t.Fatal("a shorter name matched")
	}
}
