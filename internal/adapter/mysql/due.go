package mysql

import (
	"context"
	"database/sql"

	"github.com/yumestk/CampusGo/internal/contracts"
)

func (f *FundStore) DueSettlements(ctx context.Context, after contracts.DueCursor, limit int) ([]contracts.DueTask, error) {
	return duePage(ctx, f.DB, "delivered_at", "status='DELIVERED' AND delivered_at<=TIMESTAMPADD(SECOND,-86400,CURRENT_TIMESTAMP(3))", after, limit)
}

func (g *TaskStore) DueTimeouts(ctx context.Context, after contracts.DueCursor, limit int) ([]contracts.DueTask, error) {
	return duePage(ctx, g.DB, "confirm_deadline", "status='LOCKED' AND confirm_deadline<=CURRENT_TIMESTAMP(3)", after, limit)
}

func duePage(ctx context.Context, db *sql.DB, deadline, predicate string, after contracts.DueCursor, limit int) ([]contracts.DueTask, error) {
	query := "SELECT id,campus_id,round," + deadline + " FROM errand WHERE " + predicate
	var args []any
	if after.ID != 0 {
		query += " AND (" + deadline + ">? OR (" + deadline + "=? AND id>?))"
		args = append(args, after.At, after.At, after.ID)
	}
	query += " ORDER BY " + deadline + ",id LIMIT ?"
	rows, err := db.QueryContext(ctx, query, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []contracts.DueTask
	for rows.Next() {
		var task contracts.DueTask
		if err = rows.Scan(&task.ID, &task.CampusID, &task.Round, &task.At); err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}
