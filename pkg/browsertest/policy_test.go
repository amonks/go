package browsertest

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"monks.co/pkg/csp"
)

// collector gathers what the guard reports, in place of failing the
// test, for the tests that watch it refuse things.
type collector struct {
	mu     sync.Mutex
	failed []error
}

func (c *collector) add(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failed = append(c.failed, err)
}

// violations waits, briefly, for at least n reports — a browser posts
// them after the page has moved on — and returns what arrived.
func (c *collector) violations(t *testing.T, n int) []Violation {
	t.Helper()
	deadline := time.Now().Add(StepTimeout)
	for {
		c.mu.Lock()
		var got []Violation
		for _, err := range c.failed {
			if v, ok := errors.AsType[Violation](err); ok {
				got = append(got, v)
			}
		}
		c.mu.Unlock()
		if len(got) >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (c *collector) all() []error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.failed)
}

// probePage is a page rendered the way the fleet renders one: the
// nonce the request carried on the scripts it trusts. Beside them, the
// three things the nonce does not cover, and the two things the policy
// grants without one.
func probePage(nonce string) string {
	return fmt.Sprintf(`<!doctype html><title>probe</title><body>
<script nonce="%[1]s">window.nonced = true</script>
<script>window.unnonced = true</script>
<button id="b" onclick="window.handler = true">b</button>
<script nonce="%[1]s">
	document.getElementById("b").click();
	try { eval("window.evaled = true") } catch (e) {}
	WebAssembly.compile(new Uint8Array([0, 97, 115, 109, 1, 0, 0, 0])).then(() => { window.wasm = true });
	new Worker(URL.createObjectURL(new Blob(["postMessage(1)"]))).onmessage = () => { window.worker = true };
</script>`, nonce)
}

// The policy is the proxy's: what the nonce covers runs, the tag, the
// handler and the eval it does not cover are refused and reported, and
// wasm and a blob: worker — which the policy grants — run unreported.
// The nonce is fresh on every load, as the proxy mints it.
func TestPagesRunUnderTheScriptPolicy(t *testing.T) {
	var mu sync.Mutex
	var nonces []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce := r.Header.Get(csp.NonceHeader)
		mu.Lock()
		nonces = append(nonces, nonce)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, probePage(nonce))
	}))
	defer server.Close()

	var c collector
	ctx := NewBrowser(t, withFailures(c.add))
	var ran map[string]bool
	if err := chromedp.Run(ctx,
		Step("load the page", chromedp.Navigate(server.URL)),
		Poll(`window.wasm === true && window.worker === true`),
		chromedp.Evaluate(`({nonced: !!window.nonced, unnonced: !!window.unnonced, handler: !!window.handler, evaled: !!window.evaled})`, &ran),
		Step("load it again", chromedp.Navigate(server.URL+"/again")),
		Poll(`window.nonced === true`),
	); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"nonced": true, "unnonced": false, "handler": false, "evaled": false}
	for k, v := range want {
		if ran[k] != v {
			t.Errorf("window.%s = %v, want %v", k, ran[k], v)
		}
	}

	got := c.violations(t, 6)
	var first []string
	for _, v := range got {
		if v.Document == server.URL+"/" {
			first = append(first, v.Directive+" "+v.Blocked)
		}
	}
	slices.Sort(first)
	if wantFirst := []string{"script-src eval", "script-src-attr inline", "script-src-elem inline"}; !slices.Equal(first, wantFirst) {
		t.Errorf("violations on the first load = %q, want %q (all: %v)", first, wantFirst, got)
	}
	if !strings.Contains(fmt.Sprint(got), "the fleet's policy") {
		t.Errorf("a violation should say which policy refused it: %v", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(nonces) < 2 || nonces[0] == "" || nonces[0] == nonces[1] {
		t.Errorf("nonces the server saw = %q, want a fresh one per load", nonces)
	}
}

// A response that governs its own scripts gets no second script policy,
// as the proxy adds none, and what its own policy refuses is the page's
// business: it is not the fleet's policy refusing it.
func TestAPageWithItsOwnScriptPolicyKeepsIt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/self.js" {
			w.Header().Set("Content-Type", "text/javascript")
			fmt.Fprint(w, `window.self_ran = true`)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Security-Policy", "script-src 'self'")
		fmt.Fprint(w, `<!doctype html><script>window.inline = true</script><script src="/self.js"></script>`)
	}))
	defer server.Close()

	var c collector
	ctx := NewBrowser(t, withFailures(c.add))
	var inline bool
	if err := chromedp.Run(ctx,
		Step("load the page", chromedp.Navigate(server.URL)),
		Poll(`window.self_ran === true`),
		chromedp.Evaluate(`!!window.inline`, &inline),
	); err != nil {
		t.Fatal(err)
	}
	if inline {
		t.Error("the page's own policy should have refused its inline script")
	}
	// Give a report the time one takes to arrive, then insist none did.
	time.Sleep(500 * time.Millisecond)
	if got := c.all(); len(got) != 0 {
		t.Errorf("the guard reported %v on a page that governs its own scripts", got)
	}
}

// A cookie the page is given still comes back, and a page the browser
// would serve from its cache is still served under the policy: the
// guard drops the conditional headers that would let the server answer
// 304, and a cached copy is not run without the policy.
func TestTheGuardKeepsCookiesAndOutlastsTheCache(t *testing.T) {
	var mu sync.Mutex
	var cookies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		cookies = append(cookies, r.Header.Get("Cookie"))
		mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "kept", Path: "/"})
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Cache-Control", "max-age=3600")
		w.Header().Set("ETag", `"one"`)
		if r.Header.Get("If-None-Match") != "" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		fmt.Fprint(w, probePage(r.Header.Get(csp.NonceHeader)))
	}))
	defer server.Close()

	var c collector
	ctx := NewBrowser(t, withFailures(c.add))
	var again map[string]bool
	if err := chromedp.Run(ctx,
		Step("load the page", chromedp.Navigate(server.URL)),
		Poll(`window.nonced === true`),
		Step("leave it", chromedp.Navigate(server.URL+"/elsewhere")),
		Poll(`window.nonced === true`),
		Step("come back", chromedp.Navigate(server.URL)),
		Poll(`window.nonced === true`),
		chromedp.Evaluate(`({unnonced: !!window.unnonced, handler: !!window.handler})`, &again),
	); err != nil {
		t.Fatal(err)
	}
	if again["unnonced"] || again["handler"] {
		t.Errorf("the second load ran %v, which the policy refuses", again)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(cookies) < 3 || cookies[1] != "session=kept" || cookies[2] != "session=kept" {
		t.Errorf("cookies the server saw = %q, want three loads, the one it set sent back on the last two", cookies)
	}
}

// The fleet's directives ride every page, a page governing its own
// scripts included: a form posting off the origin is refused and
// reported, as form-action 'self' refuses it in production.
func TestAFormPostingElsewhereIsRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/submit.js" {
			w.Header().Set("Content-Type", "text/javascript")
			fmt.Fprint(w, `document.getElementById("f").submit()`)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Security-Policy", "script-src 'self'")
		fmt.Fprint(w, `<!doctype html><form id="f" method="post" action="http://127.0.0.2:1/collect"></form><script src="/submit.js"></script>`)
	}))
	defer server.Close()

	var c collector
	ctx := NewBrowser(t, withFailures(c.add))
	if err := chromedp.Run(ctx, Step("load the page", chromedp.Navigate(server.URL))); err != nil {
		t.Fatal(err)
	}
	got := c.violations(t, 1)
	if len(got) != 1 || got[0].Directive != "form-action" {
		t.Errorf("violations = %v, want the form's post refused under form-action", got)
	}
}

// Answer answers a request in the browser, and leaves the guard's own
// patterns standing: registering an answer is a Fetch.enable, which
// replaces every pattern the tab had.
func TestAnswerLeavesThePageGuarded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, probePage(r.Header.Get(csp.NonceHeader)))
	}))
	defer server.Close()

	var c collector
	ctx := NewBrowser(t, withFailures(c.add))
	var asked sync.WaitGroup
	asked.Add(1)
	Answer(t, ctx, "https://example.invalid/*", network.ResourceTypeDocument, func(r *network.Request) Response {
		defer asked.Done()
		return Response{Header: http.Header{"Content-Type": {"text/html"}}, Body: []byte(`<p id="answered">answered ` + r.URL + `</p>`)}
	})
	var text string
	if err := chromedp.Run(ctx,
		Step("load a page", chromedp.Navigate(server.URL)),
		Poll(`window.nonced === true`),
		Step("follow a link off the machine", chromedp.Navigate("https://example.invalid/away")),
		Step("read the answer", chromedp.Text("#answered", &text, chromedp.ByQuery)),
	); err != nil {
		t.Fatal(err)
	}
	asked.Wait()
	if text != "answered https://example.invalid/away" {
		t.Errorf("answered page reads %q", text)
	}
	if got := c.violations(t, 3); len(got) < 3 {
		t.Errorf("after Answer the page reported %v, want the guard still refusing its three", got)
	}
}

// A tab NewTab opens is guarded too: the Fetch domain is per target.
func TestNewTabIsGuarded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, probePage(r.Header.Get(csp.NonceHeader)))
	}))
	defer server.Close()

	var c collector
	ctx := NewBrowser(t, withFailures(c.add))
	tab := NewTab(t, ctx)
	var unnonced bool
	if err := chromedp.Run(tab,
		Step("load the page in the second tab", chromedp.Navigate(server.URL)),
		Poll(`window.nonced === true`),
		chromedp.Evaluate(`!!window.unnonced`, &unnonced),
	); err != nil {
		t.Fatal(err)
	}
	if unnonced {
		t.Error("the second tab ran a script the policy refuses")
	}
	if got := c.violations(t, 3); len(got) < 3 {
		t.Errorf("the second tab reported %v, want its three refusals", got)
	}
}

// A suite that enables Fetch itself replaces the guard's patterns, and
// its pages run with every script allowed — which looks like a page
// with nothing to refuse, unless something notices. The guard notices
// a document committing that it never handled.
func TestAPageTheGuardNeverSawFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><p>unguarded</p>`)
	}))
	defer server.Close()

	var c collector
	ctx := NewBrowser(t, withFailures(c.add))
	if err := chromedp.Run(ctx,
		fetch.Enable().WithPatterns([]*fetch.RequestPattern{{URLPattern: "https://elsewhere.invalid/*"}}),
		Step("load the page", chromedp.Navigate(server.URL+"/page")),
	); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(StepTimeout)
	for len(c.all()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	got := c.all()
	if len(got) != 1 || !strings.Contains(got[0].Error(), server.URL+"/page") || !strings.Contains(got[0].Error(), "browsertest.Answer") {
		t.Errorf("guard reported %v, want the unguarded page named, with the way out", got)
	}
}

func TestMatchURLPattern(t *testing.T) {
	for _, tc := range []struct {
		pattern, url string
		want         bool
	}{
		{"*", "https://ss.cx/cc/", true},
		{"", "https://ss.cx/cc/", true},
		{"https://ss.cx/*", "https://ss.cx/cc/improve", true},
		{"https://ss.cx/*", "https://monks.co/", false},
		{"https://ss.cx/c?/", "https://ss.cx/cc/", true},
		{"https://ss.cx/c?/", "https://ss.cx/c/", false},
		{`https://ss.cx/\*`, "https://ss.cx/*", true},
		{`https://ss.cx/\*`, "https://ss.cx/x", false},
		{"*.png", "http://127.0.0.1:1/a/b.png", true},
		{"*.png", "http://127.0.0.1:1/a/b.png?x", false},
	} {
		if got := matchURLPattern(tc.pattern, tc.url); got != tc.want {
			t.Errorf("matchURLPattern(%q, %q) = %v, want %v", tc.pattern, tc.url, got, tc.want)
		}
	}
}

// A page the guard answered still reaches the suite's other loopback
// servers, as one Chrome loaded from the network does. A fulfilled
// response carries no remote address, so Chrome cannot tell it came
// from loopback, and its Local Network Access checks would refuse every
// request from it to another loopback origin.
func TestAGuardedPageReachesAnotherLoopbackOrigin(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "reached")
	}))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!doctype html><script nonce="%s">fetch(%q).then(r => r.text()).then(t => { window.fetched = t }, e => { window.fetched = String(e) })</script>`, r.Header.Get(csp.NonceHeader), other.URL)
	}))
	defer server.Close()

	ctx := NewBrowser(t)
	var fetched string
	if err := chromedp.Run(ctx,
		Step("load the page", chromedp.Navigate(server.URL)),
		Poll(`window.fetched !== undefined`),
		chromedp.Evaluate(`window.fetched`, &fetched),
	); err != nil {
		t.Fatal(err)
	}
	if fetched != "reached" {
		t.Errorf("the page's fetch of another loopback origin got %q", fetched)
	}
}

// A document a suite answers itself is a page like any other: it runs
// under the policy, and its answer is handed the nonce the request
// carries, so an answer standing a page up at its production URL can
// render the scripts it trusts.
func TestAnAnsweredPageRunsUnderThePolicy(t *testing.T) {
	var c collector
	ctx := NewBrowser(t, withFailures(c.add))
	Answer(t, ctx, "https://example.invalid/*", network.ResourceTypeDocument, func(r *network.Request) Response {
		nonce, _ := r.Headers[csp.NonceHeader].(string)
		return Response{Header: http.Header{"Content-Type": {"text/html"}}, Body: []byte(probePage(nonce))}
	})
	var ran map[string]bool
	if err := chromedp.Run(ctx,
		Step("load the answered page", chromedp.Navigate("https://example.invalid/page")),
		Poll(`window.nonced === true`),
		chromedp.Evaluate(`({unnonced: !!window.unnonced, handler: !!window.handler})`, &ran),
	); err != nil {
		t.Fatal(err)
	}
	if ran["unnonced"] || ran["handler"] {
		t.Errorf("the answered page ran %v, which the policy refuses", ran)
	}
	if got := c.violations(t, 3); len(got) < 3 {
		t.Errorf("the answered page reported %v, want its three refusals", got)
	}
}

// A content origin gets the content-domain policy instead: the suite's
// own pages may frame it, it posts nowhere, and it states no script
// policy, its bytes carrying their own sandbox.
func TestAContentOriginGetsTheContentPolicy(t *testing.T) {
	content := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><script>parent.postMessage("content ran", "*")</script>`)
	}))
	defer content.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!doctype html><script nonce="%s">addEventListener("message", e => { window.heard = e.data })</script><iframe src="%s/file"></iframe>`, r.Header.Get(csp.NonceHeader), content.URL)
	}))
	defer server.Close()

	var c collector
	ctx := NewBrowser(t, withFailures(c.add))
	ContentOrigin(t, ctx, content.URL)
	var heard string
	if err := chromedp.Run(ctx,
		Step("load the page framing the content", chromedp.Navigate(server.URL)),
		Poll(`window.heard !== undefined`),
		chromedp.Evaluate(`window.heard`, &heard),
	); err != nil {
		t.Fatal(err)
	}
	if heard != "content ran" {
		t.Errorf("the framed content said %q", heard)
	}
	time.Sleep(500 * time.Millisecond)
	if got := c.all(); len(got) != 0 {
		t.Errorf("the guard reported %v for a framed content origin", got)
	}
}

// ServeAt stands a handler up at a production origin: its pages run at
// that origin under the policy, with the nonce in the request, so a
// form posting to the origin's own absolute URLs — same-origin there —
// is admitted, and the requests reach the handler with their bodies.
func TestServeAtAProductionOrigin(t *testing.T) {
	var mu sync.Mutex
	var posted string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			r.ParseForm()
			mu.Lock()
			posted = r.URL.Path + " " + r.Form.Get("doc")
			mu.Unlock()
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<!doctype html><p id="done">posted</p>`)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!doctype html><form id="f" method="post" action="https://ss.example/cc/run"><input name="doc" value="tidy.md"></form>
<script>window.unnonced = true</script>
<script nonce="%s">setTimeout(() => document.getElementById("f").submit(), 300)</script>`, r.Header.Get(csp.NonceHeader))
	})

	var c collector
	ctx := NewBrowser(t, withFailures(c.add))
	ServeAt(t, ctx, "https://ss.example", handler)
	if err := chromedp.Run(ctx,
		Step("load the page at its production origin", chromedp.Navigate("https://ss.example/text/doc")),
		Step("the form posts", chromedp.WaitVisible("#done", chromedp.ByQuery)),
	); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if posted != "/cc/run tidy.md" {
		t.Errorf("the handler got the post %q", posted)
	}
	got := c.violations(t, 1)
	if len(got) != 1 || got[0].Directive != "script-src-elem" {
		t.Errorf("violations = %v, want only the un-nonced script refused", got)
	}
}

// A refusal as the last thing a test does still fails it: the browser
// reports a refusal after the fact, and a test that ends at once would
// otherwise close the browser with the report unsent. Each shape is run
// several times, since what it guards against is a race with teardown.
func TestARefusalAtTheEndOfATestIsCaught(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce := r.Header.Get(csp.NonceHeader)
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/inline":
			fmt.Fprint(w, `<!doctype html><script>window.unnonced = true</script>`)
		case "/eval":
			fmt.Fprintf(w, `<!doctype html><button id="b">b</button><script nonce="%s">document.getElementById("b").addEventListener("click", () => { try { eval("1") } catch (e) {} })</script>`, nonce)
		case "/form":
			fmt.Fprint(w, `<!doctype html><form method="post" action="http://127.0.0.2:1/collect"><button id="b">send</button></form>`)
		}
	}))
	defer server.Close()

	for _, tc := range []struct {
		name, path, directive string
		click                 bool
	}{
		{"an inline script on the last page", "/inline", "script-src-elem", false},
		{"an eval in the last click", "/eval", "script-src", true},
		{"a form the last click posts elsewhere", "/form", "form-action", true},
	} {
		for i := range 4 {
			var c collector
			t.Run(fmt.Sprintf("%s/%d", tc.name, i), func(t *testing.T) {
				ctx := NewBrowser(t, withFailures(c.add))
				actions := []chromedp.Action{Step("load the page", chromedp.Navigate(server.URL+tc.path))}
				if tc.click {
					actions = append(actions, Step("click", chromedp.Click("#b", chromedp.ByQuery)))
				}
				if err := chromedp.Run(ctx, actions...); err != nil {
					t.Fatal(err)
				}
			})
			// The subtest's browser is closed; what it reported is all
			// it will ever report.
			got := c.violations(t, 0)
			if !slices.ContainsFunc(got, func(v Violation) bool { return v.Directive == tc.directive }) {
				t.Errorf("%s (run %d): the guard reported %v, want a refusal under %s", tc.name, i, c.all(), tc.directive)
			}
		}
	}
}

// A dedicated worker loaded from a URL gets the policy on its own
// response, as the proxy stamps every response, and what it refuses
// there reaches the test like a page's refusal.
func TestARefusalInAURLWorkerIsReported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/w.js" {
			w.Header().Set("Content-Type", "text/javascript")
			fmt.Fprint(w, `try { eval("1"); postMessage("eval ran") } catch (e) { postMessage("eval refused") }`)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!doctype html><script nonce="%s">new Worker("/w.js").onmessage = e => { window.said = e.data }</script>`, r.Header.Get(csp.NonceHeader))
	}))
	defer server.Close()

	var c collector
	ctx := NewBrowser(t, withFailures(c.add))
	var said string
	if err := chromedp.Run(ctx,
		Step("load the page", chromedp.Navigate(server.URL)),
		Poll(`window.said !== undefined`),
		chromedp.Evaluate(`window.said`, &said),
	); err != nil {
		t.Fatal(err)
	}
	if said != "eval refused" {
		t.Errorf("the worker said %q, want its eval refused", said)
	}
	got := c.violations(t, 1)
	if len(got) == 0 || got[0].Directive != "script-src" || !strings.HasSuffix(got[0].Document, "/w.js") {
		t.Errorf("violations = %v, want the worker's eval", got)
	}
}

// A handler ServeAt runs is handed a request the guard cancels when the
// test ends, so a stream — an event source, a long poll — ends with it
// instead of holding the test open.
func TestServeAtEndsAStreamWithTheTest(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/events" {
			w.Header().Set("Content-Type", "text/event-stream")
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!doctype html><script nonce="%s">new EventSource("/events"); window.opened = true</script>`, r.Header.Get(csp.NonceHeader))
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t.Run("a page holding a stream", func(t *testing.T) {
			ctx := NewBrowser(t)
			ServeAt(t, ctx, "https://ss.example", handler)
			if err := chromedp.Run(ctx,
				Step("load the page", chromedp.Navigate("https://ss.example/")),
				Poll(`window.opened === true`),
			); err != nil {
				t.Fatal(err)
			}
		})
	}()
	select {
	case <-done:
	case <-time.After(2 * StepTimeout):
		t.Fatal("the test never ended: the guard is waiting on a stream ServeAt is serving")
	}
}

// ServeAt hands its handler the cookies the browser holds for the
// origin, so a session it set comes back on the next request.
func TestServeAtCarriesCookies(t *testing.T) {
	var mu sync.Mutex
	var cookies []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The pages, not the favicon the browser asks the origin for.
		if r.URL.Path == "/one" || r.URL.Path == "/two" {
			mu.Lock()
			cookies = append(cookies, r.Header.Get("Cookie"))
			mu.Unlock()
		}
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "kept", Path: "/", Secure: true, HttpOnly: true})
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><p id="p">page</p>`)
	})
	ctx := NewBrowser(t)
	ServeAt(t, ctx, "https://ss.example", handler)
	if err := chromedp.Run(ctx,
		Step("load the first page", chromedp.Navigate("https://ss.example/one")),
		Step("load the second", chromedp.Navigate("https://ss.example/two")),
	); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(cookies) != 2 || cookies[0] != "" || cookies[1] != "session=kept" {
		t.Errorf("cookies the handler saw = %q, want none and then the session it set", cookies)
	}
}

// A content origin's own refusals are the content policy working on
// untrusted bytes, as they are in production, where a content domain
// has no report endpoint: they do not fail the test.
func TestAContentOriginsRefusalsAreItsOwn(t *testing.T) {
	content := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><form id="f" method="post" action="/collect"></form><script>document.getElementById("f").submit(); parent.postMessage("tried", "*")</script>`)
	}))
	defer content.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!doctype html><script nonce="%s">addEventListener("message", e => { window.heard = e.data })</script><iframe src="%s/file"></iframe>`, r.Header.Get(csp.NonceHeader), content.URL)
	}))
	defer server.Close()

	var c collector
	t.Run("the framed content posts a form", func(t *testing.T) {
		ctx := NewBrowser(t, withFailures(c.add))
		ContentOrigin(t, ctx, content.URL)
		if err := chromedp.Run(ctx,
			Step("load the page framing the content", chromedp.Navigate(server.URL)),
			Poll(`window.heard === "tried"`),
		); err != nil {
			t.Fatal(err)
		}
		time.Sleep(500 * time.Millisecond)
	})
	if got := c.all(); len(got) != 0 {
		t.Errorf("the guard reported %v, want a content origin's refusals left to it", got)
	}
}

// A fixture rendered once, at setup, under FixtureNonce is served to
// each request with that request's nonce in its place: on every tag
// the component nonced, and on no other.
func TestNoncedServesAFixtureWithTheRequestsNonce(t *testing.T) {
	page := `<script nonce="` + FixtureNonce + `">a()</script><script>b()</script>`
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set(csp.NonceHeader, "fresh")
	if got, want := Nonced(r, page), `<script nonce="fresh">a()</script><script>b()</script>`; got != want {
		t.Errorf("Nonced = %q, want %q", got, want)
	}
}
