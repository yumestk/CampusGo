package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/go-sql-driver/mysql"
	"github.com/yumestk/CampusGo/internal/platform/database"
)

func probeMySQL(ctx context.Context, runID string) error {
	db, err := database.Open(ctx, "campusgo_verification")
	if err != nil {
		return err
	}
	defer db.Close()
	var isolation string
	if err := db.QueryRowContext(ctx, "SELECT @@transaction_isolation").Scan(&isolation); err != nil {
		return err
	}
	if isolation != "READ-COMMITTED" {
		return fmt.Errorf("unexpected isolation %s", isolation)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "INSERT INTO d0_protocol_probe (id,state) VALUES (?,?)", runID, "pending"); err != nil {
		return err
	}
	if err := tx.Rollback(); err != nil {
		return err
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM d0_protocol_probe WHERE id=?", runID).Scan(&count); err != nil || count != 0 {
		return fmt.Errorf("rollback visibility: count=%d: %v", count, err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "INSERT INTO d0_protocol_probe (id,state) VALUES (?,?)", runID, "pending"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("probe commit result unknown; inspect run %s: %w", runID, err)
	}
	_, err = db.ExecContext(ctx, "INSERT INTO d0_protocol_probe (id,state) VALUES (?,?)", runID, "duplicate")
	var duplicate *mysql.MySQLError
	if !errors.As(err, &duplicate) || duplicate.Number != 1062 {
		return fmt.Errorf("unique constraint did not reject duplicate: %v", err)
	}
	var winners atomic.Int32
	failures := make(chan error, 16)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			result, err := db.ExecContext(ctx, "UPDATE d0_protocol_probe SET state='committed' WHERE id=? AND state='pending'", runID)
			if err != nil {
				failures <- err
				return
			}
			n, err := result.RowsAffected()
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
		return fmt.Errorf("CAS winner count=%d, want 1", winners.Load())
	}
	_, err = db.ExecContext(ctx, "DELETE FROM d0_protocol_probe WHERE id=?", runID)
	return err
}
