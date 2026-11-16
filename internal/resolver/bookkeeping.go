package resolver

import (
	"context"
	"time"
)

type requestBookkeepingKey struct{}

// Client admission must not wait for optional evidence/history writes behind
// the catalog's SQLite writer. Keep a bounded queue and one lazy worker; idle
// resolvers have no worker. Memory state is updated before enqueuing persistence.
func (s *Service) persistBookkeeping(ctx context.Context, write func(context.Context)) {
	if foreground, _ := ctx.Value(requestBookkeepingKey{}).(bool); !foreground || s.db == nil {
		write(ctx)
		return
	}
	parent := context.WithValue(context.WithoutCancel(ctx), requestBookkeepingKey{}, false)
	job := func(_ context.Context) {
		wctx, cancel := context.WithTimeout(parent, 2*time.Second)
		stop := context.AfterFunc(s.bgCtx, cancel)
		defer stop()
		defer cancel()
		write(wctx)
	}
	s.bgMu.Lock()
	defer s.bgMu.Unlock()
	if s.bgClosed {
		return
	}
	const limit = 64
	if len(s.bookkeeping) >= limit {
		s.log.Debug("resolver bookkeeping queue full; dropping optional write")
		return
	}
	s.bookkeeping = append(s.bookkeeping, job)
	if s.bookkeepingRunning {
		return
	}
	s.bookkeepingRunning = true
	s.bgWG.Add(1)
	go func() {
		defer s.bgWG.Done()
		for {
			s.bgMu.Lock()
			if s.bgClosed || len(s.bookkeeping) == 0 {
				s.bookkeeping = nil
				s.bookkeepingRunning = false
				s.bgMu.Unlock()
				return
			}
			next := s.bookkeeping[0]
			s.bookkeeping[0] = nil
			s.bookkeeping = s.bookkeeping[1:]
			s.bgMu.Unlock()
			next(s.bgCtx)
		}
	}()
}
