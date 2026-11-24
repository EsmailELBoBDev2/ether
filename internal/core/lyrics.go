package core

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Lyrics are a playable item's words: timed lines when the source has them,
// otherwise plain text. Any media class may have them (songs, podcasts).
type Lyrics struct {
	Lines        []LyricLine
	Plain        string
	Instrumental bool
	// Source names where they came from, for attribution ("LRCLIB").
	Source string
}

// LyricLine is one line and when it starts.
type LyricLine struct {
	Start time.Duration
	Text  string
}

var lrcLine = regexp.MustCompile(`^((?:\[\d{1,3}:\d{1,2}(?:[.:]\d{1,3})?\])+)(.*)$`)
var lrcStamp = regexp.MustCompile(`\[(\d{1,3}):(\d{1,2})(?:[.:](\d{1,3}))?\]`)

// ParseLRC reads LRC timed lyrics ("[mm:ss.xx] words"), one line per stamp,
// in time order. Metadata tags and untimed lines are dropped.
func ParseLRC(lrc string) []LyricLine {
	var out []LyricLine
	for _, raw := range strings.Split(lrc, "\n") {
		m := lrcLine.FindStringSubmatch(strings.TrimSpace(raw))
		if m == nil {
			continue
		}
		text := strings.TrimSpace(m[2])
		for _, stamp := range lrcStamp.FindAllStringSubmatch(m[1], -1) {
			minutes, _ := strconv.Atoi(stamp[1])
			seconds, _ := strconv.Atoi(stamp[2])
			fraction := time.Duration(0)
			if stamp[3] != "" {
				digits, _ := strconv.Atoi(stamp[3])
				fraction = time.Duration(digits) * time.Second / time.Duration(pow10(len(stamp[3])))
			}
			out = append(out, LyricLine{Start: time.Duration(minutes)*time.Minute + time.Duration(seconds)*time.Second + fraction, Text: text})
		}
	}
	for i := 1; i < len(out); i++ { // stable insertion sort: lines repeat stamps out of order
		for j := i; j > 0 && out[j].Start < out[j-1].Start; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func pow10(n int) int {
	p := 1
	for range n {
		p *= 10
	}
	return p
}
