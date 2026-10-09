package rcs

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/zagnu-labs/rcs-messenger/internal/leader"
	"github.com/zagnu-labs/rcs-messenger/internal/logg"
	"github.com/zagnu-labs/rcs-messenger/internal/providers/rcsdirect"
)

const (
	DefaultMonitorInterval = time.Minute
	monitorTimeout         = 15 * time.Second
)

type Announcer interface {
	Announce(ctx context.Context, text string) error
}

type Monitor struct {
	svc      *Service
	alert    Announcer
	interval time.Duration

	known   bool
	healthy bool
}

func NewMonitor(svc *Service, alert Announcer, interval time.Duration) *Monitor {
	if interval <= 0 {
		interval = DefaultMonitorInterval
	}
	return &Monitor{svc: svc, alert: alert, interval: interval}
}

func (m *Monitor) Start(ctx context.Context, ldr leader.Checker) {
	if m == nil || m.svc == nil || !m.svc.Configured() {
		logg.Info().Msg("RCS health monitor not started, sending is disabled")
		return
	}

	logg.Info().Dur("interval", m.interval).Msg("RCS health monitor started")

	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ldr != nil && !ldr.IsLeader() {
				continue
			}
			m.check(ctx)
		}
	}
}

func (m *Monitor) check(ctx context.Context) {
	probe, cancel := context.WithTimeout(ctx, monitorTimeout)
	defer cancel()

	health, err := m.svc.Health(probe)
	healthy := err == nil && health.Ready()

	if m.known && healthy == m.healthy {
		return
	}

	first := !m.known
	m.known, m.healthy = true, healthy

	if healthy {
		if first {
			logg.Info().Msg("RCS sender healthy")
			return
		}
		logg.Info().Msg("RCS sender recovered")
		m.announce(ctx, "✅ <b>RCS sender recovered</b>\nMessages are going out again.")
		return
	}

	reason := diagnose(health, err)
	logg.Error().Str("reason", reason).Msg("RCS sender unhealthy")
	m.announce(ctx, fmt.Sprintf(
		"🚨 <b>RCS sender down</b>\n%s\n\nNothing is being delivered over RCS until this is fixed.",
		html.EscapeString(reason),
	))
}

func (m *Monitor) announce(ctx context.Context, text string) {
	if m.alert == nil {
		return
	}
	send, cancel := context.WithTimeout(ctx, monitorTimeout)
	defer cancel()
	if err := m.alert.Announce(send, text); err != nil {
		logg.Error().Err(err).Msg("RCS health alert could not be delivered")
	}
}

func diagnose(h *rcsdirect.Health, err error) string {
	if err != nil {
		if errors.Is(err, rcsdirect.ErrNotConfigured) {
			return "No rcs-direct base URL is configured."
		}
		return "The sender is unreachable: " + err.Error()
	}
	if h == nil {
		return "The sender returned no health at all."
	}

	if isAuthFailure(h.LastError) {
		return "the Google session has expired and needs a fresh HAR refresh (" + h.LastError + ")"
	}

	var faults []string
	switch {
	case !h.LoggedIn:
		faults = append(faults, "there is no paired session, the phone must be paired again")
	case !h.Connected:
		faults = append(faults, "the sender is not connected to Google")
	default:
		if !h.SessionActive {
			faults = append(faults, "another Messages for web session took over this Google account")
		}
		if !h.PhoneResponding {
			faults = append(faults, "the phone is off, offline or not responding")
		}
		if !anyRCSSIM(h) {
			faults = append(faults, "no SIM on the phone has RCS enabled")
		}
	}
	if len(faults) == 0 {
		faults = append(faults, "the sender reported itself not ready")
	}

	out := strings.Join(faults, "; ")
	if h.LastError != "" {
		out += " (last error: " + h.LastError + ")"
	}
	return out
}

func isAuthFailure(lastError string) bool {
	s := strings.ToLower(lastError)
	for _, marker := range []string{"401", "session_cookie_invalid", "authentication credential", "unauthorized"} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

func anyRCSSIM(h *rcsdirect.Health) bool {
	for _, s := range h.SIMs {
		if s.RCSEnabled {
			return true
		}
	}
	return false
}
