package rcsphone

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
	"go.mau.fi/util/exhttp"

	"github.com/zagnu-labs/rcs-messenger/internal/leader"
)

const (
	connectRetry = 30 * time.Second
	simWait      = 30 * time.Second
)

type Runner struct {
	store  *Store
	sender *Sender
	log    zerolog.Logger

	onInbound func(from, text, messageID string)

	mu      sync.Mutex
	agent   *Agent
	holding bool
}

func NewRunner(store *Store, sender *Sender, log zerolog.Logger) *Runner {
	return &Runner{store: store, sender: sender, log: log}
}

func (r *Runner) OnInbound(fn func(from, text, messageID string)) {
	r.onInbound = fn
}

func (r *Runner) Start(ctx context.Context, ldr leader.Checker) {
	r.log.Info().Msg("rcs phone runner started")

	ticker := time.NewTicker(connectRetry)
	defer ticker.Stop()

	r.reconcile(ctx, ldr)
	for {
		select {
		case <-ctx.Done():
			r.disconnect()
			return
		case <-ticker.C:
			r.reconcile(ctx, ldr)
		}
	}
}

func (r *Runner) reconcile(ctx context.Context, ldr leader.Checker) {
	shouldHold := ldr == nil || ldr.IsLeader()

	r.mu.Lock()
	holding, agent := r.holding, r.agent
	r.mu.Unlock()

	if !shouldHold {
		if holding {
			r.log.Info().Msg("rcs: no longer the leader, releasing the phone session")
			r.disconnect()
		}
		return
	}

	if holding && agent != nil && agent.cli.IsConnected() {
		return
	}
	if holding {
		r.log.Warn().Msg("rcs: connection dropped, reconnecting")
		r.disconnect()
	}

	if err := r.connect(ctx); err != nil {
		if errors.Is(err, ErrNoSession) {
			r.log.Debug().Msg("rcs: no phone paired yet")
			return
		}
		r.log.Error().Err(err).Msg("rcs: could not connect to the phone")
	}
}

func (r *Runner) connect(ctx context.Context) error {
	ad, err := r.store.Load(ctx)
	if err != nil {
		return err
	}

	agent := &Agent{
		log:         r.log,
		sims:        map[string]*gmproto.SIMCard{},
		waiters:     map[string]chan *gmproto.Message{},
		recent:      map[string]msgRecord{},
		convNumbers: map[string]string{},
	}
	agent.phoneResponding.Store(true)
	agent.persist = func(a *libgm.AuthData) {
		saveCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := r.store.Save(saveCtx, a); err != nil {
			r.log.Error().Err(err).Msg("rcs: refreshed token could not be stored")
		}
	}
	agent.onInbound = r.onInbound

	agent.cli = libgm.NewClient(ad, nil, r.log.With().Str("component", "libgm").Logger(), exhttp.ClientSettings{})
	agent.cli.SetEventHandler(agent.handleEvent)

	connCtx := r.log.WithContext(context.Background())
	if err := agent.cli.FetchConfig(connCtx); err != nil {
		r.log.Warn().Err(err).Msg("rcs: fetching config failed, continuing")
	}
	if err := agent.cli.Connect(connCtx); err != nil {
		return err
	}

	deadline := time.Now().Add(simWait)
	for time.Now().Before(deadline) && agent.simCount() == 0 {
		time.Sleep(500 * time.Millisecond)
	}

	r.mu.Lock()
	r.agent, r.holding = agent, true
	r.mu.Unlock()
	r.sender.attach(agent)

	for _, s := range agent.simList() {
		r.log.Info().Str("carrier", s.Carrier).Bool("rcs_enabled", s.RCSEnabled).Msg("rcs: SIM")
	}
	r.log.Info().Msg("rcs: connected to the phone")
	return nil
}

func (r *Runner) disconnect() {
	r.mu.Lock()
	agent := r.agent
	r.agent, r.holding = nil, false
	r.mu.Unlock()

	r.sender.detach()
	if agent != nil && agent.cli != nil {
		agent.cli.Disconnect()
	}
}

func (r *Runner) Reload(ctx context.Context) error {
	r.disconnect()
	return r.connect(ctx)
}
