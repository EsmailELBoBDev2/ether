package screens

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/braedonsaunders/ether/internal/core"
)

// Plugin pushes (docs/plugin-screens.md, Records, revs and live updates). A
// plugin may say "re-read now" (Invalidate) or hand Ether new values for its
// own records (PutRecords). Both reach only streams that are open on that
// plugin's pages; nothing is stored or queued for anyone else.

type pluginSignal struct {
	records map[string]RecordEntry
}

type pluginWatch struct {
	mu       sync.Mutex
	next     int
	watchers map[string]map[int]chan pluginSignal
}

func (s *Service) watchPlugins(pluginIDs []string) (<-chan pluginSignal, func()) {
	ch := make(chan pluginSignal, 4)
	w := &s.pushes
	w.mu.Lock()
	if w.watchers == nil {
		w.watchers = map[string]map[int]chan pluginSignal{}
	}
	w.next++
	id := w.next
	for _, p := range pluginIDs {
		if w.watchers[p] == nil {
			w.watchers[p] = map[int]chan pluginSignal{}
		}
		w.watchers[p][id] = ch
	}
	w.mu.Unlock()
	return ch, func() {
		w.mu.Lock()
		for _, p := range pluginIDs {
			delete(w.watchers[p], id)
			if len(w.watchers[p]) == 0 {
				delete(w.watchers, p)
			}
		}
		w.mu.Unlock()
	}
}

func (s *Service) signal(pluginID string, sig pluginSignal) int {
	w := &s.pushes
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, ch := range w.watchers[pluginID] {
		select {
		case ch <- sig:
			n++
		default:
			// A stream already has a re-read pending; one is enough.
		}
	}
	return n
}

func pluginsOf(topics []openTopic) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range topics {
		if p := t.target.plugin.pluginID; !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// Invalidate drops pluginID's cached source answers and makes every open
// stream on its pages read again. It returns how many streams heard it.
func (s *Service) Invalidate(pluginID string) int {
	if s == nil || pluginID == "" {
		return 0
	}
	s.answers.forget(pluginID)
	return s.signal(pluginID, pluginSignal{})
}

// PutRecord is one plugin-defined record a plugin pushes.
type PutRecord struct {
	Type  string
	ID    string
	Value any
}

// MaxPutRecords bounds one push.
const MaxPutRecords = 100

// ErrNoDocuments reports a push from a plugin with no document screens.
var ErrNoDocuments = errors.New("screens: plugin has no screens or surfaces")

// PutRecords validates and rewrites a plugin's pushed records (as a source
// answer's records) and sends them to open streams that show them. Images
// in pushed records are dropped: grants are per viewer, and a push has no
// viewer, so a plugin that needs a new picture invalidates instead.
func (s *Service) PutRecords(ctx context.Context, pluginID string, recs []PutRecord) (int, error) {
	if s == nil || s.docs == nil {
		return 0, ErrNoDocuments
	}
	if len(recs) > MaxPutRecords {
		return 0, fmt.Errorf("screens: at most %d records per push", MaxPutRecords)
	}
	s.docs.mu.RLock()
	e := s.docs.byID[pluginID]
	s.docs.mu.RUnlock()
	if e == nil {
		return 0, ErrNoDocuments
	}
	rc := &readCtx{ctx: ctx, s: s, t: target{plugin: e}, records: map[string]any{}, events: map[string]*eventFetch{}, viewer: &core.User{}}
	env := &envelopes{rc: rc, known: map[string]bool{}, events: map[string]bool{}, entities: map[string]entityRef{}}
	out := map[string]any{}
	for _, r := range recs {
		if !recordTypeRe.MatchString(r.Type) || !recordIDRe.MatchString(r.ID) {
			return 0, fmt.Errorf("screens: record type and id must be slugs")
		}
		out["p:"+r.Type+":"+r.ID] = env.walk(normalize(r.Value))
	}
	// Events and entities need a viewer to be filtered; a push only carries
	// the plugin's own records.
	stamped := map[string]RecordEntry{}
	s.revs.stamp(out, stamped)
	return s.signal(pluginID, pluginSignal{records: stamped}), nil
}

func normalize(v any) any {
	return v
}
