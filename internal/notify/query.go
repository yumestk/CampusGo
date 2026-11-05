package notify

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	"github.com/yumestk/CampusGo/internal/platform/fault"
)

type Notice struct {
	ID       int64     `json:"id,string"`
	ErrandID int64     `json:"errandId,string"`
	Type     string    `json:"type"`
	Content  string    `json:"content"`
	Read     bool      `json:"read"`
	Time     time.Time `json:"time"`
}
type Page struct {
	Items      []Notice `json:"items"`
	NextCursor string   `json:"nextCursor"`
}

func List(ctx context.Context, db *sql.DB, user int64, cursor string, size int) (Page, error) {
	p := Page{Items: []Notice{}}
	before := int64(9223372036854775807)
	var err error
	if cursor != "" {
		before, err = strconv.ParseInt(cursor, 10, 64)
		if err != nil || before <= 0 || strconv.FormatInt(before, 10) != cursor {
			return p, fault.New("INVALID_REQUEST", 400)
		}
	}
	if size < 1 || size > 100 {
		return p, fault.New("INVALID_REQUEST", 400)
	}
	rows, err := db.QueryContext(ctx, "SELECT id,errand_id,type,content,read_flag,created_at FROM notification WHERE user_id=? AND id<? ORDER BY id DESC LIMIT ?", user, before, size+1)
	if err != nil {
		return p, fault.Dependency(err)
	}
	defer rows.Close()
	for rows.Next() {
		var n Notice
		if err = rows.Scan(&n.ID, &n.ErrandID, &n.Type, &n.Content, &n.Read, &n.Time); err != nil {
			return p, fault.Dependency(err)
		}
		p.Items = append(p.Items, n)
	}
	if err = rows.Err(); err != nil {
		return p, fault.Dependency(err)
	}
	if len(p.Items) > size {
		p.Items = p.Items[:size]
		p.NextCursor = strconv.FormatInt(p.Items[size-1].ID, 10)
	}
	return p, nil
}
func Unread(ctx context.Context, db *sql.DB, user int64) (string, error) {
	var n int64
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM notification WHERE user_id=? AND read_flag=FALSE", user).Scan(&n)
	if err != nil {
		return "", fault.Dependency(err)
	}
	return strconv.FormatInt(n, 10), nil
}
func Read(ctx context.Context, db *sql.DB, user, id int64) error {
	// Ownership is checked on the same row; repeated reads remain idempotent.
	var found int64
	err := db.QueryRowContext(ctx, "SELECT id FROM notification WHERE id=? AND user_id=?", id, user).Scan(&found)
	if err == sql.ErrNoRows {
		return fault.New("NOT_FOUND", 404)
	}
	if err != nil {
		return fault.Dependency(err)
	}
	_, err = db.ExecContext(ctx, "UPDATE notification SET read_flag=TRUE WHERE id=? AND user_id=?", id, user)
	if err != nil {
		return fault.Dependency(err)
	}
	return nil
}
