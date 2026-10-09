package rcsphone

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/mautrix-gmessages/pkg/libgm"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
	"go.mau.fi/util/exhttp"
)

const (
	qrExpiry      = 30 * time.Second
	qrMaxAttempts = 20
	emojiWait     = 3 * time.Minute
)

type PairStatus struct {
	Running bool   `json:"running"`
	Paired  bool   `json:"paired"`
	Done    bool   `json:"done"`
	Phone   string `json:"phone,omitempty"`
	Error   string `json:"error,omitempty"`
}

type attempt struct {
	mu      sync.Mutex
	qr      string
	paired  bool
	done    bool
	phone   string
	failure string
	cancel  context.CancelFunc
}

type Pairing struct {
	store  *Store
	runner *Runner
	log    zerolog.Logger

	mu     sync.Mutex
	active *attempt
}

func NewPairing(store *Store, runner *Runner, log zerolog.Logger) *Pairing {
	return &Pairing{store: store, runner: runner, log: log}
}

func (p *Pairing) StartQR() error {
	p.mu.Lock()
	if p.active != nil {
		p.active.mu.Lock()
		running := !p.active.done
		p.active.mu.Unlock()
		if running {
			p.mu.Unlock()
			return errors.New("a pairing attempt is already running")
		}
	}
	p.mu.Unlock()

	ad := libgm.NewAuthData()
	cli := libgm.NewClient(ad, nil, p.log.With().Str("component", "libgm").Logger(), exhttp.ClientSettings{})
	cli.SetEventHandler(func(any) {})

	paired := make(chan *gmproto.PairedData, 1)
	callback := func(data *gmproto.PairedData) { paired <- data }
	cli.PairCallback.Store(&callback)

	base := p.log.WithContext(context.Background())
	if err := cli.FetchConfig(base); err != nil {
		return fmt.Errorf("fetching Messages config: %w", err)
	}

	bgCtx, cancel := context.WithCancel(base)
	qr, err := cli.StartLogin(bgCtx)
	if err != nil {
		cancel()
		return fmt.Errorf("starting pairing: %w", err)
	}

	att := &attempt{qr: qr, cancel: cancel}
	p.mu.Lock()
	p.active = att
	p.mu.Unlock()

	go p.watchQR(cli, att, paired)
	return nil
}

func (p *Pairing) watchQR(cli *libgm.Client, att *attempt, paired <-chan *gmproto.PairedData) {
	defer att.cancel()

	for tries := 1; ; tries++ {
		select {
		case data := <-paired:
			cli.Disconnect()
			if err := p.save(cli.AuthData); err != nil {
				att.finish("", "paired, but storing the session failed: "+err.Error())
				return
			}
			att.finish(data.GetMobile().GetSourceID(), "")
			p.log.Info().Str("phone", data.GetMobile().GetSourceID()).Msg("rcs: phone paired")
			return

		case <-time.After(qrExpiry):
			if tries >= qrMaxAttempts {
				cli.Disconnect()
				att.finish("", "the QR code was not scanned in time, start pairing again")
				return
			}
			next, err := cli.RefreshPhoneRelay()
			if err != nil {
				cli.Disconnect()
				att.finish("", "refreshing the QR code failed: "+err.Error())
				return
			}
			att.setQR(next)
		}
	}
}

func (p *Pairing) QR() (string, error) {
	p.mu.Lock()
	att := p.active
	p.mu.Unlock()
	if att == nil {
		return "", errors.New("no pairing attempt is running")
	}

	att.mu.Lock()
	defer att.mu.Unlock()
	if att.done {
		return "", errors.New("the pairing attempt has finished")
	}
	return att.qr, nil
}

func (p *Pairing) Status() PairStatus {
	p.mu.Lock()
	att := p.active
	p.mu.Unlock()
	if att == nil {
		return PairStatus{}
	}

	att.mu.Lock()
	defer att.mu.Unlock()
	return PairStatus{
		Running: !att.done,
		Paired:  att.paired,
		Done:    att.done,
		Phone:   att.phone,
		Error:   att.failure,
	}
}

func (p *Pairing) Cancel() {
	p.mu.Lock()
	att := p.active
	p.active = nil
	p.mu.Unlock()
	if att != nil {
		att.cancel()
		att.finish("", "pairing was cancelled")
	}
}

func (p *Pairing) PairWithCookies(ctx context.Context, har []byte) (string, error) {
	cookies, err := cookiesFromHARBytes(har)
	if err != nil {
		return "", err
	}
	if err := requireCookies(cookies); err != nil {
		return "", err
	}

	ad := libgm.NewAuthData()
	ad.SetCookies(cookies)
	cli := libgm.NewClient(ad, nil, p.log.With().Str("component", "libgm").Logger(), exhttp.ClientSettings{})
	cli.SetEventHandler(func(any) {})

	base := p.log.WithContext(ctx)
	if err := cli.FetchConfig(base); err != nil {
		return "", fmt.Errorf("fetching Messages config (the cookies may be expired): %w", err)
	}

	pairCtx, cancelPair := context.WithTimeout(p.log.WithContext(context.WithoutCancel(ctx)), emojiWait)

	emoji, sess, err := cli.StartGaiaPairing(pairCtx, pairCtx)
	if err != nil {
		cancelPair()
		cli.Disconnect()
		switch {
		case errors.Is(err, libgm.ErrNoDevicesFound):
			return "", errors.New("no phone found for this Google account, is Messages signed in on it")
		case errors.Is(err, libgm.ErrPairingInitTimeout):
			return "", errors.New("the phone did not respond, make sure it is online with Messages open")
		}
		return "", fmt.Errorf("starting Google pairing: %w", err)
	}

	go func() {
		defer cancelPair()
		phoneID, err := cli.FinishGaiaPairing(pairCtx, sess)
		cli.Disconnect()
		if err != nil {
			p.log.Error().Err(err).Msg("rcs: emoji pairing did not complete")
			return
		}
		if err := p.save(cli.AuthData); err != nil {
			p.log.Error().Err(err).Msg("rcs: paired, but storing the session failed")
			return
		}
		p.log.Info().Str("phone", phoneID).Msg("rcs: phone paired by emoji")
	}()

	return emoji, nil
}

func (p *Pairing) RefreshWithCookies(ctx context.Context, har []byte) error {
	ad, err := p.store.Load(ctx)
	if err != nil {
		return err
	}
	if ad.Mobile.GetSourceID() == "" {
		return errors.New("the stored session has no paired phone, pair first")
	}

	cookies, err := cookiesFromHARBytes(har)
	if err != nil {
		return err
	}
	if err := requireCookies(cookies); err != nil {
		return err
	}

	ad.SetCookies(cookies)
	cli := libgm.NewClient(ad, nil, p.log.With().Str("component", "libgm").Logger(), exhttp.ClientSettings{})
	base := p.log.WithContext(ctx)
	if err := cli.FetchConfig(base); err != nil {
		return fmt.Errorf("re-auth failed, the cookies may be for the wrong account or expired: %w", err)
	}
	if got := cli.Config.GetDeviceInfo().GetEmail(); got != "" && got != ad.Mobile.GetSourceID() {
		return fmt.Errorf("those cookies are for %s but this session is paired to %s", got, ad.Mobile.GetSourceID())
	}
	if err := cli.Connect(base); err != nil {
		return fmt.Errorf("config loaded but polling still failed: %w", err)
	}
	cli.Disconnect()

	if err := p.save(ad); err != nil {
		return err
	}
	p.log.Info().Str("phone", ad.Mobile.GetSourceID()).Msg("rcs: session refreshed")
	return nil
}

func (p *Pairing) save(ad *libgm.AuthData) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := p.store.Save(ctx, ad); err != nil {
		return err
	}
	if p.runner != nil {
		go func() {
			reload, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := p.runner.Reload(reload); err != nil {
				p.log.Error().Err(err).Msg("rcs: paired but could not connect with the new session")
			}
		}()
	}
	return nil
}

func (a *attempt) setQR(qr string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.qr = qr
}

func (a *attempt) finish(phone, failure string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.done = true
	a.phone = phone
	a.failure = failure
	a.paired = failure == "" && phone != ""
}

func requireCookies(cookies map[string]string) error {
	var missing []string
	for _, name := range requiredCookies {
		if cookies[name] == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("the HAR is missing required cookies: %s", strings.Join(missing, ", "))
	}
	return nil
}
