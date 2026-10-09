package rcs

import (
	"context"
	"strings"
	"unicode"

	"github.com/zagnu-labs/rcs-messenger/internal/logg"
)

type Blocker interface {
	BlockNumber(ctx context.Context, phone, reason string) (bool, error)
}

var optOutWords = map[string]bool{
	"stop":        true,
	"stopall":     true,
	"stop all":    true,
	"unsubscribe": true,
	"cancel":      true,
	"end":         true,
	"quit":        true,
	"optout":      true,
	"opt out":     true,
	"revoke":      true,
}

func IsOptOut(text string) bool {
	cleaned := strings.TrimFunc(strings.ToLower(strings.TrimSpace(text)), func(r rune) bool {
		return unicode.IsPunct(r) || unicode.IsSpace(r)
	})
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	return optOutWords[cleaned]
}

type Inbound struct {
	From      string `json:"from"`
	Text      string `json:"text"`
	MessageID string `json:"messageId,omitempty"`
}

type InboundResult struct {
	From    string `json:"from"`
	OptOut  bool   `json:"optOut"`
	Blocked bool   `json:"blocked"`
}

func (s *Service) HandleInbound(ctx context.Context, in Inbound) (*InboundResult, error) {
	from, err := NormalizeNumber(in.From)
	if err != nil {
		return nil, badNumberErr(err)
	}

	if !IsOptOut(in.Text) {
		return &InboundResult{From: from}, nil
	}

	logg.Info().Str("from", from).Msg("RCS inbound opt-out")

	if s == nil || s.blocker == nil {
		logg.Warn().Str("from", from).
			Msg("RCS opt-out received but no blocker configured, number NOT suppressed")
		return &InboundResult{From: from, OptOut: true}, nil
	}

	recorded, err := s.blocker.BlockNumber(ctx, from, "replied "+strings.TrimSpace(in.Text)+" over RCS")
	if err != nil {
		logg.Error().Err(err).Str("from", from).Msg("RCS opt-out could not be recorded")
		return nil, blockFailedErr(err)
	}
	if !recorded {
		logg.Warn().Str("from", from).
			Msg("RCS opt-out was NOT recorded with the opt-out store, this number will still be texted")
	}

	return &InboundResult{From: from, OptOut: true, Blocked: recorded}, nil
}
