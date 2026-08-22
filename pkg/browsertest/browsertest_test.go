package browsertest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/chromedp/chromedp"
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
		Step("read the greeting", chromedp.Text("#greeting", &greeting, chromedp.ByQuery)),
	); err != nil {
		t.Fatalf("driving the page: %v", err)
	}
	if greeting != "hello" {
		t.Errorf("greeting = %q, want %q", greeting, "hello")
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
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
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
			Step("read the path", chromedp.Text("#who", &who, chromedp.ByQuery)),
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
