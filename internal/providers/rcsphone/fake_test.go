package rcsphone

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

// fakeGM stands in for libgm.Client and the phone behind it. It speaks in
// libgm's own protobuf types, so everything the Agent does with a reply (read
// the tombstones, decode the capability record, match status updates to the
// send) runs for real. What it cannot prove is that the real phone replies this
// way: that needs a paired session. See AUDIT.md, "web path".
type fakeGM struct {
	mu    sync.Mutex
	agent *Agent

	conv       *gmproto.Conversation
	msgs       []*gmproto.Message // the thread, any order
	capability []byte             // GET_CONVERSATION_TYPE field 8; nil makes the probe fail
	probeErr   error
	createErr  error
	fetchErr   error

	// onCheck runs at the start of every GetOrCreateConversation with the
	// 1-based call number, so a test can let a chat change over time.
	onCheck func(f *fakeGM, n int)

	// What SendMessage does: reply, then push these statuses as events.
	sendStatus       gmproto.SendMessageResponse_Status
	sendErr          error
	statuses         []gmproto.MessageStatusType
	messageType      int64           // 4 = RCS
	afterSend        func(f *fakeGM) // runs once the request is accepted, to change the thread
	breakReadOnError bool            // SendMessage errors and the thread becomes unreadable
	landOnError      bool            // SendMessage errors, but the message reached the thread
	silent           bool            // accept the request but never send a status
	hold             chan struct{}   // closes when SendMessage may return (concurrency test)
	sends            []*gmproto.SendMessageRequest
	inflight         int
	maxInflight      int
	checks           int
	connected        bool
	loggedIn         bool
}

func newFakeGM() *fakeGM {
	return &fakeGM{
		conv: &gmproto.Conversation{
			ConversationID:    "conv1",
			Type:              gmproto.ConversationType_RCS,
			SendMode:          gmproto.ConversationSendMode_SEND_MODE_AUTO,
			DefaultOutgoingID: "sim1",
		},
		capability:  capabilityBytes(1, 1, []byte{2, 1}, 1790790119943),
		sendStatus:  gmproto.SendMessageResponse_SUCCESS,
		statuses:    []gmproto.MessageStatusType{gmproto.MessageStatusType_OUTGOING_SENDING, gmproto.MessageStatusType_OUTGOING_COMPLETE, gmproto.MessageStatusType_OUTGOING_DELIVERED},
		messageType: 4,
		connected:   true,
		loggedIn:    true,
	}
}

// newSimAgent builds an Agent wired to f with one SIM that has RCS on.
func newSimAgent(f *fakeGM) *Agent {
	a := &Agent{
		log:         zerolog.Nop(),
		cli:         f,
		sims:        map[string]*gmproto.SIMCard{},
		waiters:     map[string]chan *gmproto.Message{},
		recent:      map[string]msgRecord{},
		convNumbers: map[string]string{},
	}
	a.phoneResponding.Store(true)
	a.sims["sim1"] = &gmproto.SIMCard{
		SIMParticipant: &gmproto.SIMParticipant{ID: "sim1"},
		RCSChats:       &gmproto.RCSChats{Enabled: true},
		SIMData:        &gmproto.SIMData{SIMPayload: &gmproto.SIMPayload{}},
	}
	f.agent = a
	return a
}

// thread helpers -----------------------------------------------------------

func (f *fakeGM) setTombstones(ts ...*gmproto.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = ts
}

func (f *fakeGM) addMessage(m *gmproto.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, m)
}

func (f *fakeGM) sendCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sends)
}

// libgm.Client surface ------------------------------------------------------

func (f *fakeGM) IsConnected() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.connected }
func (f *fakeGM) IsLoggedIn() bool  { f.mu.Lock(); defer f.mu.Unlock(); return f.loggedIn }
func (f *fakeGM) Disconnect()       {}

func (f *fakeGM) GetOrCreateConversation(_ context.Context, _ *gmproto.GetOrCreateConversationRequest) (*gmproto.GetOrCreateConversationResponse, error) {
	f.mu.Lock()
	f.checks++
	n, hook := f.checks, f.onCheck
	f.mu.Unlock()
	if hook != nil {
		hook(f, n)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &gmproto.GetOrCreateConversationResponse{Conversation: proto.Clone(f.conv).(*gmproto.Conversation)}, nil
}

func (f *fakeGM) GetConversationType(_ context.Context, _ string) (*gmproto.GetConversationTypeResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.probeErr != nil {
		return nil, f.probeErr
	}
	resp := &gmproto.GetConversationTypeResponse{}
	if f.capability != nil {
		resp.ProtoReflect().SetUnknown(f.capability)
	}
	return resp, nil
}

func (f *fakeGM) GetConversation(_ context.Context, _ string) (*gmproto.Conversation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return proto.Clone(f.conv).(*gmproto.Conversation), nil
}

func (f *fakeGM) FetchMessages(_ context.Context, _ string, _ int64, _ *gmproto.Cursor) (*gmproto.ListMessagesResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	return &gmproto.ListMessagesResponse{Messages: append([]*gmproto.Message(nil), f.msgs...)}, nil
}

func (f *fakeGM) MarkRead(context.Context, string, string) error               { return nil }
func (f *fakeGM) SetTyping(context.Context, string, *gmproto.SIMPayload) error { return nil }
func (f *fakeGM) SetActiveSession(context.Context) error                       { return nil }

func (f *fakeGM) SendMessage(_ context.Context, req *gmproto.SendMessageRequest) (*gmproto.SendMessageResponse, error) {
	f.mu.Lock()
	f.sends = append(f.sends, req)
	f.inflight++
	if f.inflight > f.maxInflight {
		f.maxInflight = f.inflight
	}
	hold, after, silent := f.hold, f.afterSend, f.silent
	status, err := f.sendStatus, f.sendErr
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.inflight--; f.mu.Unlock() }()

	if hold != nil {
		<-hold
	}
	if err != nil {
		f.mu.Lock()
		if f.breakReadOnError {
			f.fetchErr = errPhoneGone
		}
		if f.landOnError {
			f.msgs = append(f.msgs, &gmproto.Message{MessageID: "msg-landed", TmpID: req.GetTmpID(), Type: f.messageType})
		}
		f.mu.Unlock()
		return nil, err
	}
	if status != gmproto.SendMessageResponse_SUCCESS {
		return &gmproto.SendMessageResponse{Status: status}, nil
	}
	if after != nil {
		after(f)
	}
	if !silent {
		for i, st := range f.statuses {
			f.agent.handleEvent(&libgm.WrappedMessage{Message: &gmproto.Message{
				MessageID:      "msg1",
				TmpID:          req.GetTmpID(),
				ConversationID: req.GetConversationID(),
				Type:           f.messageType,
				Timestamp:      time.Now().UnixMicro() + int64(i),
				MessageStatus:  &gmproto.MessageStatus{Status: st},
			}})
		}
	}
	return &gmproto.SendMessageResponse{Status: gmproto.SendMessageResponse_SUCCESS}, nil
}

var errPhoneGone = errors.New("phone gone")
