package rcsandroid

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/zagnu-labs/rcs-messenger/internal/rcs"
)

func TestNumberFromName(t *testing.T) {
	for name, want := range map[string]string{
		"(555) 123-4567":   "+15551234567",
		"+1 555 123 4567":  "+15551234567",
		"1-555-123-4567":   "+15551234567",
		"+44 20 7946 0958": "+442079460958",
		"555.123.4567":     "+15551234567",
	} {
		if got, ok := NumberFromName(name); !ok || got != want {
			t.Errorf("%q: got %q %v want %q", name, got, ok, want)
		}
	}
	for _, name := range []string{"Ann Lee", "Mom", "Pizza 24/7", "12345", ""} {
		if got, ok := NumberFromName(name); ok {
			t.Errorf("%q should not read as a number, got %q", name, got)
		}
	}
}

func TestRealConversationListAndIncomingBubbles(t *testing.T) {
	m, _ := newMatcher(DefaultSelectors())
	rows := m.ConversationList(loadFixture(t, "real_conversation_list.xml"))
	if len(rows) != 1 || rows[0].Name != "(555) 123-4567" || rows[0].Snippet != "Link" || rows[0].Time != "10:26 AM" {
		t.Fatalf("real list: %+v", rows)
	}
	in := m.IncomingMessages(loadFixture(t, "real_sms_thread.xml"))
	if strings.Join(in, "|") != "Hello test|Second test" {
		t.Fatalf("real incoming bubbles: %v", in)
	}
}

// pagesRunner serves a list screen and a thread screen from real dumps, the way
// the app moves between them: the MAIN launch shows the list, tapping a row
// opens the thread, back returns to the list.
type pagesRunner struct {
	mu     sync.Mutex
	list   string
	thread string
	page   string
	taps   int
}

func (p *pagesRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	line := strings.Join(args, " ")
	switch {
	case args[0] == "get-state":
		return []byte("device\n"), nil
	case args[0] == "exec-out" && len(args) > 1 && args[1] == "uiautomator":
		if p.page == "thread" {
			return []byte(p.thread), nil
		}
		return []byte(p.list), nil
	case strings.Contains(line, "android.intent.action.MAIN"):
		p.page = "list"
		return []byte("Status: ok"), nil
	case strings.Contains(line, "input tap"):
		p.page, p.taps = "thread", p.taps+1
	case strings.Contains(line, "KEYCODE_BACK"):
		p.page = "list"
	case strings.Contains(line, "dumpsys window"):
		return []byte("mWakefulness=Awake"), nil
	}
	return nil, nil
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type got struct{ from, text, id string }

func TestPollInboundDeliversNewIncomingMessagesOnce(t *testing.T) {
	r := &pagesRunner{list: readFile(t, "real_conversation_list.xml"), thread: readFile(t, "real_sms_thread.xml")}
	s := newSender(newPhone(KindSMS), newClock(), func(c *Config) { c.Runner = r })
	var mu sync.Mutex
	var msgs []got
	s.OnInbound(func(from, text, id string) { mu.Lock(); msgs = append(msgs, got{from, text, id}); mu.Unlock() })
	ctx := context.Background()

	n, err := s.PollInbound(ctx)
	if err != nil || n != 2 || len(msgs) != 2 {
		t.Fatalf("first pass: n=%d err=%v msgs=%v", n, err, msgs)
	}
	if msgs[0].from != "+15551234567" || msgs[0].text != "Hello test" || !strings.HasPrefix(msgs[0].id, "android:") {
		t.Fatalf("first message: %+v", msgs[0])
	}
	// nothing changed: the chat is not even opened again
	taps := r.taps
	if n, _ := s.PollInbound(ctx); n != 0 || r.taps != taps {
		t.Fatalf("an unchanged chat must be skipped: n=%d taps %d->%d", n, taps, r.taps)
	}
	// the row changes but the same messages are in it: opened, nothing delivered twice
	r.list = strings.Replace(r.list, `text="Link"`, `text="Link 2"`, 1)
	if n, _ := s.PollInbound(ctx); n != 0 || r.taps != taps+1 {
		t.Fatalf("a changed row is opened but old messages are not repeated: n=%d taps=%d", n, r.taps)
	}
	// a new incoming STOP arrives
	r.list = strings.Replace(r.list, `text="Link 2"`, `text="STOP"`, 1)
	r.thread = strings.Replace(r.thread, `text="Second test"`, `text="STOP"`, 2) // the incoming copy is the later one
	n, _ = s.PollInbound(ctx)
	var stop bool
	for _, m := range msgs {
		stop = stop || m.text == "STOP"
	}
	if !stop {
		t.Fatalf("the STOP was not delivered: n=%d msgs=%v", n, msgs)
	}
}

type blocker struct {
	mu    sync.Mutex
	phone []string
}

func (b *blocker) BlockNumber(_ context.Context, phone, _ string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.phone = append(b.phone, phone)
	return true, nil
}

func TestStopReplyBlocksTheNumberThroughTheService(t *testing.T) {
	r := &pagesRunner{list: readFile(t, "real_conversation_list.xml"),
		thread: strings.Replace(readFile(t, "real_sms_thread.xml"), `text="Hello test"`, `text="stop"`, 2)}
	s := newSender(newPhone(KindSMS), newClock(), func(c *Config) { c.Runner = r })
	b := &blocker{}
	svc := rcs.NewService(s, rcs.Config{Enabled: true}).SetBlocker(b)
	s.OnInbound(func(from, text, id string) {
		_, _ = svc.HandleInbound(context.Background(), rcs.Inbound{From: from, Text: text, MessageID: id})
	})
	if _, err := s.PollInbound(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(b.phone) == 0 || b.phone[0] != "+15551234567" {
		t.Fatalf("a STOP reply must block the number, blocked: %v", b.phone)
	}
}

func TestAContactNameChatIsSkippedNotMisattributed(t *testing.T) {
	list := strings.Replace(readFile(t, "real_conversation_list.xml"), `text="(555) 123-4567"`, `text="Ann Lee"`, 1)
	r := &pagesRunner{list: list, thread: readFile(t, "real_sms_thread.xml")}
	s := newSender(newPhone(KindSMS), newClock(), func(c *Config) { c.Runner = r })
	var n int
	s.OnInbound(func(string, string, string) { n++ })
	if _, err := s.PollInbound(context.Background()); err != nil || n != 0 || r.taps != 0 {
		t.Fatalf("a saved contact's chat has no readable number and must be skipped: n=%d taps=%d err=%v", n, r.taps, err)
	}
}
