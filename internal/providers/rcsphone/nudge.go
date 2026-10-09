package rcsphone

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"

	"github.com/zagnu-labs/rcs-messenger/internal/providers/rcsdirect"
)

// A nudge is one of the calls the Messages web client makes when a thread is
// opened, sent on its own against a chat the phone has not finalized (see
// finalizing in e2ee.go). Opening the thread on the handset makes the phone
// rewrite the created-tombstone within a minute; GET_CONVERSATION_TYPE, which
// every check already makes, does not (tested 2026-09-30). These are the next
// candidates. A check runs before and after so the answer is in the response:
// Changed says whether the tombstone evidence moved.

var ErrBadNudge = errors.New("unknown nudge action")

// nudgeSettle is how long the phone gets to react before the after-check.
const nudgeSettle = 2 * time.Second

type NudgeStep struct {
	Action string   `json:"action"`
	Result []string `json:"result,omitempty"`
	Error  string   `json:"error,omitempty"`
}

type NudgeResult struct {
	To             string       `json:"to"`
	ConversationID string       `json:"conversationId"`
	Before         *CheckResult `json:"before"`
	Steps          []NudgeStep  `json:"steps"`
	After          *CheckResult `json:"after"`
	Changed        bool         `json:"changed"`
}

func (a *Agent) Nudge(ctx context.Context, rawTo, action string) (*NudgeResult, error) {
	if !rcsdirect.ValidNudge(action) {
		return nil, fmt.Errorf("%w: %q", ErrBadNudge, action)
	}
	before, err := a.Check(ctx, rawTo)
	if err != nil {
		return nil, err
	}
	res := &NudgeResult{To: before.To, ConversationID: before.ConversationID, Before: before}

	actions := []string{action}
	if action == rcsdirect.NudgeAll {
		actions = rcsdirect.NudgeSteps
	}
	for _, act := range actions {
		res.Steps = append(res.Steps, a.nudge(ctx, act, before))
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(nudgeSettle):
	}

	after, err := a.Check(ctx, rawTo)
	if err != nil {
		return nil, fmt.Errorf("check after nudge: %w", err)
	}
	res.After = after
	res.Changed = after.E2EE != before.E2EE || after.E2EEEvidence != before.E2EEEvidence
	return res, nil
}

func (a *Agent) nudge(ctx context.Context, action string, chk *CheckResult) NudgeStep {
	step := NudgeStep{Action: action}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	var err error
	switch action {
	case rcsdirect.NudgeGetConversation:
		var conv *gmproto.Conversation
		if conv, err = a.cli.GetConversation(ctx, chk.ConversationID); err == nil {
			step.Result = conversationFields(conv)
		}
	case rcsdirect.NudgeMarkRead:
		err = a.cli.MarkRead(ctx, chk.ConversationID, chk.conv.GetLatestMessageID())
	case rcsdirect.NudgeTyping:
		if chk.sim == nil {
			err = errors.New("no SIM matched the conversation")
			break
		}
		err = a.cli.SetTyping(ctx, chk.ConversationID, chk.sim.GetSIMData().GetSIMPayload())
	default:
		err = fmt.Errorf("%w: %q", ErrBadNudge, action)
	}
	if err != nil {
		step.Error = err.Error()
	}
	a.log.Info().
		Str("action", action).
		Str("conversation_id", chk.ConversationID).
		Strs("result", step.Result).
		Str("error", step.Error).
		Msg("nudge")
	return step
}

// conversationFields is what the phone says about a chat beyond what the
// check already reports, including fields libgm has not named, so that an
// unfinalized chat can be compared with a finalized one.
func conversationFields(conv *gmproto.Conversation) []string {
	if conv == nil {
		return nil
	}
	out := []string{
		fmt.Sprintf("status = %s", conv.GetStatus()),
		fmt.Sprintf("subType = %v", conv.GetSubType()),
		fmt.Sprintf("thirdType = %v", conv.GetThirdType()),
	}
	out = append(out, unknownFields("conversation", conv)...)
	for i, p := range conv.GetParticipants() {
		who := fmt.Sprintf("participant[%d]", i)
		if p.GetIsMe() {
			who += "(me)"
		}
		// Named in libgm's schema but with placeholder names, so they are
		// as unexplained as the unknown fields and dumped alongside them.
		out = append(out,
			fmt.Sprintf("%s.bs = %d", who, p.GetBs()),
			fmt.Sprintf("%s.someInt1 = %d", who, p.GetSomeInt1()),
			fmt.Sprintf("%s.someInt2 = %d", who, p.GetSomeInt2()),
		)
		out = append(out, unknownFields(who, p)...)
	}
	return out
}
