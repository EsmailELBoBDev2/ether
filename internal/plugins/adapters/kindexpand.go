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
	MaxKindExpandPages           = 20
	MaxKindExpandItems           = 2000
	containerExpandFacet         = "__container__"
	maxContainerHydratePerPlugin = 4
	containerHydrateBGTimeout    = 2 * time.Minute
)

// containerHydrateWait bounds how long opening an unhydrated (or stale)
// container waits for its first page of children before returning stored rows
// while pagination continues in the background.
var containerHydrateWait = 1800 * time.Millisecond

type containerHydrateCall struct {
	firstPage chan struct{}
}

// firstOpenWait bounds how long the first page of a studio or performer
// waits for its fill's first page: the plugin returns the newest titles first,
// so once that page is stored the first screen is already right, and the rest
// of the catalogue keeps filling in behind the viewer's scroll.
var firstOpenWait = 20 * time.Second

var (
	_              experience.PluginSliceExpander = (*KindCatalog)(nil)
	expandLaunchMu sync.Mutex
	// expandReady holds one channel per running fill (dedup key), closed once
	// the fill has stored its first page (or ended), so first-page readers
	// can wait for it.
	expandReady sync.Map
)

type expandTarget struct {
	facet    string
	valueKey string
	req      core.KindExpandRequest
}

// MaybeExpand checks whether viewer browse query q opens a single declared
// expandable facet bucket or a plugin person's credits on lib. When no fresh
// expansion exists within ttlHours it launches one via Manager.StartExpandJob.
// The first page of the slice then waits (at most firstOpenWait) for the
// fill's first stored page, so the first open shows the newest titles. It
// reports whether it gave up before that page arrived; later pages never wait.
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
	var waits []chan struct{}
	now := c.items.Now()
	routePath := "/ether/v1/kinds/" + rec.KindKey + "/expand"
	anyRunning := false

	for _, t := range targets {
		dedupKey := rec.KindKey + "\x00" + t.facet + "\x00" + t.valueKey
		expandLaunchMu.Lock()
		if c.mgr.IsExpandJobRunning(rec.PluginID, dedupKey) {
			if ch, ok := expandReady.Load(dedupKey); ok {
				waits = append(waits, ch.(chan struct{}))
			}
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
		firstPage := make(chan struct{})
		var firstOnce sync.Once
		signal := func() { firstOnce.Do(func() { close(firstPage) }) }
		target := t
		started, startErr := c.mgr.StartExpandJob(rec.PluginID, routePath, dedupKey, func(jobCtx context.Context, run *plugins.ExpandJobRunner) error {
			defer func() {
				expandReady.Delete(dedupKey)
				signal()
			}()
			<-ready
			return c.runFacetExpansion(jobCtx, run, rec, lib.ID, routePath, target, signal)
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
		expandReady.Store(dedupKey, firstPage)
		waits = append(waits, firstPage)
		close(ready)
		expandLaunchMu.Unlock()
		anyRunning = true
	}

	if !anyRunning {
		return false
	}
	// The first page waits for the fill's first stored page (its own fill or
	// another viewer's), so a viewer opening a studio or performer sees the
	// right titles the first time. Later pages never wait.
	if q.Offset != 0 || q.StartAt != 0 || q.AdjacentTo != 0 || len(waits) == 0 || firstOpenWait <= 0 {
		return true
	}
	timer := time.NewTimer(firstOpenWait)
	defer timer.Stop()
	for _, ch := range waits {
		select {
		case <-ch:
		case <-timer.C:
			return true
		case <-ctx.Done():
			return true
		}
	}
	return false
}

func (c *KindCatalog) runFacetExpansion(jobCtx context.Context, run *plugins.ExpandJobRunner, rec pluginitems.KindRecord, libID core.LibraryID, routePath string, target expandTarget, firstPage func()) error {
	var (
		pages              int
		callTime, saveTime time.Duration
		began              = time.Now()
	)
	finish := func(state string, added int, errMsg string) {
		c.log.Info("facet expansion finished", "plugin", rec.PluginID, "kind", rec.KindKey, "facet", target.facet,
			"state", state, "pages", pages, "added", added, "took", time.Since(began), "plugin_time", callTime, "save_time", saveTime, "err", errMsg)
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

		callStart := time.Now()
		resp, err := run.Call(plugins.WithBackgroundCall(jobCtx), plugins.Request{
			PluginID: rec.PluginID,
			Method:   http.MethodPost,
			Path:     routePath,
			Headers:  map[string]string{"content-type": "application/json"},
			Body:     body,
		})
		callTime += time.Since(callStart)
		pages++
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
			saveStart := time.Now()
			acc, add, uErr := upsertSlice(batch)
			saveTime += time.Since(saveStart)
			if uErr != nil {
				finish(pluginitems.ExpansionStateError, totalAdded, uErr.Error())
				return uErr
			}
			totalAccepted += acc
			totalAdded += add
			c.items.BumpGeneration()
			firstPage()
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

// maybeHydrateContainer checks in O(1) whether an opened container (with
// ChildCount > 0) has missing or stale children past the kind's expand TTL.
// When hydration is needed, it launches a per-container deduplicated fetch
// via POST /ether/v1/kinds/{kind}/expand with {parentId, level} (falling back
// to POST /ether/v1/kinds/{kind}/browse with parentId), waits up to
// containerHydrateWait (~1.8s, clamped by ctx) for the first page to commit,
// and lets remaining pages finish in the background.
func (c *KindCatalog) maybeHydrateContainer(ctx context.Context, rec pluginitems.KindRecord, lib core.Library, parentItem core.KindItem, levelIdx int) {
	if c == nil || c.items == nil || c.mgr == nil {
		return
	}
	if parentItem.ChildCount <= 0 || levelIdx < 0 || levelIdx >= len(rec.Decl.Levels) {
		return
	}
	isContainer := levelIdx < len(rec.Decl.Levels)-1 || rec.Decl.Levels[levelIdx].Container
	if !isContainer || !c.IsPluginLibraryActive(ctx, lib) {
		return
	}

	ttlHours := plugins.DefaultKindExpandTTLHours
	if rec.Decl.Browse.Expand != nil {
		ttlHours = rec.Decl.Browse.Expand.EffectiveTTLHours()
	}
	ttl := time.Duration(ttlHours) * time.Hour
	now := c.items.Now()

	sfKey := rec.PluginID + "\x00" + rec.KindKey + "\x00" + parentItem.ID

	c.hydrateMu.Lock()
	if call, inflight := c.hydrateInflight[sfKey]; inflight {
		c.hydrateMu.Unlock()
		waitContainerHydrate(ctx, call.firstPage)
		return
	}
	// Check durability + stored child freshness under the lock only after
	// verifying no inflight hydration is running, avoiding duplicate launches.
	exp, hasExp, expErr := c.items.GetFacetExpansion(ctx, rec.PluginID, rec.KindKey, containerExpandFacet, parentItem.ID)
	if expErr == nil && hasExp && exp.IsFresh(now, ttl) {
		c.hydrateMu.Unlock()
		return
	}
	childCount, sampleAt, childErr := c.items.ChildrenCountAndFreshness(ctx, lib.ID, parentItem.Ref)
	if childErr == nil && childCount > 0 && (parentItem.ChildCount <= 0 || childCount >= parentItem.ChildCount) && !sampleAt.IsZero() && now.Sub(sampleAt) < ttl {
		c.hydrateMu.Unlock()
		return
	}

	call := &containerHydrateCall{firstPage: make(chan struct{})}
	if c.hydrateInflight == nil {
		c.hydrateInflight = make(map[string]*containerHydrateCall)
	}
	if c.hydrateSems == nil {
		c.hydrateSems = make(map[string]chan struct{})
	}
	c.hydrateInflight[sfKey] = call
	sem := c.hydrateSems[rec.PluginID]
	if sem == nil {
		sem = make(chan struct{}, maxContainerHydratePerPlugin)
		c.hydrateSems[rec.PluginID] = sem
	}
	c.hydrateMu.Unlock()

	go c.runContainerHydration(sfKey, sem, rec, lib.ID, parentItem, levelIdx, call)
	waitContainerHydrate(ctx, call.firstPage)
}

func waitContainerHydrate(ctx context.Context, firstPage <-chan struct{}) {
	wait := containerHydrateWait
	if wait <= 0 {
		return
	}
	if dl, ok := ctx.Deadline(); ok {
		if rem := time.Until(dl); rem <= 0 {
			return
		} else if rem < wait {
			wait = rem
		}
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-firstPage:
	case <-timer.C:
	case <-ctx.Done():
	}
}

func (c *KindCatalog) runContainerHydration(
	sfKey string,
	sem chan struct{},
	rec pluginitems.KindRecord,
	libID core.LibraryID,
	parentItem core.KindItem,
	levelIdx int,
	call *containerHydrateCall,
) {
	var firstOnce sync.Once
	signalFirst := func() { firstOnce.Do(func() { close(call.firstPage) }) }
	defer func() {
		signalFirst()
		c.hydrateMu.Lock()
		delete(c.hydrateInflight, sfKey)
		c.hydrateMu.Unlock()
	}()

	parentCtx := c.syncCtx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	bgCtx, cancel := context.WithTimeout(parentCtx, containerHydrateBGTimeout)
	defer cancel()

	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-bgCtx.Done():
		return
	}

	markCtx, markCancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = c.items.MarkFacetExpansionRunning(markCtx, rec.PluginID, rec.KindKey, libID, containerExpandFacet, parentItem.ID)
	markCancel()

	finish := func(state string, added int, errMsg string) {
		finCtx, finCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer finCancel()
		_ = c.items.FinishFacetExpansion(finCtx, rec.PluginID, rec.KindKey, libID, containerExpandFacet, parentItem.ID, state, added, errMsg)
		c.items.BumpGeneration()
	}

	childLevel := ""
	if levelIdx+1 < len(rec.Decl.Levels) {
		childLevel = rec.Decl.Levels[levelIdx+1].Key
	}
	expandPath := "/ether/v1/kinds/" + rec.KindKey + "/expand"
	browsePath := "/ether/v1/kinds/" + rec.KindKey + "/browse"

	upsertSlice := func(items []core.KindItem) (accepted int, added int, err error) {
		normalized := make([]core.KindItem, len(items))
		copy(normalized, items)
		for i := range normalized {
			if normalized[i].ParentID == "" {
				normalized[i].ParentID = parentItem.ID
			}
			if normalized[i].Level == "" && childLevel != "" {
				normalized[i].Level = childLevel
			}
		}
		for start := 0; start < len(normalized); start += pluginitems.MaxUpsertBatch {
			end := min(start+pluginitems.MaxUpsertBatch, len(normalized))
			acc, add, uErr := c.items.UpsertItemsWithAdded(bgCtx, rec.PluginID, rec.KindKey, normalized[start:end])
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
		useBrowse     bool
		totalAccepted int
		totalAdded    int
		seenCursor    = make(map[string]bool)
	)

	callExpand := func(cur string) (items []core.KindItem, next string, fallback bool, unsupported bool, err error) {
		body, mErr := json.Marshal(core.KindExpandRequest{
			ParentID: parentItem.ID,
			Level:    childLevel,
			Cursor:   cur,
		})
		if mErr != nil {
			return nil, "", false, false, mErr
		}
		resp, cErr := c.mgr.Call(plugins.WithBackgroundCall(bgCtx), rec.PluginID, plugins.Request{
			PluginID: rec.PluginID,
			Method:   http.MethodPost,
			Path:     expandPath,
			Headers:  map[string]string{"content-type": "application/json"},
			Body:     body,
		})
		if cErr != nil {
			return nil, "", false, false, cErr
		}
		if resp.Status == http.StatusNotFound || resp.Status == http.StatusNotImplemented || resp.Status == http.StatusBadRequest {
			return nil, "", true, false, nil
		}
		if resp.Status < 200 || resp.Status >= 300 {
			return nil, "", false, false, fmt.Errorf("container expand %s/%s (%s): status %d", rec.PluginID, rec.KindKey, parentItem.ID, resp.Status)
		}
		var page core.KindExpandResponse
		if len(resp.Body) > 0 {
			if uErr := json.Unmarshal(resp.Body, &page); uErr != nil {
				return nil, "", false, false, uErr
			}
		}
		return page.Items, page.EffectiveCursor(), false, false, nil
	}

	callBrowse := func(cur string) (items []core.KindItem, next string, unsupported bool, err error) {
		body, mErr := json.Marshal(core.KindBrowseRequest{
			ParentID: parentItem.ID,
			Level:    childLevel,
			Cursor:   cur,
			Limit:    pluginitems.DefaultWalkPageLimit,
		})
		if mErr != nil {
			return nil, "", false, mErr
		}
		resp, cErr := c.mgr.Call(plugins.WithBackgroundCall(bgCtx), rec.PluginID, plugins.Request{
			PluginID: rec.PluginID,
			Method:   http.MethodPost,
			Path:     browsePath,
			Headers:  map[string]string{"content-type": "application/json"},
			Body:     body,
		})
		if cErr != nil {
			return nil, "", false, cErr
		}
		if resp.Status == http.StatusNotFound || resp.Status == http.StatusNotImplemented {
			return nil, "", true, nil
		}
		if resp.Status < 200 || resp.Status >= 300 {
			return nil, "", false, fmt.Errorf("container browse %s/%s (%s): status %d", rec.PluginID, rec.KindKey, parentItem.ID, resp.Status)
		}
		var page core.KindPage
		if len(resp.Body) > 0 {
			if uErr := json.Unmarshal(resp.Body, &page); uErr != nil {
				return nil, "", false, uErr
			}
		}
		return page.Items, strings.TrimSpace(page.NextCursor), false, nil
	}

	for pageIdx := 0; pageIdx < MaxKindExpandPages && totalAccepted < MaxKindExpandItems; pageIdx++ {
		var (
			pageItems   []core.KindItem
			next        string
			fallback    bool
			unsupported bool
			err         error
		)
		if !useBrowse {
			pageItems, next, fallback, unsupported, err = callExpand(cursor)
			if err != nil {
				finish(pluginitems.ExpansionStateError, totalAdded, err.Error())
				return
			}
			// Fall back to /browse if /expand doesn't support container hydration
			// (404/501/400) or returned an empty first page for a non-empty container.
			if fallback || (pageIdx == 0 && len(pageItems) == 0 && next == "") {
				useBrowse = true
				pageItems, next, unsupported, err = callBrowse(cursor)
				if err != nil {
					finish(pluginitems.ExpansionStateError, totalAdded, err.Error())
					return
				}
				if unsupported {
					finish(pluginitems.ExpansionStateDone, totalAdded, "")
					return
				}
			}
		} else {
			pageItems, next, unsupported, err = callBrowse(cursor)
			if err != nil {
				finish(pluginitems.ExpansionStateError, totalAdded, err.Error())
				return
			}
			if unsupported {
				finish(pluginitems.ExpansionStateDone, totalAdded, "")
				return
			}
		}

		if len(pageItems) > 0 {
			remaining := MaxKindExpandItems - totalAccepted
			batch := pageItems
			if len(batch) > remaining {
				batch = batch[:remaining]
			}
			acc, add, uErr := upsertSlice(batch)
			if uErr != nil {
				finish(pluginitems.ExpansionStateError, totalAdded, uErr.Error())
				return
			}
			totalAccepted += acc
			totalAdded += add
			c.items.BumpGeneration()
			signalFirst()
		}

		if next == "" || len(pageItems) == 0 || seenCursor[next] || totalAccepted >= MaxKindExpandItems {
			break
		}
		seenCursor[next] = true
		cursor = next
	}

	finish(pluginitems.ExpansionStateDone, totalAdded, "")
}
