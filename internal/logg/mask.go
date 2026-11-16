package logg

import (
	"io"
	"regexp"
	"strings"
)

// Mask hides all but the last two digits of a phone number: +16823404163
// becomes +**********63. Logs say which chat without saying whose.
func Mask(number string) string {
	n := strings.TrimSpace(number)
	digits := 0
	for _, r := range n {
		if r >= '0' && r <= '9' {
			digits++
		}
	}
	if digits <= 2 {
		return n
	}
	keep := 2
	out := []rune(n)
	seen := 0
	for i := len(out) - 1; i >= 0; i-- {
		if out[i] < '0' || out[i] > '9' {
			continue
		}
		if seen < keep {
			seen++
			continue
		}
		out[i] = '*'
	}
	return string(out)
}

// A run of 10 to 15 digits, with an optional plus: a phone number, whatever
// field or error string it ended up in.
var numberRun = regexp.MustCompile(`\+?\d{10,15}`)

// ScrubNumbers masks every phone-number-shaped run in s.
func ScrubNumbers(s string) string {
	return numberRun.ReplaceAllStringFunc(s, Mask)
}

type scrubWriter struct{ w io.Writer }

// ScrubWriter wraps w so no log line leaves with a full phone number in it,
// including numbers inside error text and third-party library logs.
func ScrubWriter(w io.Writer) io.Writer { return scrubWriter{w} }

func (s scrubWriter) Write(p []byte) (int, error) {
	if _, err := s.w.Write([]byte(ScrubNumbers(string(p)))); err != nil {
		return 0, err
	}
	return len(p), nil
}
