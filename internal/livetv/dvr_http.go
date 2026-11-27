package livetv

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/livetv/dvr"
	"github.com/go-chi/chi/v5"
)

// dvrJobRequest schedules one airing: a guide programme by ID, or "record
// this channel now" for Minutes.
type dvrJobRequest struct {
	ProgrammeID    string `json:"programmeId"`
	ChannelID      string `json:"channelId"`
	Minutes        int    `json:"minutes"`
	PrePadSeconds  *int   `json:"prePadSeconds"`
	PostPadSeconds *int   `json:"postPadSeconds"`
}

type dvrStatus struct {
	Enabled bool   `json:"enabled"`
	Dir     string `json:"dir,omitempty"`
	Active  int    `json:"active"`
}

func decodeOwnerJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid request: %w", err)
	}
	return nil
}

// dvrRoutes is the owner API for recordings: /dvr/jobs (Timers), /dvr/rules
// (SeriesTimers) and /dvr/recordings.
func (s *Service) dvrRoutes(r chi.Router) {
	d := s.dvr
	on := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if d == nil {
				ownerResult(w, nil, errDVROff)
				return
			}
			h(w, r)
		}
	}
	r.Get("/dvr", func(w http.ResponseWriter, r *http.Request) {
		if d == nil {
			ownerResult(w, dvrStatus{}, nil)
			return
		}
		d.mu.Lock()
		active := len(d.active)
		d.mu.Unlock()
		ownerResult(w, dvrStatus{Enabled: true, Dir: d.settings().Dir, Active: active}, nil)
	})
	r.Get("/dvr/jobs", on(func(w http.ResponseWriter, r *http.Request) {
		q := dvr.JobQuery{RuleID: r.URL.Query().Get("ruleId"), ChannelID: r.URL.Query().Get("channelId")}
		for _, st := range strings.Split(r.URL.Query().Get("status"), ",") {
			if st = strings.TrimSpace(st); st != "" {
				q.Statuses = append(q.Statuses, dvr.JobStatus(st))
			}
		}
		if r.URL.Query().Get("upcoming") == "1" {
			q.From = s.o.Now()
		}
		jobs, err := d.store.Jobs(r.Context(), q)
		ownerResult(w, jobs, err)
	}))
	r.Get("/dvr/jobs/{id}", on(func(w http.ResponseWriter, r *http.Request) {
		j, err := d.store.Job(r.Context(), chi.URLParam(r, "id"))
		ownerResult(w, j, err)
	}))
	r.Post("/dvr/jobs", on(func(w http.ResponseWriter, r *http.Request) {
		var req dvrJobRequest
		if err := decodeOwnerJSON(r, &req); err != nil {
			ownerResult(w, nil, err)
			return
		}
		owner, err := s.dvrOwner(r.Context())
		if err != nil {
			ownerResult(w, nil, err)
			return
		}
		j, err := s.dvrScheduleOnce(r, owner, req)
		if err == nil {
			d.Wake()
		}
		ownerResult(w, j, err)
	}))
	r.Delete("/dvr/jobs/{id}", on(func(w http.ResponseWriter, r *http.Request) {
		j, err := d.cancel(r, chi.URLParam(r, "id"))
		ownerResult(w, j, err)
	}))
	r.Get("/dvr/rules", on(func(w http.ResponseWriter, r *http.Request) {
		rules, err := d.store.Rules(r.Context(), 0)
		ownerResult(w, rules, err)
	}))
	r.Post("/dvr/rules", on(func(w http.ResponseWriter, r *http.Request) {
		var rule dvr.Rule
		if err := decodeOwnerJSON(r, &rule); err != nil {
			ownerResult(w, nil, err)
			return
		}
		owner, err := s.dvrOwner(r.Context())
		if err != nil {
			ownerResult(w, nil, err)
			return
		}
		rule.ID, rule.UserID = "", owner.ID
		s.dvrRuleDefaults(&rule)
		err = d.store.SaveRule(r.Context(), &rule)
		if err == nil {
			d.Replan()
		}
		ownerResult(w, rule, err)
	}))
	r.Put("/dvr/rules/{id}", on(func(w http.ResponseWriter, r *http.Request) {
		old, err := d.store.Rule(r.Context(), chi.URLParam(r, "id"))
		if err != nil {
			ownerResult(w, nil, err)
			return
		}
		var rule dvr.Rule
		if err := decodeOwnerJSON(r, &rule); err != nil {
			ownerResult(w, nil, err)
			return
		}
		rule.ID, rule.UserID = old.ID, old.UserID
		err = d.store.SaveRule(r.Context(), &rule)
		if err == nil {
			d.Replan()
		}
		ownerResult(w, rule, err)
	}))
	r.Delete("/dvr/rules/{id}", on(func(w http.ResponseWriter, r *http.Request) {
		err := d.store.DeleteRule(r.Context(), chi.URLParam(r, "id"))
		if err == nil {
			d.Replan()
		}
		ownerResult(w, map[string]bool{"ok": err == nil}, err)
	}))
	r.Get("/dvr/recordings", on(func(w http.ResponseWriter, r *http.Request) {
		recs, err := d.store.Recordings(r.Context(), 0)
		ownerResult(w, recs, err)
	}))
	r.Post("/dvr/plan", on(func(w http.ResponseWriter, r *http.Request) {
		d.Replan()
		ownerResult(w, map[string]bool{"ok": true}, nil)
	}))
}

// dvrRuleDefaults gives a new rule the server's default padding and an
// any-time slot when the request left them out.
func (s *Service) dvrRuleDefaults(rule *dvr.Rule) {
	set := s.dvr.settings()
	if rule.PrePad == 0 {
		rule.PrePad = set.PrePad
	}
	if rule.PostPad == 0 {
		rule.PostPad = set.PostPad
	}
	if rule.SlotMinute == 0 && rule.Weekdays == 0 {
		rule.SlotMinute = -1
	}
}

const dvrMaxInstantMinutes = 12 * 60

func (s *Service) dvrScheduleOnce(r *http.Request, owner *core.User, req dvrJobRequest) (dvr.Job, error) {
	ctx := r.Context()
	set := s.dvr.settings()
	pre, post := set.PrePad, set.PostPad
	if req.PrePadSeconds != nil {
		pre = time.Duration(*req.PrePadSeconds) * time.Second
	}
	if req.PostPadSeconds != nil {
		post = time.Duration(*req.PostPadSeconds) * time.Second
	}
	now := s.o.Now()
	var p dvr.Programme
	switch {
	case req.ProgrammeID != "":
		found, err := s.dvrFindProgramme(r, owner, req.ProgrammeID)
		if err != nil {
			return dvr.Job{}, err
		}
		p = found
	case req.ChannelID != "" && req.Minutes > 0:
		if req.Minutes > dvrMaxInstantMinutes {
			return dvr.Job{}, fmt.Errorf("record at most %d minutes at once", dvrMaxInstantMinutes)
		}
		c, err := s.AllowedChannel(ctx, owner, req.ChannelID)
		if err != nil {
			return dvr.Job{}, err
		}
		p = dvr.Programme{ID: "now-" + randomID()[:12], ChannelID: c.ID, ChannelName: c.Name, Start: now, End: now.Add(time.Duration(req.Minutes) * time.Minute), Title: c.Name}
		if ps, err := s.GuideForChannels(ctx, owner, now, now.Add(time.Minute), []string{c.ID}); err == nil {
			for _, g := range ps {
				if g.ChannelID == c.ID && !g.Start.After(now) && g.End.After(now) && g.Item.Title != "" {
					cur := dvrProgramme(g, c.Name)
					cur.ID, cur.Start, cur.End = p.ID, p.Start, p.End
					p = cur
					break
				}
			}
		}
		if req.PrePadSeconds == nil {
			pre = 0
		}
		if req.PostPadSeconds == nil {
			post = 0
		}
	default:
		return dvr.Job{}, fmt.Errorf("give a programmeId, or a channelId and minutes")
	}
	return s.dvr.store.ScheduleOnce(ctx, owner.ID, p, pre, post, 100)
}

// dvrFindProgramme looks a guide programme up by ID in the planner's window.
func (s *Service) dvrFindProgramme(r *http.Request, u *core.User, id string) (dvr.Programme, error) {
	now := s.o.Now()
	ps, err := dvrGuide{s}.DVRGuide(r.Context(), u.ID, now.Add(-12*time.Hour), now.Add(dvr.Horizon))
	if err != nil {
		return dvr.Programme{}, err
	}
	for _, p := range ps {
		if p.ID == id {
			return p, nil
		}
	}
	return dvr.Programme{}, core.ErrNotFound
}

// cancel stops a job. A job recording on this node stops and keeps what it
// recorded; one recording elsewhere cannot be stopped from here.
func (d *dvrRuntime) cancel(r *http.Request, id string) (dvr.Job, error) {
	j, err := d.store.CancelJob(r.Context(), id)
	if errors.Is(err, dvr.ErrRunning) {
		if d.stop(id) {
			return j, nil
		}
		return j, fmt.Errorf("this recording is running on another server")
	}
	return j, err
}
