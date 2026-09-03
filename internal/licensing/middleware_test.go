package licensing

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequireFeatureRejectsWithoutActivation(t *testing.T) {
	old := Global()
	defer func() { global.Lock(); global.manager = old; global.Unlock() }()
	m := &Manager{cfg: Config{}, integrityValid: true}
	global.Lock()
	global.manager = m
	global.Unlock()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequireFeature("core"))
	r.GET("/", func(c *gin.Context) { c.String(200, "ok") })
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	r.ServeHTTP(w, req)
	if w.Code != 402 {
		t.Fatalf("unexpected status %d", w.Code)
	}
}

func TestRequireFeatureRejectsWhenManagerMissing(t *testing.T) {
	old := Global()
	defer func() { global.Lock(); global.manager = old; global.Unlock() }()
	global.Lock()
	global.manager = nil
	global.Unlock()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequireFeature("core"))
	r.GET("/", func(c *gin.Context) { c.String(200, "ok") })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 402 {
		t.Fatalf("unexpected status %d", w.Code)
	}
}
