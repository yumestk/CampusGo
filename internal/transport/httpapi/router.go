package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/yumestk/CampusGo/internal/platform/fault"
	"github.com/yumestk/CampusGo/internal/platform/server"
)

func NewRouter(logger *slog.Logger, registry *prometheus.Registry) *gin.Engine {
	requests := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "campusgo_http_requests_total", Help: "Completed API requests."}, []string{"method", "route", "status"})
	duration := prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "campusgo_http_request_duration_seconds", Help: "API request duration."}, []string{"method", "route"})
	registry.MustRegister(requests, duration)
	router := gin.New()
	_ = router.SetTrustedProxies(nil)
	router.Use(func(c *gin.Context) {
		id, err := uuid.Parse(c.GetHeader("X-Request-Id"))
		if err != nil {
			id = uuid.New()
		}
		requestID := id.String()
		c.Set("request_id", requestID)
		c.Header("X-Request-Id", requestID)
		start := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		method := c.Request.Method
		switch method {
		case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		default:
			method = "OTHER"
		}
		requests.WithLabelValues(method, route, strconv.Itoa(c.Writer.Status())).Inc()
		duration.WithLabelValues(method, route).Observe(time.Since(start).Seconds())
		logger.InfoContext(c.Request.Context(), "http request", "request_id", requestID, "method", method, "route", route, "status", c.Writer.Status(), "elapsed", time.Since(start).String())
	}, gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, recovered any) {
		logger.ErrorContext(c.Request.Context(), "request panic", "request_id", c.GetString("request_id"), "stack", string(debug.Stack()))
		fail(c, fault.New("INTERNAL_ERROR", http.StatusInternalServerError))
	}))
	router.HandleMethodNotAllowed = true
	router.NoRoute(func(c *gin.Context) { fail(c, fault.New("NOT_FOUND", 404)) })
	router.NoMethod(func(c *gin.Context) { fail(c, fault.New("METHOD_NOT_ALLOWED", 405)) })
	router.GET("/livez", gin.WrapF(server.Liveness("api")))
	router.GET("/metrics", gin.WrapH(promhttp.HandlerFor(registry, promhttp.HandlerOpts{})))
	return router
}
