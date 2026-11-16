package rcs

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/zagnu-labs/rcs-messenger/internal/apperr"
)

const (
	CodeIdempotencyConflict apperr.Code = "rcs_idempotency_conflict"

	idempotencyTTL = 24 * time.Hour
)

// idemEntry is one keyed send: first caller runs it, everyone else with the
// same key waits for and replays its outcome. Errors are replayed too, so a
// retry can never turn into a second send; a caller that wants a fresh attempt
// uses a fresh key.
type idemEntry struct {
	phone string
	done  chan struct{}
	res   *Result
	err   error
	at    time.Time
}

type idemStore struct {
	mu      sync.Mutex
	entries map[string]*idemEntry
}

// start returns the entry for key and whether the caller created it.
func (st *idemStore) start(key, phone string, now time.Time) (*idemEntry, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.entries == nil {
		st.entries = map[string]*idemEntry{}
	}
	for k, e := range st.entries {
		select {
		case <-e.done:
			if now.Sub(e.at) > idempotencyTTL {
				delete(st.entries, k)
			}
		default: // still in flight, never swept
		}
	}
	if e, ok := st.entries[key]; ok {
		return e, false
	}
	e := &idemEntry{phone: phone, done: make(chan struct{}), at: now}
	st.entries[key] = e
	return e, true
}

func idempotencyConflictErr() *apperr.AppError {
	return apperr.New(CodeIdempotencyConflict, http.StatusUnprocessableEntity,
		"idempotency key was already used for a different number")
}

// SendKeyed is Send with an optional idempotency key. The send always runs
// detached from the caller's context: if the caller leaves (client hang-up,
// server write timeout) the send still finishes and is recorded. With a key,
// a repeat (sequential or while the first is in flight) returns the first
// outcome and never sends again. The key is bound to the normalized number;
// reuse with another number is a 422.
func (s *Service) SendKeyed(ctx context.Context, key, rawTo, message string) (*Result, error) {
	key = strings.TrimSpace(key)
	detached := context.WithoutCancel(ctx)
	if key == "" {
		return s.sendDetached(ctx, func() (*Result, error) { return s.Send(detached, rawTo, message) })
	}
	to, err := s.gate(rawTo)
	if err != nil {
		return nil, err
	}
	e, first := s.idem.start(key, to, time.Now())
	if !first && e.phone != to {
		return nil, idempotencyConflictErr()
	}
	if first {
		go func() {
			e.res, e.err = s.Send(detached, to, message)
			e.at = time.Now()
			close(e.done)
		}()
	}
	select {
	case <-e.done:
		if e.res != nil {
			cp := *e.res
			return &cp, e.err
		}
		return nil, e.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Service) sendDetached(ctx context.Context, fn func() (*Result, error)) (*Result, error) {
	type out struct {
		r *Result
		e error
	}
	ch := make(chan out, 1)
	go func() { r, e := fn(); ch <- out{r, e} }()
	select {
	case o := <-ch:
		return o.r, o.e
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
