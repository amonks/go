package browsertest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
)

// TestPressAndPollWithoutScript is the race a suite loses driving a
// page with scripting off through the acts (CI run 2770): a press that
// sends a form whose answer is slow. An act's screen is script the page
// doesn't run, so it hears nothing and passes only if the new document
// beat its look, and Poll's in-page wait looks once, in the old
// document, and never again. The raw press and the wait from Go don't
// race the answer.
func TestPressAndPollWithoutScript(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, `<!doctype html><meta charset=utf-8><title>t</title>
<form action="/sent"><input type=hidden name=q value=held><button id=go style="width: 120px; height: 40px">Apply</button></form>`)
		case "/sent":
			time.Sleep(750 * time.Millisecond)
			fmt.Fprint(w, `<!doctype html><meta charset=utf-8><title>t</title><p id=done>sent</p>`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	ctx := NewBrowser(t)
	if err := chromedp.Run(ctx,
		emulation.SetScriptExecutionDisabled(true),
		Open(server.URL+"/"),
		PressWithoutScript("#go"),
		PollWithoutScript(`location.search === "?q=held" && document.querySelector("#done") !== null`),
	); err != nil {
		t.Fatal(err)
	}
}
