package rcs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zagnu-labs/rcs-messenger/internal/apperr"
	"github.com/zagnu-labs/rcs-messenger/internal/providers/rcsdirect"
)

type fakeSender struct {
	enabled bool
	sends   int
	lastTo  string
	lastMsg string
	lastOpt rcsdirect.SendOptions
	result  *rcsdirect.SendResult
	err     error
}

func (f *fakeSender) Enabled() bool { return f.enabled }

func (f *fakeSender) Send(_ context.Context, to, message string, opts rcsdirect.SendOptions) (*rcsdirect.SendResult, error) {
	f.sends++
	f.lastTo, f.lastMsg, f.lastOpt = to, message, opts
	if f.err != nil {
		return nil, f.err
	}
	if f.result != nil {
		return f.result, nil
	}
	return &rcsdirect.SendResult{Sent: true, MessageType: 4, FinalStatus: "OUTGOING_DELIVERED"}, nil
}

func (f *fakeSender) Check(_ context.Context, to string) (*rcsdirect.Check, error) {
	return &rcsdirect.Check{To: to, Eligible: true}, nil
}

func (f *fakeSender) Health(context.Context) (*rcsdirect.Health, error) {
	return &rcsdirect.Health{LoggedIn: true, Connected: true, PhoneResponding: true}, nil
}

func (f *fakeSender) Thread(_ context.Context, to string, _ int) (*rcsdirect.Thread, error) {
	return &rcsdirect.Thread{Check: &rcsdirect.Check{To: to}}, nil
}

func code(t *testing.T, err error) apperr.Code {
	t.Helper()
	var appErr *apperr.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("want an *apperr.AppError, got %v", err)
	}
	return appErr.Code
}

func TestSendHappyPath(t *testing.T) {
	f := &fakeSender{enabled: true}
	s := NewService(f, Config{Enabled: true, RequireE2EE: true, Wait: 20 * time.Second})

	_, err := s.Send(context.Background(), "(555) 123-4567", "hello")
	if err == nil {
		t.Fatal("a 10-digit national number is not E.164 and must be refused")
	}
	if code(t, err) != CodeBadNumber {
		t.Fatalf("code = %q", code(t, err))
	}

	res, err := s.Send(context.Background(), " +1 (555) 123-4567 ", "hello")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if f.lastTo != "+15551234567" {
		t.Errorf("number not normalized: %q", f.lastTo)
	}
	if !res.Sent || res.Transport != "RCS" {
		t.Errorf("want sent over RCS, got %+v", res)
	}
	if f.lastOpt.RequireE2EE == nil || !*f.lastOpt.RequireE2EE {
		t.Error("RequireE2EE from config should reach the sender")
	}
	if f.lastOpt.Wait != 20*time.Second {
		t.Errorf("wait = %v", f.lastOpt.Wait)
	}
}

func TestDisabledSendsNothing(t *testing.T) {
	f := &fakeSender{enabled: true}
	s := NewService(f, Config{Enabled: false})

	_, err := s.Send(context.Background(), "+15551234567", "hi")
	if code(t, err) != CodeDisabled {
		t.Fatalf("code = %q, want %q", code(t, err), CodeDisabled)
	}
	if f.sends != 0 {
		t.Error("nothing should reach the sender when disabled")
	}
}

func TestNoSenderConfigured(t *testing.T) {
	s := NewService(&fakeSender{enabled: false}, Config{Enabled: true})
	if s.Enabled() {
		t.Error("enabled config with no sender URL is not enabled")
	}
	if _, err := s.Send(context.Background(), "+15551234567", "hi"); code(t, err) != CodeDisabled {
		t.Errorf("code = %q", code(t, err))
	}
}

func TestDryRunSendsNothing(t *testing.T) {
	f := &fakeSender{enabled: true}
	s := NewService(f, Config{Enabled: true, DryRun: true})

	res, err := s.Send(context.Background(), "+15551234567", "hi")
	if err != nil {
		t.Fatalf("dry run should not error: %v", err)
	}
	if !res.DryRun || res.Sent {
		t.Errorf("want a dry-run result, got %+v", res)
	}
	if f.sends != 0 {
		t.Error("dry run must not reach the phone")
	}
}

func TestAllowlistBlocksEveryoneElse(t *testing.T) {
	f := &fakeSender{enabled: true}
	s := NewService(f, Config{Enabled: true, Allowlist: []string{"+1 555-123-4567", "not-a-number"}})

	if _, err := s.Send(context.Background(), "+15559999999", "hi"); code(t, err) != CodeNotAllowlisted {
		t.Fatalf("code = %q, want %q", code(t, err), CodeNotAllowlisted)
	}
	if f.sends != 0 {
		t.Error("a blocked number must not reach the sender")
	}

	if _, err := s.Send(context.Background(), "+15551234567", "hi"); err != nil {
		t.Fatalf("allowlisted number should send: %v", err)
	}
}

func TestEmptyAllowlistAllowsAll(t *testing.T) {
	s := NewService(&fakeSender{enabled: true}, Config{Enabled: true})
	if _, err := s.Send(context.Background(), "+15559999999", "hi"); err != nil {
		t.Fatalf("empty allowlist should not block: %v", err)
	}
}

func TestEmptyMessageRejected(t *testing.T) {
	f := &fakeSender{enabled: true}
	s := NewService(f, Config{Enabled: true})

	if _, err := s.Send(context.Background(), "+15551234567", "   "); code(t, err) != CodeEmptyMessage {
		t.Fatalf("code = %q", code(t, err))
	}
	if f.sends != 0 {
		t.Error("an empty message must not reach the sender")
	}
}

func TestRefusalKeepsTheReasonAndCause(t *testing.T) {
	cause := &rcsdirect.RefusedError{Reason: "not_e2ee", Check: &rcsdirect.Check{To: "+15551234567"}}
	s := NewService(&fakeSender{enabled: true, err: cause}, Config{Enabled: true, RequireE2EE: true})

	_, err := s.Send(context.Background(), "+15551234567", "hi")
	if code(t, err) != CodeRefused {
		t.Fatalf("code = %q, want %q", code(t, err), CodeRefused)
	}

	var refused *rcsdirect.RefusedError
	if !errors.As(err, &refused) || refused.Reason != "not_e2ee" {
		t.Error("callers must still be able to read the refusal reason")
	}

	var appErr *apperr.AppError
	errors.As(err, &appErr)
	if appErr.HTTPCode != 409 {
		t.Errorf("HTTP code = %d, want 409", appErr.HTTPCode)
	}
}

func TestSenderFailureIsBadGateway(t *testing.T) {
	s := NewService(&fakeSender{enabled: true, err: errors.New("connection refused")}, Config{Enabled: true})

	_, err := s.Send(context.Background(), "+15551234567", "hi")
	if code(t, err) != CodeSenderUnavailable {
		t.Fatalf("code = %q", code(t, err))
	}
	var appErr *apperr.AppError
	errors.As(err, &appErr)
	if appErr.HTTPCode != 502 {
		t.Errorf("HTTP code = %d, want 502", appErr.HTTPCode)
	}
}

func TestNormalizeNumber(t *testing.T) {
	ok := map[string]string{
		"+15551234567":      "+15551234567",
		" +1 555-123-4567":  "+15551234567",
		"+1 (555) 123.4567": "+15551234567",
		"0015551234567":     "+15551234567",
		"+447911123456":     "+447911123456",
	}
	for in, want := range ok {
		got, err := NormalizeNumber(in)
		if err != nil || got != want {
			t.Errorf("NormalizeNumber(%q) = %q, %v; want %q", in, got, err, want)
		}
	}

	for _, in := range []string{"", "5551234567", "+0555123456", "+1555", "abc", "+1555123456789012345"} {
		if _, err := NormalizeNumber(in); !errors.Is(err, ErrBadNumber) {
			t.Errorf("NormalizeNumber(%q) should fail", in)
		}
	}
}
