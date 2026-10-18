package rcsphone

import (
	"testing"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

func tombstone(id string, ts int64, st gmproto.MessageStatusType) *gmproto.Message {
	return &gmproto.Message{
		MessageID:     id,
		Timestamp:     ts,
		MessageStatus: &gmproto.MessageStatus{Status: st},
	}
}

func TestLatestE2EEPicksTheNewestTombstone(t *testing.T) {
	msgs := []*gmproto.Message{
		tombstone("1", 100, gmproto.MessageStatusType_TOMBSTONE_ONE_ON_ONE_SMS_CREATED),
		tombstone("2", 200, gmproto.MessageStatusType_MESSAGE_STATUS_TOMBSTONE_PROTOCOL_SWITCH_TEXT_TO_E2EE),
		tombstone("3", 300, gmproto.MessageStatusType_OUTGOING_DELIVERED),
	}
	state, m := latestE2EE(msgs)
	if state != e2eeOn || m.GetMessageID() != "2" {
		t.Fatalf("got %s from message %q, want on from message 2", state, m.GetMessageID())
	}
	if state, _ := latestE2EE(nil); state != e2eeUnknown {
		t.Errorf("no tombstones should be unknown, got %s", state)
	}
}

func TestFinalizingIsTheSMSCreatedTombstoneOnAnRCSChat(t *testing.T) {
	smsCreated := tombstone("37", 1, gmproto.MessageStatusType_TOMBSTONE_ONE_ON_ONE_SMS_CREATED)
	if !finalizing(gmproto.ConversationType_RCS, smsCreated) {
		t.Error("an RCS chat whose only tombstone says SMS-created has not been finalized yet")
	}
	if finalizing(gmproto.ConversationType_SMS, smsCreated) {
		t.Error("an SMS chat created as SMS is exactly what it says")
	}
	for _, st := range []gmproto.MessageStatusType{
		gmproto.MessageStatusType_TOMBSTONE_ONE_ON_ONE_RCS_CREATED,
		gmproto.MessageStatusType_TOMBSTONE_ENCRYPTED_ONE_ON_ONE_RCS_CREATED,
		gmproto.MessageStatusType_MESSAGE_STATUS_TOMBSTONE_PROTOCOL_SWITCH_E2EE_TO_RCS,
	} {
		if finalizing(gmproto.ConversationType_RCS, tombstone("1", 1, st)) {
			t.Errorf("%s is the phone's settled view and must stand", st)
		}
	}
}

func rcsCheck() *CheckResult {
	return &CheckResult{
		conv: &gmproto.Conversation{
			Type:     gmproto.ConversationType_RCS,
			SendMode: gmproto.ConversationSendMode_SEND_MODE_AUTO,
		},
		sim: &gmproto.SIMCard{RCSChats: &gmproto.RCSChats{Enabled: true}},
	}
}

// Every combination of what the phone can tell us, and whether a send is
// allowed. There is exactly one rule: a send needs positive evidence of
// encryption, and every unclear case is a refusal.
func TestEligibilityRequiresConfirmedEncryption(t *testing.T) {
	cases := []struct {
		name       string
		e2ee       e2eeState
		pending    bool
		capability CapabilityState
		wantOK     bool
		wantReason string
	}{
		{"encrypted tombstone", e2eeOn, false, "", true, ""},
		{"encrypted tombstone, capability not recorded", e2eeOn, false, CapabilityNotYet, true, ""},
		{"unencrypted tombstone", e2eeOff, false, "", false, ErrNotE2EE.Error()},
		{"unencrypted tombstone even though capability says yes", e2eeOff, false, CapabilityYes, false, ErrNotE2EE.Error()},
		{"no tombstone in reach", e2eeUnknown, false, "", false, ErrNotE2EE.Error()},
		{"no tombstone in reach even though capability says yes", e2eeUnknown, false, CapabilityYes, false, ErrNotE2EE.Error()},
		{"first draft, capability yes", e2eeUnknown, true, CapabilityYes, true, ""},
		{"first draft, lookup not landed (also what an iPhone reads)", e2eeUnknown, true, CapabilityNotYet, false, ErrE2EEPending.Error()},
		{"first draft, probe failed so no capability", e2eeUnknown, true, "", false, ErrE2EEPending.Error()},
		{"first draft, capability no", e2eeUnknown, true, CapabilityNo, false, ErrNotE2EE.Error()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := rcsCheck()
			c.E2EE, c.e2eePending, c.E2EECapability = tc.e2ee, tc.pending, tc.capability
			ok, reason := eligibility(c)
			if ok != tc.wantOK || reason != tc.wantReason {
				t.Errorf("got ok=%v reason=%q, want ok=%v reason=%q", ok, reason, tc.wantOK, tc.wantReason)
			}
		})
	}
}

func TestAFreshChatReadingSMSIsPendingNotRefused(t *testing.T) {
	fresh := rcsCheck()
	fresh.conv.Type = gmproto.ConversationType_SMS
	fresh.E2EE = e2eeOff
	fresh.E2EEEvidence = gmproto.MessageStatusType_TOMBSTONE_ONE_ON_ONE_SMS_CREATED.String()
	fresh.E2EECapability = CapabilityNotYet
	if ok, reason := eligibility(fresh); ok || reason != ErrRCSPending.Error() {
		t.Errorf("SMS with no lookup recorded: got ok=%v reason=%q, want rcs_pending", ok, reason)
	}
	if !settles(ErrRCSPending.Error()) {
		t.Error("rcs_pending is what a fresh chat resolves out of and must be re-checked")
	}

	lookedUp := rcsCheck()
	lookedUp.conv.Type = gmproto.ConversationType_SMS
	lookedUp.E2EE = e2eeOff
	lookedUp.E2EEEvidence = gmproto.MessageStatusType_TOMBSTONE_ONE_ON_ONE_SMS_CREATED.String()
	lookedUp.E2EECapability = CapabilityNo
	if ok, reason := eligibility(lookedUp); ok || reason != ErrNotRCS.Error() {
		t.Errorf("SMS with the lookup done: got ok=%v reason=%q, want not_rcs", ok, reason)
	}

	oldSMS := rcsCheck()
	oldSMS.conv.Type = gmproto.ConversationType_SMS
	oldSMS.E2EE = e2eeUnknown
	if ok, reason := eligibility(oldSMS); ok || reason != ErrNotRCS.Error() {
		t.Errorf("an SMS chat with no created-tombstone in reach: got ok=%v reason=%q, want not_rcs", ok, reason)
	}
}
