package rcsandroid

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// fakePhone stands in for adb + a phone running Google Messages. Its screens
// are synthetic (built from the layout the app is described as having), so
// these tests prove the logic, not the real app's wording: see
// real_device_test.md in the repo docs for what has to be checked on a phone.
type fakePhone struct {
	mu sync.Mutex

	cmds   []string // every adb command, in order
	state  string   // adb get-state
	locked bool

	// kinds is what the open chat reads on each successive screen dump before
	// a send; the last one repeats. afterKind is what it reads once sent.
	kinds     []Kind
	dumps     int
	afterKind Kind
	opened    bool

	compose          string
	typedBad         bool // drop a character while typing
	sentMsg          string
	statuses         []string // status text under the message, one per dump after the send; last repeats
	sdumps           int
	noBanner         bool
	sendDescOverride string // what the send button says, whatever the hint says
	tailOverride     string // the protocol tail on our sent message's description
	echo             bool   // the other side echoes our text back (the emulator does)
	oldMsg           string // an earlier, identical outgoing message already in the thread (status "Read")
	dropSend         bool   // the send tap does not register: the text stays in the box
	header           string // the number in the title bar ("" = a contact name, nothing to read)
	offline          bool   // no validated internet connection
	rootBounds       string // the app window's bounds in the dump ("" = whole 1080x2400 screen)
}

func newPhone(kinds ...Kind) *fakePhone {
	return &fakePhone{state: "device", kinds: kinds, afterKind: KindEncrypted, statuses: []string{"Sent"}}
}

func (p *fakePhone) count(prefix string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, c := range p.cmds {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

var tapRe = regexp.MustCompile(`input tap (\d+) (\d+)`)

func (p *fakePhone) Run(_ context.Context, args ...string) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	line := strings.Join(args, " ")
	p.cmds = append(p.cmds, line)
	switch args[0] {
	case "get-state":
		if p.state != "device" {
			return []byte(p.state), fmt.Errorf("device not found")
		}
		return []byte("device\n"), nil
	case "connect", "reconnect", "wait-for-device":
		return nil, nil
	case "exec-out":
		if len(args) > 1 && args[1] == "uiautomator" {
			return p.dumpLocked(), nil
		}
	case "shell":
		cmd := args[1]
		switch {
		case strings.HasPrefix(cmd, "dumpsys connectivity"):
			if p.offline {
				return []byte("NetworkAgentInfo{ni{WIFI CONNECTED extra: } Score(Policies : EVER_EVALUATED&IS_UNMETERED)}\nNetworkAgentInfo{ni{MOBILE[NR] CONNECTED extra: ims} Score(IS_VALIDATED)}\n"), nil
			}
			return []byte("NetworkAgentInfo{ni{WIFI CONNECTED extra: } Score(EVER_VALIDATED&IS_VALIDATED)}\n"), nil
		case cmd == "wm size":
			return []byte("Physical size: 1080x2400\n"), nil
		case strings.HasPrefix(cmd, "dumpsys window"):
			if p.locked {
				return []byte("isKeyguardShowing=true\nmWakefulness=Awake"), nil
			}
			return []byte("mWakefulness=Awake"), nil
		case strings.HasPrefix(cmd, "am start"):
			p.opened, p.compose, p.sentMsg, p.dumps, p.sdumps = true, "", "", 0, 0
			return []byte("Status: ok"), nil
		case strings.HasPrefix(cmd, "input tap"):
			m := tapRe.FindStringSubmatch(cmd)
			var x, y int
			fmt.Sscan(m[1], &x)
			fmt.Sscan(m[2], &y)
			if x >= 900 && x <= 1000 && y >= 2000 && y <= 2100 && p.compose != "" && !p.dropSend { // send button
				p.sentMsg, p.compose = p.compose, ""
				p.dumps = 0
			}
		case strings.HasPrefix(cmd, "input text"):
			t := strings.TrimPrefix(cmd, "input text ")
			t = strings.TrimSuffix(strings.TrimPrefix(t, "'"), "'")
			t = strings.ReplaceAll(t, `'\''`, "'") // undo the shell quoting
			t = strings.ReplaceAll(t, "%s", " ")
			if p.typedBad && len(t) > 3 {
				t = t[:len(t)-1]
			}
			p.compose += t
		case cmd == "input keyevent KEYCODE_ENTER":
			p.compose += "\n"
		case cmd == "input keyevent KEYCODE_DEL":
			p.compose = ""
		case strings.HasPrefix(cmd, "getprop ro.product.model"):
			return []byte("Pixel 8\n"), nil
		case strings.HasPrefix(cmd, "getprop gsm.sim.state"):
			return []byte("READY\n"), nil
		case strings.HasPrefix(cmd, "getprop gsm.operator.alpha"):
			return []byte("T-Mobile\n"), nil
		case strings.HasPrefix(cmd, "pm list packages"):
			return []byte("package:com.google.android.apps.messaging\n"), nil
		case strings.HasPrefix(cmd, "cmd role get-role-holders"):
			return []byte("com.google.android.apps.messaging\n"), nil
		}
	}
	return nil, nil
}

func node(class, id, text, desc string, l, t, r, b int) string {
	return fmt.Sprintf(`<node index="0" text="%s" resource-id="%s" class="%s" package="com.google.android.apps.messaging" content-desc="%s" clickable="true" enabled="true" bounds="[%d,%d][%d,%d]" />`,
		text, id, class, desc, l, t, r, b)
}

func (p *fakePhone) kindLocked() Kind {
	if p.sentMsg != "" {
		return p.afterKind
	}
	i := p.dumps
	if i >= len(p.kinds) {
		i = len(p.kinds) - 1
	}
	return p.kinds[i]
}

func (p *fakePhone) dumpLocked() []byte {
	if !p.opened {
		return []byte("ERROR: null root node returned by UiTestAutomationBridge.")
	}
	k := p.kindLocked()
	var nodes []string
	hint := "Text message"
	if k == KindRCS || k == KindEncrypted {
		hint = "RCS message"
	}
	text := hint
	if p.compose != "" {
		text = strings.ReplaceAll(p.compose, "\n", "&#10;")
	}
	if p.header != "" {
		nodes = append(nodes, node("android.widget.TextView", "", p.header, "", 200, 60, 600, 120))
	}
	nodes = append(nodes, node("android.widget.EditText", "com.google.android.apps.messaging:id/compose_message_text", text, "", 100, 2000, 900, 2100))
	sendDesc := "Send message"
	if k == KindEncrypted {
		sendDesc = "Send encrypted message"
	}
	if k == KindSMS {
		sendDesc = "Send SMS"
	}
	if p.sendDescOverride != "" {
		sendDesc = p.sendDescOverride
	}
	nodes = append(nodes, node("android.widget.ImageButton", "com.google.android.apps.messaging:id/send_message_button_icon", "", sendDesc, 900, 2000, 1000, 2100))
	if k == KindEncrypted {
		nodes = append(nodes, node("android.widget.ImageView", "com.google.android.apps.messaging:id/compose_lock", "", "Encrypted", 60, 2030, 100, 2070))
		if !p.noBanner {
			nodes = append(nodes, node("android.widget.TextView", "com.google.android.apps.messaging:id/conversation_banner", "RCS chat with Test. This chat is now end-to-end encrypted.", "", 100, 300, 980, 380))
		}
	}
	if p.oldMsg != "" {
		nodes = append(nodes, node("android.view.ViewGroup", "com.google.android.apps.messaging:id/message_content", "", "You said  "+p.oldMsg+"  ., Read. End-to-end encrypted message", 280, 400, 1040, 520))
		nodes = append(nodes, node("android.widget.TextView", "com.google.android.apps.messaging:id/message_text", p.oldMsg, "", 300, 420, 1000, 500))
	}
	if p.sentMsg != "" {
		tail := "Now"
		if k == KindSMS {
			tail = "Now, SMS"
		}
		if p.tailOverride != "" {
			tail = p.tailOverride
		}
		nodes = append(nodes, node("android.view.ViewGroup", "com.google.android.apps.messaging:id/message_content", "", "You said  "+strings.ReplaceAll(p.sentMsg, "\n", " ")+"  ., "+tail, 280, 1480, 1040, 1620))
		nodes = append(nodes, node("android.widget.TextView", "com.google.android.apps.messaging:id/message_text", strings.ReplaceAll(p.sentMsg, "\n", "&#10;"), "", 300, 1500, 1000, 1600))
		if p.echo { // the same text on the other side, left of the screen, lower down
			nodes = append(nodes, node("android.widget.TextView", "com.google.android.apps.messaging:id/message_text", strings.ReplaceAll(p.sentMsg, "\n", "&#10;"), "", 74, 1700, 400, 1760))
		}
		i := p.sdumps
		if i >= len(p.statuses) {
			i = len(p.statuses) - 1
		}
		p.sdumps++
		if st := p.statuses[i]; st != "" {
			nodes = append(nodes, node("android.widget.TextView", "com.google.android.apps.messaging:id/message_status", st, "", 300, 1610, 1000, 1660))
		}
	}
	if p.sentMsg == "" {
		p.dumps++
	}
	root := p.rootBounds
	if root == "" {
		root = "[0,0][1080,2400]"
	}
	return []byte(`<?xml version='1.0' encoding='UTF-8' standalone='yes' ?><hierarchy rotation="0"><node index="0" text="" resource-id="" class="android.widget.FrameLayout" package="com.google.android.apps.messaging" content-desc="" bounds="` + root + `">` +
		strings.Join(nodes, "") + `</node></hierarchy>UI hierchary dumped to: /dev/tty`)
}

// fakeClock lets a test run minutes of polling instantly.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func newClock() *fakeClock { return &fakeClock{now: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	c.sleeps = append(c.sleeps, d)
	return nil
}

func newSender(p *fakePhone, clk *fakeClock, mod func(*Config)) *Sender {
	cfg := Config{Runner: p, Selectors: DefaultSelectors(), Sleep: clk.Sleep, Now: clk.Now,
		SettleBudget: 60 * time.Second, CheckBudget: 10 * time.Second, PollEvery: 2 * time.Second, AfterBudget: 10 * time.Second}
	if mod != nil {
		mod(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		panic(err)
	}
	return s
}
