// Package browsertest drives headless Chrome for the repository's
// browser tests: locating a browser, launching it with budgets a busy
// CI builder can meet, putting the page's own console in the test log,
// and shutting it down in an order that doesn't race the temp-dir
// cleanup.
//
// A suite's whole use of it is usually two calls:
//
//	ctx := browsertest.NewBrowser(t)
//	err := chromedp.Run(ctx,
//		browsertest.Step("open the dashboard", chromedp.Navigate(server.URL)),
//		browsertest.Step("the table renders", chromedp.WaitVisible("#rows", chromedp.ByQuery)),
//	)
//
// # Bound every waiting action
//
// chromedp's node queries (Click, WaitVisible, SendKeys, Focus) and
// Navigate retry until their context dies, and nothing else bounds
// them. An unbounded one therefore runs out the whole session budget
// and reports "context deadline exceeded" without saying which wait
// stalled, turning a fast, named failure into a slow, anonymous one
// that takes every later subtest down with it. Step is the fix: it
// caps one action at StepTimeout and names it in the error.
//
// The bound goes inside the action rather than around the Run, because
// a Run on a derived context ties the browser target to that context —
// cancelling it at the end of the first step would close the page every
// later step needs.
//
// An in-page poll is already bounded, but by its own separate 30s
// default rather than by anything here. Pass PollTimeout to move both
// budgets with one knob:
//
//	chromedp.PollFunction(expr, &result, browsertest.PollTimeout)
//
// # Budgets are ceilings, not costs
//
// Budget deliberately exceeds the sum of a test's steps. The step
// timeouts exist to name which step stalled; the session budget is only
// the backstop. Sizing the session to the sum inverts that, so the
// outer context expires first and every failure reports "context
// deadline exceeded" naming no wait at all — the exact shape Step
// exists to prevent.
//
// The launch allowances are loose for a related reason: the first
// Chromium launch on a fresh CI builder cold-reads ~300MB from a slow
// image device, long enough to outlast chromedp's own defaults
// (specs/ci.md § Chromium in the builder). NewBrowser pre-reads
// Chromium's install directory into the page cache first, under no
// deadline, and the timeouts backstop what the pre-read doesn't cover.
//
// # Clicking a measured coordinate
//
// Where a click must land on a coordinate rather than a node — a chart
// bar, a brushed range — scrollIntoView first and assert
// document.elementFromPoint hits the intended element before
// dispatching. The builder draws a few pixels taller than a dev laptop,
// and a click off the bottom of the viewport lands on nothing.
package browsertest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// Budget is the wall-clock allowance for a whole chromedp session, and
// StepTimeout the allowance for one Step or one PollTimeout'd poll
// within it. Both are deliberately loose; see the package doc on why
// tightening them buys nothing and costs a flaky main.
const (
	Budget      = 5 * time.Minute
	StepTimeout = 60 * time.Second
)

// PollTimeout pins an in-page poll to the same allowance Step gives a
// node query. Without it a poll carries chromedp's own 30s default,
// which drifts from StepTimeout the moment either moves.
var PollTimeout = chromedp.WithPollingTimeout(StepTimeout)

// BootTimeout is the allowance for a poll that spans a page's whole
// boot — fetching and compiling a wasm binary, then opening its
// storage — rather than one wait within a booted page. On a fresh CI
// builder that boot runs against an image disk that cold-reads at
// ~8MB/s, contended by every other package's builds, and a hermit
// open alone has outrun 30s there (CI runs 1345 and 1346) where it
// takes well under a second warm. StepTimeout sizes the one wait a
// booted page makes; a poll for "ready" is not that, and takes this
// instead. PollBootTimeout is its poll form, as PollTimeout is
// StepTimeout's.
const BootTimeout = 3 * time.Minute

var PollBootTimeout = chromedp.WithPollingTimeout(BootTimeout)

// launchTimeout is how long a headless Chrome gets to print its
// "DevTools listening on ws://..." line, and dialTimeout how long its
// DevTools socket then gets to accept chromedp's connection. Both back
// stop the page-cache pre-read: the system libraries Chromium also
// loads, and a busier builder than the one measured.
const (
	launchTimeout = 2 * time.Minute
	dialTimeout   = time.Minute
)

// Option adjusts a browser NewBrowser is about to launch.
type Option func(*options)

type options struct {
	budget time.Duration
	// execPath overrides the browser RequireChrome would resolve. Only
	// the tests here set it, to exercise a launch that fails.
	execPath string
}

// withExecPath points the launch at a given binary instead of the one
// on PATH. Unexported: a suite that needs a particular browser has an
// environment problem, not an API one.
func withExecPath(path string) Option {
	return func(o *options) { o.execPath = path }
}

// WithBudget overrides Budget for one browser. Reach for it when a
// suite's work genuinely doesn't fit — a page that compiles wasm before
// it can be driven, a test that walks a dozen screens — not to buy
// headroom for a step that should have been bounded.
func WithBudget(d time.Duration) Option {
	return func(o *options) { o.budget = d }
}

// Step caps one action at StepTimeout and names it in any error, so a
// stalled wait reports "wait for the row: context deadline exceeded"
// instead of the same deadline with no subject. See the package doc.
func Step(what string, action chromedp.Action) chromedp.ActionFunc {
	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, StepTimeout)
		defer cancel()
		if err := action.Do(ctx); err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		return nil
	}
}

// RequireChrome resolves a Chrome/Chromium binary on PATH and returns
// its real (symlink-resolved) path. Fails the test if none is found —
// a missing browser is a broken environment, not a reason to quietly
// skip the coverage.
//
// Symlink resolution is needed on macOS: invoking the binary inside
// Google Chrome.app via a symlink (a common dev shape) makes the
// dynamic loader walk `..` from the symlink's parent and die looking
// for Frameworks. Resolving the symlink hands Chrome its real path
// and Just Works. No-op on Linux.
func RequireChrome(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		path, err := exec.LookPath(candidate)
		if err != nil {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			return resolved
		}
		return path
	}
	t.Fatal("no Chrome/Chromium binary on PATH; expected one of: google-chrome, google-chrome-stable, chromium, chromium-browser")
	return ""
}

// warmChromeOnce guards the one pre-read of Chromium's install
// directory per test binary (see launchTimeout).
var warmChromeOnce sync.Once

// NewBrowser starts a headless Chrome via chromedp's ExecAllocator and
// returns a chromedp context attached to it, bounded by Budget. The
// returned context drives a single tab; open more with NewTab, which
// share the bound.
//
// All cleanup is registered with t.Cleanup. Ordering matters, and is
// why the user data dir is allocated before any cleanup is registered:
// t.Cleanup runs LIFO, so the graceful chromedp.Cancel fires while the
// allocator (and thus Chrome) is still alive, then the allocator cancel
// kills any survivors, and only then does t.TempDir's RemoveAll touch
// the dir. Chrome is fully gone before its files are removed, which is
// what keeps "directory not empty" out of the suite.
func NewBrowser(t *testing.T, opts ...Option) context.Context {
	t.Helper()
	ctx, err := newBrowser(t, t.Cleanup, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

// newBrowser is NewBrowser with its two hazards made testable: it
// returns the launch error instead of failing the test, and registers
// its teardown through cleanup rather than t.Cleanup directly, so a
// test can run that teardown itself and hold it to a deadline.
func newBrowser(t *testing.T, cleanup func(func()), opts ...Option) (context.Context, error) {
	t.Helper()
	cfg := options{budget: Budget}
	for _, opt := range opts {
		opt(&cfg)
	}
	chrome := cfg.execPath
	if chrome == "" {
		chrome = RequireChrome(t)
	}
	warmChromeOnce.Do(func() {
		dir, ok := chromeInstallDir(chrome)
		if !ok {
			t.Logf("no resources.pak beside %s; skipping the page-cache pre-read", chrome)
			return
		}
		start := time.Now()
		read := warmPageCache(dir)
		t.Logf("pre-read %.1fMB under %s into the page cache in %s", float64(read)/(1<<20), dir, time.Since(start).Round(time.Millisecond))
	})
	userDataDir := t.TempDir()
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.ExecPath(chrome),
			chromedp.UserDataDir(userDataDir),
			chromedp.Flag("headless", "new"),
			chromedp.Flag("disable-gpu", true),
			chromedp.Flag("no-sandbox", true),
			chromedp.WSURLReadTimeout(launchTimeout),
		)...)
	cleanup(cancelAlloc)
	ctx, cancelBrowser := chromedp.NewContext(allocCtx,
		chromedp.WithBrowserOption(chromedp.WithDialTimeout(dialTimeout)))
	ctx, cancelBudget := context.WithTimeout(ctx, cfg.budget)
	CaptureConsole(t, ctx)

	// Launch here rather than on the caller's first action, so a launch
	// failure is reported by the call whose job is launching instead of
	// arriving as a mystery on some later Navigate — and, the
	// load-bearing half, so the cleanup chain always has a browser to
	// close. chromedp seeds a browser context with a one-token
	// semaphore that its allocator takes on start and closes when the
	// process exits; on a context that never ran anything, the token is
	// there for the first waiter and the channel is never closed, so
	// the second waits forever. A suite that built a browser and then
	// failed its setup wedged in cleanup until the test binary's own
	// ten-minute timeout fired, blaming whatever test was running.
	if err := chromedp.Run(ctx); err != nil {
		// Take down the allocator (registered above) but not the browser
		// context: cancelling it is the second waiter on that token, and
		// there is no browser for it to wait on.
		cancelBudget()
		return nil, fmt.Errorf("launching %s: %w", chrome, err)
	}

	cleanup(cancelBrowser)
	cleanup(cancelBudget)
	cleanup(func() {
		// Plain ctx-cancel signals Chrome to exit but doesn't wait for the
		// process to release its user-data dir. chromedp.Cancel does a
		// graceful Browser.close and blocks until the process actually
		// exits. WithoutCancel strips the budget's cancel so the wait isn't
		// bounded by an already-fired deadline, and the 10s cap bounds it.
		graceful, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = chromedp.Cancel(graceful)
	})
	return ctx, nil
}

// NewTab opens another tab on the browser ctx drives and returns a
// context for it, with its own console capture and its own cleanup.
//
// It exists because chromedp's event listeners are per-target: a
// sibling made with chromedp.NewContext alone inherits the browser and
// the budget but not the capture, and its script errors go nowhere.
func NewTab(t *testing.T, ctx context.Context) context.Context {
	t.Helper()
	tab, cancelTab := chromedp.NewContext(ctx)
	t.Cleanup(cancelTab)
	CaptureConsole(t, tab)
	if err := chromedp.Run(tab); err != nil {
		t.Fatalf("opening a tab: %v", err)
	}
	return tab
}

// CaptureConsole routes console.log/warn/error and uncaught exceptions
// on ctx's target to t.Log. NewBrowser and NewTab call it; call it
// yourself only on a context you built by hand. It is per-target, not
// per-browser — that is what NewTab is for.
//
// A flake that originates in the page — a script error, a fetch
// failure, an unhandledrejection — is otherwise invisible to the test
// driver, and shows up only as the layout assertion it broke. The
// listener's goroutine exits when ctx is cancelled, during NewBrowser's
// cleanup chain. The mutex serialises the t.Log calls, because chromedp
// dispatches events from several goroutines.
func CaptureConsole(t *testing.T, ctx context.Context) {
	t.Helper()
	var mu sync.Mutex
	chromedp.ListenTarget(ctx, func(event any) {
		switch event := event.(type) {
		case *runtime.EventConsoleAPICalled:
			mu.Lock()
			defer mu.Unlock()
			parts := make([]string, 0, len(event.Args))
			for _, argument := range event.Args {
				if len(argument.Value) > 0 {
					parts = append(parts, string(argument.Value))
				} else if argument.Description != "" {
					parts = append(parts, argument.Description)
				}
			}
			t.Logf("browser console.%s: %s", event.Type, strings.Join(parts, " "))
		case *runtime.EventExceptionThrown:
			mu.Lock()
			defer mu.Unlock()
			details := event.ExceptionDetails
			if details == nil {
				return
			}
			message := details.Text
			if details.Exception != nil && details.Exception.Description != "" {
				message = details.Exception.Description
			}
			t.Logf("browser exception: %s", message)
		}
	})
}

// chromeInstallDir returns the directory the resolved Chrome binary
// lives in, if it looks like a Chrome install directory: resources.pak
// sits beside every Linux Chrome and Chromium binary. The check keeps
// the pre-read off a shared bin directory that a wrapper script might
// resolve into (and off macOS's near-empty Contents/MacOS), where it
// would read a lot and warm nothing.
func chromeInstallDir(chrome string) (string, bool) {
	dir := filepath.Dir(chrome)
	if _, err := os.Stat(filepath.Join(dir, "resources.pak")); err != nil {
		return "", false
	}
	return dir, true
}

// warmPageCache reads every regular file under dir so the kernel's page
// cache holds it, and returns the bytes read. Symlinks are skipped —
// Alpine's install directory aliases its 282MB binary as chrome, and
// the file behind a symlink is read as itself if it lives here — as
// are unreadable entries and a missing dir: a warm-up is best-effort
// by design, never a reason to fail a test.
func warmPageCache(dir string) (read int64) {
	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer file.Close()
		n, _ := io.Copy(io.Discard, file)
		read += n
		return nil
	})
	return read
}

// StageWasmExec copies $GOROOT's wasm_exec.js into dst, for a suite
// serving a Go-wasm page to the browser. Looks under lib/wasm/ (Go
// 1.24+) and falls back to misc/wasm/ for older toolchains. Fails the
// test on any error.
func StageWasmExec(t *testing.T, dst string) {
	t.Helper()
	out, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		t.Fatalf("go env GOROOT: %v", err)
	}
	goroot := strings.TrimRight(string(out), "\r\n")
	for _, rel := range []string{"lib/wasm/wasm_exec.js", "misc/wasm/wasm_exec.js"} {
		src := filepath.Join(goroot, rel)
		source, err := os.Open(src)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			t.Fatalf("opening %s: %v", src, err)
		}
		defer source.Close()
		destination, err := os.Create(filepath.Join(dst, "wasm_exec.js"))
		if err != nil {
			t.Fatalf("creating wasm_exec.js: %v", err)
		}
		defer destination.Close()
		if _, err := io.Copy(destination, source); err != nil {
			t.Fatalf("copying wasm_exec.js: %v", err)
		}
		return
	}
	t.Fatalf("wasm_exec.js not found under $GOROOT/lib/wasm or $GOROOT/misc/wasm (GOROOT=%s)", goroot)
}
