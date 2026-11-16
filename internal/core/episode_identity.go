package core

// EpisodeIdentity contains only locally mirrored metadata. A missing entry is
// unknown, never evidence that a release belongs to the requested show.
type EpisodeIdentity struct {
	OtherShows map[string][]EpisodeNamesake
	Seasons    map[int]string
	// Episodes holds the requested season's other episode titles by number.
	Episodes map[int]string
}

// EpisodeNamesake is a show whose own title matches a requested name, with
// its locally known title for the same season and episode.
type EpisodeNamesake struct {
	Year         int
	EpisodeTitle string
}
