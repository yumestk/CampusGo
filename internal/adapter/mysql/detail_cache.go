package mysql

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/platform/fault"
)

type DetailCache struct {
	Redis      *redis.Client
	Namespace  string
	LegacyRead bool
	Events     *prometheus.CounterVec
	refresh    chan detailRefresh
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

const detailWatermarkTTL = 30 * time.Minute

//go:embed lua/detail_cache.lua
var detailCacheLua string

var writeDetail = redis.NewScript(detailCacheLua)

func detailPrefix(namespace string, id int64) string {
	return fmt.Sprintf("campusgo:%s:detail:{%d}:", namespace, id)
}

func detailKey(namespace string, id int64, shard int) string {
	return detailPrefix(namespace, id) + strconv.Itoa(shard)
}

func detailKeys(namespace string, id int64) []string {
	prefix := detailPrefix(namespace, id)
	return []string{prefix + "version", prefix + "empty", prefix + "0", prefix + "1", prefix + "2", prefix + "3"}
}

func legacyDetailKey(namespace string, id int64, shard int) string {
	return fmt.Sprintf("campusgo:%s:detail:%d:%d", namespace, id, shard)
}

func (c *DetailCache) readKey(id int64, shard int) string {
	if c.LegacyRead {
		return legacyDetailKey(c.Namespace, id, shard)
	}
	return detailKey(c.Namespace, id, shard)
}

func (c *DetailCache) Invalidate(ctx context.Context, id, version int64) error {
	return c.expire(ctx, id, version, "invalidate")
}

func (c *DetailCache) PublishDetail(ctx context.Context, id, version int64) error {
	return c.expire(ctx, id, version, "published")
}

const clearLegacyNegative = `
local raw = redis.call('GET', KEYS[1])
if not raw then return 0 end
local ok, entry = pcall(cjson.decode, raw)
if ok and type(entry) == 'table' and entry.empty == true then
    return redis.call('DEL', KEYS[1])
end
return 0
`

func (c *DetailCache) expire(ctx context.Context, id, version int64, operation string) error {
	if c == nil || c.Redis == nil {
		return nil
	}
	err := writeDetail.Run(ctx, c.Redis, detailKeys(c.Namespace, id), operation, strconv.FormatInt(version, 10), "", 0, detailWatermarkTTL.Milliseconds()).Err()
	// Legacy shard keys have different hash slots. Each compatibility delete
	// stays single-key, and failure of either format is returned to the worker.
	pipe := c.Redis.Pipeline()
	for shard := range 4 {
		key := legacyDetailKey(c.Namespace, id, shard)
		if operation == "published" {
			pipe.Eval(ctx, clearLegacyNegative, []string{key})
		} else {
			pipe.Del(ctx, key)
		}
	}
	_, legacyErr := pipe.Exec(ctx)
	return errors.Join(err, legacyErr)
}

type cachedDetail struct {
	Exp   int64 `json:"exp"`
	Empty bool  `json:"empty"`
	Data  Card  `json:"data"`
}

func (c *DetailCache) put(ctx context.Context, id int64, card Card, empty bool) bool {
	if !empty && card.Status == "DRAFT" {
		return false
	}
	ttl := time.Duration(600+rand.IntN(241)-120) * time.Second
	logical := ttl / 2
	operation := "fill"
	if empty {
		ttl = time.Minute
		logical = ttl
		operation = "negative"
	}
	raw, _ := json.Marshal(cachedDetail{time.Now().Add(logical).UnixMilli(), empty, card})
	written, err := writeDetail.Run(ctx, c.Redis, detailKeys(c.Namespace, id), operation, strconv.FormatInt(card.Version, 10), raw, ttl.Milliseconds(), detailWatermarkTTL.Milliseconds()).Int()
	if err != nil {
		c.count("write_error")
		return false
	}
	if written != 1 || !c.LegacyRead {
		return written == 1
	}
	pipe := c.Redis.Pipeline()
	for shard := range 4 {
		pipe.Set(ctx, legacyDetailKey(c.Namespace, id, shard), raw, ttl)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		c.count("write_error")
		return false
	}
	return true
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
				if err := c.Invalidate(jobCtx, job.id, card.Version); err != nil {
					c.count("refresh_error")
				}
			default:
				if c.put(jobCtx, job.id, card, false) {
					c.count("rebuild")
				}
			}
			unlockDetail.Run(jobCtx, c.Redis, []string{detailPrefix(c.Namespace, job.id) + "rebuild"}, job.token)
			cancel()
		}
	}
}

var unlockDetail = redis.NewScript(`if redis.call('GET',KEYS[1])==ARGV[1] then return redis.call('DEL',KEYS[1]) end return 0`)

func (q Query) cachedDetail(ctx context.Context, u auth.User, id int64) (Card, error) {
	cache := q.Cache
	raw, readErr := cache.Redis.Get(ctx, cache.readKey(id, rand.IntN(4))).Bytes()
	if errors.Is(readErr, redis.Nil) && !cache.LegacyRead {
		raw, readErr = cache.Redis.Get(ctx, detailPrefix(cache.Namespace, id)+"empty").Bytes()
	}
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
	lockKey := detailPrefix(cache.Namespace, id) + "rebuild"
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
	if acquired && cache.put(ctx, id, body, false) {
		cache.count("rebuild")
	}
	decorate(&body, u)
	return body, nil
}
func (g *TaskStore) invalidateDetail(ctx context.Context, id, version int64) error {
	return (&DetailCache{Redis: g.Redis, Namespace: g.Namespace}).Invalidate(ctx, id, version)
}

func (g *TaskStore) reconcileDetail(ctx context.Context, c Card) error {
	entries, err := g.Redis.MGet(ctx, detailKeys(g.Namespace, c.ID)[1:]...).Result()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		raw, ok := entry.(string)
		if !ok {
			continue
		}
		var stored cachedDetail
		if c.Status == "DRAFT" || json.Unmarshal([]byte(raw), &stored) != nil || stored.Empty || stored.Data.ID != c.ID || stored.Data.Status == "DRAFT" || stored.Data.Version < c.Version {
			return g.invalidateDetail(ctx, c.ID, c.Version)
		}
	}
	return nil
}
