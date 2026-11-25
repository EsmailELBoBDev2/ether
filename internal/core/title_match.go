package core

// TitleKeyRange maps an inclusive range of rating keys by a constant offset.
type TitleKeyRange struct {
	First, Last, Delta int64
}

// TitleMatchRanges maps a title and, for a show, its season and episode keys.
func TitleMatchRanges(from, to RatingKey) []TitleKeyRange {
	ranges := []TitleKeyRange{{int64(from), int64(from), int64(to - from)}}
	if from.Kind() == KindShow {
		f, t := from.TMDB(), to.TMDB()
		ranges = append(ranges,
			TitleKeyRange{int64(SeasonKey(f, 0)), int64(SeasonKey(f, 9999)), int64(SeasonKey(t, 0) - SeasonKey(f, 0))},
			TitleKeyRange{int64(EpisodeKey(f, 0, 0)), int64(EpisodeKey(f, 999, 9999)), int64(EpisodeKey(t, 0, 0) - EpisodeKey(f, 0, 0))})
	}
	return ranges
}
