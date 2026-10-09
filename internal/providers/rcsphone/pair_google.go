package rcsphone

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Google-account pairing (the emoji flow). The phone confirms by tapping the
// emoji shown here. It needs the Google session cookies of the account that is
// signed in to Google Messages on the phone; they are read in memory from
// either a cookies JSON file or a HAR captured on messages.google.com, and are
// never printed.

// SAPISID drives the auth hash; SID/HSID/SSID/OSID carry the session. Legacy
// APISID is gone on modern accounts (replaced by __Secure-1PAPISID etc.), so it
// is not required - all cookies are forwarded regardless.
var requiredCookies = []string{"SID", "HSID", "SSID", "SAPISID", "OSID"}
var optionalCookies = []string{"APISID", "__Secure-1PAPISID", "__Secure-3PAPISID", "__Secure-1PSIDTS", "__Secure-3PSID"}

// cookiesFromHAR reads the Google session cookies from a HAR captured while
// messages.google.com was open. Later requests win, so the freshest values are used.
func cookiesFromHARBytes(raw []byte) (map[string]string, error) {
	var har struct {
		Log struct {
			Entries []struct {
				Request struct {
					URL     string `json:"url"`
					Cookies []struct {
						Name  string `json:"name"`
						Value string `json:"value"`
					} `json:"cookies"`
					Headers []struct {
						Name  string `json:"name"`
						Value string `json:"value"`
					} `json:"headers"`
				} `json:"request"`
			} `json:"entries"`
		} `json:"log"`
	}
	if err := json.Unmarshal(raw, &har); err != nil {
		return nil, fmt.Errorf("not a valid HAR file: %w", err)
	}
	// Keep every cookie, not just the required six: Google's bound-session
	// cookies (__Secure-1PSIDTS, __Secure-3PSID, ...) must ride along or the
	// request is rejected 401. Scan every entry (a cookie like APISID is only
	// recorded on some Google hosts), but accept OSID only from the
	// messages.google.com host, since other hosts carry a different OSID.
	out := map[string]string{}
	for _, e := range har.Log.Entries {
		u, err := url.Parse(e.Request.URL)
		if err != nil {
			continue
		}
		onMessagesHost := u.Host == "messages.google.com"
		pairs := map[string]string{}
		for _, c := range e.Request.Cookies {
			pairs[c.Name] = c.Value
		}
		for _, h := range e.Request.Headers {
			if strings.EqualFold(h.Name, "cookie") {
				for _, part := range strings.Split(h.Value, ";") {
					if name, value, ok := strings.Cut(strings.TrimSpace(part), "="); ok {
						pairs[name] = value
					}
				}
			}
		}
		for name, value := range pairs {
			if value == "" {
				continue
			}
			if name == "OSID" && !onMessagesHost {
				continue
			}
			out[name] = value
		}
	}
	return out, nil
}
