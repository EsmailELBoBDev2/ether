package plex

import (
	"strings"
	"testing"
)

func TestPlexWebExtrasScript(t *testing.T) {
	got := string(withPlexWebExtras([]byte("<html><head><title>Plex</title></head><body></body></html>")))
	if !strings.Contains(got, plexWebExtrasTag+"</head>") {
		t.Fatalf("script not added: %s", got)
	}
	if strings.Count(string(withPlexWebExtras([]byte(got))), "ether-extras.js") != 1 {
		t.Fatal("script added twice")
	}
	if !strings.Contains(string(plexWebExtrasJS), `settings\/server`) {
		t.Fatal("embedded script missing")
	}
}
