package httpapi

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/errand"
	"github.com/yumestk/CampusGo/internal/platform/fault"
)

func registerFulfillment(api *gin.RouterGroup, g *errand.Grabber, f *errand.Funds) {
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
	api.GET("/credit", func(c *gin.Context) {
		u := c.MustGet("user").(auth.User)
		var score, version int64
		err := g.DB.QueryRowContext(c.Request.Context(), "SELECT COALESCE((SELECT score FROM credit_score WHERE user_id=?),60),COALESCE((SELECT version FROM credit_score WHERE user_id=?),0)", u.ID, u.ID).Scan(&score, &version)
		if err != nil {
			fail(c, fault.Dependency(err))
			return
		}
		ok(c, gin.H{"score": score, "version": fmt.Sprint(version)})
	})
}
