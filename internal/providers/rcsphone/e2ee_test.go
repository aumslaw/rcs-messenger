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

func TestEligibilityDistinguishesPendingFromOff(t *testing.T) {
	pending := rcsCheck()
	pending.E2EE, pending.e2eePending = e2eeUnknown, true
	if ok, reason := eligibility(pending, true); ok || reason != ErrE2EEPending.Error() {
		t.Errorf("pending: got ok=%v reason=%q, want e2ee_pending", ok, reason)
	}
	if ok, reason := eligibility(pending, false); !ok {
		t.Errorf("without require_e2ee a pending chat is an RCS chat, refused with %q", reason)
	}

	off := rcsCheck()
	off.E2EE = e2eeOff
	if ok, reason := eligibility(off, true); ok || reason != ErrNotE2EE.Error() {
		t.Errorf("off: got ok=%v reason=%q, want not_e2ee", ok, reason)
	}
	if ok, reason := eligibility(off, false); ok || reason != ErrNotE2EE.Error() {
		t.Errorf("off without the mandate: got ok=%v reason=%q, want not_e2ee, the phone's own record says unencrypted", ok, reason)
	}

	unknown := rcsCheck()
	unknown.E2EE = e2eeUnknown
	if ok, reason := eligibility(unknown, true); ok || reason != ErrNotE2EE.Error() {
		t.Errorf("unknown with no tombstone at all: got ok=%v reason=%q, want not_e2ee", ok, reason)
	}
	if ok, reason := eligibility(unknown, false); !ok {
		t.Errorf("unknown without the mandate is sent to and verified afterwards, refused with %q", reason)
	}

	on := rcsCheck()
	on.E2EE = e2eeOn
	if ok, reason := eligibility(on, true); !ok {
		t.Errorf("on: refused with %q", reason)
	}
}

func TestAFreshChatReadingSMSIsPendingNotRefused(t *testing.T) {
	fresh := rcsCheck()
	fresh.conv.Type = gmproto.ConversationType_SMS
	fresh.E2EE = e2eeOff
	fresh.E2EEEvidence = gmproto.MessageStatusType_TOMBSTONE_ONE_ON_ONE_SMS_CREATED.String()
	fresh.E2EECapability = CapabilityNotYet
	if ok, reason := eligibility(fresh, false); ok || reason != ErrRCSPending.Error() {
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
	if ok, reason := eligibility(lookedUp, false); ok || reason != ErrNotRCS.Error() {
		t.Errorf("SMS with the lookup done: got ok=%v reason=%q, want not_rcs", ok, reason)
	}

	oldSMS := rcsCheck()
	oldSMS.conv.Type = gmproto.ConversationType_SMS
	oldSMS.E2EE = e2eeUnknown
	if ok, reason := eligibility(oldSMS, false); ok || reason != ErrNotRCS.Error() {
		t.Errorf("an SMS chat with no created-tombstone in reach: got ok=%v reason=%q, want not_rcs", ok, reason)
	}
}
