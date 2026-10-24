package rcsandroid

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/zagnu-labs/rcs-messenger/internal/logg"
)

// InboundFunc receives one incoming message: the sender's E.164 number, the
// text, and an id that is the same every time the same message is read.
type InboundFunc func(from, text, messageID string)

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

func messageKey(number, text string, occurrence int) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("%s|%s|%d", number, norm(text), occurrence)))
	return "android:" + hex.EncodeToString(sum[:8])
}

type inboundState struct {
	mu       sync.Mutex
	fn       InboundFunc
	rows     map[string]string // chat name -> last row key seen
	messages map[string]bool   // message keys already delivered
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
			logg.Warn().Str("chat", row.Name).Msg("Android inbound: chat is not a phone number (a saved contact?), cannot tell who it is; skipping")
			s.remember(row)
			continue
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
		seen := map[string]int{}
		for _, text := range s.dev.m.IncomingMessages(thread) {
			k := norm(text)
			seen[k]++
			id := messageKey(number, text, seen[k])
			s.in.mu.Lock()
			done := s.in.messages[id]
			s.in.messages[id] = true
			s.in.mu.Unlock()
			if !done {
				fn(number, text, id)
				delivered++
			}
		}
		s.remember(row)
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
