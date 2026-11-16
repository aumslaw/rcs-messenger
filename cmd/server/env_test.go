package main

import "testing"

func TestSettingsThatDoNotParseAreErrorsNotDefaults(t *testing.T) {
	for _, bad := range []string{"yes", "on", "1.5", "tru"} {
		if _, err := parseBool("RCS_DRY_RUN", bad, false); err == nil {
			t.Errorf("RCS_DRY_RUN=%q must be an error, not silently the default", bad)
		}
	}
	for _, good := range []string{"true", "false", "1", "0", "TRUE"} {
		if _, err := parseBool("RCS_DRY_RUN", good, false); err != nil {
			t.Errorf("%q should parse: %v", good, err)
		}
	}
	if v, err := parseBool("RCS_DRY_RUN", "", true); err != nil || !v {
		t.Errorf("unset keeps the default: %v %v", v, err)
	}
	for _, bad := range []string{"45s", "-1", "abc", "4.5"} {
		if _, err := parseInt("RCS_ANDROID_MIN_GAP_SECONDS", bad, 45); err == nil {
			t.Errorf("RCS_ANDROID_MIN_GAP_SECONDS=%q must be an error", bad)
		}
	}
	if v, err := parseInt("RCS_ANDROID_MIN_GAP_SECONDS", "60", 45); err != nil || v != 60 {
		t.Errorf("60 should parse: %v %v", v, err)
	}
	if v, err := parseInt("X", "", 45); err != nil || v != 45 {
		t.Errorf("unset keeps the default: %v %v", v, err)
	}
}
