package logg

import (
	"bytes"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func TestMaskKeepsOnlyTheLastTwoDigits(t *testing.T) {
	for in, want := range map[string]string{
		"+16823404163": "+*********63",
		"6823404163":   "********63",
		"12":           "12",
		"":             "",
	} {
		if got := Mask(in); got != want {
			t.Errorf("Mask(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLogLinesNeverCarryAFullNumber(t *testing.T) {
	var buf bytes.Buffer
	l := zerolog.New(ScrubWriter(&buf))
	l.Error().Str("to", "+16823404163").Msg("opening +16823404163 failed: 6823404163")
	out := buf.String()
	if strings.Contains(out, "6823404163") {
		t.Fatalf("full number leaked: %s", out)
	}
	if !strings.Contains(out, "63") {
		t.Fatalf("expected the masked tail: %s", out)
	}
	buf.Reset()
	l.Info().Msg("at 2026-11-16T22:10:28Z took 120ms")
	if !strings.Contains(buf.String(), "2026-11-16T22:10:28Z") {
		t.Fatalf("timestamps must survive: %s", buf.String())
	}
}
