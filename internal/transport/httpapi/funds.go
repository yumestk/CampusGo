package httpapi

import (
	"database/sql"
	"strconv"

	"github.com/gin-gonic/gin"

	mysqlstore "github.com/yumestk/CampusGo/internal/adapter/mysql"
	"github.com/yumestk/CampusGo/internal/application"
	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/platform/fault"
	"github.com/yumestk/CampusGo/internal/transport/wire"
	"github.com/yumestk/CampusGo/internal/wallet"
)

func registerFunds(api *gin.RouterGroup, p *application.Publisher, db *sql.DB) {
	q := wallet.Query{DB: db}
	api.POST("/errands", func(c *gin.Context) {
		u := c.MustGet("user").(auth.User)
		var b struct {
			CampusID    *wire.Integer `json:"campusId"`
			Type        string        `json:"type"`
			Title       string        `json:"title"`
			RewardCents wire.Integer  `json:"rewardCents"`
			SlotTotal   *int          `json:"slotTotal"`
		}
		if err := decode(c, &b); err != nil {
			fail(c, err)
			return
		}
		campus, slots := u.CampusID, 1
		if b.CampusID != nil {
			campus = int64(*b.CampusID)
		}
		if b.SlotTotal != nil {
			slots = *b.SlotTotal
		}
		if b.Type == "" {
			b.Type = "DELIVERY"
		}
		keys := c.Request.Header.Values("Idempotency-Key")
		if len(keys) != 1 {
			fail(c, fault.New("INVALID_REQUEST", 400))
			return
		}
		r, err := p.Publish(c.Request.Context(), u, keys[0], mysqlstore.PublishCommand{CampusID: campus, Type: b.Type, Title: b.Title, RewardCents: int64(b.RewardCents), SlotTotal: slots})
		if err != nil {
			fail(c, err)
			return
		}
		ok(c, r)
	})
	api.GET("/publish-requests/:key", func(c *gin.Context) {
		r, err := p.Result(c.Request.Context(), c.MustGet("user").(auth.User), c.Param("key"))
		if err != nil {
			fail(c, err)
			return
		}
		ok(c, r)
	})
	api.GET("/wallet", func(c *gin.Context) {
		r, err := q.Balance(c.Request.Context(), c.MustGet("user").(auth.User))
		if err != nil {
			fail(c, err)
			return
		}
		ok(c, r)
	})
	api.GET("/wallet/ledger", func(c *gin.Context) {
		var before int64
		size := 20
		var err error
		if value := c.Query("cursor"); value != "" {
			before, err = strconv.ParseInt(value, 10, 64)
			if err != nil || before <= 0 {
				fail(c, fault.New("INVALID_REQUEST", 400))
				return
			}
		}
		if value, exists := c.GetQuery("size"); exists {
			size, err = strconv.Atoi(value)
			if err != nil {
				fail(c, fault.New("INVALID_REQUEST", 400))
				return
			}
		}
		r, err := q.Ledger(c.Request.Context(), c.MustGet("user").(auth.User), before, size)
		if err != nil {
			fail(c, err)
			return
		}
		ok(c, r)
	})
	api.GET("/errands/:id/escrow", func(c *gin.Context) {
		id, err := positiveID(c.Param("id"))
		if err != nil {
			fail(c, err)
			return
		}
		r, err := q.Escrow(c.Request.Context(), c.MustGet("user").(auth.User), id)
		if err != nil {
			fail(c, err)
			return
		}
		ok(c, r)
	})
}
