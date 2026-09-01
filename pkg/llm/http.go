package llm

import (
	"net/http"
	"time"

	"monks.co/pkg/tracecontext"
)

func newHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: tracecontext.NewTransport(http.DefaultTransport),
		Timeout:   timeout,
	}
}
