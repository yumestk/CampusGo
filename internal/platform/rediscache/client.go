package rediscache

import (
	"fmt"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

func Open() (*redis.Client, error) {
	password := os.Getenv("CAMPUSGO_REDIS_PASSWORD")
	if password == "" || password == "GENERATE_REDIS_PASSWORD" {
		return nil, fmt.Errorf("CAMPUSGO_REDIS_PASSWORD is required")
	}
	addr := os.Getenv("CAMPUSGO_REDIS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:16379"
	}
	return redis.NewClient(&redis.Options{Addr: addr, Password: password, PoolSize: 8, MaxRetries: -1, DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second, PoolTimeout: time.Second, ContextTimeoutEnabled: true}), nil
}
