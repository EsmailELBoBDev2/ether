package plextv

import "testing"

func TestInviteLink(t *testing.T) {
	for body, want := range map[string]string{
		`{"id":1,"invite":{"url":"https://app.plex.tv/invite/abc"}}`: "https://app.plex.tv/invite/abc",
		`{"inviteUrl":"https://plex.tv/i/xyz"}`:                      "https://plex.tv/i/xyz",
		`{"id":1,"thumb":"https://plex.tv/users/a/avatar"}`:          "",
		`{"inviteUrl":"https://evil.example/invite"}`:                "",
		`not json`: "",
	} {
		if got := inviteLink([]byte(body)); got != want {
			t.Errorf("inviteLink(%s) = %q, want %q", body, got, want)
		}
	}
}
