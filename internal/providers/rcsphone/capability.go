package rcsphone

import (
	"fmt"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// Capability is the phone's capability record for a contact, returned as the
// unnamed field 8 of the GET_CONVERSATION_TYPE response; the conversation
// record carries the same block as field 39. libgm does not name it.
// Observed 2026-09-30 across nine contacts:
//
//	{1, 1, 0x0201, ...}  every Google Messages contact once the phone had
//	                     looked it up, whether the chat was still pending or
//	                     already encrypted. Every chat that read this and was
//	                     then sent to or opened finalized as encrypted.
//	{0, 0}               nothing recorded yet. Google Messages contacts read
//	                     this on the first probe of a cold chat and {1, 1, ...}
//	                     on a probe a minute later with nothing else done, so
//	                     the probe itself starts the lookup. An iPhone on RCS
//	                     read this on every probe, so it is also what "no
//	                     encrypted RCS" looks like; one reading cannot tell
//	                     the two apart.
//	{0, 1, 0x01}         an SMS-only contact: a recorded lookup with nothing.
//
// The lookup does not finalize the chat: a chat can read {1, 1, ...} for
// hours and keep its SMS-created tombstone until it is opened on the handset
// or sent to. RefreshedAt, when present, is when the record was refreshed.
type Capability struct {
	Flag1       uint64 `json:"flag1"`
	Flag2       uint64 `json:"flag2"`
	Version     string `json:"version,omitempty"`
	RefreshedAt string `json:"refreshedAt,omitempty"`
}

// CapabilityState is Capability read as an answer to "can this contact take
// encrypted RCS".
type CapabilityState string

const (
	CapabilityYes    CapabilityState = "yes"     // the lookup found encrypted RCS
	CapabilityNotYet CapabilityState = "not_yet" // nothing recorded; also what an iPhone reads
	CapabilityNo     CapabilityState = "no"      // a recorded lookup without encrypted RCS
)

func (c *Capability) State() CapabilityState {
	switch {
	case c == nil:
		return ""
	case c.Flag1 == 1:
		return CapabilityYes
	case c.Flag2 == 0 && c.Version == "":
		return CapabilityNotYet
	}
	return CapabilityNo
}

const capabilityField = 8

// parseCapability decodes field 8 of msg's unnamed fields, or returns nil
// when the phone did not send one.
func parseCapability(msg proto.Message) *Capability {
	if msg == nil || !msg.ProtoReflect().IsValid() {
		return nil
	}
	raw, ok := unknownBytes(msg.ProtoReflect().GetUnknown(), capabilityField)
	if !ok {
		return nil
	}
	c := &Capability{}
	for _, f := range wireFields(raw) {
		switch {
		case f.num == 1 && f.typ == protowire.VarintType:
			c.Flag1 = f.varint()
		case f.num == 2 && f.typ == protowire.VarintType:
			c.Flag2 = f.varint()
		case f.num == 3 && f.typ == protowire.BytesType:
			c.Version = fmt.Sprintf("0x%x", f.bytes())
		case f.num == 5 && f.typ == protowire.BytesType:
			for _, g := range wireFields(f.bytes()) {
				if g.num == 2 && g.typ == protowire.VarintType && g.varint() > 0 {
					c.RefreshedAt = time.UnixMilli(int64(g.varint())).UTC().Format(time.RFC3339)
				}
			}
		}
	}
	return c
}

// wireField is one protobuf wire-format field: number, type and the
// undecoded value bytes.
type wireField struct {
	num protowire.Number
	typ protowire.Type
	val []byte
}

func (f wireField) varint() uint64 { v, _ := protowire.ConsumeVarint(f.val); return v }
func (f wireField) bytes() []byte  { v, _ := protowire.ConsumeBytes(f.val); return v }

// wireFields splits b into its fields, stopping at the first undecodable one.
func wireFields(b []byte) []wireField {
	var out []wireField
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return out
		}
		b = b[n:]
		m := protowire.ConsumeFieldValue(num, typ, b)
		if m < 0 {
			return out
		}
		out = append(out, wireField{num: num, typ: typ, val: b[:m]})
		b = b[m:]
	}
	return out
}

// unknownBytes returns the contents of the first length-delimited field
// numbered want in b.
func unknownBytes(b []byte, want protowire.Number) ([]byte, bool) {
	for _, f := range wireFields(b) {
		if f.num == want && f.typ == protowire.BytesType {
			return f.bytes(), true
		}
	}
	return nil, false
}
