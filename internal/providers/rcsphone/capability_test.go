package rcsphone

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

// capabilityBytes builds field 8 the way the phone was observed to send it.
func capabilityBytes(flag1, flag2 uint64, version []byte, refreshedMs uint64) []byte {
	var inner []byte
	inner = protowire.AppendTag(inner, 1, protowire.VarintType)
	inner = protowire.AppendVarint(inner, flag1)
	inner = protowire.AppendTag(inner, 2, protowire.VarintType)
	inner = protowire.AppendVarint(inner, flag2)
	if version != nil {
		inner = protowire.AppendTag(inner, 3, protowire.BytesType)
		inner = protowire.AppendBytes(inner, version)
	}
	if refreshedMs > 0 {
		var f5 []byte
		f5 = protowire.AppendTag(f5, 1, protowire.VarintType)
		f5 = protowire.AppendVarint(f5, 1)
		f5 = protowire.AppendTag(f5, 2, protowire.VarintType)
		f5 = protowire.AppendVarint(f5, refreshedMs)
		inner = protowire.AppendTag(inner, 5, protowire.BytesType)
		inner = protowire.AppendBytes(inner, f5)
	}
	var raw []byte
	raw = protowire.AppendTag(raw, capabilityField, protowire.BytesType)
	raw = protowire.AppendBytes(raw, inner)
	return raw
}

func TestParseCapabilityReadsTheObservedShapes(t *testing.T) {
	// A Google Messages contact after the lookup, as on conversation 29
	// before its send.
	gm := parseCapability(withUnknown(capabilityBytes(1, 1, []byte{2, 1}, 1790790119943)))
	if gm == nil || gm.State() != CapabilityYes {
		t.Fatalf("looked-up google messages contact should be yes, got %+v", gm)
	}
	if gm.Version != "0x0201" || gm.RefreshedAt != "2026-09-30T17:41:59Z" {
		t.Errorf("version/refreshedAt = %q/%q", gm.Version, gm.RefreshedAt)
	}

	// The same shape without the refresh block, as after a tap on an older chat.
	if got := parseCapability(withUnknown(capabilityBytes(1, 1, []byte{2, 1}, 0))); got.State() != CapabilityYes {
		t.Errorf("yes does not depend on the refresh block, got %+v", got)
	}

	// Nothing recorded: a cold chat's first probe, and every probe of an iPhone.
	cold := parseCapability(withUnknown(capabilityBytes(0, 0, nil, 0)))
	if cold == nil || cold.State() != CapabilityNotYet || cold.Version != "" || cold.RefreshedAt != "" {
		t.Errorf("two zero flags and nothing else is not_yet, got %+v", cold)
	}

	// An SMS-only contact: a recorded lookup with nothing in it.
	sms := parseCapability(withUnknown(capabilityBytes(0, 1, []byte{1}, 0)))
	if sms == nil || sms.State() != CapabilityNo || sms.Version != "0x01" {
		t.Errorf("sms contact should be no, got %+v", sms)
	}
}

func TestParseCapabilityIsNilWithoutTheField(t *testing.T) {
	if got := parseCapability(&gmproto.GetConversationTypeResponse{Type: 2}); got != nil {
		t.Errorf("no field 8, want nil, got %+v", got)
	}
	if got := parseCapability(nil); got != nil {
		t.Errorf("nil message, want nil, got %+v", got)
	}
	var none *Capability
	if none.State() != "" {
		t.Errorf("an absent capability has no state, got %q", none.State())
	}
}
