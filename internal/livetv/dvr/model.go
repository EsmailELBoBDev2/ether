// Package dvr holds Ether's scheduled Live TV recordings: recording rules
// (Jellyfin/Emby SeriesTimers, Plex subscriptions), the jobs they expand to
// (Timers, grab operations) and the recordings jobs produce. The package
// does not import livetv; the Live TV service feeds it guide programmes and
// runs the jobs.
package dvr

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Horizon is how far ahead rules are expanded. It matches the guide span
// the Jellyfin and Emby frontends advertise in /LiveTv/GuideInfo.
const Horizon = 14 * 24 * time.Hour

// DriftWindow is how far a guide may move an airing before a scheduled job
// stops following it.
const DriftWindow = 2 * time.Hour

const (
	DefaultPrePad  = time.Minute
	DefaultPostPad = 2 * time.Minute
	maxPad         = 3 * time.Hour
)

type RuleKind string

const (
	RuleSeries  RuleKind = "series"
	RuleMovie   RuleKind = "movie"
	RuleKeyword RuleKind = "keyword"
)

// KeepMode is a rule's retention policy. The storage cleaner applies it.
type KeepMode string

const (
	KeepAll           KeepMode = "all"
	KeepLatest        KeepMode = "latest"
	KeepDays          KeepMode = "days"
	KeepUntilWatched  KeepMode = "watched"
	KeepUntilSpace    KeepMode = "space"
	defaultKeepMode            = KeepUntilSpace
	maxKeepCount               = 1000
	maxRuleTextLength          = 300
)

type Keep struct {
	Mode KeepMode `json:"mode"`
	// N is the episode count for KeepLatest or the day count for KeepDays.
	N int `json:"n,omitempty"`
}

// Rule records every airing of a show, a movie, or programmes matching a
// keyword. Weekdays is a bitmask (bit 0 = Sunday); zero means any day.
// SlotMinute is minutes after local midnight, or -1 for any time; an airing
// within 30 minutes of it matches. Episodes already recorded are skipped
// unless RecordAgain is set.
type Rule struct {
	ID            string        `json:"id"`
	UserID        int64         `json:"userId"`
	Kind          RuleKind      `json:"kind"`
	Title         string        `json:"title"`
	ShowGUID      string        `json:"showGuid,omitempty"`
	Keyword       string        `json:"keyword,omitempty"`
	ChannelID     string        `json:"channelId,omitempty"`
	Weekdays      uint8         `json:"weekdays,omitempty"`
	SlotMinute    int           `json:"slotMinute"`
	NewOnly       bool          `json:"newOnly,omitempty"`
	RecordAgain   bool          `json:"recordAgain,omitempty"`
	PrePad        time.Duration `json:"prePad"`
	PostPad       time.Duration `json:"postPad"`
	Priority      int           `json:"priority"`
	Keep          Keep          `json:"keep"`
	PreferArchive bool          `json:"preferArchive,omitempty"`
	Enabled       bool          `json:"enabled"`
	// ProgrammeID is the airing the rule was made from, for clients that
	// show it (Jellyfin/Emby SeriesTimer ProgramId).
	ProgrammeID string    `json:"programmeId,omitempty"`
	Image       string    `json:"image,omitempty"`
	Created     time.Time `json:"created"`
	Updated     time.Time `json:"updated"`
}

// Normalize fills defaults and rejects a rule that could never match.
func (r *Rule) Normalize() error {
	r.Title = clip(strings.TrimSpace(r.Title))
	r.Keyword = clip(strings.TrimSpace(r.Keyword))
	r.ShowGUID = clip(strings.TrimSpace(r.ShowGUID))
	r.ChannelID = strings.TrimSpace(r.ChannelID)
	switch r.Kind {
	case "":
		r.Kind = RuleSeries
	case RuleSeries, RuleMovie, RuleKeyword:
	default:
		return fmt.Errorf("unknown rule kind %q", r.Kind)
	}
	if r.Kind == RuleKeyword {
		if NormTitle(r.Keyword) == "" {
			return fmt.Errorf("a keyword rule needs a keyword")
		}
		if r.Title == "" {
			r.Title = r.Keyword
		}
	} else if NormTitle(r.Title) == "" && r.ShowGUID == "" {
		return fmt.Errorf("a rule needs a title")
	}
	if r.Weekdays > 0x7f {
		return fmt.Errorf("weekdays out of range")
	}
	if r.SlotMinute < -1 || r.SlotMinute >= 24*60 {
		return fmt.Errorf("time slot out of range")
	}
	var err error
	if r.PrePad, err = pad(r.PrePad); err != nil {
		return err
	}
	if r.PostPad, err = pad(r.PostPad); err != nil {
		return err
	}
	return r.Keep.normalize()
}

func (k *Keep) normalize() error {
	switch k.Mode {
	case "":
		k.Mode = defaultKeepMode
	case KeepAll, KeepUntilWatched, KeepUntilSpace:
		k.N = 0
	case KeepLatest, KeepDays:
		if k.N < 1 || k.N > maxKeepCount {
			return fmt.Errorf("keep count out of range")
		}
	default:
		return fmt.Errorf("unknown keep mode %q", k.Mode)
	}
	return nil
}

func pad(d time.Duration) (time.Duration, error) {
	if d < 0 || d > maxPad {
		return 0, fmt.Errorf("padding out of range")
	}
	return d.Round(time.Second), nil
}

func clip(s string) string {
	if len(s) <= maxRuleTextLength {
		return s
	}
	cut := maxRuleTextLength
	for cut > 0 && !utfStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func utfStart(b byte) bool { return b&0xc0 != 0x80 }

// Programme is the guide airing the DVR needs. The Live TV service fills
// it from its guide.
type Programme struct {
	ID           string    `json:"id"`
	ChannelID    string    `json:"channelId"`
	ChannelName  string    `json:"channelName,omitempty"`
	Start        time.Time `json:"start"`
	End          time.Time `json:"end"`
	Title        string    `json:"title"`
	ShowTitle    string    `json:"showTitle,omitempty"`
	EpisodeTitle string    `json:"episodeTitle,omitempty"`
	Summary      string    `json:"summary,omitempty"`
	GUID         string    `json:"guid,omitempty"`
	ShowGUID     string    `json:"showGuid,omitempty"`
	MediaType    string    `json:"mediaType,omitempty"`
	Thumb        string    `json:"thumb,omitempty"`
	Rating       string    `json:"rating,omitempty"`
	Genres       []string  `json:"genres,omitempty"`
	Season       int       `json:"season,omitempty"`
	Episode      int       `json:"episode,omitempty"`
	Premiere     bool      `json:"premiere,omitempty"`
	ReleasedMS   int64     `json:"releasedMs,omitempty"`
}

// Show is the series title an airing belongs to.
func (p Programme) Show() string {
	if p.ShowTitle != "" {
		return p.ShowTitle
	}
	return p.Title
}

// IsMovie reports whether the guide marks the airing as a film.
func (p Programme) IsMovie() bool {
	if p.MediaType == "movie" {
		return true
	}
	if p.MediaType == "episode" || p.Episode > 0 {
		return false
	}
	for _, g := range p.Genres {
		g = strings.ToLower(g)
		if g == "movie" || g == "movies" || g == "film" {
			return true
		}
	}
	return false
}

// Year is the release year, or 0.
func (p Programme) Year() int {
	if p.ReleasedMS <= 0 {
		return 0
	}
	return time.UnixMilli(p.ReleasedMS).UTC().Year()
}

// EpisodeKey identifies the content of an airing, so a repeat of an episode
// already recorded or scheduled is recognised. Airings the guide does not
// describe well enough fall back to an airing key, which only dedupes the
// same slot.
func EpisodeKey(p Programme) string {
	show := NormTitle(p.Show())
	switch {
	case p.GUID != "":
		return "guid:" + p.GUID
	case p.Episode > 0:
		return "ep:" + show + ":" + strconv.Itoa(p.Season) + ":" + strconv.Itoa(p.Episode)
	case p.EpisodeTitle != "" && NormTitle(p.EpisodeTitle) != show:
		return "title:" + show + ":" + NormTitle(p.EpisodeTitle)
	case p.IsMovie():
		return "movie:" + show + ":" + strconv.Itoa(p.Year())
	}
	return AiringKey(p)
}

// AiringKey identifies one slot on one channel.
func AiringKey(p Programme) string {
	return "air:" + p.ChannelID + ":" + strconv.FormatInt(p.Start.UTC().Unix(), 10)
}

func isAiringKey(key string) bool { return strings.HasPrefix(key, "air:") }

// NormTitle folds case, punctuation and spacing so guide
// titles from different providers compare equal ("Doctor Who (2023)" and
// "doctor who 2023").
func NormTitle(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		case r == '\'' || r == '’':
			// "Grey's" and "Greys" are the same show.
		default:
			space = true
		}
	}
	return b.String()
}

// JobStatus follows a job from the schedule to a recording.
type JobStatus string

const (
	JobScheduled JobStatus = "scheduled"
	JobConflict  JobStatus = "conflict"
	JobRecording JobStatus = "recording"
	JobCompleted JobStatus = "completed"
	JobPartial   JobStatus = "partial"
	JobFailed    JobStatus = "failed"
	// JobSkipped is a rule airing the user cancelled or the guide removed.
	// The row stays so the rule does not schedule it again.
	JobSkipped   JobStatus = "skipped"
	JobCancelled JobStatus = "cancelled"
)

// Pending reports whether the job has not started.
func (s JobStatus) Pending() bool { return s == JobScheduled || s == JobConflict }

// Final reports whether the job will not run again.
func (s JobStatus) Final() bool {
	switch s {
	case JobCompleted, JobPartial, JobFailed, JobSkipped, JobCancelled:
		return true
	}
	return false
}

// Job is one airing to record: a Jellyfin/Emby Timer or a Plex grab
// operation. RuleID is empty for a one-off.
type Job struct {
	ID          string        `json:"id"`
	UserID      int64         `json:"userId"`
	RuleID      string        `json:"ruleId,omitempty"`
	MatchKey    string        `json:"matchKey"`
	Programme   Programme     `json:"programme"`
	PrePad      time.Duration `json:"prePad"`
	PostPad     time.Duration `json:"postPad"`
	Priority    int           `json:"priority"`
	Status      JobStatus     `json:"status"`
	Reason      string        `json:"reason,omitempty"`
	RecordingID string        `json:"recordingId,omitempty"`
	Node        string        `json:"node,omitempty"`
	Created     time.Time     `json:"created"`
	Updated     time.Time     `json:"updated"`
}

// Window is the padded span the recorder runs for.
func (j Job) Window() (time.Time, time.Time) {
	return j.Programme.Start.Add(-j.PrePad), j.Programme.End.Add(j.PostPad)
}

func newID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
