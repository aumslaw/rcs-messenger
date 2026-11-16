package rcsandroid

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/zagnu-labs/rcs-messenger/internal/logg"
)

// InboundFunc receives one incoming message: the sender's E.164 number, the
// text, and an id that is the same every time the same message is read.
type InboundFunc func(from, text, messageID string) error

// ConvRow is one chat on the conversation list.
type ConvRow struct {
	Name    string
	Snippet string
	Time    string
	Bounds  Rect
}

// Key changes when the chat gets something new (a new last message or time).
func (r ConvRow) Key() string { return r.Name + "\x00" + r.Snippet + "\x00" + r.Time }

// ConversationList reads the chats on the Messages home screen, newest first.
func (m *matcher) ConversationList(s *Screen) []ConvRow {
	var rows []ConvRow
	for _, n := range s.Nodes {
		if !m.inPackage(n) || n.ID() != m.ConvNameID || norm(n.Text) == "" {
			continue
		}
		row := ConvRow{Name: norm(n.Text), Bounds: n.Bounds}
		if sn, ok := s.Find(func(c Node) bool {
			return c.ID() == m.ConvSnippetID && c.Bounds.T >= n.Bounds.T && c.Bounds.T <= n.Bounds.B+60
		}); ok {
			row.Snippet = norm(sn.Text)
		}
		if tm, ok := s.Find(func(c Node) bool {
			return c.ID() == m.ConvTimeID && abs(c.Bounds.T-n.Bounds.T) < 60
		}); ok {
			row.Time = norm(tm.Text)
		}
		rows = append(rows, row)
	}
	return rows
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// NumberFromName turns a chat name that is a phone number, like
// "(555) 123-4567" or "+44 20 7946 0958", into E.164. A saved contact's name
// has letters in it and returns "", false: its number is not on the list.
// Ten digits are taken as a US number.
func NumberFromName(name string) (string, bool) {
	var digits strings.Builder
	plus := strings.HasPrefix(strings.TrimSpace(name), "+")
	for _, r := range name {
		switch {
		case unicode.IsDigit(r):
			digits.WriteRune(r)
		case unicode.IsLetter(r):
			return "", false
		}
	}
	d := digits.String()
	switch {
	case plus && len(d) >= 7 && len(d) <= 15:
		return "+" + d, true
	case len(d) == 10:
		return "+1" + d, true
	case len(d) == 11 && strings.HasPrefix(d, "1"):
		return "+" + d, true
	}
	return "", false
}

// IncomingMessages reads the other side's messages in an open thread: the
// bubbles on the left of the screen.
func (m *matcher) IncomingMessages(s *Screen) []string {
	width := 0
	for _, n := range s.Nodes {
		width = max(width, n.Bounds.R)
	}
	var out []string
	for _, n := range s.Nodes {
		if !m.inPackage(n) || n.ID() != m.BubbleID || strings.TrimSpace(n.Text) == "" {
			continue
		}
		if cx, _ := n.Bounds.Center(); width > 0 && cx < width/2 {
			out = append(out, n.Text)
		}
	}
	return out
}

// Bubble is one incoming message with the context that tells it apart from an
// identical one: the text of the bubble just above it (either side) and how
// many bubbles with that same context and text came before it on screen.
type Bubble struct {
	Text       string
	Prev       string
	Occurrence int
	Top        bool // the first bubble on screen: its Prev is unknown (scrolled off)
}

// IncomingBubbles reads the other side's messages in an open thread, top to
// bottom, with what they need for an id that is distinct per bubble and the
// same every time the same screen is read.
func (m *matcher) IncomingBubbles(s *Screen) []Bubble {
	width := 0
	for _, n := range s.Nodes {
		width = max(width, n.Bounds.R)
	}
	all := s.FindAll(func(n Node) bool {
		return m.inPackage(n) && n.ID() == m.BubbleID && strings.TrimSpace(n.Text) != ""
	})
	sort.SliceStable(all, func(i, j int) bool { return all[i].Bounds.T < all[j].Bounds.T })
	var out []Bubble
	seen := map[string]int{}
	for i, n := range all {
		cx, _ := n.Bounds.Center()
		if width <= 0 || cx >= width/2 {
			continue
		}
		b := Bubble{Text: n.Text, Top: i == 0}
		if i > 0 {
			b.Prev = norm(all[i-1].Text)
		}
		k := b.Prev + "\x00" + norm(n.Text)
		seen[k]++
		b.Occurrence = seen[k]
		out = append(out, b)
	}
	return out
}

func messageKey(number string, b Bubble) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("%s|%s|%s|%d", number, norm(b.Text), b.Prev, b.Occurrence)))
	return "android:" + hex.EncodeToString(sum[:8])
}

type inboundState struct {
	mu       sync.Mutex
	fn       InboundFunc
	rows     map[string]string // chat name -> last row key seen
	messages map[string]bool   // message keys already delivered
	texts    map[string]bool   // number|text already delivered under any id
	names    map[string]string // contact-name chat -> number read from its details
	unres    map[string]bool   // contact-name chats whose number could not be read
}

func (s *Sender) isUnresolved(title string) bool {
	if title == "" {
		return false
	}
	s.in.mu.Lock()
	defer s.in.mu.Unlock()
	return s.in.unres[title]
}

// OnInbound sets where incoming messages go (the opt-out handler).
func (s *Sender) OnInbound(fn InboundFunc) {
	s.in.mu.Lock()
	defer s.in.mu.Unlock()
	s.in.fn = fn
}

// PollInbound makes one pass: opens the Messages home screen, and for each
// chat whose last message or time changed since the last pass, opens it and
// delivers the other side's messages it has not delivered before. Returns how
// many it delivered. Safe to call while sends happen: it takes the phone's
// turn like any other call.
func (s *Sender) PollInbound(ctx context.Context) (int, error) {
	s.in.mu.Lock()
	fn := s.in.fn
	if s.in.rows == nil {
		s.in.rows, s.in.messages = map[string]string{}, map[string]bool{}
	}
	if s.in.texts == nil {
		s.in.texts, s.in.names, s.in.unres = map[string]bool{}, map[string]string{}, map[string]bool{}
	}
	s.in.mu.Unlock()
	if fn == nil {
		return 0, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(ctx); err != nil {
		return 0, err
	}
	if err := s.dev.OpenHome(ctx); err != nil {
		return 0, err
	}
	scr, err := s.waitScreen(ctx, func(sc *Screen) bool {
		_, hasList := sc.Find(func(n Node) bool { return n.ID() == "list" })
		return hasList && len(s.dev.m.ConversationList(sc)) > 0
	})
	if err != nil {
		return 0, err
	}
	delivered := 0
	for _, row := range s.dev.m.ConversationList(scr) {
		s.in.mu.Lock()
		unchanged := s.in.rows[row.Name] == row.Key()
		s.in.mu.Unlock()
		if unchanged {
			continue
		}
		number, ok := NumberFromName(row.Name)
		if !ok {
			s.in.mu.Lock()
			number, ok = s.in.names[row.Name], s.in.names[row.Name] != ""
			s.in.mu.Unlock()
		}
		x, y := row.Bounds.Center()
		if err := s.dev.Tap(ctx, x, y); err != nil {
			return delivered, err
		}
		thread, err := s.waitScreen(ctx, func(sc *Screen) bool {
			_, ok := s.dev.m.compose(sc)
			return ok
		})
		if err != nil {
			return delivered, err
		}
		if !ok {
			// A saved contact: the list shows a name, so read the number off the
			// thread or its details. Unreadable means a STOP from this chat could
			// not opt anyone out: flag it so the sender refuses to text it.
			number, thread = s.resolveNumber(ctx, thread)
			s.in.mu.Lock()
			if number == "" {
				s.in.unres[row.Name] = true
			} else {
				s.in.names[row.Name] = number
				delete(s.in.unres, row.Name)
			}
			s.in.mu.Unlock()
			if number == "" {
				logg.Warn().Str("chat", row.Name).Msg("Android inbound: chat is saved under a name and its number could not be read; marked unresolved, sends to it are refused")
				s.remember(row)
				_ = s.dev.Key(ctx, "KEYCODE_BACK")
				continue
			}
		}
		failed := false
		for _, b := range s.dev.m.IncomingBubbles(thread) {
			text := b.Text
			id := messageKey(number, b)
			tk := number + "|" + norm(text)
			s.in.mu.Lock()
			done := s.in.messages[id]
			// The first bubble on screen has lost its context to scrolling: if
			// the same text was already delivered, it is that message.
			if b.Top && s.in.texts[tk] {
				done = true
			}
			s.in.mu.Unlock()
			if done {
				continue
			}
			// Marked delivered only after the handler succeeded: a STOP whose
			// opt-out failed to save must be read again on the next pass.
			if err := fn(number, text, id); err != nil {
				failed = true
				logg.Error().Err(err).Msg("Android inbound message could not be handled; will retry on the next pass")
				continue
			}
			s.in.mu.Lock()
			s.in.messages[id] = true
			s.in.texts[tk] = true
			s.in.mu.Unlock()
			delivered++
		}
		if !failed { // a failed chat is looked at again next pass
			s.remember(row)
		}
		_ = s.dev.Key(ctx, "KEYCODE_BACK")
		if err := s.cfg.Sleep(ctx, s.cfg.PollEvery/2); err != nil {
			return delivered, err
		}
	}
	return delivered, nil
}

// waitScreen reads the screen until ready says it has loaded, for up to about
// ten seconds, and returns the last reading either way (a list with no chats
// is a real answer, not an error).
func (s *Sender) waitScreen(ctx context.Context, ready func(*Screen) bool) (*Screen, error) {
	var last *Screen
	start := s.cfg.Now()
	for {
		scr, err := s.dev.Dump(ctx)
		if err == nil {
			last = scr
			if ready(scr) {
				return scr, nil
			}
		} else if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if s.cfg.Now().Sub(start)+s.cfg.PollEvery > 10*time.Second {
			if last == nil {
				return nil, ErrNoHierarchy
			}
			return last, nil
		}
		if err := s.cfg.Sleep(ctx, s.cfg.PollEvery); err != nil {
			return nil, err
		}
	}
}

func (s *Sender) remember(row ConvRow) {
	s.in.mu.Lock()
	defer s.in.mu.Unlock()
	s.in.rows[row.Name] = row.Key()
}

// RunInbound polls until ctx ends.
func (s *Sender) RunInbound(ctx context.Context, every time.Duration) {
	if every <= 0 {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		n, err := s.PollInbound(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			logg.Warn().Err(err).Msg("Android inbound poll failed")
		case n > 0:
			logg.Info().Int("messages", n).Msg("Android inbound messages delivered")
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

var phoneInTextRe = regexp.MustCompile(`\+?\d[\d\s().\-]{7,}\d`)

// resolveNumber reads the number of a chat saved under a contact name: from the
// title bar if it shows one, else from the contact details the title opens. It
// returns "" if no number could be read, and the thread screen to read bubbles
// from (the details screen is backed out of).
func (s *Sender) resolveNumber(ctx context.Context, thread *Screen) (string, *Screen) {
	if n, ok := NumberFromName(s.dev.m.headerNumber(thread)); ok {
		return n, thread
	}
	title, ok := thread.Find(func(n Node) bool {
		return s.dev.m.inPackage(n) && n.Bounds.T >= 0 && n.Bounds.T < 260 && norm(n.Text) != "" &&
			((s.dev.m.TitleID != "" && n.ID() == s.dev.m.TitleID) || n.Class == "android.widget.TextView")
	})
	if !ok {
		return "", thread
	}
	x, y := title.Bounds.Center()
	if err := s.dev.Tap(ctx, x, y); err != nil {
		return "", thread
	}
	details, err := s.waitScreen(ctx, func(sc *Screen) bool {
		_, hasCompose := s.dev.m.compose(sc)
		return !hasCompose
	})
	number := ""
	if err == nil {
		for _, n := range details.Nodes {
			if !s.dev.m.inPackage(n) {
				continue
			}
			for _, t := range []string{n.Text, n.Desc} {
				t = norm(t)
				if t == "" || len(t) > 60 {
					continue
				}
				if num, ok := NumberFromName(phoneInTextRe.FindString(t)); ok {
					number = num
					break
				}
			}
			if number != "" {
				break
			}
		}
	}
	_ = s.dev.Key(ctx, "KEYCODE_BACK")
	back, err := s.waitScreen(ctx, func(sc *Screen) bool {
		_, ok := s.dev.m.compose(sc)
		return ok
	})
	if err != nil {
		return number, thread
	}
	return number, back
}
