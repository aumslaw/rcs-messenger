package rcs

import "testing"

func TestNaturalStopPhrasesAreOptOutsButSentencesAreNot(t *testing.T) {
	for _, yes := range []string{"please stop", "Stop texting me", "unsubscribe me", "pls stop", "STOP ALL!", "cancel", "stop", "Please stop texting us."} {
		if !IsOptOut(yes) {
			t.Errorf("%q must be an opt-out", yes)
		}
	}
	for _, no := range []string{"can you stop by tomorrow", "please stop by at 5", "I will stop by", "end of month works", "don't stop", "cancel my order of fries tomorrow"} {
		if IsOptOut(no) {
			t.Errorf("%q must NOT be an opt-out", no)
		}
	}
}
