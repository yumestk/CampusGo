package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/yumestk/CampusGo/internal/platform/telemetry"
)

func TestPanicIsContainedAndRequestIDIsPreserved(t *testing.T) {
	router := NewRouter(slog.New(slog.NewJSONHandler(io.Discard, nil)), telemetry.NewRegistry("api")).(*gin.Engine)
	router.GET("/panic", func(c *gin.Context) { panic("probe") })
	id := uuid.NewString()
	request := httptest.NewRequest(http.MethodGet, "/panic", nil)
	request.Header.Set("X-Request-Id", id)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || response.Header().Get("X-Request-Id") != id {
		t.Fatalf("panic response status=%d request_id=%s", response.Code, response.Header().Get("X-Request-Id"))
	}
}

func TestInvalidIDsAndUnknownPathsDoNotExpandMetricLabels(t *testing.T) {
	registry := telemetry.NewRegistry("api")
	router := NewRouter(slog.New(slog.NewJSONHandler(io.Discard, nil)), registry)
	for _, path := range []string{"/unknown/one", "/unknown/two"} {
		request := httptest.NewRequest("UNRECOGNIZED", path, nil)
		request.Header.Set("X-Request-Id", "invalid")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if _, err := uuid.Parse(response.Header().Get("X-Request-Id")); err != nil {
			t.Fatal(err)
		}
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "campusgo_http_requests_total" {
			continue
		}
		if len(family.Metric) != 1 || family.Metric[0].GetCounter().GetValue() != 2 {
			t.Fatal("unknown paths created distinct metric series")
		}
		for _, label := range family.Metric[0].Label {
			if label.GetName() == "route" && label.GetValue() != "unmatched" {
				t.Fatal("raw path used as label")
			}
			if label.GetName() == "method" && label.GetValue() != "OTHER" {
				t.Fatal("raw method used as label")
			}
		}
		return
	}
	t.Fatal("HTTP counter was not recorded")
}
