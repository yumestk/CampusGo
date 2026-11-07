package main

import (
	"github.com/yumestk/CampusGo/internal/application"

	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	errand "github.com/yumestk/CampusGo/internal/adapter/mysql"
	"github.com/yumestk/CampusGo/internal/platform/database"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	name := flag.String("database", "campusgo", "database to inspect")
	replay := flag.Int64("replay", 0, "explicit DEAD repair ID to replay; default only lists failures")
	reason := flag.String("reason", "", "required audit reason for replay")
	flag.Parse()
	if *name != "campusgo" && *name != "campusgo_verification" {
		return fmt.Errorf("invalid database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := database.Open(ctx, *name)
	if err != nil {
		return err
	}
	defer db.Close()
	store := &errand.TaskStore{DB: db}
	g := &application.Repairs{Store: store}
	if *replay != 0 {
		ok, err := g.ReplayRepair(ctx, *replay, *reason)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("repair not found or not DEAD")
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"replayed": true, "id": fmt.Sprint(*replay)})
	}
	rows, err := g.DeadRepairs(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(rows)
}
