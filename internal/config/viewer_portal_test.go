package config

import "testing"

func TestViewerPortalPage(t *testing.T) {
	for raw, want := range map[string]string{
		"":                                       "",
		"https://dashboard.example.com":          "https://dashboard.example.com/ether/me",
		" https://dashboard.example.com/ ":       "https://dashboard.example.com/ether/me",
		"https://dashboard.example.com/ether/me": "https://dashboard.example.com/ether/me",
		"https://dashboard.example.com/ether/":   "https://dashboard.example.com/ether/me",
		"https://example.com/media":              "https://example.com/media/ether/me",
		"http://192.168.1.20:32400":              "http://192.168.1.20:32400/ether/me",
	} {
		if got, err := ViewerPortalPage(raw); err != nil || got != want {
			t.Errorf("ViewerPortalPage(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, bad := range []string{"dashboard.example.com", "ftp://x", "https://", "https://u:p@x.com", "https://x.com/?a=1", "https://x.com/#me"} {
		if _, err := ViewerPortalPage(bad); err == nil {
			t.Errorf("ViewerPortalPage(%q) accepted", bad)
		}
	}
	if (Server{ViewerPortalURL: "nope"}).ViewerPortalPage() != "" {
		t.Error("an unusable setting must not produce an address")
	}
}
