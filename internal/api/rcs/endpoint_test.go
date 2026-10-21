package rcs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/zagnu-labs/rcs-messenger/internal/providers/rcsdirect"
	"github.com/zagnu-labs/rcs-messenger/internal/rcs"
)

type envelope struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`
}

func router(t *testing.T, cfg rcs.Config, sender http.Handler) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	srv := httptest.NewServer(sender)
	t.Cleanup(srv.Close)

	svc := rcs.NewService(rcsdirect.New(srv.URL), cfg)
	e := NewEndpoint(svc)

	r := gin.New()
	g := r.Group("/internal/rcs")
	g.POST("/send", e.Send)
	g.POST("/check", e.Check)
	g.GET("/health", e.Health)
	g.POST("/inbound", e.Inbound)
	g.POST("/nudge", e.Nudge)
	return r
}

func TestNudgeRouteNeedsThePhoneAgent(t *testing.T) {
	r := router(t, rcs.Config{Enabled: true}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the http sender cannot nudge and must not be called")
	}))

	status, _ := post(t, r, "/internal/rcs/nudge", `{"to":"+15551234567","action":"typing"}`)
	if status != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", status)
	}

	status, env := post(t, r, "/internal/rcs/nudge", `{"to":"+15551234567","action":"reboot"}`)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", status)
	}
	if !strings.Contains(env.Error, "reboot") {
		t.Errorf("error should name the bad action, got %q", env.Error)
	}
}

func post(t *testing.T, r *gin.Engine, path, body string) (int, envelope) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var env envelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("response is not the standard envelope: %s", w.Body.String())
	}
	return w.Code, env
}

func TestSendRoute(t *testing.T) {
	r := router(t, rcs.Config{Enabled: true},
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(rcsdirect.SendResult{
				Sent: true, MessageID: "m1", MessageType: 4, FinalStatus: "OUTGOING_DELIVERED",
			})
		}))

	status, env := post(t, r, "/internal/rcs/send", `{"to":"+15551234567","message":"hi"}`)
	if status != http.StatusOK || !env.Success {
		t.Fatalf("status %d env %+v", status, env)
	}

	var res rcs.Result
	if err := json.Unmarshal(env.Data, &res); err != nil {
		t.Fatalf("data: %v", err)
	}
	if !res.Sent || res.Transport != "RCS" {
		t.Errorf("want sent over RCS, got %+v", res)
	}
}

func TestSendRouteRefusalIs409(t *testing.T) {
	r := router(t, rcs.Config{Enabled: true},
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{"refused": true, "reason": "not_rcs"})
		}))

	status, env := post(t, r, "/internal/rcs/send", `{"to":"+15551234567","message":"hi"}`)
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409", status)
	}
	if env.Success {
		t.Error("a refusal is not a success")
	}
	if !strings.Contains(env.Error, "not_rcs") {
		t.Errorf("error should name the reason, got %q", env.Error)
	}
}

func TestSendRouteRejectsBadInput(t *testing.T) {
	r := router(t, rcs.Config{Enabled: true}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the sender must not be called for bad input")
	}))

	for _, body := range []string{
		`{"to":"5551234567","message":"hi"}`,
		`{"to":"+15551234567","message":""}`,
		`not json`,
	} {
		status, _ := post(t, r, "/internal/rcs/send", body)
		if status != http.StatusBadRequest {
			t.Errorf("body %q gave %d, want 400", body, status)
		}
	}
}

func TestSendRouteDisabledIs503(t *testing.T) {
	r := router(t, rcs.Config{Enabled: false}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the sender must not be called while disabled")
	}))

	status, _ := post(t, r, "/internal/rcs/send", `{"to":"+15551234567","message":"hi"}`)
	if status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", status)
	}
}

func TestSendRouteDryRun(t *testing.T) {
	r := router(t, rcs.Config{Enabled: true, DryRun: true},
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Error("dry run must not reach the sender")
		}))

	status, env := post(t, r, "/internal/rcs/send", `{"to":"+15551234567","message":"hi"}`)
	if status != http.StatusOK || !env.Success {
		t.Fatalf("status %d env %+v", status, env)
	}
	if !strings.Contains(env.Message, "dry run") {
		t.Errorf("message = %q", env.Message)
	}
}

func TestHealthRoute(t *testing.T) {
	r := router(t, rcs.Config{Enabled: true},
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(rcsdirect.Health{
				LoggedIn: true, Connected: true, PhoneResponding: true, SessionActive: true,
				SIMs: []rcsdirect.SIM{{Carrier: "AT&T", RCSEnabled: true}},
			})
		}))

	req := httptest.NewRequest(http.MethodGet, "/internal/rcs/health", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var env envelope
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if w.Code != http.StatusOK || !env.Success {
		t.Fatalf("status %d env %+v", w.Code, env)
	}
	var data struct {
		Ready bool `json:"ready"`
	}
	_ = json.Unmarshal(env.Data, &data)
	if !data.Ready {
		t.Error("a connected phone with an RCS SIM should report ready")
	}
}

func TestInboundRouteRejectsABadNumber(t *testing.T) {
	r := router(t, rcs.Config{Enabled: true}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the sender must not be called for an inbound message")
	}))

	status, _ := post(t, r, "/internal/rcs/inbound", `{"from":"5551234567","text":"STOP"}`)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", status)
	}
}

func TestInboundRouteAcceptsAnOrdinaryReply(t *testing.T) {
	r := router(t, rcs.Config{Enabled: true}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the sender must not be called for an inbound message")
	}))

	status, env := post(t, r, "/internal/rcs/inbound", `{"from":"+15551234567","text":"thanks!"}`)
	if status != http.StatusOK || !env.Success {
		t.Fatalf("status %d env %+v", status, env)
	}

	var res rcs.InboundResult
	if err := json.Unmarshal(env.Data, &res); err != nil {
		t.Fatalf("data: %v", err)
	}
	if res.OptOut || res.Blocked {
		t.Errorf("an ordinary reply must not opt anyone out, got %+v", res)
	}
}
