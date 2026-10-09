package rcsphone

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/events"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/util"
)

// Refusal reasons returned to callers. A refusal always means nothing was sent.
var (
	ErrNotRCS  = errors.New("not_rcs")
	ErrNotE2EE = errors.New("not_e2ee")
	// ErrE2EEPending: the chat is RCS but its only protocol tombstone is the
	// SMS-created one the phone wrote before it had looked the contact up, so
	// whether it is encrypted is not known yet. See finalizing in e2ee.go.
	ErrE2EEPending = errors.New("e2ee_pending")
	// ErrRCSPending: the phone has not looked the contact up yet, so the chat
	// still reads as the SMS it was created as. See unresolved.
	ErrRCSPending   = errors.New("rcs_pending")
	ErrRCSOffOnSIM  = errors.New("rcs_disabled_on_sim")
	ErrPhoneOffline = errors.New("phone_not_responding")
	ErrNotActive    = errors.New("session_not_active")
	ErrReadOnly     = errors.New("conversation_read_only")
	ErrBadNumber    = errors.New("bad_number")
)

type Agent struct {
	log zerolog.Logger
	cli *libgm.Client

	mu      sync.Mutex
	sims    map[string]*gmproto.SIMCard // by SIM participant ID
	waiters map[string]chan *gmproto.Message
	recent  map[string]msgRecord // by message ID

	convNumbers map[string]string
	onInbound   func(from, text, messageID string)
	persist     func(*libgm.AuthData)

	phoneResponding atomic.Bool
	sessionActive   atomic.Bool
	lastError       atomic.Pointer[string]

	sendLock sync.Mutex // one send at a time per phone
}

type msgRecord struct {
	TmpID   string
	Status  gmproto.MessageStatusType
	Type    int64
	Updated time.Time
}

func (a *Agent) handleEvent(raw any) {
	switch evt := raw.(type) {
	case *libgm.WrappedMessage:
		a.recordMessage(evt.Message)
		a.forwardInbound(evt.Message)
	case *gmproto.Settings:
		if evt.SIMCards != nil {
			a.mu.Lock()
			a.sims = map[string]*gmproto.SIMCard{}
			for _, s := range evt.SIMCards {
				a.sims[s.GetSIMParticipant().GetID()] = s
			}
			a.mu.Unlock()
		}
	case *gmproto.UserAlertEvent:
		switch evt.GetAlertType() {
		case gmproto.AlertType_BROWSER_ACTIVE:
			a.sessionActive.Store(true)
		case gmproto.AlertType_BROWSER_INACTIVE:
			// Another browser/tab took over this account's web session.
			a.sessionActive.Store(false)
			a.log.Warn().Msg("session became inactive - another Messages for web session is open")
		}
	case *events.PhoneNotResponding:
		a.phoneResponding.Store(false)
		a.log.Warn().Msg("phone not responding")
	case *events.PhoneRespondingAgain:
		a.phoneResponding.Store(true)
		a.log.Info().Msg("phone responding again")
	case *events.AuthTokenRefreshed:
		if a.persist != nil {
			a.persist(a.cli.AuthData)
		}
	case *events.ListenFatalError:
		msg := fmt.Sprintf("fatal connection error: %v", evt.Error)
		a.lastError.Store(&msg)
		a.log.Error().Err(evt.Error).Msg("fatal connection error")
	case *gmproto.RevokePairData:
		msg := "the phone unpaired this session - pair again"
		a.lastError.Store(&msg)
		a.log.Error().Msg(msg)
	default:
		a.log.Debug().Type("event", raw).Msg("event")
	}
}

func (a *Agent) recordMessage(m *gmproto.Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.recent[m.GetMessageID()] = msgRecord{
		TmpID: m.GetTmpID(), Status: m.GetMessageStatus().GetStatus(), Type: m.GetType(), Updated: time.Now(),
	}
	if len(a.recent) > 5000 {
		for id, r := range a.recent {
			if time.Since(r.Updated) > time.Hour {
				delete(a.recent, id)
			}
		}
	}
	if ch, ok := a.waiters[m.GetTmpID()]; ok && m.GetTmpID() != "" {
		select {
		case ch <- m:
		default:
		}
	}
}

// --- SIMs ------------------------------------------------------------------

type simInfo struct {
	ParticipantID string `json:"participantId"`
	Carrier       string `json:"carrier"`
	Number        string `json:"number"`
	RCSEnabled    bool   `json:"rcsEnabled"`
}

func (a *Agent) simCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.sims)
}

func (a *Agent) simList() []simInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []simInfo{}
	for id, s := range a.sims {
		out = append(out, simInfo{
			ParticipantID: id,
			Carrier:       s.GetSIMData().GetCarrierName(),
			Number:        s.GetSIMData().GetFormattedPhoneNumber(),
			RCSEnabled:    s.GetRCSChats().GetEnabled(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ParticipantID < out[j].ParticipantID })
	return out
}

func (a *Agent) simFor(participantID string) *gmproto.SIMCard {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s, ok := a.sims[participantID]; ok {
		return s
	}
	if len(a.sims) == 1 {
		for _, s := range a.sims {
			return s
		}
	}
	return nil
}

// --- numbers ---------------------------------------------------------------

var e164 = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)

func normalizeNumber(raw string) (string, error) {
	n := strings.NewReplacer(" ", "", "-", "", "(", "", ")", "", ".", "").Replace(strings.TrimSpace(raw))
	if strings.HasPrefix(n, "00") {
		n = "+" + n[2:]
	}
	if !e164.MatchString(n) {
		return "", fmt.Errorf("%w: %q must be E.164, e.g. +15551234567", ErrBadNumber, raw)
	}
	return n, nil
}

// --- check -----------------------------------------------------------------

// ConversationTypeProbe is the phone's answer to GET_CONVERSATION_TYPE, an
// action the Messages web client makes and the mautrix bridge never does.
// libgm has not named its fields. Type mirrors the conversation type (2 RCS,
// 1 SMS); Bool1 and Number2 have read the same for every contact seen. The
// unnamed field 8 is the useful part and decodes to Capability. The rest is
// exposed as it arrives, for comparing chats.
type ConversationTypeProbe struct {
	Type          int32       `json:"type"`
	Bool1         bool        `json:"bool1"`
	Number2       int32       `json:"number2"`
	Capability    *Capability `json:"capability,omitempty"`
	UnknownFields []string    `json:"unknownFields,omitempty"`
	Error         string      `json:"error,omitempty"`
}

// probeTimeout bounds the probe so it cannot add libgm's 60s phone timeout
// to a check; a check without the probe is still a check.
const probeTimeout = 10 * time.Second

func (a *Agent) probeConversationType(ctx context.Context, convID string) *ConversationTypeProbe {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	resp, err := a.cli.GetConversationType(ctx, convID)
	if err != nil {
		a.log.Warn().Err(err).Str("conversation_id", convID).
			Msg("GET_CONVERSATION_TYPE failed, checking without it")
		return &ConversationTypeProbe{Error: err.Error()}
	}
	p := &ConversationTypeProbe{
		Type:          resp.GetType(),
		Bool1:         resp.GetBool1(),
		Number2:       resp.GetNumber2(),
		Capability:    parseCapability(resp),
		UnknownFields: unknownFields("conversationType", resp),
	}
	a.log.Info().
		Str("conversation_id", convID).
		Int32("type", p.Type).
		Bool("bool1", p.Bool1).
		Int32("number2", p.Number2).
		Str("e2ee_capability", string(p.Capability.State())).
		Strs("unknown_fields", p.UnknownFields).
		Msg("conversation type probe")
	return p
}

type CheckResult struct {
	To             string    `json:"to"`
	ConversationID string    `json:"conversationId"`
	Transport      string    `json:"transport"` // RCS or SMS, as the phone reports the conversation
	SendMode       string    `json:"sendMode"`
	E2EE           e2eeState `json:"e2ee"`
	E2EEEvidence   string    `json:"e2eeEvidence,omitempty"`
	E2EEEvidenceAt string    `json:"e2eeEvidenceAt,omitempty"`
	ReadOnly       bool      `json:"readOnly"`
	SIM            *simInfo  `json:"sim,omitempty"`
	// ConversationTypeProbe and ConversationFields are diagnostic output, see
	// the type and conversationFields in nudge.go. E2EECapability is the
	// probe's Capability read as yes / not_yet / no; empty when the probe
	// failed.
	ConversationTypeProbe *ConversationTypeProbe `json:"conversationTypeProbe,omitempty"`
	ConversationFields    []string               `json:"conversationFields,omitempty"`
	E2EECapability        CapabilityState        `json:"e2eeCapability,omitempty"`
	Eligible              bool                   `json:"eligible"`
	Reason                string                 `json:"reason,omitempty"`

	conv *gmproto.Conversation
	sim  *gmproto.SIMCard
	// e2eePending: E2EE is unknown because the phone has not finalized the
	// chat yet, not because no tombstone was found. Refused as e2ee_pending,
	// which callers may retry.
	e2eePending bool
}

func (a *Agent) Check(ctx context.Context, rawTo string) (*CheckResult, error) {
	to, err := normalizeNumber(rawTo)
	if err != nil {
		return nil, err
	}
	resp, err := a.cli.GetOrCreateConversation(ctx, &gmproto.GetOrCreateConversationRequest{
		Numbers: []*gmproto.ContactNumber{{MysteriousInt: 2, Number: to, Number2: to}},
	})
	if err != nil {
		return nil, fmt.Errorf("opening conversation: %w", err)
	}
	conv := resp.GetConversation()
	if conv == nil {
		return nil, fmt.Errorf("phone returned no conversation (status %s)", resp.GetStatus())
	}

	a.rememberConversation(conv.GetConversationID(), to)

	res := &CheckResult{
		To:             to,
		ConversationID: conv.GetConversationID(),
		Transport:      conv.GetType().String(),
		SendMode:       conv.GetSendMode().String(),
		ReadOnly:       conv.GetReadOnly(),
		conv:           conv,
	}

	// Before the thread is read, so that if this call is what makes the phone
	// finalize a new chat, the walk below already sees the result.
	res.ConversationTypeProbe = a.probeConversationType(ctx, conv.GetConversationID())
	res.E2EECapability = res.ConversationTypeProbe.Capability.State()
	res.ConversationFields = conversationFields(conv)

	// Walk back through the thread until a protocol tombstone is found.
	var cursor *gmproto.Cursor
	var all []*gmproto.Message
	for page := 0; page < 4; page++ {
		lm, err := a.cli.FetchMessages(ctx, conv.GetConversationID(), 50, cursor)
		if err != nil {
			return nil, fmt.Errorf("fetching messages: %w", err)
		}
		all = append(all, lm.GetMessages()...)
		if state, m := latestE2EE(all); state != e2eeUnknown {
			res.E2EE = state
			res.E2EEEvidence = m.GetMessageStatus().GetStatus().String()
			res.E2EEEvidenceAt = time.UnixMicro(m.GetTimestamp()).UTC().Format(time.RFC3339)
			if state == e2eeOff && finalizing(conv.GetType(), m) {
				// The created-tombstone is the oldest thing in the chat, so
				// there is nothing further back that could settle it.
				res.E2EE = e2eeUnknown
				res.e2eePending = true
			}
			break
		}
		cursor = lm.GetCursor()
		if cursor == nil || len(lm.GetMessages()) == 0 {
			break
		}
	}
	if res.E2EE == "" {
		res.E2EE = e2eeUnknown
	}

	if sim := a.simFor(conv.GetDefaultOutgoingID()); sim != nil {
		res.sim = sim
		res.SIM = &simInfo{
			ParticipantID: sim.GetSIMParticipant().GetID(),
			Carrier:       sim.GetSIMData().GetCarrierName(),
			Number:        sim.GetSIMData().GetFormattedPhoneNumber(),
			RCSEnabled:    sim.GetRCSChats().GetEnabled(),
		}
	}

	res.Eligible, res.Reason = eligibility(res, true)
	return res, nil
}

// A fresh chat is the phone's first draft: it reads SMS, with no capability
// recorded, until the lookup lands, which has taken from a few seconds to
// about a minute. A send re-checks for this long before giving up, and a
// refusal that can still change is reported as retryable so the caller can
// come back later.
const (
	settleBudget = 15 * time.Second
	settleDelay  = 2 * time.Second
)

// settles: refusals a fresh chat resolves out of on its own.
func settles(reason string) bool {
	switch reason {
	case ErrRCSPending.Error(), ErrE2EEPending.Error():
		return true
	}
	return false
}

func (a *Agent) checkSettled(ctx context.Context, rawTo string, requireE2EE bool) (*CheckResult, string, error) {
	start := time.Now()
	for attempt := 1; ; attempt++ {
		chk, err := a.Check(ctx, rawTo)
		if err != nil {
			return nil, "", err
		}

		ok, reason := eligibility(chk, requireE2EE)
		outOfTime := time.Since(start)+settleDelay > settleBudget
		switch {
		case ok && !lookupPending(chk):
			return chk, "", nil
		case ok && outOfTime:
			// The lookup has not landed. Send anyway: the send finalizes the
			// chat, and the outcome is recorded either way.
			return chk, "", nil
		case !ok && (!settles(reason) || outOfTime):
			return chk, reason, nil
		}

		a.log.Debug().
			Str("to", chk.To).
			Str("reason", reason).
			Str("transport", chk.Transport).
			Str("capability", string(chk.E2EECapability)).
			Int("attempt", attempt).
			Msg("conversation not settled yet, re-checking")

		select {
		case <-ctx.Done():
			return chk, reason, nil
		case <-time.After(settleDelay):
		}
	}
}

// lookupPending: the chat is unfinalized and the phone has not recorded yet
// what the contact can take. Each check starts that lookup, so a moment's
// wait usually turns it into a yes, and a yes means the send goes encrypted.
func lookupPending(c *CheckResult) bool {
	return c.e2eePending && c.E2EECapability == CapabilityNotYet
}

// unresolved: the phone wrote the chat down as SMS when it was created and
// has not recorded a capability lookup since, so its "SMS" is a first draft,
// not an answer. A contact that really is SMS-only reads capability "no"
// once the lookup has run; a Google Messages contact read "not_yet" for a
// few seconds and then became RCS (conversation 30, 2026-09-30).
func unresolved(c *CheckResult) bool {
	return c.E2EEEvidence == gmproto.MessageStatusType_TOMBSTONE_ONE_ON_ONE_SMS_CREATED.String() &&
		c.E2EECapability != CapabilityNo
}

// eligibility is the single place that decides whether a send may happen.
// Every "no" is a refusal: nothing is sent. With requireE2EE only chats
// already known to be encrypted are sent to. Without it a chat the phone has
// not finalized yet is sent to as well, because the send is what finalizes
// it and the outcome is recorded afterwards. A chat the phone records as RCS
// without encryption is refused either way, and a chat it has not looked up
// yet is refused as rcs_pending, which is retryable.
func eligibility(c *CheckResult, requireE2EE bool) (bool, string) {
	switch {
	case c.ReadOnly:
		return false, ErrReadOnly.Error()
	case c.conv.GetType() != gmproto.ConversationType_RCS && unresolved(c):
		return false, ErrRCSPending.Error()
	case c.conv.GetType() != gmproto.ConversationType_RCS:
		return false, ErrNotRCS.Error()
	case c.conv.GetSendMode() != gmproto.ConversationSendMode_SEND_MODE_AUTO:
		// XMS / XMS_LATCH means the conversation has been switched to SMS/MMS.
		return false, ErrNotRCS.Error()
	case c.sim == nil || !c.sim.GetRCSChats().GetEnabled():
		return false, ErrRCSOffOnSIM.Error()
	case c.E2EE == e2eeOff:
		return false, ErrNotE2EE.Error()
	case requireE2EE && c.e2eePending:
		return false, ErrE2EEPending.Error()
	case requireE2EE && c.E2EE != e2eeOn:
		return false, ErrNotE2EE.Error()
	}
	return true, ""
}

// --- send ------------------------------------------------------------------

type SendResult struct {
	Sent          bool         `json:"sent"`
	MessageID     string       `json:"messageId,omitempty"`
	TmpID         string       `json:"tmpId"`
	FinalStatus   string       `json:"finalStatus"`
	StatusHistory []string     `json:"statusHistory"`
	MessageType   int64        `json:"messageType"` // phone-reported: 1 SMS, 2/3 MMS, 4 RCS (per libgm notes)
	Check         *CheckResult `json:"check"`
	// After is the chat re-read once the message went out, see
	// verifyAfterSend. Nil when it could not be read.
	After *CheckResult `json:"after,omitempty"`
}

type RefusedError struct {
	Reason string
	Check  *CheckResult
}

func (e *RefusedError) Error() string { return "refused: " + e.Reason }

func (a *Agent) Send(ctx context.Context, rawTo, text string, requireE2EE bool, wait time.Duration) (*SendResult, error) {
	a.sendLock.Lock()
	defer a.sendLock.Unlock()

	if !a.phoneResponding.Load() {
		return nil, &RefusedError{Reason: ErrPhoneOffline.Error()}
	}
	chk, reason, err := a.checkSettled(ctx, rawTo, requireE2EE)
	if err != nil {
		return nil, err
	}
	if reason != "" {
		return nil, &RefusedError{Reason: reason, Check: chk}
	}

	tmpID := util.GenerateTmpID()
	updates := make(chan *gmproto.Message, 32)
	a.mu.Lock()
	a.waiters[tmpID] = updates
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.waiters, tmpID)
		a.mu.Unlock()
	}()

	req := &gmproto.SendMessageRequest{
		ConversationID: chk.ConversationID,
		MessagePayload: &gmproto.MessagePayload{
			TmpID:          tmpID,
			ConversationID: chk.ConversationID,
			ParticipantID:  chk.conv.GetDefaultOutgoingID(),
			TmpID2:         tmpID,
			MessageInfo: []*gmproto.MessageInfo{{
				Data: &gmproto.MessageInfo_MessageContent{MessageContent: &gmproto.MessageContent{Content: text}},
			}},
		},
		SIMPayload: chk.sim.GetSIMData().GetSIMPayload(),
		TmpID:      tmpID,
		// Tell the phone this must go as RCS. Whether the phone honours this
		// strictly (fail rather than fall back) is exactly what testing proves.
		ForceRCS: true,
	}
	resp, err := a.cli.SendMessage(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("send request failed: %w", err)
	}
	res := &SendResult{TmpID: tmpID, Check: chk, StatusHistory: []string{}}
	if resp.GetStatus() != gmproto.SendMessageResponse_SUCCESS {
		res.FinalStatus = "SEND_REQUEST_" + resp.GetStatus().String()
		return res, nil
	}

	timeout := time.After(wait)
	if err := awaitStatus(ctx, updates, timeout, res); err != nil {
		return res, err
	}
	if res.Sent {
		res.After = a.verifyAfterSend(ctx, rawTo)
	}
	return res, nil
}

// awaitStatus collects the phone's status updates for a send until it is
// delivered, fails, or wait runs out. An OUTGOING_COMPLETE keeps waiting for
// the delivery receipt behind it.
func awaitStatus(ctx context.Context, updates <-chan *gmproto.Message, timeout <-chan time.Time, res *SendResult) error {
	for {
		select {
		case m := <-updates:
			st := m.GetMessageStatus().GetStatus()
			if len(res.StatusHistory) == 0 || res.StatusHistory[len(res.StatusHistory)-1] != st.String() {
				res.StatusHistory = append(res.StatusHistory, st.String())
			}
			res.MessageID = m.GetMessageID()
			res.MessageType = m.GetType()
			res.FinalStatus = st.String()
			if isSendFailure(st) {
				return nil
			}
			if isSentOK(st) {
				res.Sent = true
				if st != gmproto.MessageStatusType_OUTGOING_COMPLETE {
					return nil // delivered or read: done
				}
			}
		case <-timeout:
			if res.FinalStatus == "" {
				res.FinalStatus = "NO_STATUS_WITHIN_TIMEOUT"
			}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// After a send, the phone gets this long to write down how the chat
// finalized before the result is returned without it.
const (
	verifyAttempts = 3
	verifyDelay    = 2 * time.Second
	verifyTimeout  = 10 * time.Second
)

// verifyAfterSend re-reads the chat once a message has gone out. Sending is
// what makes the phone finalize a new chat, so this is the first moment the
// tombstone can say whether the message went out encrypted. It gives the
// phone a few seconds to write that down. nil means the chat could not be
// re-read; the caller records that as unverified rather than guessing.
func (a *Agent) verifyAfterSend(ctx context.Context, rawTo string) *CheckResult {
	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()

	var last *CheckResult
	for attempt := 1; attempt <= verifyAttempts; attempt++ {
		chk, err := a.Check(ctx, rawTo)
		if err != nil {
			a.log.Warn().Err(err).Str("to", rawTo).Int("attempt", attempt).
				Msg("could not re-read the chat after sending")
			return last
		}
		last = chk
		if chk.E2EE != e2eeUnknown {
			return chk
		}
		select {
		case <-ctx.Done():
			return last
		case <-time.After(verifyDelay):
		}
	}
	return last
}

// --- thread ----------------------------------------------------------------

type ThreadMessage struct {
	MessageID string `json:"messageId"`
	Timestamp string `json:"timestamp"`
	Outgoing  bool   `json:"outgoing"`
	Status    string `json:"status"`
	Type      int64  `json:"type"`
	Transport string `json:"transport"` // from type: 1 SMS, 2/3 MMS, 4 RCS
	// Whether the conversation was end-to-end encrypted when this message was
	// sent: the newest protocol tombstone at or before the message's timestamp.
	E2EEAtSend   e2eeState `json:"e2eeAtSend"`
	E2EEEvidence string    `json:"e2eeEvidence,omitempty"`
	Text         string    `json:"text,omitempty"`
	// With ?raw=1: every field the phone sent, including ones libgm has not
	// named yet, so undocumented flags can be inspected rather than guessed.
	Raw           json.RawMessage `json:"raw,omitempty"`
	UnknownFields []string        `json:"unknownFields,omitempty"`
}

func transportFromType(t int64) string {
	switch t {
	case 1:
		return "SMS"
	case 2, 3:
		return "MMS"
	case 4:
		return "RCS"
	}
	return "unknown"
}

func (a *Agent) Thread(ctx context.Context, rawTo string, limit int64, raw bool) (*CheckResult, []ThreadMessage, error) {
	chk, err := a.Check(ctx, rawTo)
	if err != nil {
		return nil, nil, err
	}

	// Collect enough history to cover the protocol tombstones, not just the
	// messages being displayed.
	var history []*gmproto.Message
	var cursor *gmproto.Cursor
	for page := 0; page < 4; page++ {
		lm, err := a.cli.FetchMessages(ctx, chk.ConversationID, 50, cursor)
		if err != nil {
			return chk, nil, err
		}
		history = append(history, lm.GetMessages()...)
		cursor = lm.GetCursor()
		if int64(len(history)) >= limit && hasTombstone(history) || cursor == nil || len(lm.GetMessages()) == 0 {
			break
		}
	}
	sort.Slice(history, func(i, j int) bool { return history[i].GetTimestamp() > history[j].GetTimestamp() })

	out := make([]ThreadMessage, 0, limit)
	for i, m := range history {
		if int64(i) >= limit {
			break
		}
		st := m.GetMessageStatus().GetStatus()
		var text []string
		for _, part := range m.GetMessageInfo() {
			if c := part.GetMessageContent().GetContent(); c != "" {
				text = append(text, c)
			}
		}
		state, ev := e2eeAt(history, m.GetTimestamp())
		tm := ThreadMessage{
			MessageID:  m.GetMessageID(),
			Timestamp:  time.UnixMicro(m.GetTimestamp()).UTC().Format(time.RFC3339),
			Outgoing:   strings.HasPrefix(st.String(), "OUTGOING_"),
			Status:     st.String(),
			Type:       m.GetType(),
			Transport:  transportFromType(m.GetType()),
			E2EEAtSend: state,
			Text:       strings.Join(text, "\n"),
		}
		if ev != nil {
			tm.E2EEEvidence = ev.GetMessageStatus().GetStatus().String() + " @ " +
				time.UnixMicro(ev.GetTimestamp()).UTC().Format(time.RFC3339)
		}
		if raw {
			if b, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(m); err == nil {
				tm.Raw = b
			}
			tm.UnknownFields = unknownFields("message", m)
			tm.UnknownFields = append(tm.UnknownFields, unknownFields("messageStatus", m.GetMessageStatus())...)
			for i, part := range m.GetMessageInfo() {
				tm.UnknownFields = append(tm.UnknownFields, unknownFields(fmt.Sprintf("messageInfo[%d]", i), part)...)
			}
		}
		out = append(out, tm)
	}
	return chk, out, nil
}

func hasTombstone(msgs []*gmproto.Message) bool {
	state, _ := latestE2EE(msgs)
	return state != e2eeUnknown
}

// e2eeAt returns the conversation's encryption state as of timestamp ts.
func e2eeAt(msgs []*gmproto.Message, ts int64) (e2eeState, *gmproto.Message) {
	var before []*gmproto.Message
	for _, m := range msgs {
		if m.GetTimestamp() <= ts {
			before = append(before, m)
		}
	}
	return latestE2EE(before)
}

// unknownFields lists protobuf fields present on the wire that libgm's schema
// does not name. Byte fields are shown as text when printable, walked as a
// nested message when they parse as one, and as hex otherwise.
func unknownFields(where string, msg proto.Message) []string {
	if msg == nil || !msg.ProtoReflect().IsValid() {
		return nil
	}
	return walkUnknown(where, msg.ProtoReflect().GetUnknown(), 0)
}

func walkUnknown(where string, b []byte, depth int) []string {
	var out []string
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return append(out, where+" = <undecodable tail>")
		}
		b = b[n:]
		name := fmt.Sprintf("%s.field%d", where, num)
		switch typ {
		case protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return out
			}
			out = append(out, fmt.Sprintf("%s = %d", name, v))
			b = b[m:]
		case protowire.Fixed32Type:
			v, m := protowire.ConsumeFixed32(b)
			if m < 0 {
				return out
			}
			out = append(out, fmt.Sprintf("%s = %d (fixed32)", name, v))
			b = b[m:]
		case protowire.Fixed64Type:
			v, m := protowire.ConsumeFixed64(b)
			if m < 0 {
				return out
			}
			out = append(out, fmt.Sprintf("%s = %d (fixed64)", name, v))
			b = b[m:]
		case protowire.BytesType:
			v, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return out
			}
			out = append(out, describeBytes(name, v, depth)...)
			b = b[m:]
		default:
			m := protowire.ConsumeFieldValue(num, typ, b)
			if m < 0 {
				return out
			}
			out = append(out, fmt.Sprintf("%s = <wire type %d>", name, typ))
			b = b[m:]
		}
	}
	return out
}

func describeBytes(name string, v []byte, depth int) []string {
	switch {
	case len(v) == 0:
		return []string{name + ` = ""`}
	case printable(v):
		return []string{fmt.Sprintf("%s = %q", name, v)}
	case depth < 3 && parsesAsMessage(v):
		return walkUnknown(name, v, depth+1)
	}
	return []string{fmt.Sprintf("%s = 0x%x", name, v)}
}

func printable(b []byte) bool {
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

func parsesAsMessage(b []byte) bool {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 || num < 1 {
			return false
		}
		b = b[n:]
		m := protowire.ConsumeFieldValue(num, typ, b)
		if m < 0 {
			return false
		}
		b = b[m:]
	}
	return true
}

func (a *Agent) Activate(ctx context.Context) error {
	return a.cli.SetActiveSession(ctx)
}
