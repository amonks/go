package llm

import (
	"io"
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

// errorBodyLimit bounds how much of a failed response is read into the
// error it becomes. An API's error document is a few hundred bytes, so
// the limit never shortens one. It is there for whatever else answers
// at a base URL — a proxy's page, a misconfigured endpoint, a body that
// never ends — whose size would otherwise be this process's memory, and
// every log line the error lands in.
const errorBodyLimit = 64 << 10

// readErrorBody reads at most errorBodyLimit of a failed response's
// body. A read that fails partway keeps what arrived: the status is the
// error, and the body only explains it.
func readErrorBody(body io.Reader) []byte {
	b, _ := io.ReadAll(io.LimitReader(body, errorBodyLimit))
	return b
}
