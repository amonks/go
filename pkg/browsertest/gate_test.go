package browsertest

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chromedp/chromedp"

	"monks.co/pkg/csp"
)

// A page holding a form that asks before it submits fails its test
// unless it carries the gate that asks, live; what the gate does not
// read — a button with no form to submit, a button that submits
// nothing — fails nothing. The gate here stands in for pkg/confirm's
// (which this module doesn't import) by doing what makes one live:
// putting a submit listener on the window in the capture phase, asked
// for either way addEventListener takes it (true, or an options object
// with capture set). Carrying the gate's id is not enough: an empty
// script asks nothing, and neither does one listening in the bubble
// phase. Nor is the listener: one some other script puts on the window
// in the capture phase is not the gate.
func TestAFormThatAsksNeedsTheGate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce := r.Header.Get(csp.NonceHeader)
		gate := fmt.Sprintf(`<script id="monks-confirm-script" nonce="%s">window.addEventListener("submit", () => {}, true);</script>`, nonce)
		withGateID := func(body string) string {
			return fmt.Sprintf(`<!doctype html><head><script id="monks-confirm-script" nonce="%s">%s</script></head>`, nonce, body) +
				`<form method="post" data-confirm="Delete it?"><button>go</button></form>`
		}
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/form":
			fmt.Fprint(w, `<!doctype html><form method="post" data-confirm="Delete it?"><button>go</button></form>`)
		case "/button":
			fmt.Fprint(w, `<!doctype html><form method="post"><button data-confirm="Discard it?">go</button></form>`)
		case "/gated":
			fmt.Fprint(w, `<!doctype html><head>`+gate+`</head><form method="post" data-confirm="Delete it?"><button data-confirm="Discard it?">go</button></form>`)
		case "/gated-options":
			fmt.Fprint(w, withGateID(`window.addEventListener("submit", () => {}, {capture: true});`))
		case "/impostor":
			fmt.Fprintf(w, `<!doctype html><head><script nonce="%s">window.addEventListener("submit", () => {}, true);</script></head>`, nonce)
			fmt.Fprint(w, `<form method="post" data-confirm="Delete it?"><button>go</button></form>`)
		case "/empty":
			fmt.Fprint(w, withGateID(""))
		case "/bubbling":
			fmt.Fprint(w, withGateID(`window.addEventListener("submit", () => {});`))
		case "/others":
			fmt.Fprint(w, `<!doctype html><button data-confirm="Restart?">outside any form</button>`+
				`<form method="post" data-confirm=""><button type="button" data-confirm="Nothing?">submits nothing</button></form>`)
		case "/later":
			fmt.Fprintf(w, `<!doctype html><div id="slot"></div><script nonce="%s">
				addEventListener("load", () => setTimeout(() => {
					document.getElementById("slot").innerHTML = '<form method="post" action="/posted" data-confirm="Delete it?"><button id="b">go</button></form>';
				}));
			</script>`, nonce)
		default:
			fmt.Fprint(w, `<!doctype html><p>posted</p>`)
		}
	}))
	defer server.Close()

	for _, tc := range []struct {
		name, path, question string
		click                bool
	}{
		{"a form's question", "/form", "Delete it?", false},
		{"a button's question", "/button", "Discard it?", false},
		{"under the gate", "/gated", "", false},
		{"under the gate, its capture asked in an options object", "/gated-options", "", false},
		{"under a capture listener from a script without the gate's id", "/impostor", "Delete it?", false},
		{"under an empty script with the gate's id", "/empty", "Delete it?", false},
		{"under a script with the gate's id listening too late", "/bubbling", "Delete it?", false},
		{"what the gate doesn't read", "/others", "", false},
		{"a form inserted after load, when it submits", "/later", "Delete it?", true},
	} {
		var c collector
		t.Run(tc.name, func(t *testing.T) {
			ctx := NewBrowser(t, withFailures(c.add))
			actions := []chromedp.Action{Step("load the page", chromedp.Navigate(server.URL+tc.path))}
			if tc.click {
				actions = append(actions, Step("submit the inserted form", chromedp.Click("#b", chromedp.ByQuery)))
			}
			if err := chromedp.Run(ctx, actions...); err != nil {
				t.Fatal(err)
			}
		})
		// The subtest's browser is closed; what it reported is all it
		// will ever report.
		var got []UngatedConfirm
		for _, err := range c.all() {
			u, ok := errors.AsType[UngatedConfirm](err)
			if !ok {
				t.Errorf("%s: the guard reported %v", tc.name, err)
				continue
			}
			got = append(got, u)
		}
		switch {
		case tc.question == "" && len(got) != 0:
			t.Errorf("%s: reported %v, want nothing", tc.name, got)
		case tc.question != "" && (len(got) != 1 || got[0].Question != tc.question || got[0].Document != server.URL+tc.path):
			t.Errorf("%s: reported %v, want %s's %q once", tc.name, got, server.URL+tc.path, tc.question)
		}
	}
}
