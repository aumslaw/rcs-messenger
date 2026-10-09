package rcs

import (
	"context"
	"errors"
	"testing"
)

type fakeBlocker struct {
	calls      int
	phone      string
	reason     string
	unrecorded bool
	err        error
}

func (f *fakeBlocker) BlockNumber(_ context.Context, phone, reason string) (bool, error) {
	f.calls++
	f.phone, f.reason = phone, reason
	return !f.unrecorded, f.err
}

func TestIsOptOutMatchesTheStandardKeywords(t *testing.T) {
	for _, in := range []string{
		"STOP", "stop", " Stop ", "STOP.", "stop!", "Unsubscribe", "CANCEL",
		"quit", "END", "opt out", "OPTOUT", "stop all", "revoke",
	} {
		if !IsOptOut(in) {
			t.Errorf("IsOptOut(%q) = false, want true", in)
		}
	}
}

func TestIsOptOutIgnoresMessagesThatMerelyContainAKeyword(t *testing.T) {
	for _, in := range []string{
		"please stop by the store",
		"don't stop sending me these",
		"can i cancel my order",
		"where do i end up paying",
		"",
		"yes",
		"thanks!",
	} {
		if IsOptOut(in) {
			t.Errorf("IsOptOut(%q) = true, want false", in)
		}
	}
}

func TestHandleInboundBlocksOnOptOut(t *testing.T) {
	b := &fakeBlocker{}
	s := NewService(&fakeSender{enabled: true}, Config{Enabled: true}).SetBlocker(b)

	res, err := s.HandleInbound(context.Background(), Inbound{From: "+1 555-123-4567", Text: "STOP"})
	if err != nil {
		t.Fatalf("inbound: %v", err)
	}
	if !res.OptOut || !res.Blocked {
		t.Fatalf("want an opt-out that was blocked, got %+v", res)
	}
	if b.calls != 1 || b.phone != "+15551234567" {
		t.Errorf("blocker got %d calls for %q", b.calls, b.phone)
	}
	if b.reason == "" {
		t.Error("the block should carry a reason")
	}
}

func TestHandleInboundIgnoresOrdinaryReplies(t *testing.T) {
	b := &fakeBlocker{}
	s := NewService(&fakeSender{enabled: true}, Config{Enabled: true}).SetBlocker(b)

	res, err := s.HandleInbound(context.Background(), Inbound{From: "+15551234567", Text: "sounds good, thanks"})
	if err != nil {
		t.Fatalf("inbound: %v", err)
	}
	if res.OptOut || res.Blocked {
		t.Errorf("an ordinary reply must not opt anyone out, got %+v", res)
	}
	if b.calls != 0 {
		t.Error("the blocker must not be called for an ordinary reply")
	}
}

func TestHandleInboundRejectsABadNumber(t *testing.T) {
	b := &fakeBlocker{}
	s := NewService(&fakeSender{enabled: true}, Config{Enabled: true}).SetBlocker(b)

	if _, err := s.HandleInbound(context.Background(), Inbound{From: "5551234567", Text: "STOP"}); code(t, err) != CodeBadNumber {
		t.Fatalf("code = %q", code(t, err))
	}
	if b.calls != 0 {
		t.Error("nothing should be blocked for an unparseable number")
	}
}

func TestHandleInboundSurfacesABlockFailure(t *testing.T) {
	b := &fakeBlocker{err: errors.New("store down")}
	s := NewService(&fakeSender{enabled: true}, Config{Enabled: true}).SetBlocker(b)

	_, err := s.HandleInbound(context.Background(), Inbound{From: "+15551234567", Text: "STOP"})
	if code(t, err) != CodeBlockFailed {
		t.Fatalf("code = %q, want %q", code(t, err), CodeBlockFailed)
	}
}

func TestHandleInboundWithoutABlockerStillReportsTheOptOut(t *testing.T) {
	s := NewService(&fakeSender{enabled: true}, Config{Enabled: true})

	res, err := s.HandleInbound(context.Background(), Inbound{From: "+15551234567", Text: "STOP"})
	if err != nil {
		t.Fatalf("inbound: %v", err)
	}
	if !res.OptOut || res.Blocked {
		t.Errorf("want opt-out seen but not blocked, got %+v", res)
	}
}

func TestHandleInboundReportsAnUnrecordedBlockHonestly(t *testing.T) {
	b := &fakeBlocker{unrecorded: true}
	s := NewService(&fakeSender{enabled: true}, Config{Enabled: true}).SetBlocker(b)

	res, err := s.HandleInbound(context.Background(), Inbound{From: "+15551234567", Text: "STOP"})
	if err != nil {
		t.Fatalf("inbound: %v", err)
	}
	if !res.OptOut {
		t.Error("the opt-out was still seen")
	}
	if res.Blocked {
		t.Error("blocked must be false when the store never recorded it, or callers think the number is suppressed")
	}
}
