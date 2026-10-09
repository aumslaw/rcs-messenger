package rcsdirect

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type permanent interface{ Permanent() bool }

func TestSendSuccess(t *testing.T) {
	var got struct {
		To          string `json:"to"`
		Message     string `json:"message"`
		RequireE2EE *bool  `json:"requireE2ee"`
		WaitSeconds int    `json:"waitSeconds"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/send" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(SendResult{
			Sent: true, MessageID: "m1", FinalStatus: "OUTGOING_DELIVERED", MessageType: 4,
		})
	}))
	defer srv.Close()

	yes := true
	res, err := New(srv.URL).Send(context.Background(), "+15551234567", "hi", SendOptions{RequireE2EE: &yes})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if !res.DeliveredAsRCS() || res.Transport() != "RCS" {
		t.Errorf("want delivered as RCS, got sent=%v type=%d", res.Sent, res.MessageType)
	}
	if got.To != "+15551234567" || got.Message != "hi" {
		t.Errorf("payload not forwarded: %+v", got)
	}
	if got.RequireE2EE == nil || !*got.RequireE2EE {
		t.Error("requireE2ee should be forwarded as true")
	}
}

func TestSendSMSFallbackIsVisible(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(SendResult{Sent: true, MessageType: 1})
	}))
	defer srv.Close()

	res, err := New(srv.URL).Send(context.Background(), "+15551234567", "hi", SendOptions{})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if res.DeliveredAsRCS() {
		t.Error("type 1 is SMS and must not report as RCS")
	}
	if res.Transport() != "SMS" {
		t.Errorf("transport = %q, want SMS", res.Transport())
	}
}

func TestSendRefusedIsTyped(t *testing.T) {
	cases := []struct {
		reason    string
		permanent bool
	}{
		{"not_rcs", true},
		{"not_e2ee", true},
		{"rcs_disabled_on_sim", true},
		{"conversation_read_only", true},
		{"phone_not_responding", false},
		{"session_not_active", false},
		{"e2ee_pending", false},
		{"rcs_pending", false},
	}

	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"sent": false, "refused": true, "reason": tc.reason,
					"check": map[string]any{"to": "+15551234567", "eligible": false},
				})
			}))
			defer srv.Close()

			_, err := New(srv.URL).Send(context.Background(), "+15551234567", "hi", SendOptions{})
			var refused *RefusedError
			if !errors.As(err, &refused) {
				t.Fatalf("want *RefusedError, got %v", err)
			}
			if refused.Reason != tc.reason {
				t.Errorf("reason = %q, want %q", refused.Reason, tc.reason)
			}
			if refused.Permanent() != tc.permanent {
				t.Errorf("Permanent() = %v, want %v", refused.Permanent(), tc.permanent)
			}
			if refused.Check == nil || refused.Check.To != "+15551234567" {
				t.Error("refusal should carry the check that caused it")
			}
		})
	}
}

func TestBadRequestIsPermanent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "message is required"})
	}))
	defer srv.Close()

	_, err := New(srv.URL).Send(context.Background(), "+15551234567", "", SendOptions{})
	var p permanent
	if !errors.As(err, &p) || !p.Permanent() {
		t.Fatalf("400 should be permanent, got %v", err)
	}
}

func TestServerErrorIsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	_, err := New(srv.URL).Send(context.Background(), "+15551234567", "hi", SendOptions{})
	if err == nil {
		t.Fatal("want an error")
	}
	var p permanent
	if errors.As(err, &p) && p.Permanent() {
		t.Error("a 502 from the sender should not be permanent")
	}
}

func TestNotConfigured(t *testing.T) {
	c := New("")
	if c.Enabled() {
		t.Error("a blank base URL is not enabled")
	}
	if _, err := c.Send(context.Background(), "+15551234567", "hi", SendOptions{}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("want ErrNotConfigured, got %v", err)
	}
}

func TestValidNudge(t *testing.T) {
	for _, a := range append(NudgeSteps, NudgeAll) {
		if !ValidNudge(a) {
			t.Errorf("%q is a nudge", a)
		}
	}
	for _, a := range []string{"", "reboot", "ALL", "get-conversation"} {
		if ValidNudge(a) {
			t.Errorf("%q is not a nudge", a)
		}
	}
}

func TestHealthReady(t *testing.T) {
	ready := &Health{
		LoggedIn: true, Connected: true, PhoneResponding: true, SessionActive: true,
		SIMs: []SIM{{RCSEnabled: true}},
	}
	if !ready.Ready() {
		t.Error("connected phone with an RCS SIM is ready")
	}
	cases := []*Health{
		nil,
		{Connected: true, PhoneResponding: true, SessionActive: true, SIMs: []SIM{{RCSEnabled: true}}},
		{LoggedIn: true, PhoneResponding: true, SessionActive: true, SIMs: []SIM{{RCSEnabled: true}}},
		{LoggedIn: true, Connected: true, SessionActive: true, SIMs: []SIM{{RCSEnabled: true}}},
		{LoggedIn: true, Connected: true, PhoneResponding: true, SIMs: []SIM{{RCSEnabled: true}}},
		{LoggedIn: true, Connected: true, PhoneResponding: true, SessionActive: true},
		{LoggedIn: true, Connected: true, PhoneResponding: true, SessionActive: true, SIMs: []SIM{{RCSEnabled: false}}},
	}
	for i, h := range cases {
		if h.Ready() {
			t.Errorf("case %d should not be ready", i)
		}
	}
}
