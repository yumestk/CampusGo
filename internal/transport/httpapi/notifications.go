package httpapi

import (
	"database/sql"
	"github.com/gin-gonic/gin"
	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/notify"
	"github.com/yumestk/CampusGo/internal/platform/fault"
	"strconv"
)

func registerNotifications(api *gin.RouterGroup, db *sql.DB) {
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
			err = notify.Read(c.Request.Context(), db, c.MustGet("user").(auth.User).ID, id)
		}
		if err != nil {
			fail(c, err)
			return
		}
		ok(c, nil)
	})
}
