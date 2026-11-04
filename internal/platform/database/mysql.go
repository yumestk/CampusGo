package database

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"time"

	"github.com/go-sql-driver/mysql"
)

func Open(ctx context.Context, name string) (*sql.DB, error) {
	password := os.Getenv("CAMPUSGO_MYSQL_PASSWORD")
	if password == "" || password == "GENERATE_APP_PASSWORD" {
		return nil, errors.New("CAMPUSGO_MYSQL_PASSWORD must contain a local credential")
	}
	cfg := mysql.NewConfig()
	cfg.User, cfg.Passwd, cfg.DBName = "campusgo", password, name
	cfg.Net, cfg.Addr = "tcp", os.Getenv("CAMPUSGO_MYSQL_ADDR")
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:13306"
	}
	cfg.ParseTime, cfg.Loc = true, time.UTC
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 3*time.Second, 5*time.Second, 5*time.Second
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
