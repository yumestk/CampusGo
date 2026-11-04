package main

import (
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/platform/database"
	"github.com/yumestk/CampusGo/internal/wallet"
	"google.golang.org/protobuf/encoding/protowire"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	name := flag.String("database", "campusgo", "local database")
	flag.Parse()
	cfg, err := auth.LoadConfig()
	if err != nil {
		return err
	}
	if !cfg.Demo || (cfg.Environment != "local" && cfg.Environment != "test") || cfg.Issuer != "http://127.0.0.1:15556/dex" {
		return fmt.Errorf("seed requires the isolated local/demo identity source")
	}
	if *name != "campusgo" && *name != "campusgo_verification" {
		return fmt.Errorf("invalid seed database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := database.Open(ctx, *name)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, u := range []struct {
		id         int64
		name, role string
	}{{1001, "发单同学", "STUDENT"}, {2001, "跑腿同学 A", "STUDENT"}, {2002, "跑腿同学 B", "STUDENT"}, {9001, "平台仲裁员", "ARBITRATOR"}} {
		if _, err := tx.ExecContext(ctx, "INSERT INTO app_user (id,campus_id,display_name,role) VALUES (?,1,?,?) ON DUPLICATE KEY UPDATE id=id", u.id, u.name, u.role); err != nil {
			return err
		}
		// Dex v2.46.1 GenSubject: protobuf (user_id=1, conn_id=2), raw base64url.
		data := protowire.AppendString(protowire.AppendTag(nil, 1, protowire.BytesType), fmt.Sprint(u.id))
		data = protowire.AppendString(protowire.AppendTag(data, 2, protowire.BytesType), "local")
		subject := base64.RawURLEncoding.EncodeToString(data)
		if _, err := tx.ExecContext(ctx, "INSERT INTO identity_binding (issuer,subject,user_id) VALUES (?,?,?) ON DUPLICATE KEY UPDATE issuer=issuer", cfg.Issuer, subject, u.id); err != nil {
			return err
		}
		var bound int64
		if err := tx.QueryRowContext(ctx, "SELECT user_id FROM identity_binding WHERE issuer=? AND subject=?", cfg.Issuer, subject).Scan(&bound); err != nil {
			return err
		}
		if bound != u.id {
			return fmt.Errorf("existing identity binding conflicts with local seed")
		}
		opening := int64(0)
		if u.id == 1001 {
			opening = 100000
		}
		if err := wallet.SeedOpening(ctx, tx, u.id, opening); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("seed commit result unknown: %w", err)
	}
	fmt.Println("local users, OIDC bindings and opening wallets seeded; existing balances preserved")
	return nil
}
