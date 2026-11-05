package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/yumestk/CampusGo/internal/auth"
	"github.com/yumestk/CampusGo/internal/errand"
	"github.com/yumestk/CampusGo/internal/platform/fault"
	"github.com/yumestk/CampusGo/internal/transport/wire"
)

type result struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data"`
}

func ok(c *gin.Context, data any) { c.JSON(200, result{"OK", "成功", data}) }
func fail(c *gin.Context, err error) {
	var f *fault.Error
	if !errors.As(err, &f) {
		f = &fault.Error{Code: "INTERNAL_ERROR", Status: 500, Cause: err}
	}
	messages := map[string]string{"REFRESH_REUSED": "刷新凭证已被重复使用，请重新登录", "REFRESH_DISABLED": "当前模式无需刷新，请重新登录", "UNAUTHORIZED": "未登录或会话已过期", "FORBIDDEN": "没有访问权限", "IDENTITY_NOT_BOUND": "身份尚未绑定校园用户", "INVALID_REQUEST": "请求参数不合法", "DEPENDENCY_UNAVAILABLE": "依赖暂不可用，请稍后重试", "ERRAND_NOT_FOUND": "任务不存在", "INTERNAL_ERROR": "系统内部错误", "NOT_FOUND": "接口不存在", "METHOD_NOT_ALLOWED": "请求方法不支持", "OIDC_DISABLED": "未启用统一身份登录"}
	for code, message := range map[string]string{"FUND_RESULT_UNKNOWN": "资金操作结果待确认，请查询原请求结果", "FUND_EXPIRED": "该请求已过期且未执行，请刷新后重新操作", "FUNDS_INCONSISTENT": "资金事实异常，需要核查", "ACTION_RESULT_UNKNOWN": "操作结果待确认，请刷新任务进度", "NOT_DUE": "尚未到自动结算时间", "CONFIRM_RESULT_UNKNOWN": "确认结果暂不确定，请刷新进度或重试同一轮确认", "CONFIRM_DEADLINE_PASSED": "确认期限已过，请刷新任务进展", "NOT_CURRENT_GRABBER": "当前任务已不属于你", "STALE_VERSION": "任务轮次或状态已变化，请刷新", "TOO_MANY_ONGOING": "在途任务已达 5 单上限", "CREDIT_TOO_LOW": "信用分未达到要求", "INVALID_STATUS": "当前状态不能执行此操作", "ACCOUNT_NOT_FOUND": "钱包账户不存在", "ESCROW_NOT_FOUND": "托管记录不存在或无权查看", "INSUFFICIENT_BALANCE": "可用余额不足", "BALANCE_LIMIT": "账户金额或记账序号已达上限", "IDEMPOTENCY_CONFLICT": "请求号已用于不同的操作内容", "GRAB_RESULT_UNKNOWN": "抢单结果暂未确认，请查询结果或使用原请求号重试", "PUBLISH_RESULT_UNKNOWN": "发布结果暂未确认，请查询结果或使用原请求号重试"} {
		messages[code] = message
	}
	var data any
	if f.Status == 503 {
		c.Header("Retry-After", "1")
		data = gin.H{"retryAfterMillis": "1000"}
	}
	if f.Status >= 500 {
		slog.ErrorContext(c.Request.Context(), "request failed", "request_id", c.GetString("request_id"), "code", f.Code, "cause_type", fmt.Sprintf("%T", f.Cause))
	}
	c.AbortWithStatusJSON(f.Status, result{f.Code, messages[f.Code], data})
}
func decode(c *gin.Context, out any) error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	d := json.NewDecoder(c.Request.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return fault.New("INVALID_REQUEST", 400)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fault.New("INVALID_REQUEST", 400)
	}
	return nil
}
func bearer(c *gin.Context) string {
	values := c.Request.Header.Values("Authorization")
	if len(values) != 1 {
		return ""
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}
func RegisterBusiness(router *gin.Engine, s *auth.Service, q errand.Query, p *errand.Publisher, grabbers ...*errand.Grabber) {
	api := router.Group("/api")
	api.Use(func(c *gin.Context) {
		if s.Config.RequireGateway && (len(c.Request.Header.Values("X-CampusGo-Gateway")) != 1 || subtle.ConstantTimeCompare([]byte(c.GetHeader("X-CampusGo-Gateway")), []byte(s.Config.GatewaySecret)) != 1) {
			fail(c, fault.New("FORBIDDEN", 403))
			return
		}
		duration := 3 * time.Second
		if strings.HasPrefix(c.FullPath(), "/api/auth/oidc/") {
			duration = 15 * time.Second
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), duration)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Header("Cache-Control", "no-store")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Next()
	})
	api.GET("/auth/config", func(c *gin.Context) {
		callback, _ := url.Parse(s.Config.CallbackURL)
		origin := ""
		if callback != nil && callback.Host != "" {
			origin = callback.Scheme + "://" + callback.Host
		}
		ok(c, gin.H{"demoLoginEnabled": s.Config.Demo, "oidcEnabled": s.Config.Issuer != "", "oidcCallbackOrigin": origin, "authMode": s.Config.Mode})
	})
	api.POST("/auth/login", func(c *gin.Context) {
		var body struct {
			UserID wire.Integer `json:"userId"`
		}
		if err := decode(c, &body); err != nil {
			fail(c, err)
			return
		}
		session, err := s.DemoLogin(c.Request.Context(), int64(body.UserID))
		if err != nil {
			fail(c, err)
			return
		}
		ok(c, session)
	})
	api.POST("/auth/refresh", func(c *gin.Context) {
		var body struct {
			RefreshToken string `json:"refreshToken"`
		}
		if e := decode(c, &body); e != nil {
			fail(c, e)
			return
		}
		if len(c.Request.Header.Values("Idempotency-Key")) != 1 {
			fail(c, fault.New("INVALID_REQUEST", 400))
			return
		}
		out, e := s.Refresh(c.Request.Context(), body.RefreshToken, c.GetHeader("Idempotency-Key"))
		if e != nil {
			fail(c, e)
			return
		}
		ok(c, out)
	})
	api.POST("/auth/logout", func(c *gin.Context) {
		if err := s.Logout(c.Request.Context(), bearer(c)); err != nil {
			fail(c, err)
			return
		}
		ok(c, nil)
	})
	api.GET("/auth/oidc/start", func(c *gin.Context) {
		location, state, err := s.OIDCStart(c.Request.Context())
		if err != nil {
			fail(c, err)
			return
		}
		http.SetCookie(c.Writer, &http.Cookie{Name: "campusgo_oidc_state", Value: state, Path: "/api/auth/oidc", MaxAge: 300, HttpOnly: true, Secure: strings.HasPrefix(s.Config.CallbackURL, "https://"), SameSite: http.SameSiteLaxMode})
		c.Redirect(302, location)
	})
	api.GET("/auth/oidc/callback", func(c *gin.Context) {
		cookie, _ := c.Cookie("campusgo_oidc_state")
		http.SetCookie(c.Writer, &http.Cookie{Name: "campusgo_oidc_state", Value: "", Path: "/api/auth/oidc", MaxAge: -1, HttpOnly: true, Secure: strings.HasPrefix(s.Config.CallbackURL, "https://"), SameSite: http.SameSiteLaxMode})
		session, err := s.OIDCCallback(c.Request.Context(), c.Query("state"), cookie, c.Query("code"))
		if err != nil {
			fail(c, err)
			return
		}
		if strings.Contains(c.GetHeader("Accept"), "application/json") {
			ok(c, session)
			return
		}
		// Credentials never enter a URL or browser history. The opener and target origin are fixed.
		data, _ := json.Marshal(result{"OK", "成功", session})
		origin, _ := json.Marshal(s.Config.FrontendOrigin)
		nonce := rand.Text()
		c.Header("Content-Security-Policy", "default-src 'none'; script-src 'nonce-"+nonce+"'; frame-ancestors 'none'; base-uri 'none'")
		c.Header("Referrer-Policy", "no-referrer")
		c.Data(200, "text/html; charset=utf-8", []byte("<!doctype html><html lang=\"zh-CN\"><meta charset=\"utf-8\"><title>登录完成</title><p>登录完成，可以关闭此窗口。</p><script nonce=\""+nonce+"\">if(window.opener){window.opener.postMessage("+string(data)+","+string(origin)+");window.close()}</script></html>"))
	})
	protected := api.Group("")
	protected.Use(func(c *gin.Context) {
		u, err := s.Resolve(c.Request.Context(), bearer(c))
		if err != nil {
			fail(c, err)
			return
		}
		c.Set("user", u)
		c.Next()
	})
	protected.GET("/users/me", func(c *gin.Context) { ok(c, c.MustGet("user")) })
	registerFunds(protected, p, q.DB)
	if len(grabbers) > 0 && grabbers[0] != nil {
		registerGrab(protected, grabbers[0])
		f := p.Funds
		if f == nil {
			f = &errand.Funds{Grabber: grabbers[0]}
		}
		registerFulfillment(protected, grabbers[0], f)
	}
	list := func(c *gin.Context, mine bool) {
		u := c.MustGet("user").(auth.User)
		o := errand.ListOptions{CampusID: u.CampusID, Status: c.DefaultQuery("status", "PUBLISHED"), Size: 20}
		var err error
		if v, exists := c.GetQuery("campusId"); exists {
			o.CampusID, err = strconv.ParseInt(v, 10, 64)
			if err != nil {
				fail(c, fault.New("INVALID_REQUEST", 400))
				return
			}
		}
		for name, dest := range map[string]*int{"size": &o.Size, "page": &o.Page} {
			if v, exists := c.GetQuery(name); exists {
				*dest, err = strconv.Atoi(v)
				if err != nil {
					fail(c, fault.New("INVALID_REQUEST", 400))
					return
				}
			}
		}
		cursor, present := c.GetQuery("cursor")
		_, cursorPresent := c.Request.URL.Query()["cursor"]
		present = present || cursorPresent
		if cursor != "" {
			o.BeforeTime, o.Before, err = errand.ParseCursor(cursor)
			if err != nil {
				fail(c, fault.New("INVALID_REQUEST", 400))
				return
			}
		}
		if mine {
			o.Role = c.DefaultQuery("role", "PUBLISHED_BY_ME")
		}
		if present && o.Page != 0 {
			fail(c, fault.New("INVALID_REQUEST", 400))
			return
		}
		page, err := q.List(c.Request.Context(), u, o)
		if err != nil {
			fail(c, err)
			return
		}
		if present {
			ok(c, page)
		} else {
			c.Header("X-Next-Cursor", page.NextCursor)
			ok(c, page.Items)
		}
	}
	protected.GET("/errands", func(c *gin.Context) { list(c, false) })
	protected.GET("/errands/mine", func(c *gin.Context) { list(c, true) })
	protected.GET("/errands/:id", func(c *gin.Context) {
		id, err := positiveID(c.Param("id"))
		if err != nil {
			fail(c, err)
			return
		}
		card, err := q.Detail(c.Request.Context(), c.MustGet("user").(auth.User), id)
		if err != nil {
			fail(c, err)
			return
		}
		ok(c, card)
	})
	protected.GET("/errands/:id/timeline", func(c *gin.Context) {
		id, err := positiveID(c.Param("id"))
		if err != nil {
			fail(c, err)
			return
		}
		items, err := q.Timeline(c.Request.Context(), c.MustGet("user").(auth.User), id)
		if err != nil {
			fail(c, err)
			return
		}
		ok(c, items)
	})
}
func positiveID(value string) (int64, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, fault.New("INVALID_REQUEST", 400)
	}
	return id, nil
}
