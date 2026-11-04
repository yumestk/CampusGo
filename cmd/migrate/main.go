package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/yumestk/CampusGo/internal/platform/database"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	dir := flag.String("dir", "migrations", "SQL migration directory")
	name := flag.String("database", "campusgo", "target database")
	table := flag.String("table", "campusgo_schema_version", "migration history table")
	flag.Parse()
	if flag.NArg() != 1 || (flag.Arg(0) != "up" && flag.Arg(0) != "status") {
		return fmt.Errorf("usage: migrate [-dir path] [-database name] up|status")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db, err := database.Open(ctx, *name)
	if err != nil {
		return err
	}
	defer db.Close()
	if !regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`).MatchString(*table) {
		return fmt.Errorf("invalid migration table")
	}
	goose.SetTableName(*table)
	if err := goose.SetDialect("mysql"); err != nil {
		return err
	}
	return goose.RunContext(ctx, flag.Arg(0), db, *dir)
}
