package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/experience"
	"github.com/braedonsaunders/ether/internal/frontend"
	"github.com/braedonsaunders/ether/internal/pluginitems"
	"github.com/braedonsaunders/ether/internal/plugins"
)

const (
	MaxKindExpandPages = 20
	MaxKindExpandItems = 2000
)

var (
	_              experience.PluginSliceExpander = (*KindCatalog)(nil)
	expandLaunchMu sync.Mutex
)

type expandTarget struct {
	facet    string
	valueKey string
	req      core.KindExpandRequest
}

// MaybeExpand checks whether viewer browse query q opens a single declared
// expandable facet bucket or a plugin person's credits on lib. If an expansion
// for that slice is currently running it returns true immediately; if no fresh
// expansion exists within ttlHours it launches a background expansion job via
// Manager.StartExpandJob and returns true; otherwise it returns false without
// blocking the viewer request.
func (c *KindCatalog) MaybeExpand(ctx context.Context, lib core.Library, q frontend.ItemQuery) bool {
	if c == nil || c.items == nil || c.mgr == nil || lib.Source != core.LibraryPlugin {
		return false
	}
	if q.SuppressExpand || strings.TrimSpace(q.Filter.Query) != "" {
		return false
	}
	if !c.IsPluginLibraryActive(ctx, lib) {
		return false
	}
	rec, err := c.items.GetKindByLibrary(ctx, lib.ID)
	if err != nil || rec.Decl.Browse.Expand == nil || len(rec.Decl.Browse.Expand.Facets) == 0 {
		return false
	}

	expandable := make(map[string]bool, len(rec.Decl.Browse.Expand.Facets))
	for _, fk := range rec.Decl.Browse.Expand.Facets {
		expandable[fk] = true
	}
	peopleFields := make(map[string]bool)
	for _, fd := range rec.Decl.Fields {
		if fd.Type == "people" {
			peopleFields[fd.Key] = true
		}
	}

	var targets []expandTarget
	switch {
	case q.Filter.Person == 0:
		mapped := mapFilterToFacets(rec.Decl, q.Filter)
		if len(mapped) != 1 {
			return false
		}
		for field, vals := range mapped {
			if !expandable[field] || len(vals) != 1 {
				return false
			}
			val := strings.TrimSpace(vals[0])
			if val == "" {
				return false
			}
			if peopleFields[field] {
				if pid, canonName, ok, _ := c.items.LookupPersonFacetByName(ctx, lib.ID, field, val); ok && core.IsPluginPersonID(pid) {
					targets = append(targets, expandTarget{
						facet:    field,
						valueKey: strconv.FormatInt(pid, 10),
						req: core.KindExpandRequest{
							Facet:   field,
							Value:   canonName,
							ValueID: pid,
						},
					})
					break
				}
			}
			targets = append(targets, expandTarget{
				facet:    field,
				valueKey: val,
				req: core.KindExpandRequest{
					Facet: field,
					Value: val,
				},
			})
		}

	case core.IsPluginPersonID(q.Filter.Person) && len(q.Filter.Facets) == 0 && len(q.Filter.Genres) == 0 && q.Filter.Studio == "":
		var expandPeople []string
		for _, fk := range rec.Decl.Browse.Expand.Facets {
			if peopleFields[fk] && !slices.Contains(expandPeople, fk) {
				expandPeople = append(expandPeople, fk)
			}
		}
		if len(expandPeople) == 0 {
			return false
		}
		matches, err := c.items.LookupPersonFacetsByID(ctx, lib.ID, expandPeople, q.Filter.Person)
		if err != nil || len(matches) == 0 {
			return false
		}
		pidKey := strconv.FormatInt(q.Filter.Person, 10)
		for _, m := range matches {
			targets = append(targets, expandTarget{
				facet:    m.Field,
				valueKey: pidKey,
				req: core.KindExpandRequest{
					Facet:   m.Field,
					Value:   m.Name,
					ValueID: q.Filter.Person,
				},
			})
		}
	}

	if len(targets) == 0 {
		return false
	}

	ttl := time.Duration(rec.Decl.Browse.Expand.EffectiveTTLHours()) * time.Hour
	now := c.items.Now()
	routePath := "/ether/v1/kinds/" + rec.KindKey + "/expand"
	anyRunning := false

	for _, t := range targets {
		dedupKey := rec.KindKey + "\x00" + t.facet + "\x00" + t.valueKey
		expandLaunchMu.Lock()
		if c.mgr.IsExpandJobRunning(rec.PluginID, dedupKey) {
			expandLaunchMu.Unlock()
			anyRunning = true
			continue
		}
		exp, ok, err := c.items.GetFacetExpansion(ctx, rec.PluginID, rec.KindKey, t.facet, t.valueKey)
		if err == nil && ok && exp.IsFresh(now, ttl) {
			expandLaunchMu.Unlock()
			continue
		}
		ready := make(chan struct{})
		target := t
		started, startErr := c.mgr.StartExpandJob(rec.PluginID, routePath, dedupKey, func(jobCtx context.Context, run *plugins.ExpandJobRunner) error {
			<-ready
			return c.runFacetExpansion(jobCtx, run, rec, lib.ID, routePath, target)
		})
		if startErr == nil && !started {
			close(ready)
			expandLaunchMu.Unlock()
			anyRunning = true
			continue
		}
		if startErr != nil {
			close(ready)
			expandLaunchMu.Unlock()
			c.log.Debug("facet expansion deferred", "plugin", rec.PluginID, "kind", rec.KindKey, "facet", t.facet, "err", startErr)
			continue
		}
		markCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = c.items.MarkFacetExpansionRunning(markCtx, rec.PluginID, rec.KindKey, lib.ID, t.facet, t.valueKey)
		cancel()
		close(ready)
		expandLaunchMu.Unlock()
		anyRunning = true
	}

	return anyRunning
}

func (c *KindCatalog) runFacetExpansion(jobCtx context.Context, run *plugins.ExpandJobRunner, rec pluginitems.KindRecord, libID core.LibraryID, routePath string, target expandTarget) error {
	finish := func(state string, added int, errMsg string) {
		finCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.items.FinishFacetExpansion(finCtx, rec.PluginID, rec.KindKey, libID, target.facet, target.valueKey, state, added, errMsg)
		c.items.BumpGeneration()
	}

	rootLevel := "item"
	if len(rec.Decl.Levels) > 0 {
		rootLevel = rec.Decl.Levels[0].Key
	}

	upsertSlice := func(items []core.KindItem) (accepted int, added int, err error) {
		normalized := make([]core.KindItem, len(items))
		copy(normalized, items)
		for i := range normalized {
			if normalized[i].Level == "" && normalized[i].ParentID == "" {
				normalized[i].Level = rootLevel
			}
		}
		for start := 0; start < len(normalized); start += pluginitems.MaxUpsertBatch {
			end := min(start+pluginitems.MaxUpsertBatch, len(normalized))
			acc, add, uErr := c.items.UpsertItemsWithAdded(jobCtx, rec.PluginID, rec.KindKey, normalized[start:end])
			if uErr != nil {
				return accepted, added, uErr
			}
			accepted += acc
			added += add
		}
		return accepted, added, nil
	}

	var (
		cursor        string
		totalAccepted int
		totalAdded    int
		seenCursor    = make(map[string]bool)
	)

	for pageIdx := 0; pageIdx < MaxKindExpandPages && totalAccepted < MaxKindExpandItems; pageIdx++ {
		req := target.req
		req.Cursor = cursor
		body, err := json.Marshal(req)
		if err != nil {
			finish(pluginitems.ExpansionStateError, totalAdded, err.Error())
			return err
		}

		resp, err := run.Call(jobCtx, plugins.Request{
			PluginID: rec.PluginID,
			Method:   http.MethodPost,
			Path:     routePath,
			Headers:  map[string]string{"content-type": "application/json"},
			Body:     body,
		})
		if err != nil {
			finish(pluginitems.ExpansionStateError, totalAdded, err.Error())
			return err
		}
		if resp.Status == http.StatusNotFound || resp.Status == http.StatusNotImplemented {
			finish(pluginitems.ExpansionStateDone, totalAdded, "")
			return nil
		}
		if resp.Status < 200 || resp.Status >= 300 {
			callErr := fmt.Errorf("expand %s/%s (%s=%s): status %d", rec.PluginID, rec.KindKey, target.facet, target.req.Value, resp.Status)
			finish(pluginitems.ExpansionStateError, totalAdded, callErr.Error())
			return callErr
		}

		var page core.KindExpandResponse
		if len(resp.Body) > 0 {
			if err := json.Unmarshal(resp.Body, &page); err != nil {
				finish(pluginitems.ExpansionStateError, totalAdded, err.Error())
				return err
			}
		}

		if len(page.Items) > 0 {
			remaining := MaxKindExpandItems - totalAccepted
			batch := page.Items
			if len(batch) > remaining {
				batch = batch[:remaining]
			}
			acc, add, uErr := upsertSlice(batch)
			if uErr != nil {
				finish(pluginitems.ExpansionStateError, totalAdded, uErr.Error())
				return uErr
			}
			totalAccepted += acc
			totalAdded += add
			run.SetProgress(fmt.Sprintf("%s: %s (%d items)", target.facet, target.req.Value, totalAccepted))
		}

		if jobPath := strings.TrimSpace(page.Job); jobPath != "" {
			beforeCount, _ := c.items.CountKindItems(jobCtx, rec.PluginID, rec.KindKey)
			stepAdded := 0
			contErr := run.RunContinuation(jobCtx, jobPath, body, func(stepBody json.RawMessage) error {
				var stepItems struct {
					Items []core.KindItem `json:"items"`
				}
				if json.Unmarshal(stepBody, &stepItems) == nil && len(stepItems.Items) > 0 && totalAccepted < MaxKindExpandItems {
					rem := MaxKindExpandItems - totalAccepted
					batch := stepItems.Items
					if len(batch) > rem {
						batch = batch[:rem]
					}
					acc, add, uErr := upsertSlice(batch)
					if uErr != nil {
						return uErr
					}
					totalAccepted += acc
					totalAdded += add
					stepAdded += add
				}
				return nil
			})
			cntCtx, cntCancel := context.WithTimeout(context.Background(), 5*time.Second)
			afterCount, _ := c.items.CountKindItems(cntCtx, rec.PluginID, rec.KindKey)
			cntCancel()
			if hostAdded := afterCount - beforeCount - stepAdded; hostAdded > 0 {
				totalAdded += hostAdded
			}
			if contErr != nil {
				finish(pluginitems.ExpansionStateError, totalAdded, contErr.Error())
				return contErr
			}
			finish(pluginitems.ExpansionStateDone, totalAdded, "")
			return nil
		}

		next := page.EffectiveCursor()
		if next == "" || len(page.Items) == 0 || seenCursor[next] || totalAccepted >= MaxKindExpandItems {
			break
		}
		seenCursor[next] = true
		cursor = next
	}

	finish(pluginitems.ExpansionStateDone, totalAdded, "")
	return nil
}
