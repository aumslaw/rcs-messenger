package rcs

import (
	"context"
	"testing"

	"github.com/zagnu-labs/rcs-messenger/internal/providers/rcsdirect"
)

type fakeNudger struct {
	fakeSender
	action string
}

func (f *fakeNudger) Nudge(_ context.Context, to, action string) (*rcsdirect.Nudge, error) {
	f.action = action
	return &rcsdirect.Nudge{To: to, Changed: true}, nil
}

func TestNudgeValidatesTheActionBeforeReachingThePhone(t *testing.T) {
	f := &fakeNudger{fakeSender: fakeSender{enabled: true}}
	s := NewService(f, Config{Enabled: true})

	if _, err := s.Nudge(context.Background(), "+15551234567", "reboot"); code(t, err) != CodeBadNudge {
		t.Fatalf("code = %q, want %q", code(t, err), CodeBadNudge)
	}
	if f.action != "" {
		t.Error("an unknown action must not reach the sender")
	}

	out, err := s.Nudge(context.Background(), "+15551234567", rcsdirect.NudgeMarkRead)
	if err != nil {
		t.Fatalf("nudge: %v", err)
	}
	if f.action != rcsdirect.NudgeMarkRead || !out.Changed {
		t.Errorf("want %q forwarded and the result returned, got action %q result %+v",
			rcsdirect.NudgeMarkRead, f.action, out)
	}
}

func TestNudgeNeedsASenderThatCanNudge(t *testing.T) {
	s := NewService(&fakeSender{enabled: true}, Config{Enabled: true})

	_, err := s.Nudge(context.Background(), "+15551234567", rcsdirect.NudgeTyping)
	if code(t, err) != CodeNudgeUnsupported {
		t.Fatalf("code = %q, want %q", code(t, err), CodeNudgeUnsupported)
	}
}

func TestNudgeIsGatedLikeEverythingElse(t *testing.T) {
	f := &fakeNudger{fakeSender: fakeSender{enabled: true}}
	s := NewService(f, Config{Enabled: true, Allowlist: []string{"+15559999999"}})

	if _, err := s.Nudge(context.Background(), "+15551234567", rcsdirect.NudgeAll); code(t, err) != CodeNotAllowlisted {
		t.Fatalf("code = %q, want %q", code(t, err), CodeNotAllowlisted)
	}
	if f.action != "" {
		t.Error("a number off the allowlist must not reach the sender")
	}
}
