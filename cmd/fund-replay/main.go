package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/yumestk/CampusGo/internal/application"
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
	replay := flag.String("replay", "", "explicit DEAD fund request UUID to republish; default lists failures")
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

	f := &application.Funds{Store: &errand.FundStore{DB: db}}
	if *replay != "" {
		ok, err := f.Replay(ctx, *replay, *reason)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("message not found or not DEAD")
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"replayed": true, "id": *replay})
	}
	rows, err := f.Dead(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(rows)
}
