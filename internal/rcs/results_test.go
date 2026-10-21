package rcs

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zagnu-labs/rcs-messenger/internal/providers/rcsdirect"
)

type fakeResults struct {
	records  []SendRecord
	blocked  map[string]bool
	optedOut map[string]bool
	err      error
}

func (f *fakeResults) OptedOut(_ context.Context, phone string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.optedOut[phone], nil
}

func (f *fakeResults) Record(_ context.Context, r SendRecord) error {
	f.records = append(f.records, r)
	return nil
}

func (f *fakeResults) Blocked(_ context.Context, phone string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.blocked[phone], nil
}

func (f *fakeResults) Recent(context.Context, string, int) ([]SendRecord, error) {
	return f.records, nil
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		in   *rcsdirect.SendResult
		want Outcome
	}{
		{"no result", nil, OutcomeFailed},
		{"not sent", &rcsdirect.SendResult{FinalStatus: "OUTGOING_FAILED_GENERIC"}, OutcomeFailed},
		{"sms fallback", &rcsdirect.SendResult{Sent: true, MessageType: 1}, OutcomeSuccessSMS},
		{"mms fallback", &rcsdirect.SendResult{Sent: true, MessageType: 2}, OutcomeSuccessSMS},
		{"rcs, chat encrypted", &rcsdirect.SendResult{Sent: true, MessageType: 4, After: &rcsdirect.Check{E2EE: "on"}}, OutcomeSuccessE2EE},
		{"rcs, chat unencrypted", &rcsdirect.SendResult{Sent: true, MessageType: 4, After: &rcsdirect.Check{E2EE: "off"}}, OutcomeSuccessRCS},
		{"rcs, chat not re-read", &rcsdirect.SendResult{Sent: true, MessageType: 4}, OutcomeUnverified},
		{"rcs, chat still pending", &rcsdirect.SendResult{Sent: true, MessageType: 4, After: &rcsdirect.Check{E2EE: "unknown"}}, OutcomeUnverified},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, _ := classify(tc.in); got != tc.want {
				t.Errorf("classify = %s, want %s", got, tc.want)
			}
		})
	}

	for _, o := range []Outcome{OutcomeSuccessRCS, OutcomeSuccessSMS} {
		if !o.Blocks() {
			t.Errorf("%s must block the number", o)
		}
	}
	for _, o := range []Outcome{OutcomeSuccessE2EE, OutcomeUnverified, OutcomeFailed} {
		if o.Blocks() {
			t.Errorf("%s must not block the number", o)
		}
	}
}

func TestSendRecordsTheOutcome(t *testing.T) {
	f := &fakeSender{enabled: true, result: &rcsdirect.SendResult{
		Sent: true, MessageType: 4, FinalStatus: "OUTGOING_DELIVERED", MessageID: "42",
		Check: &rcsdirect.Check{ConversationID: "29", E2EE: "unknown", E2EECapability: "yes"},
		After: &rcsdirect.Check{E2EE: "on", E2EEEvidence: "TOMBSTONE_ENCRYPTED_ONE_ON_ONE_RCS_CREATED"},
	}}
	results := &fakeResults{}
	s := NewService(f, Config{Enabled: true}).SetResults(results)

	res, err := s.Send(context.Background(), "+15551234567", "hi")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if !res.Sent || res.Outcome != OutcomeSuccessE2EE || res.After == nil {
		t.Errorf("result = %+v, want sent, SUCCESS_E2EE, with the after-check", res)
	}
	if len(results.records) != 1 {
		t.Fatalf("want one record, got %d", len(results.records))
	}
	r := results.records[0]
	want := SendRecord{
		Phone: "+15551234567", ConversationID: "29", MessageID: "42", Outcome: OutcomeSuccessE2EE,
		Status: "OUTGOING_DELIVERED", Transport: "RCS", E2EEBefore: "unknown", E2EEAfter: "on",
		E2EEEvidence: "TOMBSTONE_ENCRYPTED_ONE_ON_ONE_RCS_CREATED", Capability: "yes",
	}
	if r != want {
		t.Errorf("record =\n%+v\nwant\n%+v", r, want)
	}
}

func TestUnencryptedSendStillCountsAsSent(t *testing.T) {
	f := &fakeSender{enabled: true, result: &rcsdirect.SendResult{
		Sent: true, MessageType: 4, FinalStatus: "OUTGOING_DELIVERED",
		After: &rcsdirect.Check{E2EE: "off", E2EEEvidence: "TOMBSTONE_ONE_ON_ONE_RCS_CREATED"},
	}}
	results := &fakeResults{}
	s := NewService(f, Config{Enabled: true}).SetResults(results)

	sent, err := s.SendText(context.Background(), "+15551234567", "hi")
	if err != nil || !sent {
		t.Fatalf("the caller must hear sent, the message went out: sent=%v err=%v", sent, err)
	}
	if len(results.records) != 1 || results.records[0].Outcome != OutcomeSuccessRCS {
		t.Errorf("want a SUCCESS_RCS record so the number is never auto-sent to again, got %+v", results.records)
	}
}

func TestKnownUnencryptedNumberIsRefusedBeforeThePhone(t *testing.T) {
	f := &fakeSender{enabled: true}
	results := &fakeResults{blocked: map[string]bool{"+15551234567": true}}
	s := NewService(f, Config{Enabled: true}).SetResults(results)

	_, err := s.Send(context.Background(), "+15551234567", "hi")
	if code(t, err) != CodeRefused {
		t.Fatalf("code = %q, want %q", code(t, err), CodeRefused)
	}
	if !strings.Contains(err.Error(), ReasonKnownUnencrypted) {
		t.Errorf("the refusal should name the reason, got %q", err.Error())
	}
	var p interface{ Permanent() bool }
	if !errors.As(err, &p) || !p.Permanent() {
		t.Error("known_unencrypted must read as permanent, so an auto-sender does not retry it")
	}
	if f.sends != 0 {
		t.Error("nothing may reach the phone for a number on record")
	}
	if len(results.records) != 1 || results.records[0].Status != ReasonKnownUnencrypted {
		t.Errorf("the refusal itself should be recorded, got %+v", results.records)
	}

	if _, err := s.Send(context.Background(), "+15559999999", "hi"); err != nil {
		t.Errorf("a number not on record sends as usual, got %v", err)
	}
}

func TestResultsStoreFailureRefuses(t *testing.T) {
	f := &fakeSender{enabled: true}
	s := NewService(f, Config{Enabled: true}).SetResults(&fakeResults{err: errors.New("db down")})

	_, err := s.Send(context.Background(), "+15551234567", "hi")
	if code(t, err) != CodeResultsUnavailable {
		t.Fatalf("code = %q, want %q", code(t, err), CodeResultsUnavailable)
	}
	if f.sends != 0 {
		t.Error("with the record unreadable nothing may reach the phone")
	}
}

func TestRefusalsAreRecorded(t *testing.T) {
	cause := &rcsdirect.RefusedError{Reason: "not_rcs", Check: &rcsdirect.Check{ConversationID: "7", E2EE: "off"}}
	results := &fakeResults{}
	s := NewService(&fakeSender{enabled: true, err: cause}, Config{Enabled: true}).SetResults(results)

	if _, err := s.Send(context.Background(), "+15551234567", "hi"); code(t, err) != CodeRefused {
		t.Fatalf("code = %q", code(t, err))
	}
	if len(results.records) != 1 {
		t.Fatalf("want one record, got %d", len(results.records))
	}
	r := results.records[0]
	if r.Outcome != OutcomeFailed || r.Status != "not_rcs" || r.ConversationID != "7" || r.E2EEBefore != "off" {
		t.Errorf("record = %+v", r)
	}
}

func TestWithoutResultsNothingIsBlocked(t *testing.T) {
	f := &fakeSender{enabled: true}
	s := NewService(f, Config{Enabled: true})

	if _, err := s.Send(context.Background(), "+15551234567", "hi"); err != nil {
		t.Fatalf("send: %v", err)
	}
	if f.sends != 1 {
		t.Error("with no results store the send goes ahead")
	}
	if _, err := s.Results(context.Background(), "", 10); code(t, err) != CodeDisabled {
		t.Errorf("listing without a store: code = %q, want %q", code(t, err), CodeDisabled)
	}
}

func TestResultsListingNormalizesTheNumber(t *testing.T) {
	results := &fakeResults{records: []SendRecord{{Phone: "+15551234567", Outcome: OutcomeSuccessE2EE}}}
	s := NewService(&fakeSender{enabled: true}, Config{Enabled: true}).SetResults(results)

	if _, err := s.Results(context.Background(), "5551234567", 10); code(t, err) != CodeBadNumber {
		t.Errorf("a national number: code = %q, want %q", code(t, err), CodeBadNumber)
	}
	out, err := s.Results(context.Background(), " +1 (555) 123-4567 ", 10)
	if err != nil || len(out) != 1 {
		t.Errorf("want the one record, got %v / %v", out, err)
	}
	if out, err := s.Results(context.Background(), "", 10); err != nil || len(out) != 1 {
		t.Errorf("no number lists everything, got %v / %v", out, err)
	}
}

func TestOptedOutNumberIsRefusedBeforeThePhone(t *testing.T) {
	f := &fakeSender{enabled: true}
	results := &fakeResults{optedOut: map[string]bool{"+15551234567": true}}
	s := NewService(f, Config{Enabled: true}).SetResults(results)

	_, err := s.Send(context.Background(), "+15551234567", "hi")
	if code(t, err) != CodeRefused || !strings.Contains(err.Error(), ReasonOptedOut) {
		t.Fatalf("want a refusal naming %q, got %v", ReasonOptedOut, err)
	}
	var p interface{ Permanent() bool }
	if !errors.As(err, &p) || !p.Permanent() {
		t.Error("opted_out must read as permanent so an auto-sender never retries it")
	}
	if f.sends != 0 {
		t.Error("nothing may reach the phone for a number that opted out")
	}
}

func TestOptOutReadFailureRefuses(t *testing.T) {
	f := &fakeSender{enabled: true}
	s := NewService(f, Config{Enabled: true}).SetResults(&fakeResults{err: errors.New("db down")})
	if _, err := s.Send(context.Background(), "+15551234567", "hi"); code(t, err) != CodeResultsUnavailable {
		t.Fatalf("an unreadable opt-out list must refuse, got %v", err)
	}
	if f.sends != 0 {
		t.Error("nothing may be sent when opt-outs cannot be read")
	}
}

// Encryption is not configurable: whatever the config, the sender is asked
// for it, and the zero Config is the strictest one.
func TestEncryptionIsAlwaysRequestedFromTheSender(t *testing.T) {
	f := &fakeSender{enabled: true}
	s := NewService(f, Config{Enabled: true})
	if _, err := s.Send(context.Background(), "+15551234567", "hi"); err != nil {
		t.Fatal(err)
	}
	if f.lastOpt.RequireE2EE == nil || !*f.lastOpt.RequireE2EE {
		t.Fatal("the sender must always be told to require E2EE")
	}
}
