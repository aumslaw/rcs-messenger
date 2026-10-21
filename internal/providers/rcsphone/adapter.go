package rcsphone

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/zagnu-labs/rcs-messenger/internal/providers/rcsdirect"
)

var ErrNotPaired = errors.New("no phone is paired yet")

type Sender struct {
	mu    sync.RWMutex
	agent *Agent
}

func NewSender() *Sender { return &Sender{} }

func (s *Sender) attach(a *Agent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agent = a
}

func (s *Sender) detach() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agent = nil
}

func (s *Sender) current() *Agent {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.agent
}

func (s *Sender) Enabled() bool { return s.current() != nil }

func (s *Sender) Send(ctx context.Context, to, message string, opts rcsdirect.SendOptions) (*rcsdirect.SendResult, error) {
	a := s.current()
	if a == nil {
		return nil, ErrNotPaired
	}

	// opts.RequireE2EE is deliberately not read: encryption is always
	// required, and a caller cannot ask for less.
	wait := opts.Wait
	if wait <= 0 {
		wait = 20 * time.Second
	}

	out, err := a.Send(ctx, to, message, wait)
	if err != nil {
		return nil, translateRefusal(err)
	}

	return &rcsdirect.SendResult{
		Sent:          out.Sent,
		MessageID:     out.MessageID,
		TmpID:         out.TmpID,
		FinalStatus:   out.FinalStatus,
		StatusHistory: out.StatusHistory,
		MessageType:   out.MessageType,
		Check:         toCheck(out.Check),
		After:         toCheck(out.After),
	}, nil
}

func (s *Sender) Check(ctx context.Context, to string) (*rcsdirect.Check, error) {
	a := s.current()
	if a == nil {
		return nil, ErrNotPaired
	}
	out, err := a.Check(ctx, to)
	if err != nil {
		return nil, err
	}
	return toCheck(out), nil
}

func (s *Sender) Thread(ctx context.Context, to string, limit int) (*rcsdirect.Thread, error) {
	a := s.current()
	if a == nil {
		return nil, ErrNotPaired
	}
	if limit <= 0 {
		limit = 25
	}

	chk, msgs, err := a.Thread(ctx, to, int64(limit), false)
	if err != nil {
		return nil, err
	}

	out := &rcsdirect.Thread{Check: toCheck(chk), Messages: make([]rcsdirect.ThreadMessage, 0, len(msgs))}
	for _, m := range msgs {
		out.Messages = append(out.Messages, rcsdirect.ThreadMessage{
			MessageID:    m.MessageID,
			Timestamp:    m.Timestamp,
			Outgoing:     m.Outgoing,
			Status:       m.Status,
			Type:         m.Type,
			Transport:    m.Transport,
			E2EEAtSend:   string(m.E2EEAtSend),
			E2EEEvidence: m.E2EEEvidence,
			Text:         m.Text,
		})
	}
	return out, nil
}

func (s *Sender) Health(ctx context.Context) (*rcsdirect.Health, error) {
	a := s.current()
	if a == nil {
		return &rcsdirect.Health{LastError: ErrNotPaired.Error()}, nil
	}

	var lastErr string
	if p := a.lastError.Load(); p != nil {
		lastErr = *p
	}

	sims := a.simList()
	out := &rcsdirect.Health{
		LoggedIn:        a.cli.IsLoggedIn(),
		Connected:       a.cli.IsConnected(),
		PhoneResponding: a.phoneResponding.Load(),
		SessionActive:   a.sessionActive.Load(),
		LastError:       lastErr,
		SIMs:            make([]rcsdirect.SIM, 0, len(sims)),
	}
	for _, sim := range sims {
		out.SIMs = append(out.SIMs, rcsdirect.SIM{
			ParticipantID: sim.ParticipantID,
			Carrier:       sim.Carrier,
			Number:        sim.Number,
			RCSEnabled:    sim.RCSEnabled,
		})
	}
	return out, nil
}

func (s *Sender) Activate(ctx context.Context) error {
	a := s.current()
	if a == nil {
		return ErrNotPaired
	}
	return a.Activate(ctx)
}

func (s *Sender) Nudge(ctx context.Context, to, action string) (*rcsdirect.Nudge, error) {
	a := s.current()
	if a == nil {
		return nil, ErrNotPaired
	}
	out, err := a.Nudge(ctx, to, action)
	if err != nil {
		return nil, err
	}
	res := &rcsdirect.Nudge{
		To:             out.To,
		ConversationID: out.ConversationID,
		Before:         toCheck(out.Before),
		After:          toCheck(out.After),
		Changed:        out.Changed,
		Steps:          make([]rcsdirect.NudgeStep, 0, len(out.Steps)),
	}
	for _, st := range out.Steps {
		res.Steps = append(res.Steps, rcsdirect.NudgeStep{Action: st.Action, Result: st.Result, Error: st.Error})
	}
	return res, nil
}

func toCheck(c *CheckResult) *rcsdirect.Check {
	if c == nil {
		return nil
	}
	out := &rcsdirect.Check{
		To:                 c.To,
		ConversationID:     c.ConversationID,
		Transport:          c.Transport,
		SendMode:           c.SendMode,
		E2EE:               string(c.E2EE),
		E2EEEvidence:       c.E2EEEvidence,
		E2EEEvidenceAt:     c.E2EEEvidenceAt,
		ReadOnly:           c.ReadOnly,
		ConversationFields: c.ConversationFields,
		E2EECapability:     string(c.E2EECapability),
		Eligible:           c.Eligible,
		Reason:             c.Reason,
	}
	if c.SIM != nil {
		out.SIM = &rcsdirect.SIM{
			ParticipantID: c.SIM.ParticipantID,
			Carrier:       c.SIM.Carrier,
			Number:        c.SIM.Number,
			RCSEnabled:    c.SIM.RCSEnabled,
		}
	}
	if p := c.ConversationTypeProbe; p != nil {
		out.ConversationTypeProbe = &rcsdirect.ConversationTypeProbe{
			Type:          p.Type,
			Bool1:         p.Bool1,
			Number2:       p.Number2,
			UnknownFields: p.UnknownFields,
			Error:         p.Error,
		}
		if c := p.Capability; c != nil {
			out.ConversationTypeProbe.Capability = &rcsdirect.Capability{
				Flag1:       c.Flag1,
				Flag2:       c.Flag2,
				Version:     c.Version,
				RefreshedAt: c.RefreshedAt,
			}
		}
	}
	return out
}

func translateRefusal(err error) error {
	var refused *RefusedError
	if errors.As(err, &refused) {
		return &rcsdirect.RefusedError{
			Reason: refused.Reason,
			Check:  toCheck(refused.Check),
		}
	}
	return err
}
