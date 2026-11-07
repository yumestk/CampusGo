package httpapi

import (
	"database/sql"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/yumestk/CampusGo/internal/application"
	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/notify"
	"github.com/yumestk/CampusGo/internal/platform/fault"
)

func registerNotifications(api *gin.RouterGroup, db *sql.DB, service *application.Notifications) {
	api.GET("/notifications", func(c *gin.Context) {
		size, err := strconv.Atoi(c.DefaultQuery("size", "20"))
		if err != nil {
			fail(c, fault.New("INVALID_REQUEST", 400))
			return
		}
		p, err := notify.List(c.Request.Context(), db, c.MustGet("user").(auth.User).ID, c.Query("cursor"), size)
		if err != nil {
			fail(c, err)
			return
		}
		ok(c, p)
	})
	api.GET("/notifications/unread", func(c *gin.Context) {
		n, err := notify.Unread(c.Request.Context(), db, c.MustGet("user").(auth.User).ID)
		if err != nil {
			fail(c, err)
			return
		}
		ok(c, gin.H{"count": n})
	})
	api.POST("/notifications/:id/read", func(c *gin.Context) {
		id, err := positiveID(c.Param("id"))
		if err == nil {
			err = service.MarkRead(c.Request.Context(), c.MustGet("user").(auth.User), id)
		}
		if err != nil {
			fail(c, err)
			return
		}
		ok(c, nil)
	})
}
