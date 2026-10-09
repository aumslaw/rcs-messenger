package rcsphone

import (
	"strings"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

func (a *Agent) OnInbound(fn func(from, text, messageID string)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.onInbound = fn
}

func (a *Agent) rememberConversation(conversationID, number string) {
	if conversationID == "" || number == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.convNumbers == nil {
		a.convNumbers = map[string]string{}
	}
	a.convNumbers[conversationID] = number
}

func (a *Agent) inboundTarget(conversationID string) (string, func(from, text, messageID string)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.convNumbers[conversationID], a.onInbound
}

func isIncoming(m *gmproto.Message) bool {
	return strings.HasPrefix(m.GetMessageStatus().GetStatus().String(), "INCOMING_")
}

func messageText(m *gmproto.Message) string {
	var parts []string
	for _, p := range m.GetMessageInfo() {
		if c := p.GetMessageContent().GetContent(); c != "" {
			parts = append(parts, c)
		}
	}
	return strings.Join(parts, "\n")
}

func (a *Agent) forwardInbound(m *gmproto.Message) {
	if !isIncoming(m) {
		return
	}
	text := messageText(m)
	if strings.TrimSpace(text) == "" {
		return
	}

	from, fn := a.inboundTarget(m.GetConversationID())
	if fn == nil {
		return
	}
	if from == "" {
		a.log.Warn().Str("conversation_id", m.GetConversationID()).
			Msg("inbound message from an unknown conversation, cannot resolve the number")
		return
	}

	go fn(from, text, m.GetMessageID())
}
