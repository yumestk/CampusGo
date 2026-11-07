package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	mysqlstore "github.com/yumestk/CampusGo/internal/adapter/mysql"
	"github.com/yumestk/CampusGo/internal/application"
	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/notify"
)

func registerTestBusiness(router *gin.Engine, s *auth.Service, q mysqlstore.Query, p *mysqlstore.PublishStore, stores ...*mysqlstore.TaskStore) {
	services := BusinessServices{Notifications: &application.Notifications{Store: notify.ReadStore{DB: q.DB}}, Publisher: &application.Publisher{Store: p}}
	if len(stores) > 0 && stores[0] != nil {
		g := stores[0]
		services.Grab, _ = application.NewGrab(g, g, application.AdmissionFunc(func(ctx context.Context, user, task int64, query bool) (func(), error) {
			if g.Limits == nil {
				return func() {}, nil
			}
			return g.Admit(ctx, user, task, query)
		}))
		services.Lifecycle = &application.Lifecycle{Store: g}
		services.Credit = &mysqlstore.CreditQuery{DB: g.DB, Redis: g.Redis, Namespace: g.Namespace}
		services.Funds = &application.Funds{Store: &mysqlstore.FundStore{DB: q.DB, IDs: g.IDs}}
	}
	RegisterBusiness(router, s, q, services)
}
