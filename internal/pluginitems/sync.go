package pluginitems

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/plugins"
)

const (
	DefaultWalkPageLimit = 200
	DefaultChangesLimit  = 500
	MaxUpsertBatch       = plugins.MaxKindUpsertItems
)

// SyncOptions controls a single SyncKind invocation.
type SyncOptions struct {
	Force       bool   // bypass the 50%-delete guard (admin override)
	FullWalk    bool   // force a full browse walk even if a change cursor exists
	Concurrency int    // max concurrent child browse requests (default MaxSyncConcurrency = 4)
	Caller      Caller // optional override for Service's configured Caller
}

// SyncResult reports the outcome of a SyncKind run.
type SyncResult struct {
	Upserted   int
	Deleted    int
	Skipped    int
	NextCursor string
	RetryAfter time.Duration
}

type syncWorker struct {
	cancel context.CancelFunc
	done   chan struct{}
}

type syncProgress struct {
	running     bool
	phase       string
	syncedItems int
	pages       int
	itemsTotal  int
}

func (s *Service) setSyncProgress(pluginID, kindKey string, p syncProgress) {
	if s == nil {
		return
	}
	key := workerKey(pluginID, kindKey)
	s.workersMu.Lock()
	if s.syncProgress == nil {
		s.syncProgress = map[string]syncProgress{}
	}
	s.syncProgress[key] = p
	s.workersMu.Unlock()
}

func (s *Service) clearSyncProgress(pluginID, kindKey string) {
	if s == nil {
		return
	}
	key := workerKey(pluginID, kindKey)
	s.workersMu.Lock()
	delete(s.syncProgress, key)
	s.workersMu.Unlock()
}

func (s *Service) clearSyncProgressIfPhase(pluginID, kindKey, phase string) {
	if s == nil {
		return
	}
	key := workerKey(pluginID, kindKey)
	s.workersMu.Lock()
	if cur, ok := s.syncProgress[key]; ok && cur.phase == phase {
		delete(s.syncProgress, key)
	}
	s.workersMu.Unlock()
}

func (s *Service) getSyncProgress(pluginID, kindKey string) (syncProgress, bool) {
	if s == nil {
		return syncProgress{}, false
	}
	key := workerKey(pluginID, kindKey)
	s.workersMu.Lock()
	p, ok := s.syncProgress[key]
	s.workersMu.Unlock()
	return p, ok
}

// SyncKind executes a pull sync for a registered plugin kind (spec §3.5, §4.4).
//
// Sync algorithm:
//  1. Initial sync (when cursor is empty and the store has 0 items for the kind):
//     walks /ether/v1/kinds/{kind}/browse from the root level (parentId="") with
//     limit=200, then breadth-first into child levels with bounded concurrency (4).
//  2. Delta sync (when cursor != "" and storedCount > 0 and !opts.FullWalk):
//     calls POST /ether/v1/kinds/{kind}/changes with {"cursor": cursor, "limit": 500}.
//     If the plugin returns 404/501 (changes not implemented), falls back to re-walk diff.
//     Otherwise chains while complete == false, and records nextCursor in plugin_sync_state.
//  3. Fallback re-walk (when changes is not supported or FullWalk is requested
//     on an already-populated kind):
//     re-walks /browse, diffs by etag (skipping unchanged items), resets missed_walks=0
//     on seen items, increments missed_walks on unseen items, and deletes items unseen
//     in 2 consecutive complete walks.
//  4. Guardrail: if >50% of existing items in a library would be deleted in a single
//     pass (and the library has at least 2 items) and !opts.Force, the delete is
//     aborted, plugin_sync_state records the error, OnAttention/Health is notified,
//     and ErrDeleteGuard is returned.
func (s *Service) SyncKind(ctx context.Context, pluginID, kindKey string, opts SyncOptions) (SyncResult, error) {
	if s == nil || s.db == nil {
		return SyncResult{}, fmt.Errorf("pluginitems: nil service")
	}
	if !s.leader() {
		return SyncResult{}, nil
	}
	caller := opts.Caller
	if caller == nil {
		s.workersMu.Lock()
		caller = s.caller
		s.workersMu.Unlock()
	}
	if caller == nil {
		return SyncResult{}, fmt.Errorf("pluginitems: nil plugin route caller")
	}

	rec, err := s.GetKind(ctx, pluginID, kindKey)
	if err != nil {
		return SyncResult{}, err
	}
	if rec.Decl.Sync.Mode == plugins.KindSyncPush || rec.Decl.Sync.Mode == "" {
		return SyncResult{}, nil
	}

	state, err := s.SyncStateOf(ctx, pluginID, kindKey)
	if err != nil {
		return SyncResult{}, err
	}
	storedCount, err := s.countKindItems(ctx, pluginID, kindKey)
	if err != nil {
		return SyncResult{}, err
	}

	startedAt := s.now()
	nowStart := startedAt.Unix()
	_, _ = s.db.ExecContext(ctx,
		`UPDATE plugin_sync_state SET last_started = ? WHERE plugin_id = ? AND kind_key = ?`,
		nowStart, pluginID, kindKey)
	s.setSyncProgress(pluginID, kindKey, syncProgress{
		running:    true,
		phase:      "syncing",
		itemsTotal: storedCount,
	})
	defer s.clearSyncProgressIfPhase(pluginID, kindKey, "syncing")
	libName := rec.Decl.Label
	if libName == "" {
		libName = kindKey
	}
	trigger := "scheduled"
	if opts.FullWalk || opts.Force {
		trigger = "manual"
	}
	s.emitSyncEvent(SyncEvent{
		Phase:       "started",
		PluginID:    pluginID,
		KindKey:     kindKey,
		LibraryID:   int64(rec.LibraryID),
		LibraryName: libName,
		Trigger:     trigger,
	})

	var (
		res     SyncResult
		syncErr error
	)
	switch {
	case !opts.FullWalk && state.Cursor != "" && storedCount > 0:
		var notSupported bool
		res, notSupported, syncErr = s.syncViaChanges(ctx, caller, rec, state.Cursor, storedCount, opts)
		if notSupported {
			res, syncErr = s.syncRewalkDiff(ctx, caller, rec, storedCount, opts)
		}
	case storedCount == 0 || (state.Cursor == "" && state.LastOK.IsZero()):
		res, syncErr = s.syncInitialWalk(ctx, caller, rec, opts)
	default:
		res, syncErr = s.syncRewalkDiff(ctx, caller, rec, storedCount, opts)
	}

	nowEnd := s.now().Unix()
	durMS := max(s.now().Sub(startedAt).Milliseconds(), int64(0))
	if syncErr != nil {
		_, _ = s.db.ExecContext(ctx,
			`UPDATE plugin_sync_state SET last_error = ? WHERE plugin_id = ? AND kind_key = ?`,
			syncErr.Error(), pluginID, kindKey)
		if !errors.Is(syncErr, plugins.ErrBusy) {
			s.noteAttention(ctx, pluginID, syncErr.Error())
		}
		s.emitSyncEvent(SyncEvent{
			Phase:       "failed",
			PluginID:    pluginID,
			KindKey:     kindKey,
			LibraryID:   int64(rec.LibraryID),
			LibraryName: libName,
			Trigger:     trigger,
			DurationMS:  durMS,
			Error:       syncErr.Error(),
		})
		return res, syncErr
	}

	nextCursor := res.NextCursor
	if nextCursor == "" {
		nextCursor = state.Cursor
	}
	totalAfter, _ := s.countKindItems(ctx, pluginID, kindKey)
	gen := int64(s.syncGen.Add(1))
	_, _ = s.db.ExecContext(ctx, `
		UPDATE plugin_sync_state
		SET cursor = ?, generation = ?, last_ok = ?, last_error = '', items = ?
		WHERE plugin_id = ? AND kind_key = ?`,
		nextCursor, gen, nowEnd, int64(totalAfter), pluginID, kindKey,
	)
	firstSync := state.LastOK.IsZero() || storedCount == 0
	s.noteLibraryItems(rec.LibraryID, int64(totalAfter), firstSync)
	s.emitSyncEvent(SyncEvent{
		Phase:       "completed",
		PluginID:    pluginID,
		KindKey:     kindKey,
		LibraryID:   int64(rec.LibraryID),
		LibraryName: libName,
		Trigger:     trigger,
		Added:       res.Upserted,
		Removed:     res.Deleted,
		DurationMS:  durMS,
	})
	return res, nil
}

func (s *Service) countKindItems(ctx context.Context, pluginID, kindKey string) (int, error) {
	var total int
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(t.n), 0)
		FROM plugin_item_tally t
		JOIN plugin_kinds pk ON pk.library_id = t.library_id
		WHERE pk.plugin_id = ? AND pk.kind_key = ?`,
		pluginID, kindKey,
	).Scan(&total)
	return total, err
}

func exceedsDeleteGuard(storedCount, deleteCount int, force bool) bool {
	if force || storedCount < 2 || deleteCount <= 0 {
		return false
	}
	return float64(deleteCount)/float64(storedCount) > DeleteGuardRatio
}

// syncInitialWalk performs the initial breadth-first walk over /browse,
// persisting each fetched page incrementally so items appear in frontends
// immediately and partial progress survives errors or restarts, then seeds
// cursor if the kind implements /changes.
func (s *Service) syncInitialWalk(ctx context.Context, caller Caller, rec KindRecord, opts SyncOptions) (SyncResult, error) {
	var (
		res        SyncResult
		progressMu sync.Mutex
		pages      int
		firstNoted bool
	)
	onPage := func(batch []core.KindItem) error {
		if len(batch) == 0 {
			return nil
		}
		var pageUpserted int
		for start := 0; start < len(batch); start += MaxUpsertBatch {
			end := min(start+MaxUpsertBatch, len(batch))
			n, err := s.upsertItemsForSync(ctx, rec.PluginID, rec.KindKey, batch[start:end])
			if err != nil {
				return err
			}
			pageUpserted += n
		}
		totalNow, _ := s.countKindItems(ctx, rec.PluginID, rec.KindKey)
		progressMu.Lock()
		res.Upserted += pageUpserted
		pages++
		upsertedSoFar := res.Upserted
		pagesSoFar := pages
		notifyFirst := !firstNoted && totalNow > 0
		if notifyFirst {
			firstNoted = true
		}
		progressMu.Unlock()

		s.setSyncProgress(rec.PluginID, rec.KindKey, syncProgress{
			running:     true,
			phase:       "syncing",
			syncedItems: upsertedSoFar,
			pages:       pagesSoFar,
			itemsTotal:  totalNow,
		})
		if notifyFirst {
			s.noteLibraryItems(rec.LibraryID, int64(totalNow), true)
		}
		return nil
	}

	_, retryAfter, err := s.walkBrowseTreeStream(ctx, caller, rec, opts.Concurrency, false, onPage)
	res.RetryAfter = retryAfter
	if err != nil {
		return res, err
	}
	// Probe /changes with empty cursor to capture baseline NextCursor if supported.
	page, notSupp, _, err := s.callKindChanges(ctx, caller, rec.PluginID, rec.KindKey, "", 1)
	if err == nil && !notSupp && page.NextCursor != "" {
		res.NextCursor = page.NextCursor
	}
	return res, nil
}

// syncViaChanges chains /changes pages from sinceCursor until Complete is true.
func (s *Service) syncViaChanges(ctx context.Context, caller Caller, rec KindRecord, sinceCursor string, storedCount int, opts SyncOptions) (SyncResult, bool, error) {
	var (
		res        SyncResult
		cursor     = sinceCursor
		allUpserts []core.KindItem
		allDeletes []string
	)
	for pageIdx := 0; pageIdx < 1000; pageIdx++ {
		page, notSupp, retryAfter, err := s.callKindChanges(ctx, caller, rec.PluginID, rec.KindKey, cursor, DefaultChangesLimit)
		for attempt := 0; err != nil && waitRetryAfter(ctx, retryAfter, attempt); attempt++ {
			page, notSupp, retryAfter, err = s.callKindChanges(ctx, caller, rec.PluginID, rec.KindKey, cursor, DefaultChangesLimit)
		}
		if retryAfter > res.RetryAfter {
			res.RetryAfter = retryAfter
		}
		if notSupp {
			return res, true, nil
		}
		if err != nil {
			return res, false, err
		}
		allUpserts = append(allUpserts, page.Upserts...)
		allDeletes = append(allDeletes, page.Deletes...)
		if page.NextCursor != "" {
			cursor = page.NextCursor
			res.NextCursor = page.NextCursor
		}
		if page.Complete || page.NextCursor == "" {
			break
		}
	}

	if exceedsDeleteGuard(storedCount, len(allDeletes), opts.Force) {
		return res, false, fmt.Errorf("%w: attempting to delete %d of %d items in %s/%s",
			ErrDeleteGuard, len(allDeletes), storedCount, rec.PluginID, rec.KindKey)
	}

	for start := 0; start < len(allUpserts); start += MaxUpsertBatch {
		end := min(start+MaxUpsertBatch, len(allUpserts))
		n, err := s.upsertItemsForSync(ctx, rec.PluginID, rec.KindKey, allUpserts[start:end])
		if err != nil {
			return res, false, err
		}
		res.Upserted += n
	}

	for start := 0; start < len(allDeletes); start += MaxUpsertBatch {
		end := min(start+MaxUpsertBatch, len(allDeletes))
		if err := s.DeleteItems(ctx, rec.PluginID, rec.KindKey, allDeletes[start:end]); err != nil {
			return res, false, err
		}
		res.Deleted += end - start
	}

	return res, false, nil
}

// syncRewalkDiff re-walks /browse, diffs items by etag, and deletes items unseen
// in 2 consecutive complete walks (subject to the 50%-delete guard).
func (s *Service) syncRewalkDiff(ctx context.Context, caller Caller, rec KindRecord, storedCount int, opts SyncOptions) (SyncResult, error) {
	var res SyncResult
	walked, retryAfter, err := s.walkBrowseTree(ctx, caller, rec, opts.Concurrency)
	res.RetryAfter = retryAfter
	if err != nil {
		return res, err
	}

	existingEtags, existingMissed, err := s.loadExistingEtagsAndMissed(ctx, rec.PluginID, rec.KindKey)
	if err != nil {
		return res, err
	}

	seen := make(map[string]bool, len(walked))
	var toUpsert []core.KindItem
	var seenUnchanged []string

	for _, item := range walked {
		extID := strings.TrimSpace(item.ID)
		if extID == "" {
			continue
		}
		seen[extID] = true
		oldEtag, exists := existingEtags[extID]
		if exists && item.ETag != "" && oldEtag == item.ETag {
			res.Skipped++
			if existingMissed[extID] > 0 {
				seenUnchanged = append(seenUnchanged, extID)
			}
			continue
		}
		toUpsert = append(toUpsert, item)
	}

	// Determine unseen items and which ones hit 2 consecutive missed walks.
	var (
		toIncrement []string
		toDelete    []string
	)
	for extID, prevMissed := range existingMissed {
		if seen[extID] {
			continue
		}
		if prevMissed+1 >= 2 {
			toDelete = append(toDelete, extID)
		} else {
			toIncrement = append(toIncrement, extID)
		}
	}

	unseenCount := len(toIncrement) + len(toDelete)
	if exceedsDeleteGuard(storedCount, len(toDelete), opts.Force) || exceedsDeleteGuard(storedCount, unseenCount, opts.Force) {
		return res, fmt.Errorf("%w: %d of %d items missing in walk for %s/%s",
			ErrDeleteGuard, unseenCount, storedCount, rec.PluginID, rec.KindKey)
	}

	for start := 0; start < len(toUpsert); start += MaxUpsertBatch {
		end := min(start+MaxUpsertBatch, len(toUpsert))
		n, err := s.upsertItemsForSync(ctx, rec.PluginID, rec.KindKey, toUpsert[start:end])
		if err != nil {
			return res, err
		}
		res.Upserted += n
	}

	if len(seenUnchanged) > 0 {
		if err := s.setMissedWalks(ctx, rec.PluginID, rec.KindKey, seenUnchanged, 0); err != nil {
			return res, err
		}
	}
	if len(toIncrement) > 0 {
		if err := s.incrementMissedWalks(ctx, rec.PluginID, rec.KindKey, toIncrement); err != nil {
			return res, err
		}
	}
	for start := 0; start < len(toDelete); start += MaxUpsertBatch {
		end := min(start+MaxUpsertBatch, len(toDelete))
		if err := s.DeleteItems(ctx, rec.PluginID, rec.KindKey, toDelete[start:end]); err != nil {
			return res, err
		}
		res.Deleted += end - start
	}

	return res, nil
}

func (s *Service) loadExistingEtagsAndMissed(ctx context.Context, pluginID, kindKey string) (map[string]string, map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT external_id, etag, missed_walks
		FROM plugin_items
		WHERE plugin_id = ? AND kind_key = ?`,
		pluginID, kindKey,
	)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	etags := make(map[string]string)
	missed := make(map[string]int)
	for rows.Next() {
		var extID, etag string
		var m int
		if err := rows.Scan(&extID, &etag, &m); err != nil {
			return nil, nil, err
		}
		etags[extID] = etag
		missed[extID] = m
	}
	return etags, missed, rows.Err()
}

func (s *Service) setMissedWalks(ctx context.Context, pluginID, kindKey string, externalIDs []string, val int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	for _, extID := range externalIDs {
		if _, err := tx.ExecContext(ctx, `
			UPDATE plugin_items SET missed_walks = ?, seen_at = ?
			WHERE plugin_id = ? AND kind_key = ? AND external_id = ?`,
			val, now, pluginID, kindKey, extID,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Service) incrementMissedWalks(ctx context.Context, pluginID, kindKey string, externalIDs []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, extID := range externalIDs {
		if _, err := tx.ExecContext(ctx, `
			UPDATE plugin_items SET missed_walks = missed_walks + 1
			WHERE plugin_id = ? AND kind_key = ? AND external_id = ?`,
			pluginID, kindKey, extID,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// walkBrowseTree walks /ether/v1/kinds/{kind}/browse starting at the root level
// (parentId="") with limit=200, then breadth-first into child levels with bounded
// concurrency (default MaxSyncConcurrency = 4).
func (s *Service) walkBrowseTree(ctx context.Context, caller Caller, rec KindRecord, concurrency int) ([]core.KindItem, time.Duration, error) {
	return s.walkBrowseTreeStream(ctx, caller, rec, concurrency, true, nil)
}

func (s *Service) walkBrowseTreeStream(ctx context.Context, caller Caller, rec KindRecord, concurrency int, collectAll bool, onPage func([]core.KindItem) error) ([]core.KindItem, time.Duration, error) {
	if concurrency <= 0 {
		concurrency = MaxSyncConcurrency
	}
	childLevelOf := make(map[string]string, len(rec.Decl.Levels))
	rootLevel := "item"
	if len(rec.Decl.Levels) > 0 {
		rootLevel = rec.Decl.Levels[0].Key
		for i := 0; i+1 < len(rec.Decl.Levels); i++ {
			childLevelOf[rec.Decl.Levels[i].Key] = rec.Decl.Levels[i+1].Key
		}
	}

	_, rootHasChildren := childLevelOf[rootLevel]
	keepRoot := collectAll || rootHasChildren
	rootItems, maxRetry, err := s.fetchAllBrowsePagesStream(ctx, caller, rec.PluginID, rec.KindKey, rootLevel, "", keepRoot, onPage)
	if err != nil {
		return nil, maxRetry, err
	}

	var allItems []core.KindItem
	if collectAll {
		allItems = make([]core.KindItem, 0, len(rootItems))
		allItems = append(allItems, rootItems...)
	}

	currentWave := rootItems
	currentLevel := rootLevel

	for {
		nextLevel, hasChildren := childLevelOf[currentLevel]
		if !hasChildren || len(currentWave) == 0 {
			break
		}
		_, nextHasChildren := childLevelOf[nextLevel]
		keepNext := collectAll || nextHasChildren

		type parentTask struct {
			parentExtID string
		}
		tasks := make([]parentTask, 0, len(currentWave))
		for _, p := range currentWave {
			if p.ID != "" {
				tasks = append(tasks, parentTask{parentExtID: p.ID})
			}
		}
		if len(tasks) == 0 {
			break
		}

		type taskResult struct {
			idx        int
			items      []core.KindItem
			retryAfter time.Duration
			err        error
		}
		results := make([]taskResult, len(tasks))
		sem := make(chan struct{}, concurrency)
		var wg sync.WaitGroup

		for i, t := range tasks {
			i, t := i, t
			wg.Add(1)
			go func() {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					results[i] = taskResult{idx: i, err: ctx.Err()}
					return
				}
				defer func() { <-sem }()

				children, ra, err := s.fetchAllBrowsePagesStream(ctx, caller, rec.PluginID, rec.KindKey, nextLevel, t.parentExtID, keepNext, onPage)
				results[i] = taskResult{
					idx:        i,
					items:      children,
					retryAfter: ra,
					err:        err,
				}
			}()
		}
		wg.Wait()

		var nextWave []core.KindItem
		for _, r := range results {
			if r.retryAfter > maxRetry {
				maxRetry = r.retryAfter
			}
			if r.err != nil {
				return nil, maxRetry, r.err
			}
			for j := range r.items {
				if r.items[j].ParentID == "" {
					r.items[j].ParentID = tasks[r.idx].parentExtID
				}
				if r.items[j].Level == "" {
					r.items[j].Level = nextLevel
				}
			}
			if collectAll {
				allItems = append(allItems, r.items...)
			}
			if nextHasChildren {
				nextWave = append(nextWave, r.items...)
			}
		}

		currentWave = nextWave
		currentLevel = nextLevel
	}

	return allItems, maxRetry, nil
}

func (s *Service) fetchAllBrowsePages(ctx context.Context, caller Caller, pluginID, kindKey, level, parentID string) ([]core.KindItem, time.Duration, error) {
	return s.fetchAllBrowsePagesStream(ctx, caller, pluginID, kindKey, level, parentID, true, nil)
}

func (s *Service) fetchAllBrowsePagesStream(ctx context.Context, caller Caller, pluginID, kindKey, level, parentID string, collect bool, onPage func([]core.KindItem) error) ([]core.KindItem, time.Duration, error) {
	var (
		all        []core.KindItem
		cursor     string
		maxRetry   time.Duration
		seenCursor = make(map[string]bool)
	)
	for pageIdx := 0; pageIdx < 10000; pageIdx++ {
		req := core.KindBrowseRequest{
			ParentID: parentID,
			Level:    level,
			Cursor:   cursor,
			Limit:    DefaultWalkPageLimit,
		}
		page, ra, err := s.callKindBrowse(ctx, caller, pluginID, kindKey, req)
		for attempt := 0; err != nil && waitRetryAfter(ctx, ra, attempt); attempt++ {
			page, ra, err = s.callKindBrowse(ctx, caller, pluginID, kindKey, req)
		}
		if ra > maxRetry {
			maxRetry = ra
		}
		if err != nil {
			return nil, maxRetry, err
		}
		for i := range page.Items {
			if page.Items[i].Level == "" {
				page.Items[i].Level = level
			}
			if page.Items[i].ParentID == "" && parentID != "" {
				page.Items[i].ParentID = parentID
			}
		}
		if onPage != nil && len(page.Items) > 0 {
			if err := onPage(page.Items); err != nil {
				return nil, maxRetry, err
			}
		}
		if collect {
			all = append(all, page.Items...)
		}
		next := strings.TrimSpace(page.NextCursor)
		if next == "" || len(page.Items) == 0 || seenCursor[next] {
			break
		}
		seenCursor[next] = true
		cursor = next
	}
	return all, maxRetry, nil
}

func (s *Service) callKindBrowse(ctx context.Context, caller Caller, pluginID, kindKey string, req core.KindBrowseRequest) (core.KindPage, time.Duration, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return core.KindPage{}, 0, err
	}
	resp, err := caller.Call(ctx, pluginID, plugins.Request{
		PluginID: pluginID,
		Method:   http.MethodPost,
		Path:     fmt.Sprintf("/ether/v1/kinds/%s/browse", kindKey),
		Headers:  map[string]string{"content-type": "application/json"},
		Body:     body,
	})
	if err != nil {
		return core.KindPage{}, 0, err
	}
	ra := parseRetryAfter(resp)
	if err := checkPluginResponseError(resp); err != nil {
		return core.KindPage{}, ra, err
	}
	if len(resp.Body) > MaxBrowseBodyBytes {
		return core.KindPage{}, ra, fmt.Errorf("pluginitems: browse response exceeds %d bytes", MaxBrowseBodyBytes)
	}
	var page core.KindPage
	if len(resp.Body) > 0 {
		if err := json.Unmarshal(resp.Body, &page); err != nil {
			return core.KindPage{}, ra, fmt.Errorf("pluginitems: decode browse response: %w", err)
		}
	}
	return page, ra, nil
}

func (s *Service) callKindChanges(ctx context.Context, caller Caller, pluginID, kindKey, cursor string, limit int) (core.KindChangePage, bool, time.Duration, error) {
	reqPayload := struct {
		Cursor     string `json:"cursor,omitempty"`
		SinceToken string `json:"since_token,omitempty"`
		Limit      int    `json:"limit"`
	}{
		Cursor:     cursor,
		SinceToken: cursor,
		Limit:      limit,
	}
	body, err := json.Marshal(reqPayload)
	if err != nil {
		return core.KindChangePage{}, false, 0, err
	}
	resp, err := caller.Call(ctx, pluginID, plugins.Request{
		PluginID: pluginID,
		Method:   http.MethodPost,
		Path:     fmt.Sprintf("/ether/v1/kinds/%s/changes", kindKey),
		Headers:  map[string]string{"content-type": "application/json"},
		Body:     body,
	})
	if err != nil {
		return core.KindChangePage{}, false, 0, err
	}
	if resp.Status == http.StatusNotFound || resp.Status == http.StatusNotImplemented {
		return core.KindChangePage{}, true, 0, nil
	}
	ra := parseRetryAfter(resp)
	if err := checkPluginResponseError(resp); err != nil {
		return core.KindChangePage{}, false, ra, err
	}
	if len(resp.Body) > MaxBrowseBodyBytes {
		return core.KindChangePage{}, false, ra, fmt.Errorf("pluginitems: changes response exceeds %d bytes", MaxBrowseBodyBytes)
	}
	var page core.KindChangePage
	if len(resp.Body) > 0 {
		if err := json.Unmarshal(resp.Body, &page); err != nil {
			return core.KindChangePage{}, false, ra, fmt.Errorf("pluginitems: decode changes response: %w", err)
		}
	}
	return page, false, ra, nil
}

type pluginErrorEnvelope struct {
	Error             string `json:"error"`
	Message           string `json:"message"`
	RetryAfterSeconds int    `json:"retryAfterSeconds"`
	RetryAfterSnake   int    `json:"retry_after_seconds"`
}

// maxSyncRetryWait and maxSyncRetries bound how long one sync waits out a
// plugin's rate limit (a 429 or 503 with Retry-After) before giving up.
const (
	maxSyncRetryWait = 2 * time.Minute
	maxSyncRetries   = 8
)

// waitRetryAfter waits out a short Retry-After so a rate-limited page is
// retried in place instead of failing the whole sync. It reports whether
// the caller should try the same request again.
func waitRetryAfter(ctx context.Context, ra time.Duration, attempt int) bool {
	if ra <= 0 || ra > maxSyncRetryWait || attempt >= maxSyncRetries {
		return false
	}
	t := time.NewTimer(ra)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func checkPluginResponseError(resp plugins.Response) error {
	if resp.Status >= 200 && resp.Status < 300 {
		return nil
	}
	var env pluginErrorEnvelope
	_ = json.Unmarshal(resp.Body, &env)
	if env.Error != "" || env.Message != "" {
		return fmt.Errorf("pluginitems: plugin route status %d (%s): %s", resp.Status, env.Error, env.Message)
	}
	return fmt.Errorf("pluginitems: plugin route status %d", resp.Status)
}

func parseRetryAfter(resp plugins.Response) time.Duration {
	for k, v := range resp.Headers {
		if strings.EqualFold(k, "Retry-After") {
			if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs > 0 {
				return time.Duration(secs) * time.Second
			}
		}
	}
	var env pluginErrorEnvelope
	if err := json.Unmarshal(resp.Body, &env); err == nil {
		if env.RetryAfterSeconds > 0 {
			return time.Duration(env.RetryAfterSeconds) * time.Second
		}
		if env.RetryAfterSnake > 0 {
			return time.Duration(env.RetryAfterSnake) * time.Second
		}
	}
	return 0
}

// StartKindSync starts (or restarts) a background pull-sync worker for a kind
// without blocking the caller on an in-flight previous pass. Any superseded
// worker is cancelled immediately and drained by the new worker goroutine
// before starting the next pass, preserving strict per-kind serialization.
func (s *Service) StartKindSync(parentCtx context.Context, pluginID, kindKey string, interval time.Duration) {
	if s == nil {
		return
	}
	if interval <= 0 {
		interval = 15 * time.Minute
	}

	ctx, cancel := context.WithCancel(parentCtx)
	done := make(chan struct{})
	key := workerKey(pluginID, kindKey)

	s.workersMu.Lock()
	var prevDone chan struct{}
	if prev, ok := s.workers[key]; ok && prev != nil {
		prev.cancel()
		prevDone = prev.done
	} else if d, ok := s.draining[key]; ok {
		prevDone = d
	}
	var reindexDone chan struct{}
	if rw, ok := s.reindexers[key]; ok && rw != nil {
		reindexDone = rw.done
	}
	delete(s.draining, key)
	s.workers[key] = &syncWorker{cancel: cancel, done: done}
	s.workersMu.Unlock()

	go func() {
		defer close(done)
		if prevDone != nil {
			// prev was already cancelled; wait for it to finish so Close/StopKindSync
			// transitively drain every superseded worker before returning.
			<-prevDone
		}
		if ctx.Err() != nil {
			return
		}
		if reindexDone != nil {
			select {
			case <-reindexDone:
			case <-ctx.Done():
				return
			}
		}
		res, _ := s.SyncKind(ctx, pluginID, kindKey, SyncOptions{})
		wait := interval
		if res.RetryAfter > wait {
			wait = res.RetryAfter
		}
		timer := time.NewTimer(wait)
		defer timer.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				res, _ := s.SyncKind(ctx, pluginID, kindKey, SyncOptions{})
				nextWait := interval
				if res.RetryAfter > nextWait {
					nextWait = res.RetryAfter
				}
				timer.Reset(nextWait)
			}
		}
	}()
}

// CancelKindSync cancels any running background sync worker for (pluginID,
// kindKey) without blocking the caller on an in-flight network or plugin call.
// Subsequent StartKindSync, StopKindSync, PurgeKind, or Close calls still wait
// for the cancelled worker to exit.
func (s *Service) CancelKindSync(pluginID, kindKey string) {
	if s == nil {
		return
	}
	key := workerKey(pluginID, kindKey)
	s.workersMu.Lock()
	if w, ok := s.workers[key]; ok {
		delete(s.workers, key)
		if w != nil {
			s.draining[key] = w.done
			w.cancel()
		}
	}
	s.workersMu.Unlock()
}

// StopKindSync stops the background sync and reindex workers for (pluginID,
// kindKey) and waits for any in-flight pass to exit.
func (s *Service) StopKindSync(pluginID, kindKey string) {
	if s == nil {
		return
	}
	key := workerKey(pluginID, kindKey)
	s.workersMu.Lock()
	w, ok := s.workers[key]
	if ok {
		delete(s.workers, key)
	}
	drain := s.draining[key]
	delete(s.draining, key)
	rw := s.reindexers[key]
	delete(s.reindexers, key)
	s.workersMu.Unlock()
	if ok && w != nil {
		w.cancel()
		<-w.done
	}
	if drain != nil {
		<-drain
	}
	if rw != nil {
		rw.cancel()
		<-rw.done
	}
}

// StopPluginSync stops all background sync and reindex workers belonging to pluginID.
func (s *Service) StopPluginSync(pluginID string) {
	if s == nil {
		return
	}
	prefix := pluginID + "\x00"
	s.workersMu.Lock()
	var toWait []*syncWorker
	var drains []chan struct{}
	for k, w := range s.workers {
		if strings.HasPrefix(k, prefix) {
			delete(s.workers, k)
			toWait = append(toWait, w)
		}
	}
	for k, d := range s.draining {
		if strings.HasPrefix(k, prefix) {
			delete(s.draining, k)
			drains = append(drains, d)
		}
	}
	for k, rw := range s.reindexers {
		if strings.HasPrefix(k, prefix) {
			delete(s.reindexers, k)
			toWait = append(toWait, rw)
		}
	}
	s.workersMu.Unlock()
	for _, w := range toWait {
		if w != nil {
			w.cancel()
			<-w.done
		}
	}
	for _, d := range drains {
		if d != nil {
			<-d
		}
	}
}

// Close stops all background sync workers, reindexers, and planner statistics refreshes.
func (s *Service) Close() {
	if s == nil {
		return
	}
	if s.bgCancel != nil {
		s.bgCancel()
	}
	s.workersMu.Lock()
	var toWait []*syncWorker
	var drains []chan struct{}
	for k, w := range s.workers {
		delete(s.workers, k)
		toWait = append(toWait, w)
	}
	for k, d := range s.draining {
		delete(s.draining, k)
		drains = append(drains, d)
	}
	for k, rw := range s.reindexers {
		delete(s.reindexers, k)
		toWait = append(toWait, rw)
	}
	s.workersMu.Unlock()
	for _, w := range toWait {
		if w != nil {
			w.cancel()
			<-w.done
		}
	}
	for _, d := range drains {
		if d != nil {
			<-d
		}
	}
	s.stats.close()
}

func workerKey(pluginID, kindKey string) string {
	return pluginID + "\x00" + kindKey
}
