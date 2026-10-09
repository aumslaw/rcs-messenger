package rcs

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zagnu-labs/rcs-messenger/internal/providers/rcsdirect"
)

type recorder struct {
	sent []string
	err  error
}

func (r *recorder) Announce(_ context.Context, text string) error {
	r.sent = append(r.sent, text)
	return r.err
}

type healthSender struct {
	fakeSender
	health *rcsdirect.Health
	err    error
}

func (h *healthSender) Health(context.Context) (*rcsdirect.Health, error) {
	if h.err != nil {
		return nil, h.err
	}
	return h.health, nil
}

func okHealth() *rcsdirect.Health {
	return &rcsdirect.Health{
		LoggedIn: true, Connected: true, PhoneResponding: true, SessionActive: true,
		SIMs: []rcsdirect.SIM{{RCSEnabled: true}},
	}
}

func monitorFor(h *healthSender, alert Announcer) *Monitor {
	h.enabled = true
	return NewMonitor(NewService(h, Config{Enabled: true}), alert, 0)
}

func TestMonitorStaysQuietWhileHealthy(t *testing.T) {
	alert := &recorder{}
	m := monitorFor(&healthSender{health: okHealth()}, alert)

	m.check(context.Background())
	m.check(context.Background())
	m.check(context.Background())

	if len(alert.sent) != 0 {
		t.Fatalf("a healthy sender must not alert, got %v", alert.sent)
	}
}

func TestMonitorAlertsOnceOnFailureAndOnceOnRecovery(t *testing.T) {
	alert := &recorder{}
	sender := &healthSender{health: okHealth()}
	m := monitorFor(sender, alert)

	m.check(context.Background())

	sender.health.PhoneResponding = false
	m.check(context.Background())
	m.check(context.Background())

	if len(alert.sent) != 1 {
		t.Fatalf("want exactly one down alert, got %d: %v", len(alert.sent), alert.sent)
	}
	if !strings.Contains(alert.sent[0], "not responding") {
		t.Errorf("the alert should name the fault, got %q", alert.sent[0])
	}

	sender.health.PhoneResponding = true
	m.check(context.Background())
	m.check(context.Background())

	if len(alert.sent) != 2 {
		t.Fatalf("want a single recovery alert, got %d: %v", len(alert.sent), alert.sent)
	}
	if !strings.Contains(alert.sent[1], "recovered") {
		t.Errorf("second alert should be the recovery, got %q", alert.sent[1])
	}
}

func TestMonitorAlertsWhenItStartsUnhealthy(t *testing.T) {
	alert := &recorder{}
	m := monitorFor(&healthSender{err: errors.New("connection refused")}, alert)

	m.check(context.Background())

	if len(alert.sent) != 1 {
		t.Fatalf("a sender broken at boot must alert, got %v", alert.sent)
	}
	if !strings.Contains(alert.sent[0], "unreachable") {
		t.Errorf("alert = %q", alert.sent[0])
	}
}

func TestMonitorSurvivesAFailingAlerter(t *testing.T) {
	alert := &recorder{err: errors.New("alert channel down")}
	sender := &healthSender{health: okHealth()}
	m := monitorFor(sender, alert)

	m.check(context.Background())
	sender.health.LoggedIn = false
	m.check(context.Background())

	if len(alert.sent) != 1 {
		t.Fatal("the monitor should still have attempted the alert")
	}
	if !m.known || m.healthy {
		t.Error("state must still record the failure even when the alert could not be delivered")
	}
}

func TestDiagnoseNamesEveryFault(t *testing.T) {
	cases := []struct {
		health *rcsdirect.Health
		want   string
	}{
		{&rcsdirect.Health{Connected: true, PhoneResponding: true, SessionActive: true, SIMs: []rcsdirect.SIM{{RCSEnabled: true}}}, "paired again"},
		{&rcsdirect.Health{LoggedIn: true, Connected: true, PhoneResponding: true, SIMs: []rcsdirect.SIM{{RCSEnabled: true}}}, "took over"},
		{&rcsdirect.Health{LoggedIn: true, PhoneResponding: true, SessionActive: true, SIMs: []rcsdirect.SIM{{RCSEnabled: true}}}, "not connected"},
		{&rcsdirect.Health{LoggedIn: true, Connected: true, SessionActive: true, SIMs: []rcsdirect.SIM{{RCSEnabled: true}}}, "not responding"},
		{&rcsdirect.Health{LoggedIn: true, Connected: true, PhoneResponding: true, SessionActive: true}, "no SIM"},
	}
	for _, tc := range cases {
		if got := diagnose(tc.health, nil); !strings.Contains(got, tc.want) {
			t.Errorf("diagnose = %q, want it to mention %q", got, tc.want)
		}
	}

	got := diagnose(&rcsdirect.Health{LastError: "fatal connection error"}, nil)
	if !strings.Contains(got, "fatal connection error") {
		t.Errorf("lastError should be surfaced, got %q", got)
	}
}

func TestDiagnoseBlamesExpiredCookiesNotACompetingSession(t *testing.T) {
	expired := &rcsdirect.Health{
		LoggedIn:        true,
		PhoneResponding: true,
		LastError:       "fatal connection error: http 401 while polling",
	}

	got := diagnose(expired, nil)
	if !strings.Contains(got, "HAR refresh") {
		t.Errorf("a 401 must point at the session refresh, got %q", got)
	}
	if strings.Contains(got, "took over") {
		t.Errorf("a 401 must not be blamed on a competing session, got %q", got)
	}
}

func TestDisabledMonitorNeverStarts(t *testing.T) {
	alert := &recorder{}
	m := NewMonitor(NewService(&fakeSender{enabled: true}, Config{Enabled: false}), alert, 0)

	m.Start(context.Background(), nil)

	if len(alert.sent) != 0 {
		t.Error("a disabled service must not alert")
	}
}

func TestMonitorRunsEvenWithNoPhonePaired(t *testing.T) {
	alert := &recorder{}
	sender := &healthSender{err: errors.New("no phone is paired yet")}
	sender.enabled = false
	m := NewMonitor(NewService(sender, Config{Enabled: true}), alert, 0)

	m.check(context.Background())

	if len(alert.sent) != 1 {
		t.Fatalf("an unpaired phone is exactly what the monitor must report, got %v", alert.sent)
	}
	if !strings.Contains(alert.sent[0], "unreachable") {
		t.Errorf("alert = %q", alert.sent[0])
	}
}
