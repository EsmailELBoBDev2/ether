package newznab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/braedonsaunders/ether/internal/core"
)

const (
	// DefaultSearchSettle is the default settle window after the first results
	// arrive when SearchOptions.FirstResults is true and SettleSet is false.
	DefaultSearchSettle = 250 * time.Millisecond

	// maxDetachedSearchFills bounds how many in-flight per-indexer cache-fill
	// searches may remain detached after early return or WaitTimeout across a
	// Manager at once.
	maxDetachedSearchFills = 64
)

// SearchOptions configures fan-out filtering, early return, and wait budgets
// for Manager.SearchWithOptions. The zero value matches Manager.Search: query
// every enabled indexer and wait for all of them.
type SearchOptions struct {
	// FirstResults returns early once the fastest indexer(s) produce at least
	// one result plus Settle (DefaultSearchSettle when SettleSet is false; 0
	// returns immediately), while remaining indexers keep running in the
	// background to populate Ether's search cache.
	FirstResults bool
	// Settle sets the settle window after the first results arrive (under
	// FirstResults) or after MinResults is met (when FirstResults is false and
	// SettleSet is true).
	Settle    time.Duration
	SettleSet bool
	// MinResults sets a minimum deduplicated result count for early return.
	// When combined with FirstResults, returns as soon as either MinResults is
	// reached or the settle window after the first results elapses.
	MinResults int
	// SkipIndexers excludes matching enabled indexers by id or name
	// (case-insensitive, trimmed).
	SkipIndexers []string
	// OnlyIndexers, when non-empty, restricts the search to matching enabled
	// indexers by id or name (case-insensitive, trimmed). SkipIndexers still
	// removes entries from the OnlyIndexers set.
	OnlyIndexers []string
	// WaitTimeout bounds how long the caller waits before returning whatever
	// arrived so far (marking unfinished indexers timed_out while they keep
	// filling Ether's search cache up to BackgroundTimeout). Zero waits for ctx.
	WaitTimeout time.Duration
	// BackgroundTimeout caps how long detached background indexers may continue
	// running after an early return or WaitTimeout. Zero uses defaultHTTPTimeout.
	BackgroundTimeout time.Duration
}

// SearchResult carries merged, deduplicated releases alongside per-indexer
// outcomes for every enabled indexer in priority order.
type SearchResult struct {
	Releases []core.Release
	Indexers []core.IndexerSearchStatus
}

type searchFlight struct {
	done    chan struct{}
	results []core.Release
	err     error
}

type searchSlot struct {
	client   *Client
	id       string
	name     string
	skipped  bool
	started  time.Time
	finished bool
	duration time.Duration
	results  []core.Release
	err      error
}

// EnabledIndexerStatuses returns one status entry per currently enabled indexer
// in priority order, all tagged with status and 0 ms.
func (m *Manager) EnabledIndexerStatuses(status string) []core.IndexerSearchStatus {
	if m == nil {
		return nil
	}
	clients := m.Clients()
	out := make([]core.IndexerSearchStatus, 0, len(clients))
	for _, cl := range clients {
		if !cl.Config().Enabled {
			continue
		}
		out = append(out, core.IndexerSearchStatus{
			IndexerID: cl.indexerID(),
			Indexer:   cl.Name(),
			Status:    status,
			MS:        0,
		})
	}
	return out
}

func normalizeIndexerFilter(list []string) map[string]bool {
	var set map[string]bool
	for _, raw := range list {
		k := strings.ToLower(strings.TrimSpace(raw))
		if k == "" {
			continue
		}
		if set == nil {
			set = map[string]bool{}
		}
		set[k] = true
	}
	return set
}

func matchesIndexerFilter(set map[string]bool, id, name string) bool {
	if len(set) == 0 {
		return false
	}
	if set[strings.ToLower(strings.TrimSpace(id))] {
		return true
	}
	if n := strings.ToLower(strings.TrimSpace(name)); n != "" && set[n] {
		return true
	}
	return false
}

// SearchWithOptions queries enabled indexers in parallel subject to opts,
// returning merged priority-ordered releases and per-indexer status for every
// enabled indexer.
func (m *Manager) SearchWithOptions(ctx context.Context, req core.SearchRequest, opts SearchOptions) (SearchResult, error) {
	allClients := m.Clients()
	skipSet := normalizeIndexerFilter(opts.SkipIndexers)
	onlySet := normalizeIndexerFilter(opts.OnlyIndexers)

	slots := make([]searchSlot, 0, len(allClients))
	skipIDs := map[string]bool{}
	active := 0
	for _, cl := range allClients {
		cfg := cl.Config()
		if !cfg.Enabled {
			continue
		}
		id := cl.indexerID()
		name := cl.Name()
		skipped := core.IndexerSkipped(ctx, id) ||
			(core.TorrentOnlySearch(ctx) && !cfg.IsTorrent()) ||
			matchesIndexerFilter(skipSet, id, name) ||
			(len(onlySet) > 0 && !matchesIndexerFilter(onlySet, id, name))
		if skipped {
			skipIDs[id] = true
		} else {
			active++
		}
		slots = append(slots, searchSlot{
			client:  cl,
			id:      id,
			name:    name,
			skipped: skipped,
		})
	}

	if active == 0 {
		statuses := make([]core.IndexerSearchStatus, len(slots))
		for i, s := range slots {
			statuses[i] = core.IndexerSearchStatus{
				IndexerID: s.id,
				Indexer:   s.name,
				Status:    core.IndexerStatusSkipped,
				MS:        0,
			}
		}
		return SearchResult{Indexers: statuses}, nil
	}

	if len(skipIDs) > 0 {
		ctx = core.WithIndexerSkip(ctx, skipIDs)
	}

	earlyReturn := opts.FirstResults || opts.MinResults > 0
	hasWaitTimeout := opts.WaitTimeout > 0
	detachOnCutoff := earlyReturn || hasWaitTimeout

	var settle time.Duration
	switch {
	case opts.SettleSet:
		settle = max(0, opts.Settle)
	case opts.FirstResults:
		settle = DefaultSearchSettle
	default:
		settle = 0
	}

	bgLimit := opts.BackgroundTimeout
	if bgLimit <= 0 {
		bgLimit = defaultHTTPTimeout
	}

	runCtx := ctx
	var bgCancel context.CancelFunc
	var bgID int
	if detachOnCutoff {
		m.bgMu.Lock()
		if m.bgClosed {
			m.bgMu.Unlock()
			return SearchResult{}, context.Canceled
		}
		runCtx, bgCancel = context.WithTimeout(context.WithoutCancel(ctx), bgLimit)
		if m.bgCancels == nil {
			m.bgCancels = map[int]context.CancelFunc{}
		}
		m.bgNextID++
		bgID = m.bgNextID
		m.bgCancels[bgID] = bgCancel
		m.bgWG.Add(1)
		m.bgMu.Unlock()
	}

	finishBG := func() {
		if !detachOnCutoff {
			return
		}
		m.bgMu.Lock()
		delete(m.bgCancels, bgID)
		m.bgMu.Unlock()
		bgCancel()
		m.bgWG.Done()
	}

	parallel := m.par
	if parallel <= 0 {
		parallel = max(1, active)
	}
	sem := make(chan struct{}, parallel)

	var stateMu sync.Mutex
	seenKeys := make(map[string]struct{})
	doneCount := 0
	notify := make(chan struct{}, 1)
	signal := func() {
		select {
		case notify <- struct{}{}:
		default:
		}
	}

	searchStart := time.Now()
	var wg sync.WaitGroup
	for i := range slots {
		if slots[i].skipped {
			continue
		}
		wg.Add(1)
		go func(idx int, cl *Client) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-runCtx.Done():
				stateMu.Lock()
				slots[idx].err = runCtx.Err()
				slots[idx].duration = time.Since(searchStart)
				slots[idx].finished = true
				doneCount++
				stateMu.Unlock()
				signal()
				return
			}
			started := time.Now()
			stateMu.Lock()
			slots[idx].started = started
			stateMu.Unlock()

			res, err := m.searchClientCoalesced(runCtx, cl, req)
			dur := time.Since(started)

			stateMu.Lock()
			slots[idx].results = res
			slots[idx].err = err
			slots[idx].duration = dur
			slots[idx].finished = true
			if err == nil {
				for _, r := range res {
					seenKeys[mergeDedupeKey(r)] = struct{}{}
				}
			}
			doneCount++
			stateMu.Unlock()
			signal()
		}(i, slots[i].client)
	}

	var waitTimer *time.Timer
	var waitCh <-chan time.Time
	if hasWaitTimeout {
		waitTimer = time.NewTimer(opts.WaitTimeout)
		defer waitTimer.Stop()
		waitCh = waitTimer.C
	}

	var settleTimer *time.Timer
	var settleCh <-chan time.Time
	defer func() {
		if settleTimer != nil {
			settleTimer.Stop()
		}
	}()
	settleStarted := false

	earlyCut := false
	timedOutWait := false
	parentDone := false

waitLoop:
	for {
		stateMu.Lock()
		allDone := doneCount == active
		uniqueCount := len(seenKeys)
		stateMu.Unlock()

		if allDone {
			break
		}

		if opts.MinResults > 0 && uniqueCount >= opts.MinResults {
			if !opts.FirstResults && opts.SettleSet && settle > 0 {
				if !settleStarted {
					settleStarted = true
					settleTimer = time.NewTimer(settle)
					settleCh = settleTimer.C
				}
			} else {
				earlyCut = true
				break
			}
		}

		if opts.FirstResults && uniqueCount > 0 {
			if settle <= 0 {
				earlyCut = true
				break
			}
			if !settleStarted {
				settleStarted = true
				settleTimer = time.NewTimer(settle)
				settleCh = settleTimer.C
			}
		}

		select {
		case <-notify:
		case <-settleCh:
			earlyCut = true
			break waitLoop
		case <-waitCh:
			timedOutWait = true
			break waitLoop
		case <-ctx.Done():
			parentDone = true
			break waitLoop
		}
	}

	if parentDone {
		if bgCancel != nil {
			bgCancel()
		}
		wg.Wait()
		finishBG()
		m.recordSlotsAfterWait(ctx, slots)
		merged, statuses, errs := snapshotSearchSlots(slots, searchStart)
		if len(merged) == 0 {
			return SearchResult{Indexers: statuses}, ctx.Err()
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return SearchResult{Indexers: statuses}, ctx.Err()
		}
		_ = errs
		return SearchResult{Releases: merged, Indexers: statuses}, nil
	}

	stateMu.Lock()
	merged, statuses, errs := snapshotSearchSlots(slots, searchStart)
	remaining := active - doneCount
	stateMu.Unlock()

	if remaining == 0 {
		wg.Wait()
		finishBG()
		m.recordSlotsAfterWait(ctx, slots)
		if len(merged) == 0 && len(errs) > 0 && !earlyCut && !timedOutWait {
			return SearchResult{Indexers: statuses}, aggregateSearchErrors(errs)
		}
		return SearchResult{Releases: merged, Indexers: statuses}, nil
	}

	// Early return or WaitTimeout with slow indexers still in flight: keep them
	// running in the background (within our detached cap) so their results
	// populate Ether's search cache for subsequent calls.
	reserved := 0
	if detachOnCutoff {
		m.bgMu.Lock()
		if !m.bgClosed && m.detachedCount+remaining <= maxDetachedSearchFills {
			m.detachedCount += remaining
			reserved = remaining
		}
		m.bgMu.Unlock()
		if reserved == 0 {
			bgCancel()
		}
		go func(rem int) {
			defer func() {
				if rem > 0 {
					m.bgMu.Lock()
					m.detachedCount -= rem
					m.bgMu.Unlock()
				}
				finishBG()
			}()
			wg.Wait()
			m.recordSlotsAfterWait(runCtx, slots)
		}(reserved)
	} else {
		wg.Wait()
		m.recordSlotsAfterWait(ctx, slots)
	}

	return SearchResult{Releases: merged, Indexers: statuses}, nil
}

func (m *Manager) searchClientCoalesced(ctx context.Context, cl *Client, req core.SearchRequest) ([]core.Release, error) {
	if cl.ttl() <= 0 {
		return cl.Search(ctx, req)
	}
	speculative := core.SpeculativeIndexerWork(ctx) || core.IsBackgroundCheck(ctx)
	key := cl.indexerID() + "|" + cacheKey(cl, req)
	if speculative {
		key += "|bg"
	} else {
		key += "|fg"
	}
	for {
		m.flightMu.Lock()
		if f, ok := m.flights[key]; ok {
			m.flightMu.Unlock()
			select {
			case <-f.done:
				if errors.Is(f.err, context.Canceled) && ctx.Err() == nil {
					continue
				}
				return append([]core.Release(nil), f.results...), f.err
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		f := &searchFlight{done: make(chan struct{})}
		if m.flights == nil {
			m.flights = map[string]*searchFlight{}
		}
		m.flights[key] = f
		m.flightMu.Unlock()

		res, err := func() (res []core.Release, err error) {
			defer func() {
				f.results = append([]core.Release(nil), res...)
				f.err = err
				m.flightMu.Lock()
				if m.flights[key] == f {
					delete(m.flights, key)
				}
				m.flightMu.Unlock()
				close(f.done)
			}()
			return cl.Search(ctx, req)
		}()
		return res, err
	}
}

func snapshotSearchSlots(slots []searchSlot, searchStart time.Time) ([]core.Release, []core.IndexerSearchStatus, []error) {
	now := time.Now()
	seen := map[string]int{}
	var merged []core.Release
	var errs []error
	statuses := make([]core.IndexerSearchStatus, len(slots))

	for i, s := range slots {
		st := core.IndexerSearchStatus{
			IndexerID: s.id,
			Indexer:   s.name,
		}
		switch {
		case s.skipped:
			st.Status = core.IndexerStatusSkipped
			st.MS = 0
		case !s.finished:
			st.Status = core.IndexerStatusTimedOut
			ref := s.started
			if ref.IsZero() {
				ref = searchStart
			}
			st.MS = max(int64(0), now.Sub(ref).Milliseconds())
		case s.err != nil:
			errs = append(errs, s.err)
			st.MS = max(int64(0), s.duration.Milliseconds())
			if errors.Is(s.err, context.DeadlineExceeded) {
				st.Status = core.IndexerStatusTimedOut
			} else {
				st.Status = core.IndexerStatusError
				st.Error = classifyIndexerError(s.err)
			}
		default:
			st.Status = core.IndexerStatusAnswered
			st.Results = len(s.results)
			st.MS = max(int64(0), s.duration.Milliseconds())
			for _, r := range s.results {
				key := mergeDedupeKey(r)
				if idx, ok := seen[key]; ok {
					dup := r
					dup.Fallbacks = nil
					merged[idx].Fallbacks = append(merged[idx].Fallbacks, dup)
					continue
				}
				seen[key] = len(merged)
				merged = append(merged, r)
			}
		}
		statuses[i] = st
	}
	return merged, statuses, errs
}

func aggregateSearchErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	allBudget := true
	for _, e := range errs {
		if !errors.Is(e, core.ErrBudgetExhausted) {
			allBudget = false
			break
		}
	}
	if allBudget {
		return fmt.Errorf("all indexers: %w", core.ErrBudgetExhausted)
	}
	return errs[0]
}

func (m *Manager) recordSlotsAfterWait(ctx context.Context, slots []searchSlot) {
	timings := make([]SearchTiming, 0, len(slots))
	purpose := usagePurpose(ctx)
	day := time.Now().UTC().Format("2006-01-02")
	for _, s := range slots {
		if missingAnswer(s.err) {
			core.NoteSearchIncomplete(ctx)
		}
		if s.skipped {
			continue
		}
		e := ""
		if s.err != nil {
			e = s.err.Error()
		}
		timings = append(timings, SearchTiming{Indexer: s.name, Duration: s.duration, Err: e})
		shareKey := s.name + "|" + purpose
		if s.err != nil {
			if errors.Is(s.err, errSpeculativeShare) {
				if m.shouldLogSpeculativeShare(shareKey, day) {
					m.log.Debug("indexer search failed", "indexer", s.name, "err", s.err)
				}
			} else {
				m.log.Debug("indexer search failed", "indexer", s.name, "err", s.err)
			}
		} else {
			m.clearSpeculativeShareLog(shareKey)
		}
	}
	m.searchTimingMu.Lock()
	m.searchTimings = timings
	m.searchTimingMu.Unlock()
}

// classifyIndexerError maps an indexer error to a short, credential-free code
// safe to surface to plugins.
func classifyIndexerError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, core.ErrBudgetExhausted):
		return "budget_exhausted"
	case errors.Is(err, ErrIndexerPaused):
		return "paused"
	case errors.Is(err, ErrMissingDefinition):
		return "missing_definition"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case strings.Contains(err.Error(), "does not support category searches"):
		return "category_unsupported"
	}
	var ae *APIError
	if errors.As(err, &ae) {
		switch {
		case ae.Budget:
			return "budget_exhausted"
		case ae.Code == http.StatusUnauthorized || ae.Code == http.StatusForbidden || ae.Code == 100 || ae.Code == 101 || ae.Code == 102:
			return "auth_failed"
		case ae.Code == http.StatusTooManyRequests:
			return "rate_limited"
		case ae.Code >= 500 && ae.Code < 600:
			return "upstream_error"
		case ae.Code > 0:
			return fmt.Sprintf("api_error_%d", ae.Code)
		default:
			return "api_error"
		}
	}
	return "search_failed"
}
