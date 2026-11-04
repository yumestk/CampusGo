package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
)

func main() {
	component := flag.String("component", "all", "all|mysql|redis|mq|gateway")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	runID := uuid.NewString()
	steps := []struct {
		name string
		run  func(context.Context, string) error
	}{{"mysql", probeMySQL}, {"redis", probeRedis}, {"mq", probeMQ}, {"gateway", probeGateway}}
	matched := false
	for _, step := range steps {
		if *component != "all" && *component != step.name {
			continue
		}
		matched = true
		start := time.Now()
		logger.Info("probe started", "component", step.name, "run_id", runID)
		if err := step.run(ctx, runID); err != nil {
			logger.Error("probe failed", "component", step.name, "run_id", runID, "error", err)
			os.Exit(1)
		}
		logger.Info("probe passed", "component", step.name, "run_id", runID, "elapsed", time.Since(start).String())
	}
	if !matched {
		fmt.Fprintln(os.Stderr, "unknown probe component")
		os.Exit(2)
	}
}
