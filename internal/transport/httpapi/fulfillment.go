package httpapi

import (
	"github.com/gin-gonic/gin"
	errand "github.com/yumestk/CampusGo/internal/adapter/mysql"
	"github.com/yumestk/CampusGo/internal/application"
	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/platform/fault"
	"strconv"
)

func registerFulfillment(api *gin.RouterGroup, g *application.Lifecycle, f *application.Funds, credit *errand.CreditQuery) {
	api.POST("/errands/:id/actions", func(c *gin.Context) {
		id, err := positiveID(c.Param("id"))
		if err != nil {
			fail(c, err)
			return
		}
		var b struct {
			Action string `json:"action"`
			Round  *int   `json:"expectedRound"`
		}
		if err = decode(c, &b); err != nil {
			fail(c, err)
			return
		}
		if b.Round == nil {
			fail(c, fault.New("INVALID_REQUEST", 400))
			return
		}
		u := c.MustGet("user").(auth.User)
		switch b.Action {
		case "PICK_UP", "DELIVER", "DISPUTE", "CANCEL_DRAFT":
			r, e := g.Fulfill(c.Request.Context(), u, id, *b.Round, b.Action)
			if e != nil {
				fail(c, e)
				return
			}
			ok(c, r)
		case "SETTLE", "CANCEL", "ARBITRATE_RUNNER", "ARBITRATE_PUBLISHER":
			keys := c.Request.Header.Values("Idempotency-Key")
			if len(keys) != 1 {
				fail(c, fault.New("INVALID_REQUEST", 400))
				return
			}
			r, e := f.Execute(c.Request.Context(), u, keys[0], errand.FundCommand{ErrandID: id, Round: *b.Round, Action: b.Action})
			if e != nil {
				fail(c, e)
				return
			}
			ok(c, r)
		default:
			fail(c, fault.New("INVALID_REQUEST", 400))
		}
	})
	api.GET("/fund-requests/:key", func(c *gin.Context) {
		r, e := f.Result(c.Request.Context(), c.MustGet("user").(auth.User), c.Param("key"))
		if e != nil {
			fail(c, e)
			return
		}
		ok(c, r)
	})
	self := func(c *gin.Context) {
		p, err := credit.CreditSelf(c.Request.Context(), c.MustGet("user").(auth.User))
		if err != nil {
			fail(c, err)
			return
		}
		ok(c, p)
	}
	api.GET("/credit", self)
	api.GET("/credit/self", self)
	api.GET("/credit/ranking", func(c *gin.Context) {
		u := c.MustGet("user").(auth.User)
		campus, err := positiveID(c.DefaultQuery("campusId", strconv.FormatInt(u.CampusID, 10)))
		limit, e := strconv.Atoi(c.DefaultQuery("limit", "20"))
		if err != nil || e != nil {
			fail(c, fault.New("INVALID_REQUEST", 400))
			return
		}
		p, err := credit.CreditRanking(c.Request.Context(), u, campus, limit)
		if err != nil {
			fail(c, err)
			return
		}
		ok(c, p)
	})
}
