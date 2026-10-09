package rcsphone

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

// withUnknown returns a message carrying raw bytes libgm's schema does not name.
func withUnknown(raw []byte) proto.Message {
	m := &gmproto.GetConversationTypeResponse{}
	m.ProtoReflect().SetUnknown(raw)
	return m
}

func TestUnknownFieldsDecodesWhatItCan(t *testing.T) {
	var raw []byte
	raw = protowire.AppendTag(raw, 8, protowire.BytesType)
	raw = protowire.AppendString(raw, "hello")
	raw = protowire.AppendTag(raw, 9, protowire.VarintType)
	raw = protowire.AppendVarint(raw, 42)

	var nested []byte
	nested = protowire.AppendTag(nested, 1, protowire.VarintType)
	nested = protowire.AppendVarint(nested, 7)
	raw = protowire.AppendTag(raw, 10, protowire.BytesType)
	raw = protowire.AppendBytes(raw, nested)

	raw = protowire.AppendTag(raw, 11, protowire.BytesType)
	raw = protowire.AppendBytes(raw, []byte{0xff, 0xfe})

	got := strings.Join(unknownFields("x", withUnknown(raw)), "\n")
	for _, want := range []string{
		`x.field8 = "hello"`,
		"x.field9 = 42",
		"x.field10.field1 = 7",
		"x.field11 = 0xfffe",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestUnknownFieldsIsEmptyForAFullyNamedMessage(t *testing.T) {
	if got := unknownFields("x", &gmproto.GetConversationTypeResponse{Type: 2}); len(got) != 0 {
		t.Errorf("want nothing, got %v", got)
	}
}

func TestConversationFieldsNamesWhatItKnows(t *testing.T) {
	conv := &gmproto.Conversation{
		SubType: true,
		Participants: []*gmproto.Participant{
			{IsMe: true, SomeInt1: 5},
			{SomeInt2: 9},
		},
	}
	got := strings.Join(conversationFields(conv), "\n")
	for _, want := range []string{
		"subType = true", "thirdType = false", "status = ",
		"participant[0](me).someInt1 = 5", "participant[1].someInt2 = 9",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if conversationFields(nil) != nil {
		t.Error("no conversation, no fields")
	}
}
