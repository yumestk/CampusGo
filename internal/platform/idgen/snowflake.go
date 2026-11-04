package idgen

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Epoch matches the original Java project. Worker IDs must be unique among issuers.
const Epoch int64 = 1735689600000

type Generator struct {
	mu       sync.Mutex
	worker   int64
	last     int64
	sequence int64
	now      func() time.Time
}

func New(worker int64) (*Generator, error) {
	if worker < 0 || worker > 1023 {
		return nil, fmt.Errorf("worker ID must be in [0,1023]")
	}
	return &Generator{worker: worker, last: -1, now: time.Now}, nil
}

// Next rejects clock rollback; sequence exhaustion waits with cancellation.
// Manual worker ownership and host clock checks are deployment responsibilities.
func (g *Generator) Next(ctx context.Context) (int64, error) {
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		g.mu.Lock()
		ms := g.now().UnixMilli() - Epoch
		if ms < 0 || ms >= 1<<41 || ms < g.last {
			g.mu.Unlock()
			return 0, fmt.Errorf("clock outside ID range or moved backwards")
		}
		if ms > g.last {
			g.last = ms
			g.sequence = 0
		} else {
			g.sequence++
		}
		if g.sequence < 4096 {
			id := ms<<22 | g.worker<<12 | g.sequence
			g.mu.Unlock()
			if id == 0 {
				continue
			}
			return id, nil
		}
		// Keep the sequence saturated while the current millisecond is exhausted.
		g.sequence = 4095
		g.mu.Unlock()
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0, ctx.Err()
		case <-timer.C:
		}
	}
}
