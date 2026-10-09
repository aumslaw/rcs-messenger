package rcsphone

import (
	"sort"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

// Google Messages records every change of a conversation's protocol as a
// "tombstone" message in the thread (the grey system lines). The most recent
// one says what the conversation currently is. This is the same signal the
// mautrix bridge uses to decide when to force RCS.

type e2eeState string

const (
	e2eeOn      e2eeState = "on"
	e2eeOff     e2eeState = "off"
	e2eeUnknown e2eeState = "unknown"
)

var tombstoneE2EEOn = map[gmproto.MessageStatusType]bool{
	gmproto.MessageStatusType_TOMBSTONE_ENCRYPTED_ONE_ON_ONE_RCS_CREATED:                       true,
	gmproto.MessageStatusType_TOMBSTONE_PROTOCOL_SWITCH_TO_ENCRYPTED_RCS:                       true,
	gmproto.MessageStatusType_TOMBSTONE_PROTOCOL_SWITCH_TO_ENCRYPTED_RCS_INFO:                  true,
	gmproto.MessageStatusType_MESSAGE_STATUS_TOMBSTONE_PROTOCOL_SWITCH_TEXT_TO_E2EE:            true,
	gmproto.MessageStatusType_MESSAGE_STATUS_TOMBSTONE_PROTOCOL_SWITCH_RCS_TO_E2EE:             true,
	gmproto.MessageStatusType_MESSAGE_STATUS_TOMBSTONE_ENCRYPTED_RCS_NORM_PARTICIPANT_UPGRADED: true,
}

var tombstoneE2EEOff = map[gmproto.MessageStatusType]bool{
	gmproto.MessageStatusType_TOMBSTONE_ONE_ON_ONE_SMS_CREATED:                       true,
	gmproto.MessageStatusType_TOMBSTONE_ONE_ON_ONE_RCS_CREATED:                       true,
	gmproto.MessageStatusType_TOMBSTONE_PROTOCOL_SWITCH_TO_TEXT:                      true,
	gmproto.MessageStatusType_TOMBSTONE_PROTOCOL_SWITCH_TO_RCS:                       true,
	gmproto.MessageStatusType_MESSAGE_STATUS_TOMBSTONE_PROTOCOL_SWITCH_E2EE_TO_TEXT:  true,
	gmproto.MessageStatusType_MESSAGE_STATUS_TOMBSTONE_PROTOCOL_SWITCH_E2EE_TO_RCS:   true,
	gmproto.MessageStatusType_MESSAGE_STATUS_TOMBSTONE_SMS_NORM_PARTICIPANT_UPGRADED: true,
	gmproto.MessageStatusType_MESSAGE_STATUS_TOMBSTONE_RCS_NORM_PARTICIPANT_UPGRADED: true,
}

// latestE2EE finds the newest protocol tombstone among msgs.
func latestE2EE(msgs []*gmproto.Message) (e2eeState, *gmproto.Message) {
	sorted := append([]*gmproto.Message(nil), msgs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].GetTimestamp() > sorted[j].GetTimestamp() })
	for _, m := range sorted {
		st := m.GetMessageStatus().GetStatus()
		if tombstoneE2EEOn[st] {
			return e2eeOn, m
		}
		if tombstoneE2EEOff[st] {
			return e2eeOff, m
		}
	}
	return e2eeUnknown, nil
}

// finalizing reports whether the tombstone that decided a chat's state is the
// phone's first draft of it rather than its current view. A chat the web
// session creates is written down as SMS before the phone has looked the
// contact up. The conversation's type flips to RCS within seconds, but the
// created-tombstone is only rewritten in place, same message ID and same
// timestamp, to the ENCRYPTED variant when the phone next finalizes the chat.
// That has been observed to take hours unless the thread is opened on the
// handset. Until it happens the tombstone says nothing about encryption.
func finalizing(convType gmproto.ConversationType, evidence *gmproto.Message) bool {
	return convType == gmproto.ConversationType_RCS &&
		evidence.GetMessageStatus().GetStatus() == gmproto.MessageStatusType_TOMBSTONE_ONE_ON_ONE_SMS_CREATED
}

// Outgoing statuses that end a send attempt.
func isSentOK(st gmproto.MessageStatusType) bool {
	switch st {
	case gmproto.MessageStatusType_OUTGOING_COMPLETE,
		gmproto.MessageStatusType_OUTGOING_DELIVERED,
		gmproto.MessageStatusType_OUTGOING_DISPLAYED:
		return true
	}
	return false
}

func isSendFailure(st gmproto.MessageStatusType) bool {
	switch st {
	case gmproto.MessageStatusType_OUTGOING_FAILED_GENERIC,
		gmproto.MessageStatusType_OUTGOING_FAILED_EMERGENCY_NUMBER,
		gmproto.MessageStatusType_OUTGOING_CANCELED,
		gmproto.MessageStatusType_OUTGOING_FAILED_TOO_LARGE,
		gmproto.MessageStatusType_OUTGOING_FAILED_RECIPIENT_LOST_RCS,
		gmproto.MessageStatusType_OUTGOING_FAILED_NO_RETRY_NO_FALLBACK,
		gmproto.MessageStatusType_OUTGOING_FAILED_RECIPIENT_DID_NOT_DECRYPT,
		gmproto.MessageStatusType_OUTGOING_FAILED_RECIPIENT_LOST_ENCRYPTION,
		gmproto.MessageStatusType_OUTGOING_FAILED_RECIPIENT_DID_NOT_DECRYPT_NO_MORE_RETRY,
		gmproto.MessageStatusType_OUTGOING_FAILED_RECIPIENT_NEGATIVE_DELIVERY,
		gmproto.MessageStatusType_OUTGOING_RESTRICTED,
		gmproto.MessageStatusType_OUTGOING_FAILED_TO_ENCRYPT:
		return true
	}
	return false
}
