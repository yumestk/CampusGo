package mysql

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
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
func (c *DetailCache) bloomKey() string { return "campusgo:" + c.Namespace + ":detail:bloom:v1" }
func bloomOffsets(id int64) []int64 {
	hash := sha256.Sum256([]byte(fmt.Sprint(id)))
	a := binary.BigEndian.Uint64(hash[:8])
	b := binary.BigEndian.Uint64(hash[8:16]) | 1
	offsets := make([]int64, 7)
	for i := range offsets {
		offsets[i] = int64((a + uint64(i)*b) % (1 << 20))
	}
	return offsets
}
func (c *DetailCache) bloom(ctx context.Context, id int64, register bool) {
	pipe := c.Redis.Pipeline()
	commands := []*redis.IntCmd{}
	for _, offset := range bloomOffsets(id) {
		if register {
			pipe.SetBit(ctx, c.bloomKey(), offset, 1)
		} else {
			commands = append(commands, pipe.GetBit(ctx, c.bloomKey(), offset))
		}
	}
	if _, err := pipe.Exec(ctx); err != nil {
		c.count("bloom_error")
		return
	}
	if register {
		return
	}
	for _, cmd := range commands {
		if cmd.Val() == 0 {
			c.count("bloom_negative_hint")
			return
		}
	}
	c.count("bloom_positive_hint")
}

type cachedDetail struct {
	Exp   int64 `json:"exp"`
	Empty bool  `json:"empty"`
	Data  Card  `json:"data"`
}

func (c *DetailCache) put(ctx context.Context, id int64, card Card, empty bool) {
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
	if !empty {
		c.bloom(ctx, id, true)
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
			if err == nil {
				c.put(jobCtx, job.id, card, false)
				c.count("rebuild")
			} else {
				c.count("refresh_error")
			}
			unlockDetail.Run(jobCtx, c.Redis, []string{fmt.Sprintf("campusgo:%s:detail:rebuild:%d", c.Namespace, job.id)}, job.token)
			cancel()
		}
	}
}

var unlockDetail = redis.NewScript(`if redis.call('GET',KEYS[1])==ARGV[1] then return redis.call('DEL',KEYS[1]) end return 0`)

func (q Query) cachedDetail(ctx context.Context, u auth.User, id int64) (Card, error) {
	cache := q.Cache
	cache.bloom(ctx, id, false)
	raw, readErr := cache.Redis.Get(ctx, detailKey(cache.Namespace, id, rand.IntN(4))).Bytes()
	var cached cachedDetail
	decoded := readErr == nil && json.Unmarshal(raw, &cached) == nil
	// Authorization and actions always come from SQL. Cache hits are NOT zero-query
	// API requests; the metrics expose this deliberate consistency tradeoff.
	var live Card
	cache.count("sql_authority_query")
	err := q.DB.QueryRowContext(ctx, "SELECT id,campus_id,publisher_id,grabber_id,slot_total,slot_taken,status,round,version,confirm_deadline,delivered_at FROM errand WHERE id=?", id).Scan(&live.ID, &live.CampusID, &live.PublisherID, &live.GrabberID, &live.SlotTotal, &live.SlotTaken, &live.Status, &live.Round, &live.Version, &live.ConfirmDeadline, &live.DeliveredAt)
	if errors.Is(err, sql.ErrNoRows) {
		if decoded && cached.Empty && cached.Exp > time.Now().UnixMilli() {
			cache.count("negative_hint_hit")
		} else {
			cache.put(ctx, id, Card{}, true)
		}
		return Card{}, fault.New("ERRAND_NOT_FOUND", 404)
	}
	if err != nil {
		return Card{}, fault.Dependency(err)
	}
	if live.CampusID != u.CampusID {
		return Card{}, fault.New("ERRAND_NOT_FOUND", 404)
	}
	if !Visible(live, u) {
		return Card{}, fault.New("FORBIDDEN", 403)
	}
	if decoded && cached.Empty {
		cache.count("negative_hint_miss")
	}
	valid := decoded && !cached.Empty && cached.Data.ID == id && cached.Data.CampusID == u.CampusID && cached.Data.Version == live.Version
	if valid {
		// Both fresh hits and stale-while-revalidate responses use SQL authority fields.
		cached.Data.ConfirmDeadline = live.ConfirmDeadline
		cached.Data.DeliveredAt = live.DeliveredAt
		cached.Data.GrabberID = live.GrabberID
		cached.Data.Status = live.Status
		cached.Data.Round = live.Round
		cached.Data.SlotTotal = live.SlotTotal
		cached.Data.SlotTaken = live.SlotTaken
		cached.Data.PublisherID = live.PublisherID
	}
	fresh := valid && cached.Exp > time.Now().UnixMilli()
	if fresh {
		cache.count("hit")
		decorate(&cached.Data, u)
		return cached.Data, nil
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
	body, err := scan(q.DB.QueryRowContext(ctx, "SELECT "+columns+" FROM errand WHERE id=? AND campus_id=?", id, u.CampusID))
	if errors.Is(err, sql.ErrNoRows) {
		return Card{}, fault.New("ERRAND_NOT_FOUND", 404)
	}
	if err != nil {
		return Card{}, fault.Dependency(err)
	}
	if !Visible(body, u) {
		return Card{}, fault.New("FORBIDDEN", 403)
	}
	// A miss must still work when Redis is down or another request holds the rebuild
	// lock. Only the lease owner writes the cache, and every future read rechecks SQL.
	if acquired {
		cache.put(ctx, id, body, false)
		cache.count("rebuild")
	}
	decorate(&body, u)
	return body, nil
}
func (g *TaskStore) invalidateDetail(ctx context.Context, id int64) error {
	for shard := range 4 {
		if err := g.Redis.Del(ctx, detailKey(g.Namespace, id, shard)).Err(); err != nil {
			return err
		}
	}
	return nil
}
func (g *TaskStore) reconcileDetail(ctx context.Context, c Card) error {
	cache := DetailCache{Redis: g.Redis, Namespace: g.Namespace}
	cache.bloom(ctx, c.ID, true)
	for shard := range 4 {
		raw, err := g.Redis.Get(ctx, detailKey(g.Namespace, c.ID, shard)).Bytes()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			return err
		}
		var stored cachedDetail
		if json.Unmarshal(raw, &stored) != nil || stored.Empty || stored.Data.ID != c.ID || stored.Data.Version != c.Version {
			if err = g.Redis.Del(ctx, detailKey(g.Namespace, c.ID, shard)).Err(); err != nil {
				return err
			}
		}
	}
	return nil
}
