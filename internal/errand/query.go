package errand

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/platform/fault"
)

type Card struct {
	ID               int64      `json:"id,string"`
	CampusID         int64      `json:"campusId,string"`
	PublisherID      int64      `json:"publisherId,string"`
	GrabberID        *int64     `json:"grabberId,string"`
	Type             string     `json:"type"`
	Title            string     `json:"title"`
	RewardCents      int64      `json:"rewardCents,string"`
	SlotTotal        int        `json:"slotTotal"`
	SlotTaken        int        `json:"slotTaken"`
	Status           string     `json:"status"`
	Round            int        `json:"round"`
	Version          int64      `json:"version,string"`
	Role             string     `json:"role"`
	AvailableActions []string   `json:"availableActions"`
	ConfirmDeadline  *time.Time `json:"confirmDeadline"`
	DeliveredAt      *time.Time `json:"deliveredAt"`
	CreatedAt        time.Time  `json:"-"`
}
type Page struct {
	Items      []Card `json:"items"`
	NextCursor string `json:"nextCursor"`
}
type ListOptions struct {
	CampusID     int64
	Status, Role string
	Before       int64
	BeforeTime   time.Time
	Page, Size   int
}
type Query struct {
	DB    *sql.DB
	Cache *DetailCache
}

const columns = "id,campus_id,publisher_id,grabber_id,type,title,reward_amount,slot_total,slot_taken,status,round,version,created_at,confirm_deadline,delivered_at"

// The original composite cursor is stable when IDs and commit order differ.
func ParseCursor(cursor string) (time.Time, int64, error) {
	if len(cursor) > 128 {
		return time.Time{}, 0, fault.New("INVALID_REQUEST", 400)
	}
	data, err := base64.RawURLEncoding.DecodeString(cursor)
	parts := strings.Split(string(data), ":")
	if err != nil || len(parts) != 2 {
		return time.Time{}, 0, fault.New("INVALID_REQUEST", 400)
	}
	ms, e1 := strconv.ParseInt(parts[0], 10, 64)
	id, e2 := strconv.ParseInt(parts[1], 10, 64)
	if e1 != nil || e2 != nil || ms <= 0 || ms > 253402300799999 || id <= 0 {
		return time.Time{}, 0, fault.New("INVALID_REQUEST", 400)
	}
	return time.UnixMilli(ms).UTC(), id, nil
}
func cursor(c Card) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(c.CreatedAt.UnixMilli(), 10) + ":" + strconv.FormatInt(c.ID, 10)))
}

var statuses = map[string]bool{"DRAFT": true, "PUBLISHED": true, "LOCKED": true, "ACCEPTED": true, "PICKED_UP": true, "DELIVERED": true, "SETTLED": true, "CLOSED": true, "CANCELLED": true, "DISPUTED": true, "REFUNDED": true}

func (o ListOptions) Validate(viewer auth.User) error {
	if o.CampusID != viewer.CampusID {
		return fault.New("FORBIDDEN", 403)
	}
	if o.Size < 1 || o.Size > 100 || o.Page < 0 || o.Page > 1000 || o.Before < 0 {
		return fault.New("INVALID_REQUEST", 400)
	}
	if o.Role != "" && o.Role != "PUBLISHED_BY_ME" && o.Role != "GRABBED_BY_ME" {
		return fault.New("INVALID_REQUEST", 400)
	}
	if o.Role == "" && !statuses[o.Status] {
		return fault.New("INVALID_REQUEST", 400)
	}
	if o.Before > 0 && o.Page != 0 {
		return fault.New("INVALID_REQUEST", 400)
	}
	if (o.Before > 0) != !o.BeforeTime.IsZero() {
		return fault.New("INVALID_REQUEST", 400)
	}
	return nil
}
func decorate(c *Card, viewer auth.User) {
	c.AvailableActions = []string{}
	if c.CampusID == viewer.CampusID && (c.Status == "PUBLISHED" || (c.Status == "LOCKED" && (c.GrabberID == nil || *c.GrabberID != viewer.ID))) && c.PublisherID != viewer.ID && (viewer.Role == "STUDENT" || viewer.Role == "ARBITRATOR") {
		c.AvailableActions = append(c.AvailableActions, "GRAB")
	}
	if c.CampusID == viewer.CampusID && c.Status == "LOCKED" && c.GrabberID != nil && *c.GrabberID == viewer.ID {
		c.AvailableActions = append(c.AvailableActions, "CONFIRM")
	}
	if c.CampusID == viewer.CampusID {
		runner := c.GrabberID != nil && *c.GrabberID == viewer.ID
		if runner && c.Status == "ACCEPTED" {
			c.AvailableActions = append(c.AvailableActions, "PICK_UP")
		}
		if runner && c.Status == "PICKED_UP" {
			c.AvailableActions = append(c.AvailableActions, "DELIVER")
		}
		if (runner || c.PublisherID == viewer.ID) && (c.Status == "ACCEPTED" || c.Status == "PICKED_UP" || c.Status == "DELIVERED") {
			c.AvailableActions = append(c.AvailableActions, "DISPUTE")
		}
		if c.PublisherID == viewer.ID {
			if c.Status == "DELIVERED" {
				c.AvailableActions = append(c.AvailableActions, "SETTLE")
			}
			if c.Status == "DRAFT" {
				c.AvailableActions = append(c.AvailableActions, "CANCEL_DRAFT")
			}
			if c.Status == "PUBLISHED" {
				c.AvailableActions = append(c.AvailableActions, "CANCEL")
			}
		}
		if viewer.Role == "ARBITRATOR" && c.Status == "DISPUTED" {
			c.AvailableActions = append(c.AvailableActions, "ARBITRATE_RUNNER", "ARBITRATE_PUBLISHER")
		}
	}
	c.Role = "VIEWER"
	if c.GrabberID != nil && *c.GrabberID == viewer.ID {
		c.Role = "CURRENT_RUNNER"
	}
	if c.PublisherID == viewer.ID {
		c.Role = "PUBLISHER"
	}
	if viewer.Role == "ARBITRATOR" {
		c.Role = "ARBITRATOR"
	}
}
func Visible(c Card, viewer auth.User) bool {
	return c.CampusID == viewer.CampusID && (c.Status != "DRAFT" || c.PublisherID == viewer.ID || viewer.Role == "ARBITRATOR")
}

type scanner interface{ Scan(...any) error }

func scan(row scanner) (Card, error) {
	var c Card
	err := row.Scan(&c.ID, &c.CampusID, &c.PublisherID, &c.GrabberID, &c.Type, &c.Title, &c.RewardCents, &c.SlotTotal, &c.SlotTaken, &c.Status, &c.Round, &c.Version, &c.CreatedAt, &c.ConfirmDeadline, &c.DeliveredAt)
	return c, err
}
func (q Query) List(ctx context.Context, viewer auth.User, o ListOptions) (Page, error) {
	page := Page{Items: []Card{}}
	if err := o.Validate(viewer); err != nil {
		return page, err
	}
	conditions := []string{"campus_id=?", "(status<>'DRAFT' OR publisher_id=? OR ?='ARBITRATOR')"}
	args := []any{viewer.CampusID, viewer.ID, viewer.Role}
	switch o.Role {
	case "PUBLISHED_BY_ME":
		conditions = append(conditions, "publisher_id=?")
		args = append(args, viewer.ID)
	case "GRABBED_BY_ME":
		conditions = append(conditions, "EXISTS (SELECT 1 FROM grab_record g WHERE g.errand_id=errand.id AND g.campus_id=errand.campus_id AND g.runner_id=?)")
		args = append(args, viewer.ID)
	default:
		conditions = append(conditions, "status=?")
		args = append(args, o.Status)
	}
	if o.Before > 0 {
		conditions = append(conditions, "(created_at<? OR (created_at=? AND id<?))")
		args = append(args, o.BeforeTime, o.BeforeTime, o.Before)
	}
	args = append(args, o.Size+1, o.Page*o.Size)
	rows, err := q.DB.QueryContext(ctx, "SELECT "+columns+" FROM errand WHERE "+strings.Join(conditions, " AND ")+" ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?", args...)
	if err != nil {
		return page, fault.Dependency(err)
	}
	defer rows.Close()
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return page, fault.Dependency(err)
		}
		decorate(&c, viewer)
		page.Items = append(page.Items, c)
	}
	if err := rows.Err(); err != nil {
		return page, fault.Dependency(err)
	}
	if len(page.Items) > o.Size {
		page.Items = page.Items[:o.Size]
		page.NextCursor = cursor(page.Items[o.Size-1])
	}
	return page, nil
}
func (q Query) Detail(ctx context.Context, viewer auth.User, id int64) (Card, error) {
	if q.Cache != nil {
		return q.cachedDetail(ctx, viewer, id)
	}
	c, err := scan(q.DB.QueryRowContext(ctx, "SELECT "+columns+" FROM errand WHERE id=? AND campus_id=?", id, viewer.CampusID))
	if errors.Is(err, sql.ErrNoRows) {
		return c, fault.New("ERRAND_NOT_FOUND", 404)
	}
	if err != nil {
		return c, fault.Dependency(err)
	}
	if !Visible(c, viewer) {
		return Card{}, fault.New("FORBIDDEN", 403)
	}
	decorate(&c, viewer)
	return c, nil
}

type StatusChange struct {
	Time       time.Time `json:"time"`
	From       string    `json:"from"`
	To         string    `json:"to"`
	Round      int       `json:"round"`
	OperatorID int64     `json:"operatorId,string"`
}

func (q Query) Timeline(ctx context.Context, viewer auth.User, id int64) ([]StatusChange, error) {
	if _, err := q.Detail(ctx, viewer, id); err != nil {
		return nil, err
	}
	rows, err := q.DB.QueryContext(ctx, "SELECT created_at,from_status,to_status,round,operator_id FROM errand_status_log WHERE campus_id=? AND errand_id=? ORDER BY created_at,id", viewer.CampusID, id)
	if err != nil {
		return nil, fault.Dependency(err)
	}
	defer rows.Close()
	items := []StatusChange{}
	for rows.Next() {
		var c StatusChange
		if err := rows.Scan(&c.Time, &c.From, &c.To, &c.Round, &c.OperatorID); err != nil {
			return nil, fault.Dependency(err)
		}
		items = append(items, c)
	}
	return items, fault.Dependency(rows.Err())
}
