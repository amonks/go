package browsertest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/device"

	"monks.co/pkg/csp"
)

// TestStepNamesAStalledWait is the reason Step exists: an action that
// never finishes must report which wait it was, not a bare deadline.
// Under synctest, because the only thing being measured is a timer.
func TestStepNamesAStalledWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stalls := chromedp.ActionFunc(func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		})
		err := Step("wait for the row", stalls).Do(t.Context())
		if err == nil {
			t.Fatal("a stalled step returned no error")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("error = %v, want it to wrap context.DeadlineExceeded", err)
		}
		if !strings.Contains(err.Error(), "wait for the row") {
			t.Errorf("error = %q, want it to name the wait", err)
		}
	})
}

// TestStepBoundsTheActionNotTheCaller pins the bound to StepTimeout
// rather than to whatever the session has left: a step is capped even
// when the session budget is hours away.
func TestStepBoundsTheActionNotTheCaller(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 24*time.Hour)
		defer cancel()
		var elapsed time.Duration
		start := time.Now()
		measures := chromedp.ActionFunc(func(ctx context.Context) error {
			<-ctx.Done()
			elapsed = time.Since(start)
			return ctx.Err()
		})
		_ = Step("measure", measures).Do(ctx)
		if elapsed != StepTimeout {
			t.Errorf("step ran for %s, want StepTimeout (%s)", elapsed, StepTimeout)
		}
	})
}

func TestStepPassesSuccessThrough(t *testing.T) {
	ran := false
	err := Step("do the thing", chromedp.ActionFunc(func(context.Context) error {
		ran = true
		return nil
	})).Do(t.Context())
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if !ran {
		t.Error("Step didn't run its action")
	}
}

func TestStepNamesAPlainFailure(t *testing.T) {
	sentinel := errors.New("no such node")
	err := Step("click submit", chromedp.ActionFunc(func(context.Context) error {
		return sentinel
	})).Do(t.Context())
	if !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want it to wrap the action's error", err)
	}
	if !strings.Contains(err.Error(), "click submit") {
		t.Errorf("error = %q, want it to name the step", err)
	}
}

// TestBudgetsLeaveRoomForSteps guards the sizing the package doc
// argues for: a session that expires before its steps do reports an
// anonymous deadline and Step buys nothing.
func TestBudgetsLeaveRoomForSteps(t *testing.T) {
	if Budget <= StepTimeout {
		t.Errorf("Budget (%s) must exceed StepTimeout (%s)", Budget, StepTimeout)
	}
	if launchTimeout+dialTimeout >= Budget {
		t.Errorf("launch (%s) + dial (%s) leave nothing of Budget (%s) for the test",
			launchTimeout, dialTimeout, Budget)
	}
}

func TestChromeInstallDirWantsResourcesPak(t *testing.T) {
	dir := t.TempDir()
	chrome := filepath.Join(dir, "chrome")
	if err := os.WriteFile(chrome, nil, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, ok := chromeInstallDir(chrome); ok {
		t.Error("a bare bin directory was taken for a Chrome install")
	}

	if err := os.WriteFile(filepath.Join(dir, "resources.pak"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := chromeInstallDir(chrome)
	if !ok {
		t.Fatal("an install directory with resources.pak wasn't recognised")
	}
	if got != dir {
		t.Errorf("install dir = %q, want %q", got, dir)
	}
}

func TestWarmPageCacheReadsRegularFilesOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "locales"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "locales", "b"), make([]byte, 512), 0o644); err != nil {
		t.Fatal(err)
	}
	// The alias Alpine ships: counting it would double the read for no
	// extra warmth, since the file behind it is read as itself.
	if err := os.Symlink(filepath.Join(dir, "a"), filepath.Join(dir, "chrome")); err != nil {
		t.Fatal(err)
	}

	if read := warmPageCache(dir); read != 1536 {
		t.Errorf("read %d bytes, want 1536 (the two regular files, not the symlink)", read)
	}
}

func TestWarmPageCacheToleratesAMissingDir(t *testing.T) {
	if read := warmPageCache(filepath.Join(t.TempDir(), "nope")); read != 0 {
		t.Errorf("read %d bytes from a missing directory, want 0", read)
	}
}

func TestStageWasmExec(t *testing.T) {
	dst := t.TempDir()
	StageWasmExec(t, dst)
	staged, err := os.ReadFile(filepath.Join(dst, "wasm_exec.js"))
	if err != nil {
		t.Fatalf("reading the staged wasm_exec.js: %v", err)
	}
	if len(staged) == 0 {
		t.Error("staged an empty wasm_exec.js")
	}
}

func TestRequireChromeResolvesAnExecutable(t *testing.T) {
	chrome := RequireChrome(t)
	info, err := os.Stat(chrome)
	if err != nil {
		t.Fatalf("stat %s: %v", chrome, err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("%s is not executable", chrome)
	}
	if resolved, err := filepath.EvalSymlinks(chrome); err == nil && resolved != chrome {
		t.Errorf("RequireChrome returned the symlink %s, not %s", chrome, resolved)
	}
}

// TestNewBrowserDrivesAPage is the end-to-end check: a real Chrome
// reaches a real server and the returned context can be driven. On a
// real clock, because a socket and a subprocess are what's being
// measured and neither is visible to a synctest bubble.
func TestNewBrowserDrivesAPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>t</title><p id=greeting>hello</p>`)
	}))
	defer server.Close()

	ctx := NewBrowser(t)
	var greeting string
	if err := chromedp.Run(ctx,
		Step("open the page", chromedp.Navigate(server.URL)),
		Step("read the greeting", Text("#greeting", &greeting)),
	); err != nil {
		t.Fatalf("driving the page: %v", err)
	}
	if greeting != "hello" {
		t.Errorf("greeting = %q, want %q", greeting, "hello")
	}
}

// TestNewBrowserResolvesOnlyLoopback keeps a suite off the live fleet.
// A page a test serves names production URLs — a link it follows, a
// form's action, the favicon Chrome asks of an origin it was steered to
// by interception — and a browser that resolved them sent those
// requests from the test machine to the real site. Loopback, by
// address or by name, is where every suite's server listens, and the
// one place it may reach.
func TestNewBrowserResolvesOnlyLoopback(t *testing.T) {
	page := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>t</title><p id=greeting>hello</p>`)
	})
	server := httptest.NewServer(page)
	defer server.Close()
	_, port, _ := strings.Cut(strings.TrimPrefix(server.URL, "http://"), ":")
	origins := []string{server.URL, "http://localhost:" + port}
	if l, err := net.Listen("tcp6", "[::1]:0"); err == nil {
		v6 := &httptest.Server{Listener: l, Config: &http.Server{Handler: page}}
		v6.Start()
		defer v6.Close()
		origins = append(origins, v6.URL)
	} else {
		t.Logf("no IPv6 loopback to check: %v", err)
	}

	ctx := NewBrowser(t)
	for _, origin := range origins {
		var greeting string
		if err := chromedp.Run(ctx,
			Step("open "+origin, chromedp.Navigate(origin)),
			Step("read the greeting", Text("#greeting", &greeting)),
		); err != nil {
			t.Fatalf("loopback at %s: %v", origin, err)
		}
	}
	// An address is the other way a page names a host: a tailnet peer's,
	// say. The rule maps it to nothing as it does a name.
	for _, public := range []string{"https://example.com/", "https://www.example.com/favicon.ico", "http://192.0.2.1/", "http://127.0.0.2:" + port + "/"} {
		err := chromedp.Run(ctx, Step("open "+public, chromedp.Navigate(public)))
		if err == nil || !strings.Contains(err.Error(), "ERR_NAME_NOT_RESOLVED") {
			t.Errorf("navigating to %s: %v, want net::ERR_NAME_NOT_RESOLVED", public, err)
		}
	}
}

// TestNewBrowserHoversWithAFinePointer keeps a suite's desktop the same
// on every host. Chrome reads hover and pointer from the machine's input
// devices, so on a laptop a headless page matches (hover: hover) and on a
// builder with no mouse it matches (hover: none) — and a test written
// against one reads the other's screen: a desktop's gestures asserted on
// brigid fail on the builder. Every browser is a desktop with a mouse,
// an emulated phone still turns it into a touch screen, and leaving the
// phone for a desktop viewport is a desktop again once a page loads.
// On a host with a mouse this passes without the launch setting; on the
// builder it is the regression test.
func TestNewBrowserHoversWithAFinePointer(t *testing.T) {
	const pointer = `["(hover: hover)", "(hover: none)", "(pointer: fine)", "(pointer: coarse)", "(any-hover: hover)", "(any-pointer: fine)"]
		.filter(q => matchMedia(q).matches)`
	ctx := NewBrowser(t)
	var desktop, phone, again []string
	if err := chromedp.Run(ctx,
		Step("open a page", chromedp.Navigate("about:blank")),
		chromedp.Evaluate(pointer, &desktop),
		chromedp.Emulate(device.IPhone13),
		Step("open it on a phone", chromedp.Navigate("about:blank")),
		chromedp.Evaluate(pointer, &phone),
		chromedp.EmulateViewport(1200, 900),
		Step("open it on a desktop again", chromedp.Navigate("about:blank")),
		chromedp.Evaluate(pointer, &again),
	); err != nil {
		t.Fatal(err)
	}
	mouse := []string{"(hover: hover)", "(pointer: fine)", "(any-hover: hover)", "(any-pointer: fine)"}
	if !slices.Equal(desktop, mouse) {
		t.Errorf("the desktop matches %q, want %q", desktop, mouse)
	}
	if want := []string{"(hover: none)", "(pointer: coarse)"}; !slices.Equal(phone, want) {
		t.Errorf("the emulated phone matches %q, want %q", phone, want)
	}
	if !slices.Equal(again, mouse) {
		t.Errorf("the desktop after the phone matches %q, want %q", again, mouse)
	}
}

// TestNewBrowserLaunchesEagerly guards the cleanup chain. chromedp's
// context cancel waits on a channel the allocator closes when the
// browser stops, and on a context that never ran anything nobody ever
// closes it — so a browser built and then abandoned wedges cleanup
// until the test binary's own timeout fires, ten minutes later, blaming
// an unrelated test. Asserting on the allocated browser rather than on
// the absence of a hang keeps that a named failure.
func TestNewBrowserLaunchesEagerly(t *testing.T) {
	ctx := NewBrowser(t)
	if c := chromedp.FromContext(ctx); c == nil || c.Browser == nil {
		t.Fatal("NewBrowser returned before a browser was allocated")
	}
}

// TestNewBrowserRespectsWithBudget checks the override reaches the
// returned context, so a suite that needs longer gets it and one that
// doesn't can't silently inherit it.
func TestNewBrowserRespectsWithBudget(t *testing.T) {
	ctx := NewBrowser(t, WithBudget(90*time.Second))
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("the browser context carries no deadline")
	}
	if remaining := time.Until(deadline); remaining > 90*time.Second {
		t.Errorf("deadline is %s out, want no more than the 90s budget", remaining.Round(time.Second))
	}
}

// TestFailedLaunchCleanupFinishes is the failure-path half of
// TestNewBrowserLaunchesEagerly, and the reason newBrowser registers
// the browser context's teardown only after the launch succeeds.
// chromedp's Cancel takes the allocator's one token and the context's
// own cancel then waits for a second that never comes, so registering
// both against a browser that never started deadlocks cleanup — on
// exactly the path that reports a broken environment.
func TestFailedLaunchCleanupFinishes(t *testing.T) {
	var cleanups []func()
	_, err := newBrowser(t, func(f func()) { cleanups = append(cleanups, f) },
		withExecPath(filepath.Join(t.TempDir(), "not-a-browser")))
	if err == nil {
		t.Fatal("launching a browser that isn't there reported no error")
	}

	// Run the teardown the way testing would, newest first, and hold it
	// to a deadline so a regression is a named failure here instead of
	// the test binary's own timeout somewhere else. Real clock: what's
	// being bounded is a subprocess that never started.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, cleanup := range slices.Backward(cleanups) {
			cleanup()
		}
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("cleanup after a failed launch didn't finish: something is waiting on a browser that never started")
	}
}

// TestNewTabDrivesASecondPage covers the tab helper, which exists
// because chromedp's listeners are per-target: a bare
// chromedp.NewContext sibling would drive fine and report nothing.
func TestNewTabDrivesASecondPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<!doctype html><title>t</title><p id=who>%s</p>`, r.URL.Path)
	}))
	defer server.Close()

	first := NewBrowser(t)
	second := NewTab(t, first)

	for _, tab := range []struct {
		name string
		ctx  context.Context
		path string
	}{
		{"first", first, "/one"},
		{"second", second, "/two"},
	} {
		var who string
		if err := chromedp.Run(tab.ctx,
			Step("open the page", chromedp.Navigate(server.URL+tab.path)),
			Step("read the path", Text("#who", &who)),
		); err != nil {
			t.Fatalf("%s tab: %v", tab.name, err)
		}
		if who != tab.path {
			t.Errorf("%s tab showed %q, want %q", tab.name, who, tab.path)
		}
	}
}

// TestPollTimeoutDrivesAPoll checks only that PollTimeout composes as a
// poll option and the poll reports its result. It does not prove the
// timeout itself: a predicate that settles immediately would pass under
// any allowance, and one that never settles would cost StepTimeout to
// watch.
func TestPollTimeoutDrivesAPoll(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>t</title>`)
	}))
	defer server.Close()

	ctx := NewBrowser(t)
	var settled bool
	if err := chromedp.Run(ctx,
		Step("open the page", chromedp.Navigate(server.URL)),
		Step("poll until true", chromedp.PollFunction(`() => true`, &settled, PollTimeout)),
	); err != nil {
		t.Fatalf("polling: %v", err)
	}
	if !settled {
		t.Error("the poll didn't report its result")
	}
}

// TestPollShowsThePageAStallLeft is the reason Poll exists beside
// chromedp's: a wait that never holds must say which expression it
// was and what the page showed — the state a builder-only flake
// stalled in, which no one can reproduce at the desk. A real Chrome
// on a real clock, the bound shortened so the stall is quick; a wait
// that holds passes through first, on the same browser.
func TestPollShowsThePageAStallLeft(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>t</title><h1>Queue</h1><p>1 album · 1 track</p><div>

</div><p>Dark Noise</p>`)
	}))
	defer server.Close()

	ctx := NewBrowser(t)
	if err := chromedp.Run(ctx,
		Step("open the page", chromedp.Navigate(server.URL)),
		Poll(`document.querySelector("h1").textContent === "Queue"`),
	); err != nil {
		t.Fatalf("a wait that holds: %v", err)
	}
	const never = `document.querySelectorAll(".menu .menu-item").length > 2`
	err := chromedp.Run(ctx, poll(never, 200*time.Millisecond))
	if err == nil {
		t.Fatal("a wait that never holds returned no error")
	}
	if !errors.Is(err, chromedp.ErrPollingTimeout) {
		t.Errorf("error = %v, want it to wrap chromedp.ErrPollingTimeout", err)
	}
	for _, want := range []string{never, server.URL, "Queue\n1 album · 1 track\nDark Noise"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to carry %q", err, want)
		}
	}
}

// A suite that asks for SwiftShader gets WebGL 2 though the launch
// disables the GPU.
func TestWithSoftwareWebGL(t *testing.T) {
	probe := `(() => { const c = document.createElement("canvas"); return !!c.getContext("webgl2"); })()`
	ctx := NewBrowser(t, WithSoftwareWebGL())
	var got bool
	if err := chromedp.Run(ctx,
		Step("open a blank page", chromedp.Navigate("about:blank")),
		Step("probe WebGL 2", chromedp.Evaluate(probe, &got)),
	); err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("no WebGL 2")
	}
}

// disabledFeatures replaces chromedp's own --disable-features list
// rather than adding to it, so it carries a copy of that list, taken
// from DefaultExecAllocatorOptions at the chromedp this pins. A chromedp
// upgrade fails here until the copy is checked against the new list.
func TestDisabledFeaturesFollowChromedp(t *testing.T) {
	const copiedFrom = "v0.16.0"
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Fatal("no build info to read chromedp's version from")
	}
	i := slices.IndexFunc(info.Deps, func(m *debug.Module) bool { return m.Path == "github.com/chromedp/chromedp" })
	if i < 0 {
		t.Fatal("chromedp is not among the test binary's modules")
	}
	if v := info.Deps[i].Version; v != copiedFrom {
		t.Errorf("chromedp is %s: re-copy its DefaultExecAllocatorOptions' disable-features into disabledFeatures, then set copiedFrom", v)
	}
	if !strings.HasPrefix(disabledFeatures, "site-per-process,Translate,BlinkGenPropertyTrees,") {
		t.Errorf("disabledFeatures = %q, want chromedp's list first", disabledFeatures)
	}
}

// A page can be replaced while a wait is running on it — a redirect
// landing late, a page that moves itself on — and the wait is for the
// page the test is going to, not the one it was on: it keeps waiting,
// in the new document, rather than failing because the old one went.
func TestPollOutlastsAPageReplacedWhileItWaits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/second" {
			fmt.Fprint(w, `<!doctype html><title>second</title>`)
			return
		}
		fmt.Fprintf(w, `<!doctype html><title>first</title><script nonce="%s">setTimeout(() => location.replace("/second"), 200)</script>`, r.Header.Get(csp.NonceHeader))
	}))
	defer server.Close()

	ctx := NewBrowser(t)
	if err := chromedp.Run(ctx,
		Step("open the first page", chromedp.Navigate(server.URL)),
		Poll(`document.title === "second"`),
	); err != nil {
		t.Fatal(err)
	}
}

// chromedp sends a poll's timeout in whole milliseconds and reads zero
// as none at all, so a wait with less than a millisecond of its bound
// left — a retry late in it, a bound of nothing — gives up rather than
// polling unbounded until Step's backstop.
func TestPollWithNoTimeLeftGivesUp(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><p>never</p>`)
	}))
	defer server.Close()
	ctx := NewBrowser(t)
	if err := chromedp.Run(ctx, Step("open the page", chromedp.Navigate(server.URL))); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err := chromedp.Run(ctx, poll(`false`, 0))
	if err == nil {
		t.Fatal("a wait for something false succeeded")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("a wait with no time left took %s: it polled unbounded", took.Round(time.Second))
	}
}

// TestPollOutlastsABackForwardCacheRestore: a poll that starts as the
// test goes back to a page the browser kept in its back/forward cache
// holds in the restored page. chromedp's own poll, which browsertest's
// rode on, could wait there forever: a restore delivers
// DOM.documentUpdated before Page.frameNavigated, which leaves chromedp's
// frame without the root its poll waits for (CI run 2658). Whether a
// restore's events arrive in that order is Chrome's to decide, so the
// test goes back several times; against the old poll it went red in 4
// runs of 10 under BROWSERTEST_CPU_THROTTLE=6.
func TestPollOutlastsABackForwardCacheRestore(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!doctype html><title>t</title><p id=where>%s</p><a id=on href="/b">on</a>`, r.URL.Path)
	}))
	defer server.Close()

	ctx := NewBrowser(t)
	if err := chromedp.Run(ctx, Open(server.URL+"/a"), chromedp.Evaluate(`window.wasHere = true`, nil)); err != nil {
		t.Fatal(err)
	}
	for cycle := range 5 {
		var cached bool
		if err := chromedp.Run(ctx,
			Click("#on"),
			Poll(`location.pathname === "/b" && document.readyState === "complete"`),
			chromedp.Evaluate(`setTimeout(() => history.back(), 100); true`, nil),
			poll(`location.pathname === "/a"`, 20*time.Second),
			chromedp.Evaluate(`window.wasHere === true`, &cached),
		); err != nil {
			t.Fatalf("cycle %d: %v", cycle, err)
		}
		if !cached {
			t.Skip("back reloaded /a instead of restoring it from the back/forward cache; nothing was restored")
		}
	}
}

// TestPollWaitsThroughAPageBetweenStates: a page that replaces what a
// poll reads in more than one task passes through moments the expression
// can't be evaluated in — the element it reads gone for a while — and a
// throw then is the page not being there yet, not the wait's end. A wait
// that runs out says what its final check threw.
func TestPollWaitsThroughAPageBetweenStates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, Nonced(r, `<!doctype html><title>t</title><main><p class=prompt>old</p></main>
<script nonce="`+FixtureNonce+`">
// Gone for 200ms, then back with the text the test waits for.
setTimeout(() => document.querySelector("main").replaceChildren(), 100);
setTimeout(() => { const p = document.createElement("p"); p.className = "prompt"; p.textContent = "new"; document.querySelector("main").append(p); }, 300);
</script>`))
	}))
	defer server.Close()

	ctx := NewBrowser(t)
	if err := chromedp.Run(ctx,
		Open(server.URL),
		Poll(`document.querySelector(".prompt").textContent === "new"`),
	); err != nil {
		t.Fatalf("a poll through a moment its element was gone: %v", err)
	}
	err := chromedp.Run(ctx, poll(`document.querySelector(".absent").textContent === "x"`, 300*time.Millisecond))
	if err == nil || !errors.Is(err, chromedp.ErrPollingTimeout) || !strings.Contains(err.Error(), "Cannot read properties of null") {
		t.Errorf("a poll whose expression always throws: %v, want a timeout saying what it threw", err)
	}
}

// TestPollFollowsANavigationIntoADocumentStillParsing: a wait the page
// navigates out from under starts again in the new document at once —
// while its parser may still be held, here behind a stylesheet ahead of
// an inline script, before it has reached <body>. What the expression
// reads isn't there yet, and its throw is a check that didn't hold (CI
// run 2660: agents' fork test polled .agent-prompt across the edit's
// navigation into an opsui page, whose stylesheet heads its scripts).
func TestPollFollowsANavigationIntoADocumentStillParsing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/slow.css":
			// Long enough for the restarted wait's first checks to land
			// in a document with no <body>.
			time.Sleep(400 * time.Millisecond)
			w.Header().Set("Content-Type", "text/css")
			fmt.Fprint(w, "h1 { color: black }")
		case "/b":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, Nonced(r, `<!doctype html><title>b</title><link rel=stylesheet href="/slow.css">
<script nonce="`+FixtureNonce+`">window.parsed = true;</script>
<body><h1 class=prompt>new</h1></body>`))
		default:
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, Nonced(r, `<!doctype html><title>a</title><h1 class=prompt>old</h1>
<script nonce="`+FixtureNonce+`">setTimeout(() => { location = "/b"; }, 100);</script>`))
		}
	}))
	defer server.Close()

	ctx := NewBrowser(t)
	if err := chromedp.Run(ctx,
		Open(server.URL+"/a"),
		Poll(`document.querySelector(".prompt").textContent === "new"`),
	); err != nil {
		t.Fatalf("a poll across a navigation into a document still parsing: %v", err)
	}
}
