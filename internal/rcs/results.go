package rcs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/zagnu-labs/rcs-messenger/internal/providers/rcsdirect"
)

// Outcome is how a send attempt ended, recorded per number.
//
// The policy behind it: a message is sent only when encryption was confirmed
// before the send. Afterwards the chat is re-read as a check on that
// confirmation. If the check disagrees (the message went out unencrypted or as
// SMS) the pre-send evidence was wrong: it is recorded loudly and the number is
// never auto-sent to again.
type Outcome string

const (
	OutcomeFailed      Outcome = "FAILED"             // refused, or the phone reported a failure
	OutcomeSuccessE2EE Outcome = "SUCCESS_E2EE"       // delivered, and the chat finalized encrypted
	OutcomeSuccessRCS  Outcome = "SUCCESS_RCS"        // delivered as RCS, chat finalized unencrypted: never again
	OutcomeSuccessSMS  Outcome = "SUCCESS_SMS"        // delivered, but the phone fell back to SMS/MMS: never again
	OutcomeUnverified  Outcome = "SUCCESS_UNVERIFIED" // delivered as RCS, the chat could not be re-read in time
	OutcomeOptedOut    Outcome = "OPTED_OUT"          // the number replied STOP or similar: never again
)

// Blocks reports whether a number with this outcome on record is one we do
// not auto-send to again.
func (o Outcome) Blocks() bool { return o == OutcomeSuccessRCS || o == OutcomeSuccessSMS }

type SendRecord struct {
	ID             int64   `json:"id"`
	Phone          string  `json:"phone"`
	ConversationID string  `json:"conversationId,omitempty"`
	MessageID      string  `json:"messageId,omitempty"`
	Outcome        Outcome `json:"outcome"`
	Status         string  `json:"status,omitempty"`     // the phone's final status, or the refusal reason
	Transport      string  `json:"transport,omitempty"`  // RCS, SMS or MMS, as the phone typed the message
	E2EEBefore     string  `json:"e2eeBefore,omitempty"` // on, off or unknown before the send
	E2EEAfter      string  `json:"e2eeAfter,omitempty"`  // the same after the send re-read the chat
	E2EEEvidence   string  `json:"e2eeEvidence,omitempty"`
	Capability     string  `json:"capability,omitempty"` // yes, not_yet or no before the send
	Detail         string  `json:"detail,omitempty"`
	CreatedAt      int64   `json:"createdAt"` // unix ms
}

// Results is where send outcomes live. A nil Results on the service means
// outcomes are logged only and no number is ever blocked.
type Results interface {
	Record(ctx context.Context, r SendRecord) error
	// Blocked reports whether a past outcome says never to auto-send to phone.
	Blocked(ctx context.Context, phone string) (bool, error)
	Recent(ctx context.Context, phone string, limit int) ([]SendRecord, error)
}

// classify reads a completed send into an outcome and a one-line reason.
func classify(out *rcsdirect.SendResult) (Outcome, string) {
	switch {
	case out == nil:
		return OutcomeFailed, "no result from the sender"
	case !out.Sent:
		return OutcomeFailed, out.FinalStatus
	case out.MessageType == 1 || out.MessageType == 2 || out.MessageType == 3:
		return OutcomeSuccessSMS, fmt.Sprintf("the phone typed the message %d (%s), not RCS", out.MessageType, out.Transport())
	case out.MessageType != 4:
		return OutcomeUnverified, fmt.Sprintf("the phone typed the message %d, which is not a known transport", out.MessageType)
	case out.After == nil || out.After.E2EE == "" || out.After.E2EE == "unknown":
		return OutcomeUnverified, "the chat could not be re-read after the send"
	case out.After.E2EE == "on":
		return OutcomeSuccessE2EE, ""
	}
	return OutcomeSuccessRCS, "the chat finalized without encryption"
}

// recordOf fills a SendRecord from a completed send.
func recordOf(to string, out *rcsdirect.SendResult, outcome Outcome, detail string) SendRecord {
	r := SendRecord{Phone: to, Outcome: outcome, Detail: detail}
	if out == nil {
		return r
	}
	r.MessageID = out.MessageID
	r.Status = out.FinalStatus
	r.Transport = out.Transport()
	fillBefore(&r, out.Check)
	if a := out.After; a != nil {
		r.E2EEAfter = a.E2EE
		r.E2EEEvidence = a.E2EEEvidence
	}
	return r
}

// refusalRecord fills a SendRecord from a refusal, which sent nothing.
func refusalRecord(to string, refused *rcsdirect.RefusedError) SendRecord {
	r := SendRecord{Phone: to, Outcome: OutcomeFailed, Status: refused.Reason, Detail: "refused, nothing sent"}
	fillBefore(&r, refused.Check)
	return r
}

func fillBefore(r *SendRecord, c *rcsdirect.Check) {
	if c == nil {
		return
	}
	r.ConversationID = c.ConversationID
	r.E2EEBefore = c.E2EE
	r.Capability = c.E2EECapability
	if r.E2EEEvidence == "" {
		r.E2EEEvidence = c.E2EEEvidence
	}
}

// BlockNumber records that phone opted out, so it is never sent to again.
// It makes ResultStore a Blocker. Recording twice is harmless.
func (s *ResultStore) BlockNumber(ctx context.Context, phone, reason string) (bool, error) {
	if err := s.Record(ctx, SendRecord{
		Phone: phone, Outcome: OutcomeOptedOut, Status: ReasonOptedOut, Detail: reason,
	}); err != nil {
		return false, err
	}
	return true, nil
}

// OptedOut reports whether phone has replied STOP or similar.
func (s *ResultStore) OptedOut(ctx context.Context, phone string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("no database to read rcs send results from")
	}
	var out bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM rcs_send_results WHERE phone = $1 AND outcome = $2)`,
		phone, string(OutcomeOptedOut)).Scan(&out)
	if err != nil {
		return false, fmt.Errorf("checking rcs opt-outs: %w", err)
	}
	return out, nil
}

// ResultStore keeps send outcomes in the rcs_send_results table.
type ResultStore struct{ db *sql.DB }

func NewResultStore(db *sql.DB) *ResultStore { return &ResultStore{db: db} }

func (s *ResultStore) Record(ctx context.Context, r SendRecord) error {
	if s == nil || s.db == nil {
		return errors.New("no database to record rcs send results in")
	}
	if r.CreatedAt == 0 {
		r.CreatedAt = time.Now().UnixMilli()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO rcs_send_results
			(phone, conversation_id, message_id, outcome, status, transport,
			 e2ee_before, e2ee_after, e2ee_evidence, capability, detail, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		r.Phone, r.ConversationID, r.MessageID, string(r.Outcome), r.Status, r.Transport,
		r.E2EEBefore, r.E2EEAfter, r.E2EEEvidence, r.Capability, r.Detail, r.CreatedAt)
	if err != nil {
		return fmt.Errorf("recording rcs send result: %w", err)
	}
	return nil
}

func (s *ResultStore) Blocked(ctx context.Context, phone string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("no database to read rcs send results from")
	}
	var blocked bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM rcs_send_results
			WHERE phone = $1 AND outcome IN ($2, $3)
		)`, phone, string(OutcomeSuccessRCS), string(OutcomeSuccessSMS)).Scan(&blocked)
	if err != nil {
		return false, fmt.Errorf("checking rcs send results: %w", err)
	}
	return blocked, nil
}

func (s *ResultStore) Recent(ctx context.Context, phone string, limit int) ([]SendRecord, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("no database to read rcs send results from")
	}
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, phone, conversation_id, message_id, outcome, status, transport,
		       e2ee_before, e2ee_after, e2ee_evidence, capability, detail, created_at
		FROM rcs_send_results
		WHERE $1 = '' OR phone = $1
		ORDER BY created_at DESC, id DESC
		LIMIT $2`, phone, limit)
	if err != nil {
		return nil, fmt.Errorf("reading rcs send results: %w", err)
	}
	defer rows.Close()

	out := []SendRecord{}
	for rows.Next() {
		var r SendRecord
		var outcome string
		if err := rows.Scan(&r.ID, &r.Phone, &r.ConversationID, &r.MessageID, &outcome, &r.Status, &r.Transport,
			&r.E2EEBefore, &r.E2EEAfter, &r.E2EEEvidence, &r.Capability, &r.Detail, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("reading rcs send result row: %w", err)
		}
		r.Outcome = Outcome(outcome)
		out = append(out, r)
	}
	return out, rows.Err()
}
