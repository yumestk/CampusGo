package application

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/yumestk/CampusGo/internal/contracts"
)

var poisonDueOrder = errors.New("poison due order")

type dueOrders struct {
	at       time.Time
	poisons  int64
	last     int64
	attempts map[int64]int
	done     map[int64]bool
	pages    int
}

func (q *dueOrders) page(_ context.Context, after contracts.DueCursor, limit int) ([]contracts.DueTask, error) {
	q.pages++
	var rows []contracts.DueTask
	for id := after.ID + 1; id <= q.last && len(rows) < limit; id++ {
		if !q.done[id] {
			rows = append(rows, contracts.DueTask{DueCursor: contracts.DueCursor{At: q.at, ID: id}, CampusID: 1})
		}
	}
	return rows, nil
}

func (q *dueOrders) apply(id int64) error {
	q.attempts[id]++
	if id <= q.poisons {
		return poisonDueOrder
	}
	q.done[id] = true
	return nil
}

type dueFundsProbe struct {
	SettlementStore
	*dueOrders
}

func (s dueFundsProbe) DueSettlements(ctx context.Context, after contracts.DueCursor, limit int) ([]contracts.DueTask, error) {
	return s.page(ctx, after, limit)
}
func (s dueFundsProbe) Prepare(_ context.Context, _ contracts.Actor, _ string, c FundCommand, _ []byte) (FundEvent, *FundResult, error) {
	err := s.apply(c.ErrandID)
	return FundEvent{}, &FundResult{State: "COMMITTED"}, err
}

type dueTimeoutProbe struct {
	TimeoutStore
	*dueOrders
}

func (s dueTimeoutProbe) DueTimeouts(ctx context.Context, after contracts.DueCursor, limit int) ([]contracts.DueTask, error) {
	return s.page(ctx, after, limit)
}
func (s dueTimeoutProbe) TimeoutSnapshot(_ context.Context, id int64) (contracts.Card, bool, error) {
	return contracts.Card{ID: id, Status: "LOCKED", Round: 5}, true, nil
}
func (s dueTimeoutProbe) TimeoutCycle(context.Context, int64) (int, error) { return 0, nil }
func (s dueTimeoutProbe) TimeoutStep(_ context.Context, c contracts.Card, _ *contracts.Candidate) (string, error) {
	return "REVERTED", s.apply(c.ID)
}

func TestDueScansContinuePastPoisonOrders(t *testing.T) {
	ctx := context.Background()
	for _, scanner := range []string{"auto_settle", "confirm_timeout"} {
		for _, poisonCount := range []int64{55, dueMaxPages*50 + 1} {
			t.Run(fmt.Sprintf("%s/%d_poison_orders", scanner, poisonCount), func(t *testing.T) {
				q := &dueOrders{at: time.Now(), poisons: poisonCount, last: poisonCount + 1, attempts: map[int64]int{}, done: map[int64]bool{}}
				reported := map[int64]int{}
				report := func(id int64, err error) {
					if !errors.Is(err, poisonDueOrder) {
						t.Fatalf("unexpected report for %d: %v", id, err)
					}
					reported[id]++
				}
				var run func(context.Context) error
				switch scanner {
				case "auto_settle":
					f := &Funds{Store: dueFundsProbe{dueOrders: q}, OnDueError: report}
					run = f.ScanAutoSettle
				case "confirm_timeout":
					g := &Timeouts{Store: dueTimeoutProbe{dueOrders: q}, OnDueError: report}
					run = func(ctx context.Context) error { return g.ScanDue(ctx, 50) }
				}
				if err := run(ctx); !errors.Is(err, poisonDueOrder) {
					t.Fatalf("first scan did not report poison: %v", err)
				}
				if q.pages > dueMaxPages {
					t.Fatalf("scan read %d pages, max %d", q.pages, dueMaxPages)
				}
				if poisonCount >= dueMaxPages*50 {
					if q.done[q.last] || q.pages != dueMaxPages {
						t.Fatalf("first scan did not stop at its page budget: %d pages", q.pages)
					}
					if err := run(ctx); !errors.Is(err, poisonDueOrder) {
						t.Fatalf("resumed scan did not report remaining poison: %v", err)
					}
				}
				if !q.done[q.last] {
					t.Fatalf("healthy order %d did not complete", q.last)
				}
				q.last++
				if err := run(ctx); err != nil {
					t.Fatalf("cooling poisons retried: %v", err)
				}
				if !q.done[q.last] {
					// With more than a full scan, resume where the cooling page ended.
					if err := run(ctx); err != nil || !q.done[q.last] {
						t.Fatalf("healthy order behind cooling pages failed: %v", err)
					}
				}
				for id := int64(1); id <= q.poisons; id++ {
					if q.attempts[id] != 1 || reported[id] != 1 {
						t.Fatalf("poison %d: attempts=%d, reports=%d", id, q.attempts[id], reported[id])
					}
				}
			})
		}
	}
}
