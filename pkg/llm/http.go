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

func newHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: Transport,
		Timeout:   timeout,
	}
}
