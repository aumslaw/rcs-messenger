package rcs

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zagnu-labs/rcs-messenger/internal/providers/rcsdirect"
)

// heldSender blocks every send until release is closed and counts real sends.
type heldSender struct {
	*fakeSender
	n       int32
	started chan struct{}
	release chan struct{}
}

func newHeld() *heldSender {
	return &heldSender{fakeSender: &fakeSender{enabled: true}, started: make(chan struct{}, 8), release: make(chan struct{})}
}

func (h *heldSender) Send(ctx context.Context, to, msg string, o rcsdirect.SendOptions) (*rcsdirect.SendResult, error) {
	atomic.AddInt32(&h.n, 1)
	h.started <- struct{}{}
	<-h.release
	return &rcsdirect.SendResult{Sent: true, MessageID: "m1", MessageType: 4, FinalStatus: "Sent", After: &rcsdirect.Check{E2EE: "on"}}, nil
}

func TestIdempotentSequentialReplaySendsOnce(t *testing.T) {
	f := &fakeSender{enabled: true}
	s := NewService(f, Config{Enabled: true})
	a, err := s.SendKeyed(context.Background(), "k1", "+15551234567", "hello")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.SendKeyed(context.Background(), "k1", "+15551234567", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if f.sends != 1 {
		t.Fatalf("%d real sends, want 1", f.sends)
	}
	if *a != *b {
		t.Fatalf("replay differs: %+v vs %+v", a, b)
	}
}

func TestIdempotentConcurrentReplayWhileInFlight(t *testing.T) {
	h := newHeld()
	s := NewService(h, Config{Enabled: true})
	res := make([]*Result, 2)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		res[0], _ = s.SendKeyed(context.Background(), "k1", "+15551234567", "hello")
	}()
	<-h.started // first send is now held in flight
	wg.Add(1)
	go func() {
		defer wg.Done()
		res[1], _ = s.SendKeyed(context.Background(), "k1", "+15551234567", "hello")
	}()
	time.Sleep(50 * time.Millisecond)
	close(h.release)
	wg.Wait()
	if n := atomic.LoadInt32(&h.n); n != 1 {
		t.Fatalf("%d real sends, want 1", n)
	}
	if res[0] == nil || res[1] == nil || *res[0] != *res[1] {
		t.Fatalf("replay differs: %+v vs %+v", res[0], res[1])
	}
}

func TestIdempotencyKeyBoundToNumber(t *testing.T) {
	f := &fakeSender{enabled: true}
	s := NewService(f, Config{Enabled: true})
	if _, err := s.SendKeyed(context.Background(), "k1", "+15551234567", "hi"); err != nil {
		t.Fatal(err)
	}
	_, err := s.SendKeyed(context.Background(), "k1", "+15559876543", "hi")
	if err == nil || code(t, err) != CodeIdempotencyConflict {
		t.Fatalf("want idempotency conflict, got %v", err)
	}
	if f.sends != 1 {
		t.Fatalf("%d sends, want 1", f.sends)
	}
}

// The caller leaves mid-send: the send still finishes, is recorded, and a retry
// with the key returns that outcome without sending again.
func TestIdempotentCallerCancelThenReplay(t *testing.T) {
	h := newHeld()
	rr := &recordingResults{}
	s := NewService(h, Config{Enabled: true})
	s.SetResults(rr)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { _, err := s.SendKeyed(ctx, "k1", "+15551234567", "hello"); errc <- err }()
	<-h.started
	cancel()
	if err := <-errc; err == nil {
		t.Fatal("cancelled caller should get its context error")
	}
	close(h.release)
	var got *Result
	var err error
	for i := 0; i < 100; i++ {
		got, err = s.SendKeyed(context.Background(), "k1", "+15551234567", "hello")
		if err == nil {
			break
		}
	}
	if err != nil || got == nil || !got.Sent {
		t.Fatalf("replay after cancel: %+v %v", got, err)
	}
	if n := atomic.LoadInt32(&h.n); n != 1 {
		t.Fatalf("%d real sends, want 1", n)
	}
	if len(rr.recs) != 1 {
		t.Fatalf("outcome must be recorded once despite the caller leaving: %+v", rr.recs)
	}
}

func TestKeylessSendSurvivesCallerCancel(t *testing.T) {
	h := newHeld()
	rr := &recordingResults{}
	s := NewService(h, Config{Enabled: true})
	s.SetResults(rr)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { _, err := s.SendKeyed(ctx, "", "+15551234567", "hello"); errc <- err }()
	<-h.started
	cancel()
	<-errc
	close(h.release)
	for i := 0; i < 100 && func() bool {
		s.numberLock("+15551234567").Lock()
		defer s.numberLock("+15551234567").Unlock()
		return len(rr.recs) == 0
	}(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if len(rr.recs) != 1 {
		t.Fatalf("send must be recorded after caller left: %+v", rr.recs)
	}
}
