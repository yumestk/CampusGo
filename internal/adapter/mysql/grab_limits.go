package mysql

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/yumestk/CampusGo/internal/platform/fault"
)

var runnerBucket = func() *redis.Script {
	data, err := grabScripts.ReadFile("lua/runner_rate_limit.lua")
	if err != nil {
		panic(err)
	}
	return redis.NewScript(string(data))
}()

type hotWindow struct {
	stamps []int64
	head   int
	last   int64
}
type GrabLimits struct {
	redis                  *redis.Client
	namespace              string
	writes, queries        chan struct{}
	mu                     sync.Mutex
	started                time.Time
	pruned, recoverySecond int64
	recoveryCount          int
	hot                    map[int64]*hotWindow
	taskWriteCap           int
	taskInFlight           map[int64]int
}

func NewGrabLimits(r *redis.Client, namespace string) *GrabLimits {
	return &GrabLimits{
		redis: r, namespace: namespace,
		writes: make(chan struct{}, 10), queries: make(chan struct{}, 10),
		started: time.Now(), hot: map[int64]*hotWindow{},
		taskWriteCap: 2, taskInFlight: map[int64]int{},
	}
}

func (l *GrabLimits) shared(ctx context.Context, user, task int64, query bool) error {
	prefix := fmt.Sprintf("campusgo:%s:rl:{runner:%d}:", l.namespace, user)
	keys, args := []string{prefix + "all", fmt.Sprintf("%serrand:%d", prefix, task)}, []any{2, 4, 0.5, 2}
	if query {
		keys, args = []string{prefix + "query"}, []any{5, 10}
	}
	result, err := runnerBucket.Run(ctx, l.redis, keys, args...).Int64Slice()
	if err != nil {
		// SQL result lookup has a separate bounded recovery allowance. This does
		// not bypass authentication: its own Redis dependency must still resolve.
		if query && l.recoveryAllowed() {
			return nil
		}
		return &fault.Error{Code: "DEPENDENCY_UNAVAILABLE", Status: 503, Cause: err}
	}
	if len(result) != 2 {
		return fault.New("INTERNAL_ERROR", 500)
	}
	if result[0] != 1 {
		return &fault.Error{Code: "GRAB_RATE_LIMITED", Status: 429, RetryAfterMillis: result[1]}
	}
	return nil
}
func (l *GrabLimits) recoveryAllowed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	second := int64(time.Since(l.started) / time.Second)
	if second != l.recoverySecond {
		l.recoverySecond = second
		l.recoveryCount = 0
	}
	if l.recoveryCount >= 10 {
		return false
	}
	l.recoveryCount++
	return true
}

// Exact one-second sliding window, bounded to 4096 active hot keys per API.
// Capacity exhaustion rejects new keys rather than growing process memory.
func (l *GrabLimits) hotAllowed(task int64, now int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now-l.pruned >= int64(time.Second) {
		for id, w := range l.hot {
			if now-w.last >= int64(time.Second) {
				delete(l.hot, id)
			}
		}
		l.pruned = now
	}
	w := l.hot[task]
	if w == nil {
		if len(l.hot) >= 4096 {
			return fault.New("DEPENDENCY_UNAVAILABLE", 503)
		}
		w = &hotWindow{}
		l.hot[task] = w
	}
	w.last = now
	if len(w.stamps) < 500 {
		w.stamps = append(w.stamps, now)
		return nil
	}
	if now-w.stamps[w.head] < int64(time.Second) {
		return fault.New("GRAB_RATE_LIMITED", 429)
	}
	w.stamps[w.head] = now
	w.head = (w.head + 1) % len(w.stamps)
	return nil
}

func (l *GrabLimits) acquireTaskWrite(task int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	count, exists := l.taskInFlight[task]
	if count >= l.taskWriteCap || (!exists && len(l.taskInFlight) >= 4096) {
		return false
	}
	l.taskInFlight[task]++
	return true
}

func (l *GrabLimits) releaseTaskWrite(task int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.taskInFlight[task] <= 1 {
		delete(l.taskInFlight, task)
	} else {
		l.taskInFlight[task]--
	}
}

func (l *GrabLimits) admit(ctx context.Context, user, task int64, query bool) (func(), error) {
	if l == nil {
		return func() {}, nil
	} // Core protocol fixtures omit admission controls.
	if err := l.shared(ctx, user, task, query); err != nil {
		return nil, err
	}
	return l.admitLocal(task, query, int64(time.Since(l.started)))
}

func (l *GrabLimits) admitLocal(task int64, query bool, now int64) (func(), error) {
	slots := l.writes
	if query {
		slots = l.queries
	} else {
		if err := l.hotAllowed(task, now); err != nil {
			return nil, err
		}
		if !l.acquireTaskWrite(task) {
			return nil, fault.New("DEPENDENCY_UNAVAILABLE", 503)
		}
	}
	select {
	case slots <- struct{}{}:
	default:
		if !query {
			l.releaseTaskWrite(task)
		}
		return nil, fault.New("DEPENDENCY_UNAVAILABLE", 503)
	}
	return sync.OnceFunc(func() {
		<-slots
		if !query {
			l.releaseTaskWrite(task)
		}
	}), nil
}
