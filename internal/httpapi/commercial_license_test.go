package httpapi

import (
	"context"
	"errors"
	core "github.com/J-S-Te/license-core"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"testing"
)

type expiredCommercialGate struct{}

func (expiredCommercialGate) Check(_ context.Context, op core.Operation) error {
	if op == core.MUTATE_BUSINESS {
		return errors.New("expired")
	}
	return nil
}
func TestExpiredLicensePreservesHistoryAndRejectsUnknownGETAndWrites(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(commercialLicenseMiddleware(expiredCommercialGate{}))
	for _, p := range []string{"/api/v1/projects/:id", "/api/v1/capabilities/export", "/api/v1/new-computation", "/readyz"} {
		r.GET(p, func(c *gin.Context) { c.Status(204) })
	}
	r.POST("/api/v1/projects", func(c *gin.Context) { c.Status(204) })
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"GET", "/api/v1/projects/p1", 204}, {"GET", "/api/v1/capabilities/export", 204}, {"GET", "/readyz", 204}, {"GET", "/api/v1/new-computation", 403}, {"POST", "/api/v1/projects", 403}} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status {
			t.Fatalf("%s %s: %d", tc.method, tc.path, w.Code)
		}
	}
}
