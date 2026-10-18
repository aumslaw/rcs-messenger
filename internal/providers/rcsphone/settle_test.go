package rcsphone

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func TestOnlyTransientReasonsAreRetried(t *testing.T) {
	retried := []string{ErrRCSPending.Error(), ErrE2EEPending.Error()}
	for _, r := range retried {
		if !settles(r) {
			t.Errorf("%q should be retried, a freshly created conversation resolves out of it", r)
		}
	}

	final := []string{
		ErrNotRCS.Error(),
		ErrNotE2EE.Error(),
		ErrRCSOffOnSIM.Error(),
		ErrReadOnly.Error(),
		ErrPhoneOffline.Error(),
		ErrNotActive.Error(),
		ErrBadNumber.Error(),
		"",
	}
	for _, r := range final {
		if settles(r) {
			t.Errorf("%q must not be retried, waiting cannot change it", r)
		}
	}
}

// sequence feeds settle one check result per call, repeating the last.
func sequence(results ...*CheckResult) (func(context.Context) (*CheckResult, error), *int) {
	calls := 0
	return func(context.Context) (*CheckResult, error) {
		i := calls
		if i >= len(results) {
			i = len(results) - 1
		}
		calls++
		return results[i], nil
	}, &calls
}

func pendingCheck(cap CapabilityState) *CheckResult {
	c := rcsCheck()
	c.E2EE, c.e2eePending, c.E2EECapability = e2eeUnknown, true, cap
	return c
}

// The old loop sent anyway once the budget ran out while the lookup had not
// landed. An iPhone on RCS reads not_yet forever, so that sent plain RCS.
func TestSettleNeverSendsWhenTheBudgetRunsOutUnconfirmed(t *testing.T) {
	check, calls := sequence(pendingCheck(CapabilityNotYet))
	chk, reason, err := settle(context.Background(), zerolog.Nop(), time.Millisecond, 20*time.Millisecond, check)
	if err != nil {
		t.Fatal(err)
	}
	if reason != ErrE2EEPending.Error() {
		t.Fatalf("reason = %q, want e2ee_pending: a lookup that never lands is a refusal", reason)
	}
	if chk == nil {
		t.Fatal("the refused check should still be returned for the caller to report")
	}
	if *calls < 2 {
		t.Errorf("only %d check(s): it should have kept re-checking until the budget ran out", *calls)
	}
}

func TestSettleSendsOnceTheLookupLandsAsYes(t *testing.T) {
	check, calls := sequence(pendingCheck(CapabilityNotYet), pendingCheck(CapabilityNotYet), pendingCheck(CapabilityYes))
	_, reason, err := settle(context.Background(), zerolog.Nop(), time.Millisecond, time.Second, check)
	if err != nil || reason != "" {
		t.Fatalf("reason = %q, err = %v, want eligible", reason, err)
	}
	if *calls != 3 {
		t.Errorf("%d checks, want 3", *calls)
	}
}

func TestSettleStopsAtOnceOnAPermanentRefusal(t *testing.T) {
	check, calls := sequence(pendingCheck(CapabilityNo))
	_, reason, _ := settle(context.Background(), zerolog.Nop(), time.Millisecond, time.Second, check)
	if reason != ErrNotE2EE.Error() {
		t.Fatalf("reason = %q, want not_e2ee", reason)
	}
	if *calls != 1 {
		t.Errorf("%d checks: a permanent refusal must not be waited on", *calls)
	}
}

func TestSettleStopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	check, _ := sequence(pendingCheck(CapabilityNotYet))
	_, reason, _ := settle(ctx, zerolog.Nop(), time.Hour, time.Hour*2, check)
	if reason == "" {
		t.Fatal("a cancelled context must end in a refusal, not a send")
	}
}
