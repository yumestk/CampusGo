package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/yumestk/CampusGo/internal/platform/database"
	"github.com/yumestk/CampusGo/internal/wallet"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	name := flag.String("database", "campusgo", "database to reconcile")
	flag.Parse()
	if *name != "campusgo" && *name != "campusgo_verification" {
		return fmt.Errorf("invalid database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := database.Open(ctx, *name)
	if err != nil {
		return err
	}
	defer db.Close()
	checks, err := (wallet.Query{DB: db}).Reconcile(ctx)
	if err != nil {
		return err
	}
	if err = json.NewEncoder(os.Stdout).Encode(checks); err != nil {
		return err
	}
	for _, check := range checks {
		if check.Violations != 0 {
			return fmt.Errorf("fund reconciliation failed")
		}
	}
	return nil
}
