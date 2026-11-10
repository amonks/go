package browsertest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"monks.co/pkg/csp"
)

// The guard is what puts every page a suite loads under the fleet's
// policy — csp.Policy, the directives and the nonce script-src the
// proxy states for every page it serves — and fails the test the policy
// refuses something in. Without it a suite drives its pages with
// everything allowed, and a tag rendered without the nonce, an inline
// handler, an eval, or a form posting off the origin passes its suite
// under a looser policy than the one the page is served under.
//
// It does what the proxy does, from inside the browser, through CDP's
// Fetch domain:
//
//  1. A document request to loopback carries a fresh nonce in
//     csp.NonceHeader, as the proxy forwards one, so serve.Mux
//     stamps it where the app's templates read it. It bypasses the
//     browser's cache (Cache-Control: no-cache, no conditional
//     headers): the cache sits below the interception, so a cached
//     page would arrive with the nonce of the load that cached it under
//     a policy naming this one's, and a 304 has no page to stamp.
//  2. A document or worker response — the two kinds that get a policy
//     of their own — gains the fleet's policy beside any the app stated,
//     with the nonce its request carried: csp.Policy, so the script
//     policy is left off a response whose own already governs scripts,
//     as the proxy leaves it off. The response is fulfilled from its own
//     bytes: Chrome reads a document's policy from the response the
//     network gave it, so a changed header on a continued response is
//     silently ignored.
//  3. The policy names a report-uri on a host that resolves nowhere,
//     which the guard answers itself. A report whose original-policy is
//     the guard's fails the test, naming the directive, what was
//     refused, and where. Reports, not CDP's Audits issues, because an
//     issue does not say which policy it broke, and a page stating a
//     stricter policy of its own — cc's no-JavaScript improve form —
//     would otherwise fail its suite for doing what it set out to.
//
// A loopback document the page commits without the guard having seen
// it fails the test too, because a guard that stopped seeing requests
// looks exactly like a page with nothing to refuse. The way to stop it
// seeing them is to call Fetch.enable a second time, which replaces the
// guard's patterns: a suite never enables Fetch itself. Answer is how a
// suite answers a request.
//
// A document a suite answers itself (Answer) is a page like any other:
// its answer is handed the request with its nonce, and its response
// gains the policy before the browser sees it. An origin a suite names
// with ContentOrigin stands for a content-only domain and gets that
// domain's policy instead (csp.ContentPolicy), framed by the suite's
// own pages.
//
// A refusal reaches the guard two ways, and it fails the test once. The
// browser's report is posted after the fact, so it can come too late —
// after a test that ended at once has closed the browser — or not at
// all, from a page navigated away from. So a listener the guard puts on
// every document before its own scripts sends each
// securitypolicyviolation event the moment it fires, through a CDP
// binding, and before the browser closes the guard queues a task behind
// any event still waiting to fire. The report stays for what fires no
// event on the page: a frame-ancestors refusal, and a worker loaded
// from a URL, which gets the policy on its own response, as the proxy
// stamps every response, and reports its refusals to the policy's
// report-uri. A blob: worker inherits its page's policy.
//
// A tab the page opens itself — window.open, a target=_blank link — is
// a target the guard does not hold, and runs with everything allowed.
// No suite drives one; a suite that comes to opens it with NewTab.

// reportURL is where the guard's policy sends its reports. .invalid
// never resolves (RFC 2606), and the browser resolves nothing but
// loopback anyway, so a report the guard failed to intercept fails in
// the browser rather than arriving anywhere.
const reportURL = "https://browsertest.invalid/csp-report"

// Violation is one thing the fleet's policy refused on a page.
type Violation struct {
	// Directive is the effective directive: script-src-elem for a
	// script tag, script-src-attr for an inline handler, script-src for
	// an eval, form-action for a form posting off the origin.
	Directive string
	// Blocked is what was refused: "inline", "eval", "wasm-eval", or a
	// URL.
	Blocked string
	// Document is the page it was refused on, and Source where in the
	// source, when the browser says.
	Document string
	Source   string
}

// Error makes a Violation the failure the guard reports.
func (v Violation) Error() string {
	s := fmt.Sprintf("the fleet's policy (csp.Policy) refused %s under %s on %s", v.Blocked, v.Directive, v.Document)
	if v.Source != "" {
		s += " at " + v.Source
	}
	return s
}

// Response is what Answer has the browser receive in place of the
// network's answer.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Answer has ctx's tab answer the requests matching urlPattern itself,
// with what answer returns, instead of sending them anywhere. The
// pattern takes CDP's wildcards: * for any run of characters, ? for
// one, a backslash escaping either. resourceType narrows it to one kind
// of request, or "" for every kind. A document or worker answered this
// way runs under the policy like one from the network: the request
// answer is handed carries the nonce in csp.NonceHeader, so an answer
// standing a page up at its production URL can render the scripts it
// trusts.
//
// It exists because the guard owns the tab's Fetch domain: a second
// Fetch.enable would replace the guard's patterns and leave the suite's
// pages running unguarded. answer runs on its own goroutine, once per
// request; a test counting requests through it synchronizes its own
// counter.
func Answer(t *testing.T, ctx context.Context, urlPattern string, resourceType network.ResourceType, answer func(*network.Request) Response) {
	t.Helper()
	g := guardOf(ctx)
	if g == nil {
		t.Fatal("browsertest.Answer: ctx is not a tab NewBrowser or NewTab made")
	}
	g.mu.Lock()
	g.answers = append(g.answers, answerer{pattern: urlPattern, resourceType: resourceType, answer: answer})
	g.mu.Unlock()
	if err := chromedp.Run(ctx, g.enable()); err != nil {
		t.Fatalf("browsertest.Answer %s: %v", urlPattern, err)
	}
}

// ServeAt has ctx's tab answer every request to origin (scheme and
// host, such as "https://ss.cx") from handler, in process: a suite's way
// to load its pages at their production origin, where a URL the page
// builds absolutely on its own host — a form posting to its app's
// canonical URL — is same-origin, as it is in production. The pages
// run under the policy at that origin (Answer), and the handler sees
// each request's method, path, query, headers (the nonce and the
// browser's cookies among them) and body.
//
// A response reaches the page whole, when the handler returns: a stream
// — an event source, a long poll — delivers nothing, and its request's
// context is cancelled when the test ends, so a handler waiting on it
// returns rather than holding the test open. A body the browser does
// not hand over — a file upload's parts — fails the test rather than
// arriving truncated.
func ServeAt(t *testing.T, ctx context.Context, origin string, handler http.Handler) {
	t.Helper()
	o := originOf(origin)
	if o == "" {
		t.Fatalf("browsertest.ServeAt: %q names no origin", origin)
	}
	g := guardOf(ctx)
	if g == nil {
		t.Fatal("browsertest.ServeAt: ctx is not a tab NewBrowser or NewTab made")
	}
	escaped := strings.NewReplacer(`\`, `\\`, "*", `\*`, "?", `\?`).Replace(o)
	Answer(t, ctx, escaped+"/*", "", func(r *network.Request) Response {
		var body []byte
		for _, entry := range r.PostDataEntries {
			chunk, err := base64.StdEncoding.DecodeString(entry.Bytes)
			if err != nil {
				t.Errorf("browsertest.ServeAt: a request body from %s: %v", r.URL, err)
				break
			}
			body = append(body, chunk...)
		}
		if r.HasPostData && len(body) == 0 {
			t.Errorf("browsertest.ServeAt: the browser did not hand over %s %s's body (a file upload's parts are not carried)", r.Method, r.URL)
		}
		req := httptest.NewRequestWithContext(g.stopping, r.Method, r.URL, bytes.NewReader(body))
		for name, value := range r.Headers {
			req.Header.Set(name, fmt.Sprint(value))
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return Response{Status: rec.Code, Header: rec.Header(), Body: rec.Body.Bytes()}
	})
}

// FixtureNonce is the nonce a suite renders a fixture page under when
// it renders the page once, at setup, rather than per request — in
// templ's nonce slot, templ.WithNonce(ctx, FixtureNonce), where serve.Mux
// puts a request's. Nonced swaps it for the nonce each request's policy
// names, so every script tag the component nonced carries the
// request's, as it would under serve.Mux, and a tag it forgot carries
// none and is refused.
const FixtureNonce = "browsertest-fixture-nonce"

// Nonced is page, rendered under FixtureNonce, as served to r.
func Nonced(r *http.Request, page string) string {
	return strings.ReplaceAll(page, FixtureNonce, r.Header.Get(csp.NonceHeader))
}

// ContentOrigin has ctx's tab treat origin (scheme, host and port: a
// suite's stand-in for a content-only domain, such as fs's content host)
// as the proxy treats a content domain: its documents get
// csp.ContentPolicy, framed by the origins of the tab's other pages,
// rather than csp.Policy.
func ContentOrigin(t *testing.T, ctx context.Context, origin string) {
	t.Helper()
	g := guardOf(ctx)
	if g == nil {
		t.Fatal("browsertest.ContentOrigin: ctx is not a tab NewBrowser or NewTab made")
	}
	o := originOf(origin)
	if o == "" {
		t.Fatalf("browsertest.ContentOrigin: %q names no origin", origin)
	}
	g.mu.Lock()
	g.content[o] = true
	g.mu.Unlock()
}

type answerer struct {
	pattern      string
	resourceType network.ResourceType
	answer       func(*network.Request) Response
}

type guard struct {
	t   *testing.T
	ctx context.Context
	// failed is where the guard's findings go — a Violation, or a page
	// it never saw: the test's failure, or a test of the guard itself
	// collecting them.
	failed func(error)

	mu      sync.Mutex
	nonces  map[fetch.RequestID]string
	answers []answerer
	// seen is every loopback document URL the guard has handled, which
	// the commit check reads.
	seen map[string]bool
	// content is the origins ContentOrigin named; framers the origins
	// of the other documents the guard has stamped, which a content
	// origin's policy names as the ancestors it may be framed by.
	content map[string]bool
	framers map[string]bool
	// reported is every refusal already failed, by what it refused
	// where, since one refusal arrives twice: as the page's event and
	// as the browser's report.
	reported map[string]bool
	// stopping is cancelled when the tab's guard stops, before it waits
	// on the answers in flight: the context ServeAt's requests carry.
	stopping context.Context
	stop     context.CancelFunc
	pending  sync.WaitGroup
	closed   bool
}

// guards maps each guarded tab's target to its guard, so Answer can
// find the one its ctx drives. A context value would follow a context
// to the wrong tab: chromedp.NewContext(ctx) makes a new target under
// the old one's values.
var guards sync.Map // target.ID → *guard

func guardOf(ctx context.Context) *guard {
	c := chromedp.FromContext(ctx)
	if c == nil || c.Target == nil {
		return nil
	}
	g, _ := guards.Load(c.Target.TargetID)
	gd, _ := g.(*guard)
	return gd
}

// guardTab puts ctx's tab under the policy, and returns what its
// cleanup must run: after the browser has shut down where the tab is
// NewBrowser's, before the tab closes where it is NewTab's — either way
// once no more requests can need answering. failed is nil but for the
// tests here.
func guardTab(t *testing.T, ctx context.Context, failed func(error)) (flush, stop func(), err error) {
	g := &guard{t: t, ctx: ctx, failed: failed, nonces: map[fetch.RequestID]string{}, seen: map[string]bool{}, content: map[string]bool{}, framers: map[string]bool{}, reported: map[string]bool{}}
	g.stopping, g.stop = context.WithCancel(context.Background())
	if g.failed == nil {
		g.failed = func(err error) { t.Error(err) }
	}
	target := chromedp.FromContext(ctx).Target.TargetID
	guards.Store(target, g)
	chromedp.ListenTarget(ctx, func(event any) {
		switch event := event.(type) {
		case *fetch.EventRequestPaused:
			// CDP calls block on the event loop that is calling us, so
			// each answer runs on its own goroutine.
			if g.start() {
				go func() {
					defer g.pending.Done()
					g.paused(event)
				}()
			}
		case *page.EventFrameNavigated:
			g.committed(event.Frame)
		case *runtime.EventBindingCalled:
			if event.Name == violationBinding {
				g.event(event.Payload)
			}
		}
	})
	if err := chromedp.Run(ctx,
		runtime.AddBinding(violationBinding),
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(violationListener).Do(ctx)
			return err
		}),
		g.enable(),
	); err != nil {
		g.stop()
		guards.Delete(target)
		return nil, nil, fmt.Errorf("putting the tab under the script policy: %w", err)
	}
	flush = func() {
		// A refusal's event is a task queued on the page, so a task the
		// guard queues behind it runs after it has fired, and the
		// binding's call is on the socket ahead of this answer. A page
		// gone or wedged has nothing more to say.
		wait, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = chromedp.Run(wait, chromedp.Evaluate(`new Promise(done => setTimeout(done, 0))`, nil,
			func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }))
	}
	stop = func() {
		g.mu.Lock()
		g.closed = true
		g.mu.Unlock()
		g.stop()
		g.pending.Wait()
		guards.Delete(target)
	}
	return flush, stop, nil
}

// violationBinding is the CDP binding the guard's listener reports a
// page's refusals through.
const violationBinding = "__browsertestPolicyViolation"

// violationListener is installed on every document before its own
// scripts run (CDP's scripts are not subject to the page's policy). It
// takes the binding off the page's global, so the page cannot call it,
// and sends each securitypolicyviolation event the moment it fires —
// the path a refusal takes when the browser's report would come too
// late, the test having ended.
const violationListener = `(() => {
	const send = globalThis.` + violationBinding + `;
	if (typeof send !== "function") return;
	delete globalThis.` + violationBinding + `;
	addEventListener("securitypolicyviolation", e => send(JSON.stringify({
		directive: e.effectiveDirective, blocked: e.blockedURI, document: e.documentURI,
		source: e.sourceFile, line: e.lineNumber, column: e.columnNumber, policy: e.originalPolicy,
	})), true);
})()`

// start counts one more answer in flight, unless the guard has closed:
// a WaitGroup's Add may not race its Wait.
func (g *guard) start() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return false
	}
	g.pending.Add(1)
	return true
}

// enable states every pattern the guard pauses on. Fetch.enable
// replaces the tab's patterns wholesale, which is why the guard owns
// it and Answer adds to it here.
func (g *guard) enable() chromedp.Action {
	patterns := []*fetch.RequestPattern{
		{URLPattern: "*", ResourceType: network.ResourceTypeDocument, RequestStage: fetch.RequestStageRequest},
		{URLPattern: "*", ResourceType: network.ResourceTypeDocument, RequestStage: fetch.RequestStageResponse},
		// A worker's main script is "Other" to the Fetch domain.
		{URLPattern: "*", ResourceType: network.ResourceTypeOther, RequestStage: fetch.RequestStageResponse},
		{URLPattern: reportURL + "*", RequestStage: fetch.RequestStageRequest},
	}
	g.mu.Lock()
	for _, a := range g.answers {
		patterns = append(patterns, &fetch.RequestPattern{URLPattern: a.pattern, ResourceType: a.resourceType, RequestStage: fetch.RequestStageRequest})
	}
	g.mu.Unlock()
	return fetch.Enable().WithPatterns(patterns)
}

func (g *guard) paused(event *fetch.EventRequestPaused) {
	executor := cdp.WithExecutor(g.ctx, chromedp.FromContext(g.ctx).Target)
	var err error
	switch {
	case event.ResponseErrorReason != "":
		err = fetch.ContinueRequest(event.RequestID).Do(executor)
	case event.ResponseStatusCode != 0:
		err = g.respond(executor, event)
	case strings.HasPrefix(event.Request.URL, reportURL):
		err = g.report(executor, event)
	default:
		if a, ok := g.answerFor(event); ok {
			err = g.answered(executor, event, a)
		} else if event.ResourceType == network.ResourceTypeDocument && isLoopback(event.Request.URL) {
			err = g.request(executor, event)
		} else {
			err = fetch.ContinueRequest(event.RequestID).Do(executor)
		}
	}
	// A navigation the page abandoned takes its paused request with it,
	// and the commit check is what notices a document that went
	// unguarded, so a failed answer is a note rather than a failure.
	if err != nil {
		g.mu.Lock()
		delete(g.nonces, event.RequestID)
		g.mu.Unlock()
	}
	if err != nil && g.ctx.Err() == nil {
		g.t.Logf("browsertest: answering %s %s: %v", event.Request.Method, event.Request.URL, err)
	}
}

// request sends a loopback document request on with a fresh nonce, as
// the proxy forwards one.
func (g *guard) request(executor context.Context, event *fetch.EventRequestPaused) error {
	nonce := rand.Text()
	g.mu.Lock()
	g.nonces[event.RequestID] = nonce
	g.mu.Unlock()
	headers := []*fetch.HeaderEntry{
		{Name: csp.NonceHeader, Value: nonce},
		{Name: "Cache-Control", Value: "no-cache"},
		{Name: "Pragma", Value: "no-cache"},
	}
	for name, value := range event.Request.Headers {
		switch strings.ToLower(name) {
		case strings.ToLower(csp.NonceHeader), "cache-control", "pragma", "if-none-match", "if-modified-since":
			continue
		}
		headers = append(headers, &fetch.HeaderEntry{Name: name, Value: fmt.Sprint(value)})
	}
	// interceptResponse is said explicitly because cdproto always sends
	// it, false by default, and false opts this request out of the
	// response stage the guard stamps the policy at.
	return fetch.ContinueRequest(event.RequestID).WithHeaders(headers).WithInterceptResponse(true).Do(executor)
}

// respond adds the fleet's policy to a document or worker response
// that has a body to carry it.
func (g *guard) respond(executor context.Context, event *fetch.EventRequestPaused) error {
	g.mu.Lock()
	nonce, ok := g.nonces[event.RequestID]
	delete(g.nonces, event.RequestID)
	g.mu.Unlock()
	if !ok {
		// A worker's script, whose request the guard did not pause: the
		// proxy mints a nonce for every response, and so does this.
		nonce = rand.Text()
	}
	body, err := fetch.GetResponseBody(event.RequestID).Do(executor)
	if err != nil {
		// A redirect, a 204 or 304, a HEAD: nothing to run script in, and
		// no body to fulfil the response from. Anything else would go to
		// the page unguarded, which is a failure, not a pass.
		status := event.ResponseStatusCode
		bodiless := status/100 == 3 || status == http.StatusNoContent || event.Request.Method == http.MethodHead
		if !bodiless && g.ctx.Err() == nil {
			g.failed(fmt.Errorf("browsertest: %s's %d response could not be read to put it under the fleet's policy: %v", event.Request.URL, status, err))
		}
		return fetch.ContinueRequest(event.RequestID).Do(executor)
	}
	headers := g.stamp(event.Request.URL, event.ResourceType, nonce, event.ResponseHeaders)
	params := fetch.FulfillRequest(event.RequestID, event.ResponseStatusCode).
		WithResponseHeaders(headers).
		WithBody(base64.StdEncoding.EncodeToString(body))
	if event.ResponseStatusText != "" {
		params = params.WithResponsePhrase(event.ResponseStatusText)
	}
	return params.Do(executor)
}

// cspReport is the report-uri body: CSP Level 2's names, which every
// engine still sends there.
type cspReport struct {
	Report struct {
		DocumentURI        string `json:"document-uri"`
		EffectiveDirective string `json:"effective-directive"`
		ViolatedDirective  string `json:"violated-directive"`
		OriginalPolicy     string `json:"original-policy"`
		BlockedURI         string `json:"blocked-uri"`
		SourceFile         string `json:"source-file"`
		LineNumber         int    `json:"line-number"`
		ColumnNumber       int    `json:"column-number"`
	} `json:"csp-report"`
}

func (g *guard) report(executor context.Context, event *fetch.EventRequestPaused) error {
	var body []byte
	for _, entry := range event.Request.PostDataEntries {
		chunk, err := base64.StdEncoding.DecodeString(entry.Bytes)
		if err != nil {
			g.t.Errorf("browsertest: a policy report's body: %v", err)
			break
		}
		body = append(body, chunk...)
	}
	var r cspReport
	if err := json.Unmarshal(body, &r); err != nil {
		g.t.Errorf("browsertest: a policy report the guard cannot read (%v): %q", err, body)
	} else if strings.Contains(r.Report.OriginalPolicy, reportURL) {
		v := Violation{
			Directive: r.Report.EffectiveDirective,
			Blocked:   r.Report.BlockedURI,
			Document:  r.Report.DocumentURI,
		}
		if v.Directive == "" {
			v.Directive, _, _ = strings.Cut(r.Report.ViolatedDirective, " ")
		}
		if r.Report.SourceFile != "" {
			v.Source = fmt.Sprintf("%s:%d:%d", r.Report.SourceFile, r.Report.LineNumber, r.Report.ColumnNumber)
		}
		g.violation(v)
	}
	return fetch.FulfillRequest(event.RequestID, http.StatusNoContent).Do(executor)
}

func (g *guard) see(u string) {
	g.mu.Lock()
	g.seen[u] = true
	g.mu.Unlock()
}

// event reads a refusal the page's listener sent. Only the fleet's
// policy's count: its report-uri is in the policy text, which no
// policy a page states for itself carries.
func (g *guard) event(payload string) {
	var e struct {
		Directive, Blocked, Document, Source, Policy string
		Line, Column                                 int
	}
	if err := json.Unmarshal([]byte(payload), &e, json.MatchCaseInsensitiveNames(true)); err != nil {
		g.t.Errorf("browsertest: a refusal the page's listener sent that the guard cannot read (%v): %q", err, payload)
		return
	}
	if !strings.Contains(e.Policy, reportURL) {
		return
	}
	v := Violation{Directive: e.Directive, Blocked: e.Blocked, Document: e.Document}
	if e.Source != "" {
		v.Source = fmt.Sprintf("%s:%d:%d", e.Source, e.Line, e.Column)
	}
	g.violation(v)
}

// violation fails the test for a refusal once, however many ways it
// arrived.
func (g *guard) violation(v Violation) {
	key := v.Directive + "\x00" + v.Blocked + "\x00" + v.Document + "\x00" + v.Source
	g.mu.Lock()
	seen, closed := g.reported[key], g.closed
	g.reported[key] = true
	g.mu.Unlock()
	if seen || closed {
		return
	}
	g.failed(v)
}

// committed fails the test for a loopback document the page committed
// that the guard never handled — the sign that something replaced its
// Fetch patterns.
func (g *guard) committed(frame *cdp.Frame) {
	if frame == nil || !isLoopback(frame.URL) {
		return
	}
	g.mu.Lock()
	seen, closed := g.seen[frame.URL], g.closed
	g.mu.Unlock()
	if !seen && !closed {
		g.failed(fmt.Errorf("browsertest: %s loaded without the fleet's policy; a suite that enables Fetch itself replaces the guard's patterns (use browsertest.Answer)", frame.URL))
	}
}

func (g *guard) answerFor(event *fetch.EventRequestPaused) (answerer, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, a := range g.answers {
		if (a.resourceType == "" || a.resourceType == event.ResourceType) && matchURLPattern(a.pattern, event.Request.URL) {
			return a, true
		}
	}
	return answerer{}, false
}

// answered fulfils a request with the suite's own answer. A document's
// request carries a fresh nonce into the answer, and the document or
// worker it answers with gains the policy, as one from the network
// does.
func (g *guard) answered(executor context.Context, event *fetch.EventRequestPaused, a answerer) error {
	request := event.Request
	nonce := rand.Text()
	if event.ResourceType == network.ResourceTypeDocument {
		copied := *request
		copied.Headers = network.Headers{csp.NonceHeader: nonce}
		for name, value := range request.Headers {
			if !strings.EqualFold(name, csp.NonceHeader) {
				copied.Headers[name] = value
			}
		}
		request = &copied
	}
	r := a.answer(request)
	status := r.Status
	if status == 0 {
		status = http.StatusOK
	}
	var headers []*fetch.HeaderEntry
	for name, values := range r.Header {
		for _, value := range values {
			headers = append(headers, &fetch.HeaderEntry{Name: name, Value: value})
		}
	}
	switch event.ResourceType {
	case network.ResourceTypeDocument, network.ResourceTypeOther:
		headers = g.stamp(event.Request.URL, event.ResourceType, nonce, headers)
	}
	return fetch.FulfillRequest(event.RequestID, int64(status)).
		WithResponseHeaders(headers).
		WithBody(base64.StdEncoding.EncodeToString(r.Body)).
		Do(executor)
}

// stamp returns a document or worker response's headers with the
// fleet's policy beside the response's own: the content-domain policy
// for an origin ContentOrigin named, csp.Policy for any other. A
// document it stamps is one the commit check has seen, and its origin
// one a content origin may be framed by.
func (g *guard) stamp(u string, resourceType network.ResourceType, nonce string, in []*fetch.HeaderEntry) []*fetch.HeaderEntry {
	var own []string
	for _, h := range in {
		if strings.EqualFold(h.Name, "Content-Security-Policy") {
			own = append(own, h.Value)
		}
	}
	document := resourceType == network.ResourceTypeDocument
	if document && isLoopback(u) {
		g.see(u)
	}
	origin := originOf(u)
	g.mu.Lock()
	var policy string
	if g.content[origin] {
		framers := make([]string, 0, len(g.framers))
		for f := range g.framers {
			framers = append(framers, f)
		}
		slices.Sort(framers)
		// No report-uri, as a content domain has no report endpoint: what
		// its policy refuses is the policy working on untrusted bytes.
		policy = csp.ContentPolicy(framers...)
	} else {
		if document && origin != "" {
			g.framers[origin] = true
		}
		policy = csp.Policy(nonce, strings.Join(own, ", ")) + "; report-uri " + reportURL
	}
	g.mu.Unlock()
	policies := append(own, policy)
	out := []*fetch.HeaderEntry{{Name: "Content-Security-Policy", Value: strings.Join(policies, ", ")}}
	for _, h := range in {
		switch strings.ToLower(h.Name) {
		// The body arrives decoded and whole.
		case "content-security-policy", "content-length", "content-encoding", "transfer-encoding":
			continue
		}
		out = append(out, h)
	}
	return out
}

// originOf is u's scheme, host and port, or "" when u has none.
func originOf(u string) string {
	parsed, err := url.Parse(u)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

// isLoopback says whether u is served from one of the hosts the browser
// resolves (loopbackOnly): a suite's own server.
func isLoopback(u string) bool {
	parsed, err := url.Parse(u)
	if err != nil {
		return false
	}
	switch parsed.Hostname() {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

// matchURLPattern matches CDP's request-pattern wildcards: * any run of
// characters, ? exactly one, a backslash escaping the next.
func matchURLPattern(pattern, s string) bool {
	if pattern == "" {
		return true
	}
	p, i := []rune(pattern), []rune(s)
	var match func(pi, si int) bool
	match = func(pi, si int) bool {
		for pi < len(p) {
			switch p[pi] {
			case '*':
				for k := si; k <= len(i); k++ {
					if match(pi+1, k) {
						return true
					}
				}
				return false
			case '?':
				if si >= len(i) {
					return false
				}
			case '\\':
				pi++
				if pi >= len(p) || si >= len(i) || p[pi] != i[si] {
					return false
				}
			default:
				if si >= len(i) || p[pi] != i[si] {
					return false
				}
			}
			pi++
			si++
		}
		return si == len(i)
	}
	return match(0, 0)
}
