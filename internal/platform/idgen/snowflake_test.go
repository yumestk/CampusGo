package idgen

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestConcurrentIDsAndLayout(t *testing.T) {
	g, err := New(17)
	if err != nil {
		t.Fatal(err)
	}
	ids := make(chan int64, 10000)
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			for range 1000 {
				id, err := g.Next(context.Background())
				if err != nil {
					t.Error(err)
					return
				}
				ids <- id
			}
		})
	}
	wg.Wait()
	close(ids)
	seen := map[int64]bool{}
	for id := range ids {
		if id <= 0 || seen[id] || (id>>12)&1023 != 17 {
			t.Fatal("invalid or duplicate ID")
		}
		seen[id] = true
	}
	if len(seen) != 10000 {
		t.Fatal(len(seen))
	}
}
func TestClockRollbackRangeAndSequenceCancellation(t *testing.T) {
	for _, worker := range []int64{-1, 1024} {
		if _, err := New(worker); err == nil {
			t.Fatal("invalid worker")
		}
	}
	g, _ := New(1)
	now := Epoch + 10
	g.now = func() time.Time { return time.UnixMilli(now) }
	id, err := g.Next(context.Background())
	if err != nil || id>>22 != 10 {
		t.Fatal(id, err)
	}
	now--
	if _, err = g.Next(context.Background()); err == nil {
		t.Fatal("rollback accepted")
	}
	now = Epoch - 1
	if _, err = g.Next(context.Background()); err == nil {
		t.Fatal("pre-epoch accepted")
	}
	now = Epoch + (1 << 41)
	if _, err = g.Next(context.Background()); err == nil {
		t.Fatal("timestamp overflow")
	}
	now = Epoch + 10
	g.sequence = 4095
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Millisecond)
	defer cancel()
	if _, err = g.Next(ctx); err != context.DeadlineExceeded {
		t.Fatal("exhaustion did not respect context", err)
	}
	now++
	id, err = g.Next(context.Background())
	if err != nil || id&4095 != 0 {
		t.Fatal("new millisecond did not reset sequence", err)
	}
}
