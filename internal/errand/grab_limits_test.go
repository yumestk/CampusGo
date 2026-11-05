package errand

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yumestk/CampusGo/internal/platform/fault"
	"github.com/yumestk/CampusGo/internal/platform/rediscache"
)

func TestGrabHotWindowAndBounds(t *testing.T) {
	l := NewGrabLimits(nil, "unit")
	for range 500 {
		if e := l.hotAllowed(1, 0); e != nil {
			t.Fatal(e)
		}
	}
	if e := l.hotAllowed(1, int64(time.Second)-1); e == nil {
		t.Fatal("hot task exceeded window")
	}
	if e := l.hotAllowed(2, 0); e != nil {
		t.Fatal("one hot task blocked another", e)
	}
	if e := l.hotAllowed(1, int64(time.Second)); e != nil {
		t.Fatal("window did not expire", e)
	}
	l = NewGrabLimits(nil, "bounds")
	for id := int64(1); id <= 4096; id++ {
		if e := l.hotAllowed(id, 0); e != nil {
			t.Fatal(e)
		}
	}
	if e := l.hotAllowed(4097, 0); e == nil {
		t.Fatal("unbounded map")
	}
	if e := l.hotAllowed(4097, int64(time.Second)); e != nil {
		t.Fatal("expired keys were not pruned", e)
	}
}

func TestIntegrationD8GrabLimits(t *testing.T) {
	if os.Getenv("CAMPUSGO_D8_BUSINESS") != "1" {
		t.Skip("run make verify-d8-business")
	}
	ctx := context.Background()
	r, e := rediscache.Open()
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { r.Close() })
	ns := "d8-limits-" + uuid.NewString()
	a, b := NewGrabLimits(r, ns), NewGrabLimits(r, ns)
	t.Cleanup(func() {
		var cursor uint64
		for {
			keys, next, e := r.Scan(ctx, cursor, "campusgo:"+ns+":*", 100).Result()
			if e != nil {
				t.Error(e)
				return
			}
			if len(keys) > 0 {
				r.Del(ctx, keys...)
			}
			if next == 0 {
				return
			}
			cursor = next
		}
	})
	limited := func(e error) {
		t.Helper()
		var f *fault.Error
		if !errors.As(e, &f) || f.Status != 429 {
			t.Fatal("expected rate rejection", e)
		}
	}
	t.Run("two_nodes_share_runner_and_task_buckets", func(t *testing.T) {
		for i := range 2 {
			l := a
			if i == 1 {
				l = b
			}
			release, e := l.admit(ctx, 9007199254740993, 9007199254740995, false)
			if e != nil {
				t.Fatal(e)
			}
			release()
		}
		limited(a.shared(ctx, 9007199254740993, 9007199254740995, false))
		for _, task := range []int64{6, 7} {
			release, e := b.admit(ctx, 9007199254740993, task, false)
			if e != nil {
				t.Fatal(e)
			}
			release()
		}
		limited(a.shared(ctx, 9007199254740993, 8, false))
		if e := a.shared(ctx, 9007199254740994, 9007199254740995, false); e != nil {
			t.Fatal("other runner blocked", e)
		}
		if e := a.shared(ctx, 9007199254740993, 0, true); e != nil {
			t.Fatal("query budget consumed by writes", e)
		}
	})
	t.Run("atomic_dual_bucket_and_independent_concurrency", func(t *testing.T) {
		var passed atomic.Int32
		var wg sync.WaitGroup
		for i := range 20 {
			wg.Go(func() {
				l := a
				if i%2 == 1 {
					l = b
				}
				release, e := l.admit(ctx, 50, 100, false)
				if e == nil {
					passed.Add(1)
					release()
				}
			})
		}
		wg.Wait()
		if passed.Load() != 2 {
			t.Fatal("dual bucket admitted", passed.Load())
		}
		// Hold every write slot, then use a new runner so Redis allowance is fresh.
		for range cap(a.writes) {
			a.writes <- struct{}{}
		}
		if _, e := a.admit(ctx, 51, 100, false); e == nil {
			t.Fatal("write admission queued")
		}
		release, e := a.admit(ctx, 51, 100, true)
		if e != nil {
			t.Fatal("write saturation blocked query", e)
		}
		release()
		for range cap(a.writes) {
			<-a.writes
		}
		for i := range 10 {
			if e := a.shared(ctx, 60, 0, true); e != nil {
				t.Fatal(e, i)
			}
		}
		limited(b.shared(ctx, 60, 0, true))
		if e := a.shared(ctx, 60, 100, false); e != nil {
			t.Fatal("queries consumed write bucket", e)
		}
	})
}
