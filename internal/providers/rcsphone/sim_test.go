package rcsphone

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/events"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

// These tests run Agent.Send end to end against fakeGM: the real eligibility
// rules, settle loop, status matching and after-send re-read, with only the
// network replaced. The rule under test is the bounty's: a message leaves only
// when the chat is confirmed end-to-end encrypted, and every refusal means
// SendMessage was never called.

const simTo = "+15551230000"

func quick(t *testing.T) {
	t.Helper()
	b, d, va, vd, vt := settleBudget, settleDelay, verifyAttempts, verifyDelay, verifyTimeout
	settleBudget, settleDelay = 300*time.Millisecond, 20*time.Millisecond
	verifyAttempts, verifyDelay, verifyTimeout = 2, 10*time.Millisecond, time.Second
	t.Cleanup(func() { settleBudget, settleDelay, verifyAttempts, verifyDelay, verifyTimeout = b, d, va, vd, vt })
}

var (
	encCreated = gmproto.MessageStatusType_TOMBSTONE_ENCRYPTED_ONE_ON_ONE_RCS_CREATED
	rcsCreated = gmproto.MessageStatusType_TOMBSTONE_ONE_ON_ONE_RCS_CREATED
	smsCreated = gmproto.MessageStatusType_TOMBSTONE_ONE_ON_ONE_SMS_CREATED
	e2eeToText = gmproto.MessageStatusType_MESSAGE_STATUS_TOMBSTONE_PROTOCOL_SWITCH_E2EE_TO_TEXT
	textToE2EE = gmproto.MessageStatusType_MESSAGE_STATUS_TOMBSTONE_PROTOCOL_SWITCH_TEXT_TO_E2EE
)

func refusal(t *testing.T, err error) *RefusedError {
	t.Helper()
	var r *RefusedError
	if !errors.As(err, &r) {
		t.Fatalf("want a refusal, got %v", err)
	}
	return r
}

func TestSimEncryptedChatWithCapabilitySends(t *testing.T) {
	quick(t)
	f := newFakeGM()
	f.setTombstones(tombstone("t1", 100, encCreated))
	a := newSimAgent(f)

	res, err := a.Send(context.Background(), simTo, "hello", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Sent || res.FinalStatus != gmproto.MessageStatusType_OUTGOING_DELIVERED.String() {
		t.Fatalf("sent=%v final=%q", res.Sent, res.FinalStatus)
	}
	if f.sendCount() != 1 {
		t.Fatalf("SendMessage called %d times, want 1", f.sendCount())
	}
	req := f.sends[0]
	if !req.GetForceRCS() {
		t.Error("send must carry ForceRCS")
	}
	if req.GetConversationID() != "conv1" || req.GetMessagePayload().GetMessageInfo()[0].GetMessageContent().GetContent() != "hello" {
		t.Errorf("wrong request: %v", req)
	}
	if res.MessageType != 4 {
		t.Errorf("message type %d, want 4 (RCS)", res.MessageType)
	}
	if res.After == nil || res.After.E2EE != e2eeOn {
		t.Errorf("after-send re-read should say encrypted, got %+v", res.After)
	}
}

func TestSimEveryUnconfirmedChatIsRefusedWithoutSending(t *testing.T) {
	quick(t)
	cases := []struct {
		name   string
		setup  func(f *fakeGM)
		reason string
	}{
		{"encrypted marker but capability not_yet (iPhone, stale marker)",
			func(f *fakeGM) {
				f.setTombstones(tombstone("t", 100, encCreated))
				f.capability = capabilityBytes(0, 0, nil, 0)
			},
			ErrE2EEPending.Error()},
		{"encrypted marker but capability no",
			func(f *fakeGM) {
				f.setTombstones(tombstone("t", 100, encCreated))
				f.capability = capabilityBytes(0, 1, []byte{1}, 0)
			},
			ErrNotE2EE.Error()},
		{"encrypted marker but the capability probe failed",
			func(f *fakeGM) { f.setTombstones(tombstone("t", 100, encCreated)); f.probeErr = errPhoneGone },
			ErrE2EEPending.Error()},
		{"encrypted marker but capability is flag1 alone (carrier RCS)",
			func(f *fakeGM) {
				f.setTombstones(tombstone("t", 100, encCreated))
				f.capability = capabilityBytes(1, 0, []byte{9}, 0)
			},
			ErrE2EEPending.Error()},
		{"fresh chat, capability yes, no encrypted marker yet (strict)",
			func(f *fakeGM) { f.setTombstones(tombstone("t", 100, smsCreated)) },
			ErrE2EEPending.Error()},
		{"fresh chat, capability not_yet",
			func(f *fakeGM) {
				f.setTombstones(tombstone("t", 100, smsCreated))
				f.capability = capabilityBytes(0, 0, nil, 0)
			},
			ErrE2EEPending.Error()},
		{"fresh chat, capability no",
			func(f *fakeGM) {
				f.setTombstones(tombstone("t", 100, smsCreated))
				f.capability = capabilityBytes(0, 1, []byte{1}, 0)
			},
			ErrNotE2EE.Error()},
		{"plain RCS chat",
			func(f *fakeGM) { f.setTombstones(tombstone("t", 100, rcsCreated)) },
			ErrNotE2EE.Error()},
		{"chat dropped from encrypted back to text",
			func(f *fakeGM) {
				f.setTombstones(tombstone("t1", 100, encCreated), tombstone("t2", 200, e2eeToText))
			},
			ErrNotE2EE.Error()},
		{"no protocol marker anywhere in the thread",
			func(f *fakeGM) { f.setTombstones(tombstone("m", 100, gmproto.MessageStatusType_OUTGOING_DELIVERED)) },
			ErrNotE2EE.Error()},
		{"SMS-only contact",
			func(f *fakeGM) {
				f.conv.Type = gmproto.ConversationType_SMS
				f.setTombstones(tombstone("t", 100, smsCreated))
				f.capability = capabilityBytes(0, 1, []byte{1}, 0)
			},
			ErrNotRCS.Error()},
		{"conversation switched to SMS/MMS send mode",
			func(f *fakeGM) {
				f.conv.SendMode = gmproto.ConversationSendMode_SEND_MODE_XMS
				f.setTombstones(tombstone("t", 100, encCreated))
			},
			ErrNotRCS.Error()},
		{"read-only conversation",
			func(f *fakeGM) { f.conv.ReadOnly = true; f.setTombstones(tombstone("t", 100, encCreated)) },
			ErrReadOnly.Error()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeGM()
			c.setup(f)
			a := newSimAgent(f)
			res, err := a.Send(context.Background(), simTo, "must not go out", time.Second)
			if res != nil {
				t.Fatalf("a refused send returned a result: %+v", res)
			}
			if r := refusal(t, err); r.Reason != c.reason {
				t.Fatalf("reason %q, want %q", r.Reason, c.reason)
			}
			if n := f.sendCount(); n != 0 {
				t.Fatalf("SendMessage was called %d times on a refused chat", n)
			}
		})
	}
}

func TestSimRCSOffOnTheSIMIsRefused(t *testing.T) {
	quick(t)
	f := newFakeGM()
	f.setTombstones(tombstone("t", 100, encCreated))
	a := newSimAgent(f)
	a.sims["sim1"].RCSChats.Enabled = false
	_, err := a.Send(context.Background(), simTo, "x", time.Second)
	if r := refusal(t, err); r.Reason != ErrRCSOffOnSIM.Error() || f.sendCount() != 0 {
		t.Fatalf("reason %q sends %d", r.Reason, f.sendCount())
	}
}

func TestSimUnknownSIMIsRefused(t *testing.T) {
	quick(t)
	f := newFakeGM()
	f.conv.DefaultOutgoingID = "someone-else"
	f.setTombstones(tombstone("t", 100, encCreated))
	a := newSimAgent(f)
	a.sims["sim2"] = a.sims["sim1"] // two SIMs, neither matches the chat's
	_, err := a.Send(context.Background(), simTo, "x", time.Second)
	if refusal(t, err); f.sendCount() != 0 {
		t.Fatalf("sent through an unmatched SIM")
	}
}

func TestSimPhoneNotRespondingAndBadNumberNeverReachTheClient(t *testing.T) {
	quick(t)
	f := newFakeGM()
	f.setTombstones(tombstone("t", 100, encCreated))
	a := newSimAgent(f)

	a.phoneResponding.Store(false)
	_, err := a.Send(context.Background(), simTo, "x", time.Second)
	if r := refusal(t, err); r.Reason != ErrPhoneOffline.Error() {
		t.Fatalf("reason %q", r.Reason)
	}
	a.phoneResponding.Store(true)

	for _, bad := range []string{"", "5551230000", "+1", "hello", "+1555123000099999999"} {
		if _, err := a.Send(context.Background(), bad, "x", time.Second); !errors.Is(err, ErrBadNumber) {
			t.Errorf("%q: want bad_number, got %v", bad, err)
		}
	}
	if f.sendCount() != 0 || f.checks != 0 {
		t.Fatalf("client touched: sends %d checks %d", f.sendCount(), f.checks)
	}
}

func TestSimLookupFailuresAreErrorsNotSends(t *testing.T) {
	quick(t)
	for name, mod := range map[string]func(*fakeGM){
		"cannot open the conversation": func(f *fakeGM) { f.createErr = errPhoneGone },
		"cannot read the thread":       func(f *fakeGM) { f.fetchErr = errPhoneGone },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeGM()
			f.setTombstones(tombstone("t", 100, encCreated))
			mod(f)
			a := newSimAgent(f)
			res, err := a.Send(context.Background(), simTo, "x", time.Second)
			if err == nil || res != nil || f.sendCount() != 0 {
				t.Fatalf("err=%v res=%v sends=%d", err, res, f.sendCount())
			}
		})
	}
}

// A fresh chat is written down as SMS-created, then the phone rewrites the
// marker to encrypted within seconds. The send waits for that and only then goes.
func TestSimFreshChatSendsOnlyOnceTheMarkerAppears(t *testing.T) {
	quick(t)
	f := newFakeGM()
	f.setTombstones(tombstone("t", 100, smsCreated))
	f.onCheck = func(f *fakeGM, n int) {
		if n == 4 {
			f.setTombstones(tombstone("t", 100, encCreated))
		}
	}
	a := newSimAgent(f)
	var sendsAtMarker int
	f.afterSend = func(f *fakeGM) { sendsAtMarker = f.checks }

	res, err := a.Send(context.Background(), simTo, "hi", 2*time.Second)
	if err != nil || !res.Sent {
		t.Fatalf("err=%v res=%+v", err, res)
	}
	if sendsAtMarker < 4 {
		t.Fatalf("sent after check %d, before the marker existed (check 4)", sendsAtMarker)
	}
	if f.sendCount() != 1 {
		t.Fatalf("sends %d", f.sendCount())
	}
}

// The chat turns plain between the first check and the last one: nothing goes out.
func TestSimChatThatFlipsToPlainWhileSettlingIsRefused(t *testing.T) {
	quick(t)
	f := newFakeGM()
	f.setTombstones(tombstone("t", 100, smsCreated))
	f.onCheck = func(f *fakeGM, n int) {
		if n >= 3 {
			f.setTombstones(tombstone("t", 100, smsCreated), tombstone("u", 200, rcsCreated))
		}
	}
	a := newSimAgent(f)
	_, err := a.Send(context.Background(), simTo, "x", time.Second)
	r := refusal(t, err)
	if r.Reason != ErrNotE2EE.Error() || f.sendCount() != 0 {
		t.Fatalf("reason %q sends %d", r.Reason, f.sendCount())
	}
}

func TestSimSendRejectedByThePhoneIsNotASend(t *testing.T) {
	quick(t)
	f := newFakeGM()
	f.setTombstones(tombstone("t", 100, encCreated))
	f.sendStatus = gmproto.SendMessageResponse_FAILURE_2
	a := newSimAgent(f)
	res, err := a.Send(context.Background(), simTo, "x", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent || !strings.HasPrefix(res.FinalStatus, "SEND_REQUEST_") {
		t.Fatalf("sent=%v final=%q", res.Sent, res.FinalStatus)
	}
}

func TestSimDeliveryFailureStatusesAreNotSent(t *testing.T) {
	quick(t)
	for _, st := range []gmproto.MessageStatusType{
		gmproto.MessageStatusType_OUTGOING_FAILED_RECIPIENT_LOST_ENCRYPTION,
		gmproto.MessageStatusType_OUTGOING_FAILED_TO_ENCRYPT,
		gmproto.MessageStatusType_OUTGOING_FAILED_RECIPIENT_DID_NOT_DECRYPT,
		gmproto.MessageStatusType_OUTGOING_FAILED_NO_RETRY_NO_FALLBACK,
		gmproto.MessageStatusType_OUTGOING_CANCELED,
	} {
		t.Run(st.String(), func(t *testing.T) {
			f := newFakeGM()
			f.setTombstones(tombstone("t", 100, encCreated))
			f.statuses = []gmproto.MessageStatusType{gmproto.MessageStatusType_OUTGOING_SENDING, st}
			a := newSimAgent(f)
			res, err := a.Send(context.Background(), simTo, "x", time.Second)
			if err != nil || res.Sent || res.FinalStatus != st.String() {
				t.Fatalf("err=%v sent=%v final=%q", err, res.Sent, res.FinalStatus)
			}
			if f.sendCount() != 1 {
				t.Fatalf("sends %d", f.sendCount())
			}
		})
	}
}

// The send tap went through but the chat drops to text during the send. The
// message is out; the result must say it was not encrypted so the caller flags
// and blocks the number.
func TestSimSendThatTurnsOutUnencryptedIsReportedAsUnencrypted(t *testing.T) {
	quick(t)
	f := newFakeGM()
	f.setTombstones(tombstone("t", 100, encCreated))
	f.afterSend = func(f *fakeGM) { f.addMessage(tombstone("sw", time.Now().UnixMicro()+1e6, e2eeToText)) }
	a := newSimAgent(f)
	res, err := a.Send(context.Background(), simTo, "x", time.Second)
	if err != nil || !res.Sent {
		t.Fatalf("err=%v res=%+v", err, res)
	}
	if res.After == nil || res.After.E2EE != e2eeOff {
		t.Fatalf("after-send state %+v, want off", res.After)
	}
}

func TestSimSMSTypedMessageIsVisibleToTheCaller(t *testing.T) {
	quick(t)
	f := newFakeGM()
	f.setTombstones(tombstone("t", 100, encCreated))
	f.messageType = 1 // the phone typed it as SMS
	a := newSimAgent(f)
	res, err := a.Send(context.Background(), simTo, "x", time.Second)
	if err != nil || !res.Sent || res.MessageType != 1 {
		t.Fatalf("err=%v res=%+v", err, res)
	}
}

// The request was accepted and the caller then went away: the message is on its
// way, so the answer is "sent, outcome unknown", never an error that gets retried.
func TestSimCallerLeavingAfterTheRequestIsSentOutcomeUnknown(t *testing.T) {
	quick(t)
	f := newFakeGM()
	f.setTombstones(tombstone("t", 100, encCreated))
	f.silent = true
	a := newSimAgent(f)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	res, err := a.Send(ctx, simTo, "x", 5*time.Second)
	if err != nil {
		t.Fatalf("a send the phone accepted must not return an error: %v", err)
	}
	if !res.Sent || !strings.HasPrefix(res.FinalStatus, "outcome_unknown") {
		t.Fatalf("sent=%v final=%q", res.Sent, res.FinalStatus)
	}
}

// The phone accepts the request and never reports a final status within the
// wait. The message may still go out, so this is "sent, outcome unknown" (not
// FAILED, which a caller retries into a duplicate), and the chat is re-read.
func TestSimNoFinalStatusWithinTheWaitIsSentOutcomeUnknown(t *testing.T) {
	quick(t)
	for name, statuses := range map[string][]gmproto.MessageStatusType{
		"no status at all":   nil,
		"only sending":       {gmproto.MessageStatusType_OUTGOING_SENDING},
		"sending then sent?": {gmproto.MessageStatusType_OUTGOING_SENDING, gmproto.MessageStatusType_OUTGOING_AWAITING_RETRY},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeGM()
			f.setTombstones(tombstone("t", 100, encCreated))
			f.statuses = statuses
			a := newSimAgent(f)
			res, err := a.Send(context.Background(), simTo, "x", 100*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			if !res.Sent || !strings.HasPrefix(res.FinalStatus, "outcome_unknown") {
				t.Fatalf("sent=%v final=%q", res.Sent, res.FinalStatus)
			}
			if res.After == nil {
				t.Error("an unknown outcome must be re-read so a fall back to SMS is seen")
			}
		})
	}
}

func TestSimSendsAreSerialised(t *testing.T) {
	quick(t)
	f := newFakeGM()
	f.setTombstones(tombstone("t", 100, encCreated))
	f.hold = make(chan struct{})
	a := newSimAgent(f)

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.Send(context.Background(), simTo, "x", time.Second)
		}()
	}
	time.Sleep(150 * time.Millisecond)
	close(f.hold)
	wg.Wait()
	if f.maxInflight != 1 {
		t.Fatalf("%d sends were in flight at once, want 1", f.maxInflight)
	}
	if f.sendCount() != 3 {
		t.Fatalf("sends %d", f.sendCount())
	}
}

// Status updates for some other message must never be taken for ours.
func TestSimStatusUpdatesForOtherMessagesAreIgnored(t *testing.T) {
	quick(t)
	f := newFakeGM()
	f.setTombstones(tombstone("t", 100, encCreated))
	f.silent = true
	f.afterSend = func(f *fakeGM) {
		f.agent.handleEvent(&libgm.WrappedMessage{Message: &gmproto.Message{
			MessageID: "other", TmpID: "not-ours",
			MessageStatus: &gmproto.MessageStatus{Status: gmproto.MessageStatusType_OUTGOING_DELIVERED},
		}})
	}
	a := newSimAgent(f)
	res, err := a.Send(context.Background(), simTo, "x", 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.StatusHistory) != 0 || res.MessageID != "" || strings.Contains(res.FinalStatus, "DELIVERED") {
		t.Fatalf("another message's delivery receipt was taken for ours: %+v", res)
	}
	if !strings.HasPrefix(res.FinalStatus, "outcome_unknown") {
		t.Fatalf("our own send had no status, so it is outcome unknown, got %q", res.FinalStatus)
	}
}

func TestSimCheckReportsTheEvidence(t *testing.T) {
	quick(t)
	f := newFakeGM()
	f.setTombstones(tombstone("t1", 100, smsCreated), tombstone("t2", 200, textToE2EE))
	a := newSimAgent(f)
	chk, err := a.Check(context.Background(), "+1 (555) 123-0000")
	if err != nil {
		t.Fatal(err)
	}
	if chk.To != simTo || !chk.Eligible || chk.E2EE != e2eeOn || chk.E2EECapability != CapabilityYes {
		t.Fatalf("%+v", chk)
	}
	if chk.ConversationTypeProbe == nil || chk.ConversationTypeProbe.Capability == nil {
		t.Fatalf("probe missing: %+v", chk.ConversationTypeProbe)
	}
}

func TestSimLostWebSessionRefusesWithoutSending(t *testing.T) {
	quick(t)
	cases := map[string]func(f *fakeGM, a *Agent){
		"not connected": func(f *fakeGM, a *Agent) { f.connected = false },
		"not logged in": func(f *fakeGM, a *Agent) { f.loggedIn = false },
		"phone unpaired us": func(f *fakeGM, a *Agent) {
			a.handleEvent(&gmproto.RevokePairData{})
		},
		"fatal connection error": func(f *fakeGM, a *Agent) {
			a.handleEvent(&events.ListenFatalError{Error: errPhoneGone})
		},
	}
	for name, lose := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeGM()
			f.setTombstones(tombstone("t", 100, encCreated))
			a := newSimAgent(f)
			lose(f, a)
			_, err := a.Send(context.Background(), simTo, "x", time.Second)
			if r := refusal(t, err); r.Reason != ErrSessionLost.Error() {
				t.Fatalf("reason %q, want %q", r.Reason, ErrSessionLost)
			}
			if n := f.sendCount(); n != 0 {
				t.Fatalf("%d sends reached the client", n)
			}
		})
	}
}

func TestSimSendRequestErrorThatLandedIsSentOutcomeUnknown(t *testing.T) {
	quick(t)
	f := newFakeGM()
	f.setTombstones(tombstone("t", 100, encCreated))
	f.sendErr, f.landOnError = errPhoneGone, true
	a := newSimAgent(f)
	res, err := a.Send(context.Background(), simTo, "x", time.Second)
	if err != nil {
		t.Fatalf("a message that reached the thread must not be an error (it would be retried): %v", err)
	}
	if !res.Sent || !strings.HasPrefix(res.FinalStatus, "outcome_unknown") {
		t.Fatalf("sent=%v final=%q", res.Sent, res.FinalStatus)
	}
}

func TestSimSendRequestErrorWithUnreadableThreadIsSentOutcomeUnknown(t *testing.T) {
	quick(t)
	f := newFakeGM()
	f.setTombstones(tombstone("t", 100, encCreated))
	f.sendErr = errPhoneGone
	f.breakReadOnError = true
	a := newSimAgent(f)
	res, err := a.Send(context.Background(), simTo, "x", time.Second)
	if err != nil || !res.Sent {
		t.Fatalf("an unreadable thread means we cannot say it was not sent: res=%+v err=%v", res, err)
	}
}

func TestSimSendRequestErrorThatDidNotLandIsAnErrorNotASend(t *testing.T) {
	quick(t)
	f := newFakeGM()
	f.setTombstones(tombstone("t", 100, encCreated))
	f.sendErr = errPhoneGone
	a := newSimAgent(f)
	res, err := a.Send(context.Background(), simTo, "x", time.Second)
	if err == nil || res != nil {
		t.Fatalf("thread read fine and holds no message: want an error, got res=%+v err=%v", res, err)
	}
}
