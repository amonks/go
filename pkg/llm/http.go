package llm

import (
	"net/http"
	"time"
)

// Transport is the RoundTripper behind every request this package
// sends. Nil — the default — means net/http's current default
// transport, so the published module stands alone; a host that wants
// tracing or other instrumentation installs its own, the way
// apps/llmproxy installs the fleet's traced transport. The var is
// unsynchronized, so it must be installed before the first call:
// replacing it while requests are in flight is a data race.
var Transport http.RoundTripper

// newHTTPClient is the one client constructor every request in this
// package goes through, so the redirect rule lives here rather than in
// each adapter. No provider API needs a redirect, and following one
// would carry the key and the prompt to wherever the Location points:
// net/http drops Authorization on a cross-host hop but keeps x-api-key
// and x-goog-api-key, and a 307 or 308 re-sends the body. A 3xx is
// handed back as the response, which each adapter reports as an error.
func newHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport:     Transport,
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
