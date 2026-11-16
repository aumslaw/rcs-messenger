package rcsandroid

import (
	"regexp"
	"sort"
	"strings"
)

// Kind is what the open chat is, as the Messages app itself shows it.
type Kind string

const (
	KindUnknown   Kind = "unknown"   // the screen did not say (selectors off, or still loading)
	KindSMS       Kind = "sms"       // hint reads "Text message"
	KindRCS       Kind = "rcs"       // hint reads "RCS message", no encryption shown
	KindEncrypted Kind = "encrypted" // RCS with the padlock, or the "end-to-end encrypted" line
)

// ChatView is one reading of an open chat.
type ChatView struct {
	Kind         Kind
	Hint         string // the empty message box's hint text
	Evidence     string // what decided Kind, for the log and the send record
	ComposeFound bool
	ComposeText  string // what is typed in the box ("" when the box shows its hint)
}

// norm collapses every kind of white space, including the narrow no-break
// space the app puts in times ("10:26 AM").
func norm(s string) string { return strings.Join(strings.Fields(s), " ") }

func (m *matcher) inPackage(n Node) bool { return n.Package == "" || n.Package == m.Package }

func (m *matcher) compose(s *Screen) (Node, bool) {
	if n, ok := s.Find(func(n Node) bool { return m.inPackage(n) && n.ID() == m.ComposeID }); ok {
		return n, true
	}
	return s.Find(func(n Node) bool { return m.inPackage(n) && n.Class == "android.widget.EditText" })
}

func (m *matcher) sendButton(s *Screen) (Node, bool) {
	return s.Find(func(n Node) bool {
		return m.inPackage(n) && n.Enabled && (n.ID() == m.SendID || (m.SendDesc != "" && m.sendDesc.MatchString(norm(n.Desc))))
	})
}

// ReadChat reads what the open chat is. The message box hint is the app's
// own live answer, so it wins over the thread line: an old "end-to-end
// encrypted" line can sit further up a chat that has since dropped to SMS.
func (m *matcher) ReadChat(s *Screen) ChatView {
	v := ChatView{Kind: KindUnknown}
	c, ok := m.compose(s)
	if !ok {
		v.Evidence = "no message box on screen"
		return v
	}
	v.ComposeFound = true
	if hint := norm(c.Text); m.hintSMS.MatchString(hint) || m.hintRCS.MatchString(hint) {
		v.Hint = hint
	} else if hint2 := norm(c.Desc); m.hintSMS.MatchString(hint2) || m.hintRCS.MatchString(hint2) {
		v.Hint = hint2
	} else {
		v.ComposeText = c.Text
	}

	// The thread line is supporting evidence only. Message text (a customer's
	// own reply, or a message label like "Not end-to-end encrypted") can
	// contain the same words, so it can never make a chat encrypted.
	banner, hasBanner := s.Find(func(n Node) bool {
		return m.inPackage(n) && n.Class != "android.widget.EditText" &&
			(m.banner.MatchString(norm(n.Text)) || m.banner.MatchString(norm(n.Desc)))
	})
	// The padlock is a node of its own. Message bubbles and message labels are
	// never searched: their description carries the message text.
	lock, hasLock := s.Find(func(n Node) bool {
		return m.inPackage(n) && n.Class != "android.widget.EditText" &&
			n.ID() != m.BubbleID && n.ID() != m.MessageContentID &&
			(m.lock.MatchString(n.ID()) || m.lock.MatchString(norm(n.Desc)))
	})

	// A long thread has scrolled the thread-start lock away. The app also labels
	// each message ("..., Read. End-to-end encrypted message"); the pattern is
	// anchored to the end of the description, which message text cannot reach.
	// A stale label proves nothing about the chat NOW, so this only decides that
	// typing is worth trying: the send button is the gate (see Send).
	label, hasLabel := s.Find(func(n Node) bool {
		return m.inPackage(n) && n.ID() == m.BubbleID && m.encLabel.MatchString(norm(n.Desc))
	})

	switch {
	case m.hintSMS.MatchString(v.Hint):
		v.Kind, v.Evidence = KindSMS, `message box hint "`+v.Hint+`"`
	case m.hintRCS.MatchString(v.Hint) && hasLock:
		v.Kind, v.Evidence = KindEncrypted, `message box hint "`+v.Hint+`" with padlock ("`+firstNonEmpty(lock.Desc, lock.ID())+`")`
	case m.hintRCS.MatchString(v.Hint) && hasLabel:
		v.Kind, v.Evidence = KindEncrypted, `message box hint "`+v.Hint+`" and a message the app labels end-to-end encrypted ("`+tailOf(norm(label.Desc))+`"); the send button is the final check`
	case m.hintRCS.MatchString(v.Hint):
		v.Kind, v.Evidence = KindRCS, `message box hint "`+v.Hint+`", no padlock`
		if hasBanner {
			v.Evidence += ` (thread line "` + norm(firstNonEmpty(banner.Text, banner.Desc)) + `" present but not enough)`
		}
	default:
		// No readable hint: text is typed in the box, or the screen is not a
		// chat. Never guess encrypted from the thread alone.
		v.Evidence = "hint not readable"
	}
	return v
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// Refusal reasons. A refusal always means nothing was sent.
const (
	ReasonE2EEPending    = "e2ee_pending" // RCS but no padlock yet; retryable (an iPhone on plain RCS stays here)
	ReasonNotE2EE        = "not_e2ee"     // the chat is SMS
	ReasonUnreadable     = "screen_unreadable"
	ReasonChatNotOpen    = "chat_not_open" // the app did not show a chat
	ReasonPhoneLocked    = "phone_locked"
	ReasonPhoneOffline   = "phone_offline" // no validated internet: RCS cannot send, messages would queue forever
	ReasonWrongChat      = "wrong_chat"    // the open chat is for a different number
	ReasonUnsupported    = "unsupported_text"
	ReasonTypingMismatch = "typing_mismatch"
	// ReasonNotFullscreen: Messages is in split-screen or a floating window, so
	// taps and the left/right test for who wrote a bubble cannot be trusted.
	ReasonNotFullscreen = "window_not_fullscreen"
	// ReasonUnresolvedChat: the chat is saved under a contact name and its number
	// could not be read, so an inbound STOP from it could not be honoured.
	ReasonUnresolvedChat = "unresolved_chat"
)

// eligibility is the one send rule: send only when the app shows this chat
// as end-to-end encrypted. Everything else is refused.
func eligibility(v ChatView) (bool, string) {
	switch {
	case !v.ComposeFound:
		return false, ReasonChatNotOpen
	case v.Kind == KindEncrypted:
		return true, ""
	case v.Kind == KindRCS:
		return false, ReasonE2EEPending
	case v.Kind == KindSMS:
		return false, ReasonNotE2EE
	}
	return false, ReasonUnreadable
}

// SendStatus is what the thread shows for our message.
type SendStatus string

const (
	StatusNone    SendStatus = ""
	StatusSending SendStatus = "sending"
	StatusSent    SendStatus = "sent"
	StatusFailed  SendStatus = "failed"
)

// SentMessage is our just-sent message as the thread shows it.
type SentMessage struct {
	Found    bool
	Status   SendStatus
	Raw      string // the status text or description tail the status came from
	Protocol Kind   // what the message itself says it went out as; KindUnknown if it says nothing
	Desc     string
}

func contains(outer, inner Rect) bool {
	return outer.L <= inner.L && outer.T <= inner.T && outer.R >= inner.R && outer.B >= inner.B
}

// ReadSend finds our just-sent message in the thread, its status, and the
// protocol the message itself carries. The newest outgoing copy wins: a
// thread can hold the same text from the other side too (the emulator echoes
// sends back), and outgoing messages sit on the right of the screen.
func (m *matcher) ReadSend(s *Screen, text string) SentMessage {
	want := norm(text)
	width := 0
	for _, n := range s.Nodes {
		width = max(width, n.Bounds.R)
	}
	var bubble Node
	var found, outgoing bool
	for _, n := range s.Nodes {
		if !m.inPackage(n) || n.ID() != m.BubbleID || norm(n.Text) != want {
			continue
		}
		cx, _ := n.Bounds.Center()
		out := width == 0 || cx > width/2
		if out || !outgoing { // an outgoing copy beats any incoming one; later beats earlier
			if out || !found {
				bubble, found, outgoing = n, true, out
			}
		}
	}
	if !found {
		return SentMessage{Protocol: KindUnknown}
	}
	res := SentMessage{Found: true, Protocol: KindUnknown}

	// The message's own description: "You said <text> ., Now, SMS".
	if wrap, ok := s.Find(func(n Node) bool {
		return m.inPackage(n) && n.ID() == m.MessageContentID && contains(n.Bounds, bubble.Bounds)
	}); ok {
		res.Desc = norm(wrap.Desc)
		tail := res.Desc
		if i := strings.LastIndex(tail, " ., "); i >= 0 {
			tail = tail[i+4:]
		}
		switch {
		case m.msgSMS.MatchString(tail):
			res.Protocol = KindSMS
		case m.msgEnc.MatchString(tail):
			res.Protocol = KindEncrypted
		case m.msgRCS.MatchString(tail):
			res.Protocol = KindRCS
		}
		switch {
		case m.failed.MatchString(tail):
			res.Status, res.Raw = StatusFailed, tail
		case m.sent.MatchString(tail):
			res.Status, res.Raw = StatusSent, tail
		case m.sending.MatchString(tail):
			res.Status, res.Raw = StatusSending, tail
		}
	}

	// The small status line under the message, when the app shows one.
	below := s.FindAll(func(n Node) bool {
		// Never a message bubble or label: their text is what the customer or we
		// typed, and "Payment failed" or "Read our menu" must not set the status.
		return m.inPackage(n) && n.Bounds.T >= bubble.Bounds.T && n.Text != "" &&
			n.ID() != m.BubbleID && n.ID() != m.MessageContentID &&
			(m.sent.MatchString(norm(n.Text)) || m.failed.MatchString(norm(n.Text)) || m.sending.MatchString(norm(n.Text)))
	})
	sort.SliceStable(below, func(i, j int) bool { return below[i].Bounds.T < below[j].Bounds.T })
	if len(below) > 0 {
		t := norm(below[0].Text)
		switch {
		case m.failed.MatchString(t):
			res.Status, res.Raw = StatusFailed, t
		case m.sent.MatchString(t) && res.Status != StatusFailed:
			res.Status, res.Raw = StatusSent, t
		case m.sending.MatchString(t) && res.Status == StatusNone:
			res.Status, res.Raw = StatusSending, t
		}
	}
	return res
}

// tailOf is the app-generated end of a message description, for evidence text.
func tailOf(desc string) string {
	if i := strings.LastIndex(desc, " ., "); i >= 0 {
		return desc[i+4:]
	}
	return desc
}

var headerNumRe = regexp.MustCompile(`^\+?[\d\s().\-]{10,}$`)

// headerNumber is the number in the chat's title bar, or "" when the chat is
// saved under a contact name (nothing to compare).
func (m *matcher) headerNumber(s *Screen) string {
	for _, n := range s.Nodes {
		if m.inPackage(n) && n.Bounds.T >= 0 && n.Bounds.T < 260 && headerNumRe.MatchString(strings.TrimSpace(n.Text)) {
			return digitsOf(n.Text)
		}
	}
	return ""
}

func digitsOf(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// sameNumber compares the last ten digits, so +1 prefixes and formatting do not matter.
func sameNumber(a, b string) bool {
	a, b = digitsOf(a), digitsOf(b)
	if len(a) > 10 {
		a = a[len(a)-10:]
	}
	if len(b) > 10 {
		b = b[len(b)-10:]
	}
	return a != "" && a == b
}

// headerTitle is the open chat's title (a number or a contact name).
func (m *matcher) headerTitle(s *Screen) string {
	if n, ok := s.Find(func(n Node) bool {
		return m.inPackage(n) && m.TitleID != "" && n.ID() == m.TitleID && norm(n.Text) != ""
	}); ok {
		return norm(n.Text)
	}
	for _, n := range s.Nodes {
		if m.inPackage(n) && n.Class == "android.widget.TextView" && n.Bounds.T >= 0 && n.Bounds.T < 260 && norm(n.Text) != "" {
			return norm(n.Text)
		}
	}
	return ""
}
