package application

import (
	"context"
	"github.com/yumestk/CampusGo/internal/contracts"
	fault "github.com/yumestk/CampusGo/internal/failure"
)

type NoticeReadStore interface {
	MarkNoticeRead(context.Context, int64, int64) error
}
type Notifications struct{ Store NoticeReadStore }

func (n *Notifications) MarkRead(ctx context.Context, actor contracts.Actor, id int64) error {
	if id <= 0 {
		return fault.New("INVALID_REQUEST", 400)
	}
	return n.Store.MarkNoticeRead(ctx, actor.ID, id)
}
