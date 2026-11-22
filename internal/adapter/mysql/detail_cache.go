package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/platform/fault"
)

type DetailCache struct {
	Redis     *redis.Client
	Namespace string
	Events    *prometheus.CounterVec
	refresh   chan detailRefresh
}

func NewDetailCache(r *redis.Client, namespace string, registry *prometheus.Registry) *DetailCache {
	c := &DetailCache{Redis: r, Namespace: namespace, refresh: make(chan detailRefresh, 32), Events: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "campusgo_detail_operations_total", Help: "Detail cache observations and actual SQL queries, counted independently."}, []string{"operation"})}
	registry.MustRegister(c.Events)
	return c
}
func (c *DetailCache) count(op string) {
	if c.Events != nil {
		c.Events.WithLabelValues(op).Inc()
	}
}
func detailKey(namespace string, id int64, shard int) string {
	return fmt.Sprintf("campusgo:%s:detail:%d:%d", namespace, id, shard)
}

func (c *DetailCache) Invalidate(ctx context.Context, id int64) error {
	if c == nil || c.Redis == nil {
		return nil
	}
	for shard := range 4 {
		if err := c.Redis.Del(ctx, detailKey(c.Namespace, id, shard)).Err(); err != nil {
			return err
		}
	}
	return nil
}

type cachedDetail struct {
	Exp   int64 `json:"exp"`
	Empty bool  `json:"empty"`
	Data  Card  `json:"data"`
}

func (c *DetailCache) put(ctx context.Context, id int64, card Card, empty bool) {
	if !empty && card.Status == "DRAFT" {
		return
	}
	ttl := time.Duration(600+rand.IntN(241)-120) * time.Second
	logical := ttl / 2
	if empty {
		ttl = 60 * time.Second
		logical = ttl
	}
	raw, _ := json.Marshal(cachedDetail{time.Now().Add(logical).UnixMilli(), empty, card})
	for shard := range 4 {
		if err := c.Redis.Set(ctx, detailKey(c.Namespace, id, shard), raw, ttl).Err(); err != nil {
			c.count("write_error")
			return
		}
	}
}

type detailRefresh struct {
	id    int64
	token string
}

// One bounded worker per API process preserves the original stale-while-revalidate
// behavior without spawning an unbounded goroutine for each hot-key request.
func (c *DetailCache) RunRefresh(ctx context.Context, db *sql.DB) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-c.refresh:
			jobCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			c.count("sql_body_query")
			card, err := scan(db.QueryRowContext(jobCtx, "SELECT "+columns+" FROM errand WHERE id=?", job.id))
			switch {
			case errors.Is(err, sql.ErrNoRows):
				c.put(jobCtx, job.id, Card{}, true)
			case err != nil:
				c.count("refresh_error")
			case card.Status == "DRAFT":
				if err := c.Invalidate(jobCtx, job.id); err != nil {
					c.count("refresh_error")
				}
			default:
				c.put(jobCtx, job.id, card, false)
				c.count("rebuild")
			}
			unlockDetail.Run(jobCtx, c.Redis, []string{fmt.Sprintf("campusgo:%s:detail:rebuild:%d", c.Namespace, job.id)}, job.token)
			cancel()
		}
	}
}

var unlockDetail = redis.NewScript(`if redis.call('GET',KEYS[1])==ARGV[1] then return redis.call('DEL',KEYS[1]) end return 0`)

func (q Query) cachedDetail(ctx context.Context, u auth.User, id int64) (Card, error) {
	cache := q.Cache
	raw, readErr := cache.Redis.Get(ctx, detailKey(cache.Namespace, id, rand.IntN(4))).Bytes()
	var cached cachedDetail
	decoded := readErr == nil && json.Unmarshal(raw, &cached) == nil
	now := time.Now().UnixMilli()
	if decoded && cached.Empty && cached.Exp > now {
		cache.count("negative_hit")
		return Card{}, fault.New("ERRAND_NOT_FOUND", 404)
	}
	valid := decoded && !cached.Empty && cached.Data.ID == id && cached.Data.Status != "DRAFT"
	if valid {
		if cached.Data.CampusID != u.CampusID {
			return Card{}, fault.New("ERRAND_NOT_FOUND", 404)
		}
		if !Visible(cached.Data, u) {
			return Card{}, fault.New("FORBIDDEN", 403)
		}
		if cached.Exp > now {
			cache.count("hit")
			decorate(&cached.Data, u)
			return cached.Data, nil
		}
	}
	if readErr != nil && !errors.Is(readErr, redis.Nil) {
		cache.count("read_error")
	} else {
		cache.count("miss_or_expired")
	}
	token := uuid.NewString()
	lockKey := fmt.Sprintf("campusgo:%s:detail:rebuild:%d", cache.Namespace, id)
	acquired, _ := cache.Redis.SetNX(ctx, lockKey, token, 10*time.Second).Result()
	if valid && !acquired {
		cache.count("stale_served")
		decorate(&cached.Data, u)
		return cached.Data, nil
	}
	if valid && acquired && cache.refresh != nil {
		select {
		case cache.refresh <- detailRefresh{id, token}:
			cache.count("stale_served")
			decorate(&cached.Data, u)
			return cached.Data, nil
		default:
			cache.count("refresh_queue_full")
		}
	}
	if acquired {
		defer unlockDetail.Run(ctx, cache.Redis, []string{lockKey}, token)
	}
	cache.count("sql_body_query")
	body, err := scan(q.DB.QueryRowContext(ctx, "SELECT "+columns+" FROM errand WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		if acquired {
			cache.put(ctx, id, Card{}, true)
		}
		return Card{}, fault.New("ERRAND_NOT_FOUND", 404)
	}
	if err != nil {
		return Card{}, fault.Dependency(err)
	}
	if body.CampusID != u.CampusID {
		return Card{}, fault.New("ERRAND_NOT_FOUND", 404)
	}
	if !Visible(body, u) {
		return Card{}, fault.New("FORBIDDEN", 403)
	}
	// Concurrent misses can read SQL. The rebuild lock owner populates the cache.
	if acquired && body.Status != "DRAFT" {
		cache.put(ctx, id, body, false)
		cache.count("rebuild")
	}
	decorate(&body, u)
	return body, nil
}
func (g *TaskStore) invalidateDetail(ctx context.Context, id int64) error {
	return (&DetailCache{Redis: g.Redis, Namespace: g.Namespace}).Invalidate(ctx, id)
}
func (g *TaskStore) reconcileDetail(ctx context.Context, c Card) error {
	if c.Status == "DRAFT" {
		return g.invalidateDetail(ctx, c.ID)
	}
	for shard := range 4 {
		raw, err := g.Redis.Get(ctx, detailKey(g.Namespace, c.ID, shard)).Bytes()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			return err
		}
		var stored cachedDetail
		if json.Unmarshal(raw, &stored) != nil || stored.Empty || stored.Data.ID != c.ID || stored.Data.Status == "DRAFT" || stored.Data.Version != c.Version {
			if err = g.Redis.Del(ctx, detailKey(g.Namespace, c.ID, shard)).Err(); err != nil {
				return err
			}
		}
	}
	return nil
}
