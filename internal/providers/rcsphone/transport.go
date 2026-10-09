package rcsphone

import (
	"net/http"
	"strconv"
	"strings"

	"go.mau.fi/util/exhttp"
)

// authUserRoundTripper adds "X-Goog-AuthUser: <n>" to every request to a Google
// host. Google's multi-login uses that header to pick which signed-in account a
// cookie-authenticated request acts as, so it lets us drive a secondary account
// (e.g. authuser 3) with the browser's existing cookies - no fresh sign-in,
// no 2FA. authuser 0 is the default account and needs no header.
type authUserRoundTripper struct {
	base     http.RoundTripper
	authUser string
}

func (t *authUserRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	if strings.HasSuffix(host, "google.com") || strings.HasSuffix(host, "googleapis.com") {
		req = req.Clone(req.Context())
		// The RPC/polling endpoints select the account by this header.
		req.Header.Set("X-Goog-AuthUser", t.authUser)
		// The messages.google.com/web pages select the account by URL path
		// (/web/u/3/...), not the header. Insert the account index if absent.
		if host == "messages.google.com" && strings.HasPrefix(req.URL.Path, "/web/") &&
			!strings.HasPrefix(req.URL.Path, "/web/u/") {
			req.URL.Path = "/web/u/" + t.authUser + "/" + strings.TrimPrefix(req.URL.Path, "/web/")
		}
	}
	return t.base.RoundTrip(req)
}

// clientSettings returns HTTP settings that pin all Google requests to the given
// signed-in account index. authUser <= 0 returns default settings unchanged.
func clientSettings(authUser int) exhttp.ClientSettings {
	if authUser <= 0 {
		return exhttp.ClientSettings{}
	}
	n := strconv.Itoa(authUser)
	return exhttp.ClientSettings{
		TransportOverride: func(cs exhttp.ClientSettings) http.RoundTripper {
			return &authUserRoundTripper{
				base:     cs.Configure(&http.Transport{}),
				authUser: n,
			}
		},
	}
}
