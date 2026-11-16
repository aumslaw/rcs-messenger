package rcsandroid

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/zagnu-labs/rcs-messenger/internal/providers/rcsdirect"
)

func TestSplitScreenWindowIsRefusedWithNothingTyped(t *testing.T) {
	p := newPhone(KindEncrypted)
	p.rootBounds = "[0,1200][1080,2400]" // Messages on the bottom half only
	s := newSender(p, newClock(), nil)
	_, err := s.Send(context.Background(), "+15551234567", "hello", rcsdirect.SendOptions{})
	var re *rcsdirect.RefusedError
	if !errors.As(err, &re) || re.Reason != ReasonNotFullscreen {
		t.Fatalf("want %s refusal, got %v", ReasonNotFullscreen, err)
	}
	if p.count("input text") != 0 || p.sentMsg != "" {
		t.Fatalf("nothing may be typed or sent in split screen: cmds=%v", p.cmds)
	}
}

func TestFullScreenWindowStillSends(t *testing.T) {
	p := newPhone(KindEncrypted)
	s := newSender(p, newClock(), nil)
	res, err := s.Send(context.Background(), "+15551234567", "hello", rcsdirect.SendOptions{})
	if err != nil || !res.Sent {
		t.Fatalf("full screen send: %+v %v", res, err)
	}
}

// contactRunner is a phone whose one chat is saved as "Ann Lee": the thread
// shows the name, tapping the title opens a details page with the number
// (or none), and the thread holds a STOP from her.
type contactRunner struct {
	mu        sync.Mutex
	page      string
	detailNum string // "" = the details page shows no number
	incoming  []string
	taps      int
}

func bubbleNode(id, text string, l, t, r, b int) string {
	return node("android.widget.TextView", "com.google.android.apps.messaging:id/"+id, text, "", l, t, r, b)
}

func wrap(nodes ...string) []byte {
	return []byte(`<?xml version='1.0' encoding='UTF-8' standalone='yes' ?><hierarchy rotation="0"><node index="0" text="" resource-id="" class="android.widget.FrameLayout" package="com.google.android.apps.messaging" content-desc="" bounds="[0,0][1080,2400]">` +
		strings.Join(nodes, "") + `</node></hierarchy>`)
}

func (c *contactRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	line := strings.Join(args, " ")
	switch {
	case args[0] == "get-state":
		return []byte("device\n"), nil
	case args[0] == "exec-out" && len(args) > 1 && args[1] == "uiautomator":
		switch c.page {
		case "thread":
			ns := []string{
				node("android.widget.TextView", "com.google.android.apps.messaging:id/conversation_title", "Ann Lee", "", 200, 60, 600, 120),
				node("android.widget.EditText", "com.google.android.apps.messaging:id/compose_message_text", "RCS message", "", 100, 2000, 900, 2100),
			}
			for i, m := range c.incoming {
				ns = append(ns, bubbleNode("message_text", m, 74, 400+i*100, 400, 460+i*100))
			}
			return wrap(ns...), nil
		case "details":
			ns := []string{node("android.widget.TextView", "", "Ann Lee", "", 100, 300, 600, 360)}
			if c.detailNum != "" {
				ns = append(ns, node("android.widget.TextView", "", c.detailNum, "", 100, 400, 600, 460))
			}
			return wrap(ns...), nil
		}
		return wrap(
			node("android.support.v7.widget.RecyclerView", "com.google.android.apps.messaging:id/list", "", "", 0, 200, 1080, 2300),
			node("android.widget.TextView", "com.google.android.apps.messaging:id/conversation_name", "Ann Lee", "", 200, 300, 800, 360),
			node("android.widget.TextView", "com.google.android.apps.messaging:id/conversation_snippet", "STOP", "", 200, 360, 800, 420),
			node("android.widget.TextView", "com.google.android.apps.messaging:id/conversation_timestamp", "10:26 AM", "", 900, 300, 1060, 360),
		), nil
	case strings.Contains(line, "android.intent.action.MAIN"):
		c.page = "list"
		return []byte("Status: ok"), nil
	case strings.Contains(line, "input tap"):
		c.taps++
		m := tapRe.FindStringSubmatch(line)
		var y int
		fmt.Sscan(m[2], &y)
		switch {
		case c.page == "list":
			c.page = "thread"
		case c.page == "thread" && y < 260:
			c.page = "details"
		}
	case strings.Contains(line, "KEYCODE_BACK"):
		if c.page == "details" {
			c.page = "thread"
		} else {
			c.page = "list"
		}
	case strings.Contains(line, "dumpsys window"):
		return []byte("mWakefulness=Awake"), nil
	}
	return nil, nil
}

func TestStopInContactNamedChatOptsTheNumberOut(t *testing.T) {
	r := &contactRunner{incoming: []string{"STOP"}, detailNum: "+1 (555) 987-6543"}
	s := newSender(newPhone(KindSMS), newClock(), func(c *Config) { c.Runner = r })
	var from, text string
	s.OnInbound(func(f, tx, id string) error { from, text = f, tx; return nil })
	n, err := s.PollInbound(context.Background())
	if err != nil || n != 1 || from != "+15559876543" || text != "STOP" {
		t.Fatalf("a STOP in a contact-named chat must reach the handler with the real number: n=%d err=%v from=%q text=%q", n, err, from, text)
	}
	if s.isUnresolved("Ann Lee") {
		t.Fatal("a resolved chat must not be flagged unresolved")
	}
}

func TestUnresolvedContactChatIsRecordedAndSendRefuses(t *testing.T) {
	r := &contactRunner{incoming: []string{"STOP"}} // details page shows no number
	in := newSender(newPhone(KindSMS), newClock(), func(c *Config) { c.Runner = r })
	called := 0
	in.OnInbound(func(string, string, string) error { called++; return nil })
	if _, err := in.PollInbound(context.Background()); err != nil {
		t.Fatal(err)
	}
	if called != 0 {
		t.Fatalf("with no number nothing can be attributed: %d", called)
	}
	if !in.isUnresolved("Ann Lee") {
		t.Fatal("the chat must be recorded as unresolved, not skipped silently")
	}

	// The same sender then opens that chat to send: it must refuse, typing nothing.
	p := newPhone(KindEncrypted)
	p.header = "Ann Lee"
	in.dev.r = p
	_, err := in.Send(context.Background(), "+15559876543", "hello", rcsdirect.SendOptions{})
	var re *rcsdirect.RefusedError
	if !errors.As(err, &re) || re.Reason != ReasonUnresolvedChat {
		t.Fatalf("want %s refusal, got %v", ReasonUnresolvedChat, err)
	}
	if p.count("input text") != 0 {
		t.Fatal("nothing may be typed into an unresolved chat")
	}
}

// Identical text that scrolls off screen must not collide with the earlier
// copy, and re-reading the same screen must not deliver anything twice.
func TestIdenticalMessagesGetDistinctStableIDs(t *testing.T) {
	m, _ := newMatcher(DefaultSelectors())
	mk := func(ns ...string) *Screen { sc, _ := ParseScreen(wrap(ns...)); return sc }
	in := func(text string, y int) string { return bubbleNode("message_text", text, 74, y, 400, y+60) }
	out := func(text string, y int) string { return bubbleNode("message_text", text, 700, y, 1000, y+60) }

	// same screen: two identical bubbles
	two := mk(in("ok", 400), in("ok", 600))
	b := m.IncomingBubbles(two)
	if len(b) != 2 || messageKey("+1", b[0]) == messageKey("+1", b[1]) {
		t.Fatalf("two identical bubbles on screen need distinct ids: %+v", b)
	}
	// stable across re-reads of the same screen
	b2 := m.IncomingBubbles(two)
	if messageKey("+1", b[1]) != messageKey("+1", b2[1]) {
		t.Fatal("ids must be stable across re-reads")
	}

	// delivery: first "ok" alone, then the first has scrolled off and a new "ok" follows our reply
	delivered := map[string]int{}
	screen := mk(in("ok", 400))
	deliver := func() int {
		n := 0
		for _, bb := range m.IncomingBubbles(screen) {
			id := messageKey("+1", bb)
			if delivered[id] == 0 {
				n++
			}
			delivered[id]++
		}
		return n
	}
	if deliver() != 1 {
		t.Fatal("first ok")
	}
	screen = mk(out("got it", 400), in("ok", 500))
	if deliver() != 1 {
		t.Fatal("a second identical ok after the first scrolled off must get its own id")
	}
	if deliver() != 0 {
		t.Fatal("re-reading the same screen must deliver nothing")
	}
}

func TestPollInboundDeliversASecondIdenticalMessageOnce(t *testing.T) {
	r := &pagesRunner{list: readFile(t, "real_conversation_list.xml"), thread: readFile(t, "real_sms_thread.xml")}
	s := newSender(newPhone(KindSMS), newClock(), func(c *Config) { c.Runner = r })
	var ids []string
	s.OnInbound(func(_, text, id string) error { ids = append(ids, text+"/"+id); return nil })
	ctx := context.Background()
	if n, _ := s.PollInbound(ctx); n != 2 {
		t.Fatalf("first pass: %d", n)
	}
	// the same text arrives again (list row changes) after an outgoing reply pushed the old one up
	r.list = strings.Replace(r.list, `text="Link"`, `text="Second test"`, 1)
	r.thread = strings.Replace(r.thread, `text="Hello test"`, `text="Second test"`, 2)
	n, _ := s.PollInbound(ctx)
	if n != 1 {
		t.Fatalf("the repeated text is a new message and must be delivered once, got %d", n)
	}
	again, _ := s.PollInbound(ctx)
	if again != 0 {
		t.Fatalf("an unchanged screen must deliver nothing, got %d (first changed pass %d)", again, n)
	}
}
