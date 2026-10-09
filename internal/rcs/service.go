package rcs

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/zagnu-labs/rcs-messenger/internal/logg"
	"github.com/zagnu-labs/rcs-messenger/internal/providers/rcsdirect"
)

var ErrBadNumber = errors.New("number must be E.164, e.g. +15551234567")

var e164 = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)

type Config struct {
	Enabled     bool
	RequireE2EE bool
	Wait        time.Duration
	DryRun      bool
	Allowlist   []string
}

type sender interface {
	Enabled() bool
	Send(ctx context.Context, to, message string, opts rcsdirect.SendOptions) (*rcsdirect.SendResult, error)
	Check(ctx context.Context, to string) (*rcsdirect.Check, error)
	Health(ctx context.Context) (*rcsdirect.Health, error)
	Thread(ctx context.Context, to string, limit int) (*rcsdirect.Thread, error)
}

type Service struct {
	client    sender
	blocker   Blocker
	results   Results
	cfg       Config
	allowlist map[string]bool
}

func (s *Service) SetBlocker(b Blocker) *Service {
	if s != nil {
		s.blocker = b
	}
	return s
}

// SetResults gives the service somewhere to record send outcomes. Without
// it outcomes are logged only and no number is ever blocked.
func (s *Service) SetResults(r Results) *Service {
	if s != nil {
		s.results = r
	}
	return s
}

func NewService(client sender, cfg Config) *Service {
	allow := map[string]bool{}
	for _, raw := range cfg.Allowlist {
		n, err := NormalizeNumber(raw)
		if err != nil {
			logg.Warn().Str("number", raw).Msg("RCS allowlist entry is not E.164, ignoring")
			continue
		}
		allow[n] = true
	}
	return &Service{client: client, cfg: cfg, allowlist: allow}
}

func (s *Service) Enabled() bool {
	return s != nil && s.cfg.Enabled && s.client != nil && s.client.Enabled()
}

func (s *Service) Configured() bool { return s != nil && s.cfg.Enabled }

func (s *Service) DryRun() bool { return s != nil && s.cfg.DryRun }

type Result struct {
	To          string           `json:"to"`
	Sent        bool             `json:"sent"`
	DryRun      bool             `json:"dryRun,omitempty"`
	Transport   string           `json:"transport"`
	Outcome     Outcome          `json:"outcome,omitempty"`
	FinalStatus string           `json:"finalStatus,omitempty"`
	MessageID   string           `json:"messageId,omitempty"`
	Check       *rcsdirect.Check `json:"check,omitempty"` // what eligibility saw before the send
	After       *rcsdirect.Check `json:"after,omitempty"` // the chat re-read once the message was out
}

func (s *Service) Send(ctx context.Context, rawTo, message string) (*Result, error) {
	to, err := s.gate(rawTo)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(message) == "" {
		return nil, emptyMessageErr()
	}

	if blocked, err := s.knownUnencrypted(ctx, to); err != nil {
		return nil, err
	} else if blocked {
		logg.Info().Str("to", to).Msg("RCS send refused, a past send to this number went out unencrypted")
		s.record(ctx, SendRecord{
			Phone: to, Outcome: OutcomeFailed, Status: ReasonKnownUnencrypted,
			Detail: "refused, nothing sent: a past send to this number went out without encryption",
		})
		// A RefusedError so callers that classify structurally (an
		// auto-sender) see it as permanent and do not retry it.
		refused := &rcsdirect.RefusedError{Reason: ReasonKnownUnencrypted}
		return nil, refusedErr(refused, ReasonKnownUnencrypted, false)
	}

	if s.cfg.DryRun {
		logg.Info().Str("to", to).Int("chars", len(message)).Msg("RCS dry run, nothing sent")
		return &Result{To: to, DryRun: true, Transport: "none"}, nil
	}

	requireE2EE := s.cfg.RequireE2EE
	out, err := s.client.Send(ctx, to, message, rcsdirect.SendOptions{
		RequireE2EE: &requireE2EE,
		Wait:        s.cfg.Wait,
	})
	if err != nil {
		var refused *rcsdirect.RefusedError
		if errors.As(err, &refused) {
			logg.Info().
				Str("to", to).
				Str("reason", refused.Reason).
				Bool("retryable", !refused.Permanent()).
				Msg("RCS send refused, nothing sent")
			s.record(ctx, refusalRecord(to, refused))
			return nil, refusedErr(err, refused.Reason, !refused.Permanent())
		}
		logg.Error().Err(err).Str("to", to).Msg("RCS send failed")
		s.record(ctx, SendRecord{Phone: to, Outcome: OutcomeFailed, Status: "sender_error", Detail: err.Error()})
		return nil, senderErr(err)
	}

	outcome, detail := classify(out)
	s.record(ctx, recordOf(to, out, outcome, detail))

	evt := logg.Info()
	if outcome.Blocks() {
		evt = logg.Warn()
	}
	evt.
		Str("to", to).
		Bool("sent", out.Sent).
		Str("outcome", string(outcome)).
		Str("transport", out.Transport()).
		Str("status", out.FinalStatus).
		Str("detail", detail).
		Msg("RCS send")

	return &Result{
		To:          to,
		Sent:        out.Sent,
		Transport:   out.Transport(),
		Outcome:     outcome,
		FinalStatus: out.FinalStatus,
		MessageID:   out.MessageID,
		Check:       out.Check,
		After:       out.After,
	}, nil
}

// knownUnencrypted asks the results whether a past send to this number went
// out without encryption. A results store that cannot answer fails closed.
func (s *Service) knownUnencrypted(ctx context.Context, to string) (bool, error) {
	if s.results == nil {
		return false, nil
	}
	blocked, err := s.results.Blocked(ctx, to)
	if err != nil {
		logg.Error().Err(err).Str("to", to).Msg("RCS send results could not be read, refusing")
		return false, resultsErr(err)
	}
	return blocked, nil
}

// record writes an outcome down. A failure to record is logged, not
// returned: by then the message has gone or the refusal is decided.
func (s *Service) record(ctx context.Context, r SendRecord) {
	if s.results == nil {
		return
	}
	if err := s.results.Record(ctx, r); err != nil {
		logg.Error().Err(err).Str("to", r.Phone).Str("outcome", string(r.Outcome)).
			Msg("RCS send outcome could not be recorded")
	}
}

// Results lists past send outcomes, newest first, for one number or for all.
func (s *Service) Results(ctx context.Context, rawTo string, limit int) ([]SendRecord, error) {
	if !s.Configured() || s.results == nil {
		return nil, disabledErr()
	}
	to := ""
	if strings.TrimSpace(rawTo) != "" {
		n, err := NormalizeNumber(rawTo)
		if err != nil {
			return nil, badNumberErr(err)
		}
		to = n
	}
	out, err := s.results.Recent(ctx, to, limit)
	if err != nil {
		return nil, resultsErr(err)
	}
	return out, nil
}

func (s *Service) Check(ctx context.Context, rawTo string) (*rcsdirect.Check, error) {
	to, err := s.gate(rawTo)
	if err != nil {
		return nil, err
	}
	out, err := s.client.Check(ctx, to)
	if err != nil {
		return nil, senderErr(err)
	}
	return out, nil
}

func (s *Service) Thread(ctx context.Context, rawTo string, limit int) (*rcsdirect.Thread, error) {
	to, err := s.gate(rawTo)
	if err != nil {
		return nil, err
	}
	out, err := s.client.Thread(ctx, to, limit)
	if err != nil {
		return nil, senderErr(err)
	}
	return out, nil
}

// nudger is optional: only the in-process phone agent can nudge a chat.
type nudger interface {
	Nudge(ctx context.Context, to, action string) (*rcsdirect.Nudge, error)
}

func (s *Service) Nudge(ctx context.Context, rawTo, action string) (*rcsdirect.Nudge, error) {
	to, err := s.gate(rawTo)
	if err != nil {
		return nil, err
	}
	if !rcsdirect.ValidNudge(action) {
		return nil, badNudgeErr(action)
	}
	n, ok := s.client.(nudger)
	if !ok {
		return nil, nudgeUnsupportedErr()
	}
	out, err := n.Nudge(ctx, to, action)
	if err != nil {
		return nil, senderErr(err)
	}
	return out, nil
}

func (s *Service) Health(ctx context.Context) (*rcsdirect.Health, error) {
	if s == nil || s.client == nil || !s.cfg.Enabled {
		return nil, disabledErr()
	}
	out, err := s.client.Health(ctx)
	if err != nil {
		return nil, senderErr(err)
	}
	return out, nil
}

func (s *Service) gate(rawTo string) (string, error) {
	if !s.Enabled() {
		return "", disabledErr()
	}
	to, err := NormalizeNumber(rawTo)
	if err != nil {
		return "", badNumberErr(err)
	}
	if len(s.allowlist) > 0 && !s.allowlist[to] {
		return "", notAllowlistedErr(to)
	}
	return to, nil
}

func NormalizeNumber(raw string) (string, error) {
	n := strings.NewReplacer(" ", "", "-", "", "(", "", ")", "", ".", "").Replace(strings.TrimSpace(raw))
	if strings.HasPrefix(n, "00") {
		n = "+" + n[2:]
	}
	if !e164.MatchString(n) {
		return "", fmt.Errorf("%w: got %q", ErrBadNumber, raw)
	}
	return n, nil
}

func (s *Service) SendText(ctx context.Context, to, message string) (bool, error) {
	res, err := s.Send(ctx, to, message)
	if err != nil {
		return false, err
	}
	return res.Sent, nil
}
