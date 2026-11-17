package core

// EpisodeNumber is a season/episode pair in one numbering system.
type EpisodeNumber struct{ Season, Episode int }

// EpisodeLayout contains bounded, local canonical facts. A missing episode in
// an incompletely mirrored season is unknown, rather than evidence of absence.
type EpisodeLayout struct {
	Numbers  map[EpisodeNumber]int
	Seasons  map[int]bool
	Complete map[int]bool
}

type ReleaseNumbering struct {
	Aliases     []EpisodeNumber
	PackTargets []EpisodeNumber
}

// EpisodeAliases returns distinct additional forms, never the canonical form.
func EpisodeAliases(req SearchRequest) []EpisodeNumber {
	var out []EpisodeNumber
	add := func(n EpisodeNumber) {
		if n.Season <= 0 || n.Episode <= 0 || (n.Season == req.Season && n.Episode == req.Episode) {
			return
		}
		for _, prev := range out {
			if prev == n {
				return
			}
		}
		out = append(out, n)
	}
	add(EpisodeNumber{req.AltSeason, req.AltEpisode})
	for _, n := range req.ReleaseAliases {
		add(n)
	}
	return out
}

// AliasSearchRequest changes the query's numbers while retaining series IDs.
func AliasSearchRequest(req SearchRequest, n EpisodeNumber) SearchRequest {
	req.Season, req.Episode = n.Season, n.Episode
	req.AltSeason, req.AltEpisode = 0, 0
	req.ReleaseAliases, req.ReleasePackTargets = nil, nil
	req.ReleaseAliasesOnly, req.ReleaseNumberingKnown = false, false
	req.Cour, req.CourEpisode, req.Anime = 0, 0, false
	return req
}
