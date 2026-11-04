package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

func probeRedis(ctx context.Context, runID string) error {
	client := redis.NewClient(&redis.Options{
		Addr: os.Getenv("CAMPUSGO_REDIS_ADDR"), Password: os.Getenv("CAMPUSGO_REDIS_PASSWORD"),
		PoolSize: 8, DialTimeout: 3 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second,
	})
	defer client.Close()
	if err := client.Ping(ctx).Err(); err != nil {
		return err
	}
	key := "campusgo:d0:{" + runID + "}:reservation"
	defer client.Del(context.Background(), key)
	acquire := redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then return 0 end
redis.call('PSETEX', KEYS[1], ARGV[2], ARGV[1])
return 1`)
	release := redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
return redis.call('DEL', KEYS[1])`)
	var winners atomic.Int32
	failures := make(chan error, 16)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			n, err := acquire.Run(ctx, client, []string{key}, runID, 5000).Int()
			if err != nil {
				failures <- err
				return
			}
			winners.Add(int32(n))
		})
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		return err
	}
	if winners.Load() != 1 {
		return fmt.Errorf("Lua reservation winners=%d, want 1", winners.Load())
	}
	n, err := release.Run(ctx, client, []string{key}, "old-token").Int()
	if err != nil || n != 0 {
		return fmt.Errorf("stale token release result=%d: %v", n, err)
	}
	ttl, err := client.PTTL(ctx, key).Result()
	if err != nil || ttl <= 0 {
		return fmt.Errorf("reservation TTL=%s: %v", ttl, err)
	}
	n, err = release.Run(ctx, client, []string{key}, runID).Int()
	if err != nil || n != 1 {
		return fmt.Errorf("owner release result=%d: %v", n, err)
	}
	return nil
}
