package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Service         string
	Addr            string
	ShutdownTimeout time.Duration
}

func Load(service string) (Config, error) {
	var key, addr string
	switch service {
	case "api":
		key, addr = "CAMPUSGO_API_ADDR", "127.0.0.1:18080"
	case "worker":
		key, addr = "CAMPUSGO_WORKER_ADDR", "127.0.0.1:18081"
	default:
		return Config{}, fmt.Errorf("unknown service %q", service)
	}
	if value, ok := os.LookupEnv(key); ok {
		addr = value
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return Config{}, fmt.Errorf("invalid %s: %w", key, err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return Config{}, fmt.Errorf("%s must contain a numeric port between 1 and 65535", key)
	}
	timeout := 10 * time.Second
	if value, ok := os.LookupEnv("CAMPUSGO_SHUTDOWN_TIMEOUT"); ok {
		timeout, err = time.ParseDuration(value)
		if err != nil || timeout <= 0 {
			return Config{}, fmt.Errorf("CAMPUSGO_SHUTDOWN_TIMEOUT must be a positive duration")
		}
	}
	return Config{Service: service, Addr: addr, ShutdownTimeout: timeout}, nil
}
