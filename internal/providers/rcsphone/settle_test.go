package rcsphone

import "testing"

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
