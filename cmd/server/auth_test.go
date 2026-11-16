package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestOpenBindNeedsAToken(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080"} {
		if err := checkBind(addr, ""); err != nil {
			t.Errorf("%s is loopback and should start: %v", addr, err)
		}
	}
	for _, addr := range []string{"0.0.0.0:8080", ":8080", "10.0.0.5:8080", "[::]:8080"} {
		if err := checkBind(addr, ""); err == nil {
			t.Errorf("%s without a token must refuse to start", addr)
		}
		if err := checkBind(addr, "secret"); err != nil {
			t.Errorf("%s with a token should start: %v", addr, err)
		}
	}
}

func TestTokenGuardsEveryRouteButHealth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	g := e.Group("/internal/rcs")
	g.Use(requireToken("s3cret"))
	ok := func(c *gin.Context) { c.String(200, "ok") }
	g.GET("/health", ok)
	g.POST("/send", ok)
	do := func(method, path, auth string) int {
		req := httptest.NewRequest(method, path, nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		e.ServeHTTP(w, req)
		return w.Code
	}
	if c := do(http.MethodPost, "/internal/rcs/send", ""); c != 401 {
		t.Errorf("no token: %d", c)
	}
	if c := do(http.MethodPost, "/internal/rcs/send", "Bearer wrong"); c != 401 {
		t.Errorf("wrong token: %d", c)
	}
	if c := do(http.MethodPost, "/internal/rcs/send", "Bearer s3cret"); c != 200 {
		t.Errorf("right token: %d", c)
	}
	if c := do(http.MethodGet, "/internal/rcs/health", ""); c != 200 {
		t.Errorf("health stays open: %d", c)
	}
}
