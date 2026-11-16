package rcsandroid

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zagnu-labs/rcs-messenger/internal/providers/rcsdirect"
)

const msg = "Hey Ann - I'm the founder of Zagnu.com.\n\nAny interest in chatting about it?"

func TestParseScreenIgnoresTrailingNoise(t *testing.T) {
	p := newPhone(KindSMS)
	p.opened = true
	s, err := ParseScreen(p.dumpLocked())
	if err != nil || len(s.Nodes) < 3 {
		t.Fatalf("parse: %v nodes=%d", err, len(s.Nodes))
	}
	if _, err := ParseScreen([]byte("ERROR: null root node")); !errors.Is(err, ErrNoHierarchy) {
		t.Fatalf("want ErrNoHierarchy, got %v", err)
	}
}

func readKind(t *testing.T, p *fakePhone) ChatView {
	t.Helper()
	p.opened = true
	scr, err := ParseScreen(p.dumpLocked())
	if err != nil {
		t.Fatal(err)
	}
	m, _ := newMatcher(DefaultSelectors())
	return m.ReadChat(scr)
}

func TestReadChatKinds(t *testing.T) {
	cases := []struct {
		name string
		k    Kind
		want Kind
	}{{"sms", KindSMS, KindSMS}, {"plain rcs", KindRCS, KindRCS}, {"encrypted", KindEncrypted, KindEncrypted}}
	for _, c := range cases {
		if v := readKind(t, newPhone(c.k)); v.Kind != c.want || v.Evidence == "" {
			t.Errorf("%s: got %s (%s)", c.name, v.Kind, v.Evidence)
		}
	}
	// the banner alone (no padlock) is NOT enough: message text can say the same words
	p := newPhone(KindEncrypted)
	p.opened = true
	scr, _ := ParseScreen(p.dumpLocked())
	var kept []Node
	for _, n := range scr.Nodes {
		if n.ID() != "compose_lock" {
			kept = append(kept, n)
		}
	}
	m, _ := newMatcher(DefaultSelectors())
	if v := m.ReadChat(&Screen{Nodes: kept}); v.Kind != KindRCS {
		t.Errorf("banner only must not read as encrypted: %s (%s)", v.Kind, v.Evidence)
	}
	// a stale "encrypted" line above an SMS hint must lose to the hint
	stale := append([]Node{{Package: DefaultSelectors().Package, Text: "This chat is now end-to-end encrypted"}}, kept...)
	for i, n := range stale {
		if n.ID() == "compose_message_text" {
			stale[i].Text = "Text message"
		}
	}
	if v := m.ReadChat(&Screen{Nodes: stale}); v.Kind != KindSMS {
		t.Errorf("stale banner beat the SMS hint: %s", v.Kind)
	}
	// nothing on screen
	if v := m.ReadChat(&Screen{}); v.Kind != KindUnknown || v.ComposeFound {
		t.Errorf("empty screen: %+v", v)
	}
}

// realShaped builds a chat screen shaped like the real Moto G dumps: the padlock
// is its own node with description "Lock icon", the banner and every bubble share
// the id message_text, and a bubble's description carries its text and label.
func realShaped(hint string, lock bool, extra ...Node) *Screen {
	pkg := DefaultSelectors().Package
	nodes := []Node{
		{Package: pkg, Class: "android.widget.EditText", ResourceID: pkg + ":id/compose_message_text", Text: hint},
		{Package: pkg, ResourceID: pkg + ":id/message_text", Text: "RCS chat with (555) 010-0100"},
	}
	if lock {
		nodes = append(nodes, Node{Package: pkg, Desc: "Lock icon"})
	}
	return &Screen{Nodes: append(nodes, extra...)}
}

func TestRealShapedEncryptedChatReadsEncrypted(t *testing.T) {
	m, _ := newMatcher(DefaultSelectors())
	if v := m.ReadChat(realShaped("RCS message", true)); v.Kind != KindEncrypted || !strings.Contains(v.Evidence, "Lock icon") {
		t.Fatalf("a chat with the Lock icon node must read encrypted, with that node as evidence: %s (%s)", v.Kind, v.Evidence)
	}
}

// A plain RCS chat (an iPhone on RCS) must never read as encrypted because a
// message in it says so. Each case below is a way text on screen can contain
// the words the old patterns looked for.
func TestMessageTextCannotMakeAPlainRCSChatLookEncrypted(t *testing.T) {
	pkg := DefaultSelectors().Package
	m, _ := newMatcher(DefaultSelectors())
	bubble := func(text, desc string) Node {
		return Node{Package: pkg, ResourceID: pkg + ":id/message_text", Text: text, Desc: desc}
	}
	cases := map[string]Node{
		"customer asks the question":    bubble("is this end-to-end encrypted?", "(555) 010-0100 said is this end-to-end encrypted? 4:09 PM ."),
		"customer sends the banner":     bubble("This chat is now end-to-end encrypted. Learn more", ""),
		"customer says lock icon":       bubble("lock icon", "(555) 010-0100 said lock icon 4:09 PM ., Not end-to-end encrypted"),
		"label forged inside the text":  bubble("x ., Read. End-to-end encrypted message y", "(555) 010-0100 said x ., Read. End-to-end encrypted message y 4:09 PM ."),
		"label forged, plain tail":      bubble("x ., End-to-end encrypted message", "(555) 010-0100 said x ., End-to-end encrypted message 4:09 PM ., Not end-to-end encrypted"),
		"incoming label not encrypted":  bubble("hi", "(555) 010-0100 said hi 4:09 PM ., Not end-to-end encrypted"),
		"label wrapper with its text":   {Package: pkg, ResourceID: pkg + ":id/message_content", Desc: "You said encryption padlock lock 4:09 PM ., Now"},
		"customer text word encryption": bubble("encryption", "(555) 010-0100 said encryption"),
	}
	for name, n := range cases {
		if v := m.ReadChat(realShaped("RCS message", false, n)); v.Kind != KindRCS {
			t.Errorf("%s: plain RCS chat read as %s (%s)", name, v.Kind, v.Evidence)
		}
	}
}

// A draft left in the box hides the hint. With the banner on screen that used to
// read as encrypted; nothing may now be guessed from the thread.
func TestLeftoverDraftWithBannerIsNotEncrypted(t *testing.T) {
	pkg := DefaultSelectors().Package
	m, _ := newMatcher(DefaultSelectors())
	banner := Node{Package: pkg, ResourceID: pkg + ":id/message_text", Text: "This chat is now end-to-end encrypted. Learn more"}
	if v := m.ReadChat(realShaped("a draft someone left behind", false, banner)); v.Kind != KindUnknown {
		t.Fatalf("a draft plus the banner must read unknown, got %s (%s)", v.Kind, v.Evidence)
	}
}

// The settle read says encrypted, but by the read taken right before typing the
// chat has dropped back to plain RCS (the contact changed device). Nothing may be
// typed or sent.
func TestChatThatDropsToPlainRCSAfterSettlingIsRefused(t *testing.T) {
	p := newPhone(KindEncrypted, KindRCS)
	_, err := newSender(p, newClock(), nil).Send(context.Background(), "+15551234567", msg, rcsdirect.SendOptions{})
	var refused *rcsdirect.RefusedError
	if !errors.As(err, &refused) || refused.Reason != ReasonE2EEPending {
		t.Fatalf("a chat that dropped to plain RCS must be refused e2ee_pending, got %v", err)
	}
	if p.sentMsg != "" || p.compose != "" {
		t.Fatalf("nothing may be sent or left in the box: sent=%q box=%q", p.sentMsg, p.compose)
	}
}

// The caller gives up (HTTP disconnect) after the send button was tapped. The
// message is out, so the result must say so, not come back as a bare error that
// gets recorded as "nothing went out" and retried into a duplicate.
func TestCallerCancelAfterTheTapStillReturnsAResult(t *testing.T) {
	p := newPhone(KindEncrypted)
	p.statuses = []string{"Sending"}
	ctx, cancel := context.WithCancel(context.Background())
	clk := newClock()
	s := newSender(p, clk, func(c *Config) {
		c.Sleep = func(cx context.Context, d time.Duration) error {
			if p.sentMsg != "" { // the tap has happened: the caller goes away now
				cancel()
			}
			return clk.Sleep(cx, d)
		}
	})
	res, err := s.Send(ctx, "+15551234567", msg, rcsdirect.SendOptions{Wait: 6 * time.Second})
	if err != nil {
		t.Fatalf("a send cancelled after the tap must return a result, got error %v", err)
	}
	if !res.Sent || !strings.HasPrefix(res.FinalStatus, "sent_unconfirmed") {
		t.Fatalf("the message is out and must read sent_unconfirmed: %+v", res)
	}
}

// A message whose own words look like a status ("Payment failed", "Read our
// menu") must not set the status. Before the fix the failed-pattern matched the
// bubble itself, the result said Sent=false, and a caller would retry.
func TestMessageTextCannotSetTheSendStatus(t *testing.T) {
	for _, text := range []string{"Payment failed, call us", "Read our menu and delivered fresh daily"} {
		p := newPhone(KindEncrypted)
		p.statuses = []string{"Sending"}
		res, err := newSender(p, newClock(), nil).Send(context.Background(), "+15551234567", text, rcsdirect.SendOptions{Wait: 6 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		if !res.Sent || strings.HasPrefix(res.FinalStatus, "failed") || res.FinalStatus == text {
			t.Fatalf("%q: message text must not decide the status: %+v", text, res)
		}
		if !strings.HasPrefix(res.FinalStatus, "sent_unconfirmed") {
			t.Fatalf("%q: the real status is still Sending, so this must be sent_unconfirmed: %+v", text, res)
		}
	}
}

// A long encrypted thread has scrolled the thread-start lock away. The app's own
// label on a message ("..., Read. End-to-end encrypted message") is enough to
// make typing worth trying, and it cannot be forged from message text.
func TestLongThreadIsRecognisedByTheAppsMessageLabel(t *testing.T) {
	pkg := DefaultSelectors().Package
	m, _ := newMatcher(DefaultSelectors())
	labelled := Node{Package: pkg, ResourceID: pkg + ":id/message_text", Text: "hi",
		Desc: "You said  hi Oct 29, 6:05 PM ., Read. End-to-end encrypted message"}
	if v := m.ReadChat(realShaped("RCS message", false, labelled)); v.Kind != KindEncrypted || !strings.Contains(v.Evidence, "final check") {
		t.Fatalf("a labelled message in an RCS chat must read encrypted (pending the send button): %s (%s)", v.Kind, v.Evidence)
	}
}

// The send button is the authority. The thread can look encrypted (a stale lock
// or label) while the button says the app will not send encrypted: nothing may
// be sent and the typed text must be cleared.
func TestSendButtonThatIsNotEncryptedIsTheFinalGate(t *testing.T) {
	for _, desc := range []string{"Send message", "Send RCS message", "Send", "Send encrypted message now please"} {
		p := newPhone(KindEncrypted)
		p.sendDescOverride = desc
		_, err := newSender(p, newClock(), nil).Send(context.Background(), "+15551234567", msg, rcsdirect.SendOptions{})
		var refused *rcsdirect.RefusedError
		if !errors.As(err, &refused) || refused.Reason != ReasonE2EEPending {
			t.Fatalf("button %q: must be refused e2ee_pending, got %v", desc, err)
		}
		if p.sentMsg != "" || p.compose != "" {
			t.Fatalf("button %q: nothing may be sent or left typed: sent=%q box=%q", desc, p.sentMsg, p.compose)
		}
	}
}

// A leftover task or a failed open must never type into someone else's chat.
func TestChatForADifferentNumberIsNeverTypedInto(t *testing.T) {
	p := newPhone(KindEncrypted)
	p.header = "(555) 000-1111"
	_, err := newSender(p, newClock(), nil).Send(context.Background(), "+15551234567", msg, rcsdirect.SendOptions{})
	var refused *rcsdirect.RefusedError
	if !errors.As(err, &refused) || refused.Reason != ReasonWrongChat {
		t.Fatalf("a chat titled with another number must be refused wrong_chat, got %v", err)
	}
	if p.compose != "" || p.sentMsg != "" || p.count("shell input text") != 0 {
		t.Fatalf("nothing may be typed or sent: box=%q sent=%q typed=%d", p.compose, p.sentMsg, p.count("shell input text"))
	}
}

func TestChatWithTheRightNumberInTheTitleSends(t *testing.T) {
	p := newPhone(KindEncrypted)
	p.header = "(555) 123-4567"
	res, err := newSender(p, newClock(), nil).Send(context.Background(), "+15551234567", msg, rcsdirect.SendOptions{Wait: 6 * time.Second})
	if err != nil || !res.Sent {
		t.Fatalf("a matching title must not block the send: %+v %v", res, err)
	}
}

// With no validated internet, RCS cannot send and messages sit in "Sending"
// forever. Refuse before opening a chat.
func TestPhoneWithoutInternetIsRefusedBeforeAnythingIsOpened(t *testing.T) {
	p := newPhone(KindEncrypted)
	p.offline = true
	_, err := newSender(p, newClock(), nil).Send(context.Background(), "+15551234567", msg, rcsdirect.SendOptions{})
	var refused *rcsdirect.RefusedError
	if !errors.As(err, &refused) || refused.Reason != ReasonPhoneOffline {
		t.Fatalf("no internet must be refused phone_offline, got %v", err)
	}
	if p.count("shell am start") != 0 {
		t.Fatal("no chat may be opened while the phone is offline")
	}
	h, _ := newSender(p, newClock(), nil).Health(context.Background())
	if h.SessionActive || !strings.Contains(h.LastError, "internet") {
		t.Fatalf("health must say there is no internet: %+v", h)
	}
}

// adb errors reach logs and HTTP responses: typed text, the PIN and the number stay out.
func TestAdbErrorTextHidesTheMessageThePINAndTheNumber(t *testing.T) {
	got := redactArgs([]string{"-s", "SER", "shell", "input text 'Hi%sAmy%s1234'", "shell", "am start -d 'sms:+17346951196' -p x"})
	for _, leak := range []string{"Amy", "1234", "17346951196"} {
		if strings.Contains(got, leak) {
			t.Fatalf("%q leaked into %q", leak, got)
		}
	}
}

// Busy with a send and never read: Health must not claim ready.
func TestHealthDoesNotClaimReadyWhileBusyAndUnread(t *testing.T) {
	s := newSender(newPhone(KindEncrypted), newClock(), nil)
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.Health(context.Background())
	if err != nil || h.Connected || h.PhoneResponding || h.SessionActive {
		t.Fatalf("a busy phone with no reading must not read as ready: %+v %v", h, err)
	}
}

func TestSendWaitsForThePadlockThenSendsEncrypted(t *testing.T) {
	// a brand new chat reads "Text message", then "RCS message", then the padlock
	p := newPhone(KindSMS, KindSMS, KindRCS, KindEncrypted)
	p.statuses = []string{"Sending", "Sent"}
	clk := newClock()
	res, err := newSender(p, clk, nil).Send(context.Background(), "+15551234567", msg, rcsdirect.SendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Sent || res.MessageType != 4 || !res.DeliveredAsRCS() {
		t.Fatalf("not delivered as RCS: %+v", res)
	}
	if res.Check == nil || res.Check.E2EE != "on" || !res.Check.Eligible || res.Check.E2EECapability != "yes" {
		t.Fatalf("before-check should show encrypted: %+v", res.Check)
	}
	if res.After == nil || res.After.E2EE != "on" {
		t.Fatalf("after-check should show encrypted: %+v", res.After)
	}
	if res.FinalStatus != "Sent" || strings.Join(res.StatusHistory, ",") != "sending,sent" {
		t.Fatalf("status: %q %v", res.FinalStatus, res.StatusHistory)
	}
	if p.sentMsg != msg {
		t.Fatalf("typed text differs:\n%q\n%q", p.sentMsg, msg)
	}
	if !strings.Contains(strings.Join(p.cmds, "\n"), "sms:+15551234567") {
		t.Fatal("never opened the chat for the number")
	}
	if len(clk.sleeps) < 2 {
		t.Fatalf("did not wait for the lookup to settle: %v", clk.sleeps)
	}
}

func TestPlainRCSIsNeverSent(t *testing.T) {
	// an iPhone on plain RCS reads "RCS message" with no padlock, forever
	p := newPhone(KindRCS)
	_, err := newSender(p, newClock(), nil).Send(context.Background(), "+15551234567", msg, rcsdirect.SendOptions{})
	var ref *rcsdirect.RefusedError
	if !errors.As(err, &ref) || ref.Reason != ReasonE2EEPending || ref.Permanent() {
		t.Fatalf("want retryable e2ee_pending refusal, got %v", err)
	}
	if ref.Check == nil || ref.Check.E2EE != "off" || ref.Check.Eligible {
		t.Fatalf("check: %+v", ref.Check)
	}
	if p.count("shell input text") != 0 || p.count("shell input tap") != 0 {
		t.Fatalf("typed or tapped on a chat that is not encrypted: %v", p.cmds)
	}
}

func TestSMSOnlyContactIsNeverSent(t *testing.T) {
	p := newPhone(KindSMS)
	_, err := newSender(p, newClock(), nil).Send(context.Background(), "+15551234567", msg, rcsdirect.SendOptions{})
	var ref *rcsdirect.RefusedError
	if !errors.As(err, &ref) || ref.Reason != ReasonNotE2EE || !ref.Permanent() {
		t.Fatalf("want permanent not_e2ee refusal, got %v", err)
	}
	if ref.Check.Transport != "SMS" || ref.Check.E2EECapability != "no" {
		t.Fatalf("check: %+v", ref.Check)
	}
	if p.count("shell input text") != 0 {
		t.Fatal("typed into an SMS chat")
	}
}

func TestUnreadableScreenRefuses(t *testing.T) {
	p := newPhone(KindEncrypted)
	// nothing opened: dumps fail, so the chat can never be read
	_, err := newSender(p, newClock(), func(c *Config) { c.Runner = &noOpen{p} }).Send(context.Background(), "+15551234567", msg, rcsdirect.SendOptions{})
	var ref *rcsdirect.RefusedError
	if !errors.As(err, &ref) || ref.Reason != ReasonChatNotOpen {
		t.Fatalf("want chat_not_open, got %v", err)
	}
}

// noOpen is a phone whose chat never opens.
type noOpen struct{ p *fakePhone }

func (n *noOpen) Run(ctx context.Context, args ...string) ([]byte, error) {
	if len(args) > 1 && strings.HasPrefix(args[1], "am start") {
		return []byte("Status: ok"), nil
	}
	return n.p.Run(ctx, args...)
}

func TestTypingMismatchIsClearedNotSent(t *testing.T) {
	p := newPhone(KindEncrypted)
	p.typedBad = true
	_, err := newSender(p, newClock(), nil).Send(context.Background(), "+15551234567", msg, rcsdirect.SendOptions{})
	var ref *rcsdirect.RefusedError
	if !errors.As(err, &ref) || ref.Reason != ReasonTypingMismatch {
		t.Fatalf("want typing_mismatch, got %v", err)
	}
	if p.sentMsg != "" || p.compose != "" {
		t.Fatalf("a wrong message was sent or left in the box: sent=%q box=%q", p.sentMsg, p.compose)
	}
}

func TestUnsupportedTextIsRefusedBeforeTouchingThePhone(t *testing.T) {
	for _, text := range []string{"hello 😀", "100%s sure", "café"} {
		p := newPhone(KindEncrypted)
		_, err := newSender(p, newClock(), nil).Send(context.Background(), "+15551234567", text, rcsdirect.SendOptions{})
		var ref *rcsdirect.RefusedError
		if !errors.As(err, &ref) || ref.Reason != ReasonUnsupported {
			t.Errorf("%q: want unsupported_text, got %v", text, err)
		}
		if len(p.cmds) != 0 {
			t.Errorf("%q: touched the phone: %v", text, p.cmds)
		}
	}
}

func TestFailedSendIsReportedFailed(t *testing.T) {
	p := newPhone(KindEncrypted)
	p.statuses = []string{"Sending", "Not sent. Tap to try again"}
	res, err := newSender(p, newClock(), nil).Send(context.Background(), "+15551234567", msg, rcsdirect.SendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent || !strings.HasPrefix(res.FinalStatus, "failed") {
		t.Fatalf("a failed send must not read as sent: %+v", res)
	}
}

func TestSentButStatusNeverArrivesIsNotRetriedIntoADuplicate(t *testing.T) {
	p := newPhone(KindEncrypted)
	p.statuses = []string{"Sending"}
	res, err := newSender(p, newClock(), nil).Send(context.Background(), "+15551234567", msg, rcsdirect.SendOptions{Wait: 6 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Sent || !strings.HasPrefix(res.FinalStatus, "sent_unconfirmed") {
		t.Fatalf("an unconfirmed send is out of the box and must count as sent: %+v", res)
	}
}

// Regression (2026-11-13, real phone): the same text had been sent to this chat
// before, the send tap did not register, and the old identical bubble was read
// as our new message, so the probe reported sent=true with the text still in
// the compose box.
func TestOldIdenticalMessageIsNotMistakenForOurSend(t *testing.T) {
	p := newPhone(KindEncrypted)
	p.oldMsg, p.dropSend = msg, true
	res, err := newSender(p, newClock(), nil).Send(context.Background(), "+15551234567", msg, rcsdirect.SendOptions{Wait: 6 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent || res.FinalStatus != "send_not_registered" {
		t.Fatalf("an unregistered send must not read as sent when an identical older message exists: %+v", res)
	}
	if p.compose != "" {
		t.Fatalf("the unsent text must be cleared from the box, got %q", p.compose)
	}
}

// The same history with a send that does register is still a success.
func TestRepeatedMessageThatDoesSendIsReportedSent(t *testing.T) {
	p := newPhone(KindEncrypted)
	p.oldMsg = msg
	res, err := newSender(p, newClock(), nil).Send(context.Background(), "+15551234567", msg, rcsdirect.SendOptions{Wait: 6 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Sent {
		t.Fatalf("a send that registered must read as sent: %+v", res)
	}
}

func TestChatThatTurnsOutSMSAfterwardsIsFlagged(t *testing.T) {
	p := newPhone(KindEncrypted)
	p.afterKind = KindSMS // the phone fell back after the check
	res, err := newSender(p, newClock(), nil).Send(context.Background(), "+15551234567", msg, rcsdirect.SendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Sent || res.MessageType != 1 || res.After.E2EE != "off" {
		t.Fatalf("the after-read must expose the fallback: %+v", res)
	}
}

func TestLockedPhoneIsRefused(t *testing.T) {
	p := newPhone(KindEncrypted)
	p.locked = true
	_, err := newSender(p, newClock(), nil).Send(context.Background(), "+15551234567", msg, rcsdirect.SendOptions{})
	var ref *rcsdirect.RefusedError
	if !errors.As(err, &ref) || ref.Reason != ReasonPhoneLocked || ref.Permanent() {
		t.Fatalf("want retryable phone_locked, got %v", err)
	}
	if p.count("shell input text") != 0 && !strings.Contains(strings.Join(p.cmds, "\n"), "KEYCODE_WAKEUP") {
		t.Fatal("unexpected commands")
	}
}

func TestSendsAreSpacedLikeAPerson(t *testing.T) {
	p := newPhone(KindEncrypted)
	clk := newClock()
	s := newSender(p, clk, func(c *Config) { c.MinGap = 45 * time.Second })
	for i := 0; i < 2; i++ {
		p.sentMsg = ""
		if _, err := s.Send(context.Background(), "+15551234567", "hi there", rcsdirect.SendOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	var long int
	for _, d := range clk.sleeps {
		if d >= 30*time.Second {
			long++
		}
	}
	if long != 1 {
		t.Fatalf("want exactly one long pause before the second send, sleeps=%v", clk.sleeps)
	}
}

func TestCheckReportsWithoutSending(t *testing.T) {
	for k, want := range map[Kind]string{KindEncrypted: "on", KindRCS: "off", KindSMS: "off"} {
		p := newPhone(k)
		c, err := newSender(p, newClock(), nil).Check(context.Background(), "+15551234567")
		if err != nil || c.E2EE != want || c.Eligible != (k == KindEncrypted) {
			t.Errorf("%s: %+v %v", k, c, err)
		}
		if p.count("shell input text") != 0 {
			t.Errorf("%s: a check typed text", k)
		}
	}
}

func TestHealth(t *testing.T) {
	p := newPhone(KindEncrypted)
	h, err := newSender(p, newClock(), nil).Health(context.Background())
	if err != nil || !h.Ready() || h.LastError != "" {
		t.Fatalf("a connected, ready phone: %+v %v", h, err)
	}
	p.state = "offline"
	h, _ = newSender(p, newClock(), nil).Health(context.Background())
	if h.Ready() || h.Connected || h.LastError == "" {
		t.Fatalf("an offline phone must not be ready: %+v", h)
	}
}

func TestThreadReadsMessages(t *testing.T) {
	p := newPhone(KindEncrypted)
	p.sentMsg = "earlier message"
	p.opened = true
	th, err := newSender(p, newClock(), nil).Thread(context.Background(), "+15551234567", 10)
	if err != nil {
		t.Fatal(err)
	}
	// opening the chat resets the fake phone, so only the check is meaningful here
	if th.Check == nil || th.Check.E2EE != "on" {
		t.Fatalf("%+v", th.Check)
	}
}

func TestSendButtonSaysSMSIsTheLastLineOfDefence(t *testing.T) {
	// the hint read encrypted, then the phone dropped to SMS before the tap
	p := newPhone(KindEncrypted)
	p.sendDescOverride = "Send SMS"
	_, err := newSender(p, newClock(), nil).Send(context.Background(), "+15551234567", msg, rcsdirect.SendOptions{})
	var ref *rcsdirect.RefusedError
	if !errors.As(err, &ref) || ref.Reason != ReasonNotE2EE || !strings.Contains(ref.Check.E2EEEvidence, "send button") {
		t.Fatalf("want a refusal from the send button label, got %v", err)
	}
	if p.sentMsg != "" || p.compose != "" {
		t.Fatalf("message was sent or left in the box: sent=%q box=%q", p.sentMsg, p.compose)
	}
}

func TestMessageLabelSaysSMSBeatsAnEncryptedChat(t *testing.T) {
	p := newPhone(KindEncrypted)
	p.tailOverride = "Now, SMS" // the message itself went out as SMS
	res, err := newSender(p, newClock(), nil).Send(context.Background(), "+15551234567", msg, rcsdirect.SendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.MessageType != 1 || res.DeliveredAsRCS() || res.After.E2EE != "off" {
		t.Fatalf("a message labelled SMS must read as SMS: %+v after=%+v", res, res.After)
	}
}

func TestOurOutgoingCopyIsReadNotTheEcho(t *testing.T) {
	p := newPhone(KindEncrypted)
	p.echo = true
	res, err := newSender(p, newClock(), nil).Send(context.Background(), "+15551234567", msg, rcsdirect.SendOptions{})
	if err != nil || !res.Sent {
		t.Fatalf("%+v %v", res, err)
	}
}

// The tests below read screens captured from a real Google Messages on an
// Android 14 emulator (SMS only: the emulator has no RCS).
func loadFixture(t *testing.T, name string) *Screen {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ParseScreen(raw)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRealScreenReadsAsSMS(t *testing.T) {
	m, _ := newMatcher(DefaultSelectors())
	v := m.ReadChat(loadFixture(t, "real_sms_thread.xml"))
	if v.Kind != KindSMS || v.Hint != "Text message" || !v.ComposeFound {
		t.Fatalf("real SMS chat read as %+v", v)
	}
	if ok, reason := eligibility(v); ok || reason != ReasonNotE2EE {
		t.Fatalf("an SMS chat must be refused: %v %s", ok, reason)
	}
}

func TestRealScreenWithTypedTextHasSMSSendButton(t *testing.T) {
	m, _ := newMatcher(DefaultSelectors())
	scr := loadFixture(t, "real_sms_typed.xml")
	btn, ok := m.sendButton(scr)
	if !ok || !m.sendSMS.MatchString(norm(btn.Desc)) {
		t.Fatalf("send button on a real screen: %+v ok=%v", btn, ok)
	}
	box, _ := m.compose(scr)
	if norm(box.Text) != "Typed not sent" {
		t.Fatalf("typed text on a real screen: %q", box.Text)
	}
	if v := m.ReadChat(scr); v.ComposeText == "" || v.Hint != "" {
		t.Fatalf("with text typed the hint is hidden: %+v", v)
	}
}

func TestRealThreadMessageCarriesItsProtocol(t *testing.T) {
	m, _ := newMatcher(DefaultSelectors())
	scr := loadFixture(t, "real_sms_thread.xml")
	// the fixture holds the earlier "Hello test" message sent over SMS
	sm := m.ReadSend(scr, "Hello test")
	if !sm.Found || sm.Protocol != KindSMS || !strings.Contains(sm.Desc, "You said") {
		t.Fatalf("real sent message: %+v", sm)
	}
}
