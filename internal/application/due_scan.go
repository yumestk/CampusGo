package application

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/yumestk/CampusGo/internal/contracts"
)

const (
	dueMaxPages    = 8
	dueMaxFailures = 4096
	dueMaxBackoff  = 2 * time.Minute
)

type dueFailure struct {
	retryAt time.Time
	delay   time.Duration
}

type dueScan struct {
	mu       sync.Mutex
	cursor   contracts.DueCursor
	failures map[int64]dueFailure
}

func (s *dueScan) run(ctx context.Context, limit int, page func(context.Context, contracts.DueCursor, int) ([]contracts.DueTask, error), apply func(context.Context, contracts.DueTask) error, report func(int64, error)) error {
	if limit < 1 || limit > 100 {
		return fmt.Errorf("invalid scan limit")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for id, failure := range s.failures {
		if now.After(failure.retryAt.Add(dueMaxBackoff)) {
			delete(s.failures, id)
		}
	}
	var failures []error
	for range dueMaxPages {
		rows, err := page(ctx, s.cursor, limit)
		if err != nil {
			failures = append(failures, err)
			break
		}
		for _, task := range rows {
			if err = ctx.Err(); err != nil {
				return errors.Join(append(failures, err)...)
			}
			s.cursor = task.DueCursor
			if time.Now().Before(s.failures[task.ID].retryAt) {
				continue
			}
			itemCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			err = apply(itemCtx, task)
			cancel()
			if err == nil {
				delete(s.failures, task.ID)
				continue
			}
			s.coolDown(task.ID, time.Now())
			if report != nil {
				report(task.ID, err)
			}
			failures = append(failures, fmt.Errorf("due errand %d: %w", task.ID, err))
		}
		if len(rows) < limit {
			s.cursor = contracts.DueCursor{}
			break
		}
	}
	return errors.Join(failures...)
}

func (s *dueScan) coolDown(id int64, now time.Time) {
	if s.failures == nil {
		s.failures = make(map[int64]dueFailure)
	}
	failure, tracked := s.failures[id]
	if !tracked && len(s.failures) == dueMaxFailures {
		var oldest time.Time
		var evict int64
		for candidate, entry := range s.failures {
			if oldest.IsZero() || entry.retryAt.Before(oldest) {
				oldest, evict = entry.retryAt, candidate
			}
		}
		delete(s.failures, evict)
	}
	failure.delay = min(max(2*failure.delay, 2*time.Second), dueMaxBackoff)
	failure.retryAt = now.Add(failure.delay)
	s.failures[id] = failure
}
