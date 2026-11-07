package httpapi

import (
	"github.com/gin-gonic/gin"

	mysqlstore "github.com/yumestk/CampusGo/internal/adapter/mysql"
	"github.com/yumestk/CampusGo/internal/application"
	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/platform/fault"
)

func registerGrab(api *gin.RouterGroup, g *application.Grab, lifecycle *application.Lifecycle) {
	api.POST("/errands/:id/confirm", func(c *gin.Context) {
		id, err := positiveID(c.Param("id"))
		if err != nil {
			fail(c, err)
			return
		}
		var body struct {
			ExpectedRound *int `json:"expectedRound"`
		}
		if err = decode(c, &body); err != nil {
			fail(c, err)
			return
		}
		if body.ExpectedRound == nil {
			fail(c, fault.New("INVALID_REQUEST", 400))
			return
		}
		card, err := lifecycle.Confirm(c.Request.Context(), c.MustGet("user").(auth.User), id, *body.ExpectedRound)
		if err != nil {
			fail(c, err)
			return
		}
		ok(c, card)
	})
	api.POST("/errands/:id/grab", func(c *gin.Context) {
		id, err := positiveID(c.Param("id"))
		if err != nil {
			fail(c, err)
			return
		}
		var body struct {
			ExpectedRound *int `json:"expectedRound"`
		}
		if err = decode(c, &body); err != nil {
			fail(c, err)
			return
		}
		keys := c.Request.Header.Values("Idempotency-Key")
		if len(keys) != 1 || body.ExpectedRound == nil {
			fail(c, fault.New("INVALID_REQUEST", 400))
			return
		}
		r, err := g.Grab(c.Request.Context(), c.MustGet("user").(auth.User), keys[0], mysqlstore.GrabCommand{ErrandID: id, ExpectedRound: *body.ExpectedRound})
		if err != nil {
			fail(c, err)
			return
		}
		grabResponse(c, r)
	})
	api.GET("/errands/:id/grab-requests/:key", func(c *gin.Context) {
		id, err := positiveID(c.Param("id"))
		if err != nil {
			fail(c, err)
			return
		}
		r, err := g.Result(c.Request.Context(), c.MustGet("user").(auth.User), c.Param("key"), id)
		if err != nil {
			fail(c, err)
			return
		}
		grabResponse(c, r)
	})
}
func grabResponse(c *gin.Context, r mysqlstore.GrabResult) {
	if r.State == "PROCESSING" || r.State == "NOT_OBSERVED" {
		c.Header("Retry-After", "1")
		c.JSON(202, result{"OK", "结果处理中", r})
		return
	}
	if r.State == "REJECTED" || r.State == "EXPIRED" {
		c.JSON(409, result{r.ErrorCode, "本次抢单未成功", r})
		return
	}
	ok(c, r)
}
