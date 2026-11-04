package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOnlyLivenessIsExposed(t *testing.T) {
	router := NewRouter()
	for path, status := range map[string]int{"/livez": http.StatusOK, "/readyz": http.StatusNotFound, "/api/errands": http.StatusNotFound} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != status {
			t.Errorf("%s returned %d, want %d", path, response.Code, status)
		}
	}
}
