package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/yumestk/CampusGo/internal/platform/server"
)

func NewRouter() http.Handler {
	router := gin.New()
	_ = router.SetTrustedProxies(nil)
	router.Use(gin.Recovery())
	router.GET("/livez", gin.WrapF(server.Liveness("api")))
	return router
}
