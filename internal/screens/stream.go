package screens

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plugins/screendoc"
)

// Live updates for open pages and surfaces. A stream holds up to four
// topics. It subscribes the live-events hub to exactly the feeds and events
// its sources read, so feeds are polled only while someone watches, and on
// each change it re-reads the topics' sources and sends what changed:
// records whose revision moved, and sources whose membership moved.

// LiveSubscriber subscribes the live-events hub for one viewer. updates
// signals that something the subscription covers changed.
type LiveSubscriber interface {
	Subscribe(ctx context.Context, viewer *core.User, feeds, eventIDs []string) (updates <-chan struct{}, closeFn func(), err error)
}

// SetLiveSubscriber installs the hub subscription used by streams.
func (s *Service) SetLiveSubscriber(ls LiveSubscriber) {
	if s != nil {
		s.mu.Lock()
		s.live = ls
		s.mu.Unlock()
	}
}

// Frame is one stream frame.
type Frame struct {
	Event string
	Data  json.RawMessage
}

// MaxStreamTopics bounds the topics of one stream.
const MaxStreamTopics = 4

// ErrTooManyTopics reports a stream asking for more than MaxStreamTopics.
var ErrTooManyTopics = errors.New("screens: too many stream topics")

// streamCoalesce gathers bursts of hub updates into one re-read.
const streamCoalesce = 400 * time.Millisecond

type openTopic struct {
	topic  Topic
	target target
}

// Stream opens topics for viewer and returns its frames. Frames stop when
// ctx ends or close is called.
func (s *Service) Stream(ctx context.Context, viewer *core.User, tokens []string, since int64, images ImageSigner) (<-chan Frame, func(), error) {
	if len(tokens) > MaxStreamTopics {
		return nil, nil, ErrTooManyTopics
	}
	var topics []openTopic
	feeds := map[string]bool{}
	events := map[string]bool{}
	for _, tok := range tokens {
		tp, err := s.OpenTopic(viewer, tok)
		if err != nil {
			return nil, nil, err
		}
		req := tp.Request()
		t, err := s.findTarget(ctx, viewer, req.Kind, req.ID, req.Page, req.ClientInfo)
		if err != nil {
			return nil, nil, err
		}
		b, err := bind(t, req.Params, tp.TZ)
		if err != nil {
			return nil, nil, err
		}
		for _, id := range topicSources(t, tp) {
			src := t.doc.Sources[id]
			feed, _ := src.Args["feed"].(string)
			switch src.From {
			case screendoc.FromLiveEvent:
				if id := strings.TrimPrefix(b.text(src.Args, "id"), "event:"); id != "" {
					events[id] = true
				}
			case screendoc.FromWatch:
				if id := strings.TrimPrefix(b.text(src.Args, "event"), "event:"); id != "" {
					events[id] = true
				}
			case screendoc.FromLiveEvents, screendoc.FromLiveSchedule, screendoc.FromLiveLeagues:
				if feed != "" {
					feeds[t.plugin.pluginID+":"+feed] = true
				}
			}
		}
		topics = append(topics, openTopic{topic: tp, target: t})
	}
	sctx, cancel := context.WithCancel(ctx)
	var updates <-chan struct{}
	unsubscribe := func() {}
	s.mu.RLock()
	ls := s.live
	s.mu.RUnlock()
	if ls != nil && (len(feeds) > 0 || len(events) > 0) {
		u, closeFn, err := ls.Subscribe(sctx, viewer, keys(feeds), keys(events))
		if err != nil {
			cancel()
			return nil, nil, err
		}
		updates, unsubscribe = u, closeFn
	}
	out := make(chan Frame, 8)
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			unsubscribe()
		})
	}
	go func() {
		defer close(out)
		defer stop()
		sent := map[string]int64{}
		floor := since
		push := func() bool {
			for _, ot := range topics {
				req := ot.topic.Request()
				req.Sources = topicSources(ot.target, ot.topic)
				req.Images = images
				res, err := s.Data(sctx, viewer, req)
				if err != nil {
					if errors.Is(err, core.ErrNotFound) {
						// The page went away for this viewer (unpublished,
						// access revoked): end the stream.
						return false
					}
					continue
				}
				changed := map[string]RecordEntry{}
				for k, e := range res.Records {
					if e.Rev > floor && e.Rev > sent["r:"+k] {
						changed[k] = e
						sent["r:"+k] = e.Rev
					}
				}
				if len(changed) > 0 && !send(sctx, out, "records", map[string]any{"records": changed}) {
					return false
				}
				for id, v := range res.Sources {
					rev := s.revs.stampSource(ot.target.plugin.pluginID+"\x00"+ot.target.wireID+"\x00"+ot.target.page+"\x00"+id+"\x00"+canonicalBindings(mustBind(ot), ot.target.doc.Sources[id].Deps), v)
					key := "s:" + ot.topic.Key() + "\x00" + id
					if rev <= floor || rev <= sent[key] {
						continue
					}
					sent[key] = rev
					frame := map[string]any{"source": id, "rev": rev, "value": v}
					if ot.target.kind == "surface" {
						frame["surface"] = ot.target.wireID
					} else {
						frame["screen"] = ot.target.wireID
						frame["page"] = ot.target.page
					}
					if !send(sctx, out, "source", frame) {
						return false
					}
				}
			}
			floor = 0
			return true
		}
		if !push() {
			return
		}
		for {
			select {
			case <-sctx.Done():
				return
			case _, ok := <-updates:
				if !ok {
					return
				}
				timer := time.NewTimer(streamCoalesce)
			drain:
				for {
					select {
					case <-updates:
					case <-timer.C:
						break drain
					case <-sctx.Done():
						timer.Stop()
						return
					}
				}
				if !push() {
					return
				}
			}
		}
	}()
	return out, stop, nil
}

func mustBind(ot openTopic) bindings {
	b, err := bind(ot.target, ot.topic.Params, ot.topic.TZ)
	if err != nil {
		return bindings{}
	}
	return b
}

// topicSources are the sources a topic re-reads: the ones its data read
// asked for, else every non-lazy source.
func topicSources(t target, tp Topic) []string {
	if len(tp.Sources) > 0 {
		var out []string
		for _, id := range tp.Sources {
			if _, ok := t.doc.Sources[id]; ok {
				out = append(out, id)
			}
		}
		return out
	}
	var out []string
	for _, id := range t.doc.SourceIDs() {
		if !t.doc.Sources[id].Lazy {
			out = append(out, id)
		}
	}
	return out
}

func send(ctx context.Context, out chan<- Frame, event string, v any) bool {
	raw, err := json.Marshal(v)
	if err != nil {
		return true
	}
	select {
	case out <- Frame{Event: event, Data: raw}:
		return true
	case <-ctx.Done():
		return false
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
