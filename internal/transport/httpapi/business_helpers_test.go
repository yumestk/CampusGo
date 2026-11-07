package httpapi

import (
	"github.com/gin-gonic/gin"
	errand "github.com/yumestk/CampusGo/internal/adapter/mysql"
	"github.com/yumestk/CampusGo/internal/application"
	"github.com/yumestk/CampusGo/internal/auth"
)

func registerTestBusiness(router *gin.Engine, s *auth.Service, q errand.Query, p *errand.PublishStore, stores ...*errand.TaskStore) {
	services := BusinessServices{Publisher: &application.Publisher{Store: p}}
	if len(stores) > 0 && stores[0] != nil {
		g := stores[0]
		services.Grab = &application.Grab{Store: g, Projection: g, Admission: g}
		services.Lifecycle = &application.Lifecycle{Store: g}
		services.Credit = g
		services.Funds = &application.Funds{Store: &errand.FundStore{DB: q.DB, IDs: g.IDs}}
	}
	RegisterBusiness(router, s, q, services)
}
