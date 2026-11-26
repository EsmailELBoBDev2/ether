package plex

import "net/http"

// plexWebURL is the Plex Web app Plex hosts for every server.
const plexWebURL = "https://app.plex.tv/desktop/"

// redirectPlexWeb sends a browser that opens /web to the hosted Plex Web app.
func redirectPlexWeb(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, plexWebURL, http.StatusFound)
}
