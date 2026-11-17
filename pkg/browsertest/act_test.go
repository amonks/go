package browsertest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/device"
	"github.com/chromedp/chromedp/kb"
)

// actPages serves each fixture page by path, its scripts nonced for the
// guard's policy. A page's script logs what reached it on window.seen,
// which the tests read back.
func actPages(t *testing.T, pages map[string]string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, Nonced(r, `<!doctype html><meta charset=utf-8><title>t</title>
<style>body { margin: 0; font: 16px sans-serif } button { width: 120px; height: 40px }</style>
<script nonce="`+FixtureNonce+`">window.seen = []; const note = (s) => window.seen.push(s);</script>
`+page))
	}))
	t.Cleanup(server.Close)
	return server
}

func seen(t *testing.T, ctx context.Context) []string {
	t.Helper()
	var got []string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.seen`, &got)); err != nil {
		t.Fatalf("reading what the page saw: %v", err)
	}
	return got
}

func eval(expr string) func(context.Context) error {
	return func(ctx context.Context) error {
		return chromedp.Evaluate(expr, nil).Do(ctx)
	}
}

func wantSeen(t *testing.T, ctx context.Context, want ...string) {
	t.Helper()
	got := seen(t, ctx)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("the page saw %q, want %q", got, want)
	}
}

const buttonPage = `<main><button id=go>Go</button></main>
<script nonce="` + FixtureNonce + `">
document.addEventListener("click", (e) => { if (e.target.closest("button")) note("click " + e.target.closest("button").dataset.n); });
document.querySelector("#go").dataset.n = "first";
</script>`

// TestClickSurvivesASwapBetweenFindAndAct is the race a chromedp node
// action loses: the page swaps the button for an equal one after the
// act found it and before the press arrives — a live page's refresh
// landing mid-click (CI runs 1716, 2603). The act holds no node across
// that gap, so the press lands on the button the page shows by then.
func TestClickSurvivesASwapBetweenFindAndAct(t *testing.T) {
	server := actPages(t, map[string]string{"/": buttonPage})
	ctx := NewBrowser(t)
	swap := eval(`(() => { const b = document.createElement("button"); b.id = "go"; b.textContent = "Go"; b.dataset.n = "second"; document.querySelector("#go").replaceWith(b); })()`)
	if err := chromedp.Run(ctx, Open(server.URL), Click("#go", withBetween(swap))); err != nil {
		t.Fatal(err)
	}
	wantSeen(t, ctx, "click second")
}

// TestClickNeverLandsOnWhatMovedIntoItsPlace is the hit-target race:
// between aiming and pressing, the page puts something else where the
// button was — a banner arriving above it, an image loading and pushing
// it down (CI run 1215). The screen stops the press that lands on the
// newcomer before the page hears it, and the act aims again.
func TestClickNeverLandsOnWhatMovedIntoItsPlace(t *testing.T) {
	server := actPages(t, map[string]string{"/": `<main><button id=go data-n=target>Go</button></main>
<script nonce="` + FixtureNonce + `">
for (const type of ["pointerdown", "mousedown", "mouseup", "click"]) document.addEventListener(type, (e) => note(type + " " + (e.target.dataset.n ?? e.target.tagName)));
</script>`})
	ctx := NewBrowser(t)
	var once atomic.Bool
	push := func(ctx context.Context) error {
		if once.Swap(true) {
			return nil
		}
		return eval(`(() => { const d = document.createElement("button"); d.dataset.n = "decoy"; d.textContent = "Decoy"; document.querySelector("main").prepend(d); })()`)(ctx)
	}
	if err := chromedp.Run(ctx, Open(server.URL), Click("#go", withBetween(push))); err != nil {
		t.Fatal(err)
	}
	wantSeen(t, ctx, "pointerdown target", "mousedown target", "mouseup target", "click target")
}

// TestClickWaitsUntilAPersonCould covers each way a page is not yet
// ready for a click a test has already decided on — the control is
// disabled until its data lands (CI run 1171), covered by an overlay,
// or inside a region the page is replacing — and the click lands once
// the page is.
func TestClickWaitsUntilAPersonCould(t *testing.T) {
	server := actPages(t, map[string]string{
		"/disabled": `<button id=go disabled>Go</button>
<script nonce="` + FixtureNonce + `">
const go = document.querySelector("#go");
go.addEventListener("click", () => note("clicked"));
setTimeout(() => { go.disabled = false; }, 300);
</script>`,
		"/aria-disabled": `<button id=go aria-disabled=true>Go</button>
<script nonce="` + FixtureNonce + `">
const go = document.querySelector("#go");
go.addEventListener("click", () => note("clicked"));
setTimeout(() => go.removeAttribute("aria-disabled"), 300);
</script>`,
		"/covered": `<button id=go>Go</button><div id=veil style="position:fixed;inset:0"></div>
<script nonce="` + FixtureNonce + `">
document.querySelector("#go").addEventListener("click", () => note("clicked"));
document.querySelector("#veil").addEventListener("click", () => note("veil"));
setTimeout(() => document.querySelector("#veil").remove(), 300);
</script>`,
		"/hidden": `<button id=go hidden>Go</button>
<script nonce="` + FixtureNonce + `">
const go = document.querySelector("#go");
go.addEventListener("click", () => note("clicked"));
setTimeout(() => { go.hidden = false; }, 300);
</script>`,
		"/below": `<div style="height:3000px"></div><button id=go>Go</button>
<script nonce="` + FixtureNonce + `">
document.querySelector("#go").addEventListener("click", () => note("clicked"));
</script>`,
		"/absent": `<main></main>
<script nonce="` + FixtureNonce + `">
setTimeout(() => { const b = document.createElement("button"); b.id = "go"; b.textContent = "Go"; b.onclick = null; b.addEventListener("click", () => note("clicked")); document.querySelector("main").append(b); }, 300);
</script>`,
	})
	ctx := NewBrowser(t)
	for _, path := range []string{"/disabled", "/aria-disabled", "/covered", "/hidden", "/below", "/absent"} {
		t.Run(strings.TrimPrefix(path, "/"), func(t *testing.T) {
			if err := chromedp.Run(ctx, chromedp.Navigate(server.URL+path), Click("#go")); err != nil {
				t.Fatal(err)
			}
			wantSeen(t, ctx, "clicked")
		})
	}
}

// TestActsWaitOutABusyRegion is the first-refresh race (CI runs 1609,
// 1716, 1733): a live page's catch-up replaces what a test found the
// moment after it loaded. The page marks the region aria-busy while the
// replacement is under way, and an act — and Open — waits it out, so
// the test reaches the content the page settles on.
func TestActsWaitOutABusyRegion(t *testing.T) {
	const refreshing = `<main aria-busy=true><button id=go data-n=stale>Go</button><p id=status>stale</p></main>
<script nonce="` + FixtureNonce + `">
document.addEventListener("click", (e) => note("click " + e.target.dataset.n));
setTimeout(() => {
	const main = document.querySelector("main");
	main.innerHTML = '<button id=go data-n=fresh>Go</button><p id=status>fresh</p>';
	main.removeAttribute("aria-busy");
}, 400);
</script>`
	server := actPages(t, map[string]string{"/": refreshing, "/held": `<main aria-busy=true><button id=go data-n=stale>Go</button></main>
<script nonce="` + FixtureNonce + `">document.addEventListener("click", (e) => note("click " + e.target.dataset.n));</script>`})
	ctx := NewBrowser(t)

	t.Run("click", func(t *testing.T) {
		if err := chromedp.Run(ctx, chromedp.Navigate(server.URL), Click("#go")); err != nil {
			t.Fatal(err)
		}
		wantSeen(t, ctx, "click fresh")
	})
	t.Run("see", func(t *testing.T) {
		var status string
		if err := chromedp.Run(ctx, chromedp.Navigate(server.URL), Visible("p", HasText("fresh")), chromedp.Evaluate(`document.querySelector("#status").textContent`, &status)); err != nil {
			t.Fatal(err)
		}
		if status != "fresh" {
			t.Errorf("saw %q, want the settled %q", status, "fresh")
		}
	})
	t.Run("read", func(t *testing.T) {
		var status string
		if err := chromedp.Run(ctx, chromedp.Navigate(server.URL), Text("#status", &status)); err != nil {
			t.Fatal(err)
		}
		if status != "fresh" {
			t.Errorf("read %q, want the settled %q", status, "fresh")
		}
	})
	t.Run("open", func(t *testing.T) {
		var status string
		if err := chromedp.Run(ctx, Open(server.URL), chromedp.Evaluate(`document.querySelector("#status").textContent`, &status)); err != nil {
			t.Fatal(err)
		}
		if status != "fresh" {
			t.Errorf("after Open the page shows %q, want the settled %q", status, "fresh")
		}
	})
	t.Run("while busy", func(t *testing.T) {
		if err := chromedp.Run(ctx, chromedp.Navigate(server.URL+"/held"), Click("#go", WhileBusy())); err != nil {
			t.Fatal(err)
		}
		wantSeen(t, ctx, "click stale")
	})
}

// TestAnActThatCannotHappenSaysWhy keeps a stalled act a named failure:
// what it meant, why a person couldn't, and what the page showed.
func TestAnActThatCannotHappenSaysWhy(t *testing.T) {
	server := actPages(t, map[string]string{
		"/covered":  `<button id=go>Go</button><div class=veil style="position:fixed;inset:0;background:#fff8">Loading</div>`,
		"/busy":     `<main id=list aria-busy=true><button id=go>Go</button></main>`,
		"/disabled": `<button id=go disabled>Go</button>`,
		"/none":     `<p>nothing here</p>`,
	})
	ctx := NewBrowser(t)
	for path, want := range map[string]string{
		"/covered":  `click #go: #go is covered at (`,
		"/busy":     `click #go: #go is inside main#list[aria-busy=true] "Go", which is aria-busy`,
		"/disabled": `click #go: #go is disabled`,
		"/none":     `click #go: #go matches nothing`,
	} {
		t.Run(strings.TrimPrefix(path, "/"), func(t *testing.T) {
			err := chromedp.Run(ctx, chromedp.Navigate(server.URL+path), Click("#go", withTimeout(300*time.Millisecond)))
			if err == nil {
				t.Fatal("an act that cannot happen returned no error")
			}
			if !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "\npage: "+server.URL+path) {
				t.Errorf("error = %q, want it to say %q and show the page", err, want)
			}
		})
	}
	err := chromedp.Run(ctx, chromedp.Navigate(server.URL+"/covered"), Click("#go", withTimeout(300*time.Millisecond)))
	if err == nil || !strings.Contains(err.Error(), `by div.veil "Loading"`) {
		t.Errorf("error = %v, want it to name what covers the button", err)
	}
	// Inside a Step, whose bound is the act's own, the act still says
	// why rather than leaving the Step's deadline to say nothing.
	err = chromedp.Run(ctx, chromedp.Navigate(server.URL+"/disabled"), chromedp.ActionFunc(func(ctx context.Context) error {
		step, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		return Click("#go").Do(step)
	}))
	if err == nil || !strings.Contains(err.Error(), "#go is disabled") {
		t.Errorf("an act under a shorter deadline: %v, want it to say why", err)
	}
}

// TestClickAtLandsOnAWholePixelInside is the fractional-coordinate
// race: two bands meet at x = 100.4, and a click at the first band's
// right edge, sent as a fraction, is rounded by Chromium into whichever
// pixel it likes — the neighbour's, sometimes (CI run 2341). The act
// lands on a whole pixel inside the box it names, a pixel in from its
// edges.
func TestClickAtLandsOnAWholePixelInside(t *testing.T) {
	server := actPages(t, map[string]string{"/": `<div id=a style="position:absolute;left:0;top:0;width:100.4px;height:50.6px"></div>
<div id=b style="position:absolute;left:100.4px;top:0;width:100.2px;height:50.6px"></div>
<div id=c style="position:absolute;left:0;top:50.6px;width:200.6px;height:20px"></div>
<script nonce="` + FixtureNonce + `">
document.addEventListener("click", (e) => note(e.target.id + " " + e.clientX + "," + e.clientY));
</script>`})
	ctx := NewBrowser(t)
	if err := chromedp.Run(ctx, Open(server.URL),
		ClickAt("#a", 1, 1),
		ClickAt("#b", 0, 0),
		ClickAt("#b", 1, 1),
	); err != nil {
		t.Fatal(err)
	}
	wantSeen(t, ctx, "a 99,49", "b 102,1", "b 199,49")
}

// TestTypeArrivesAsOneInput is the autosave race: chromedp types a
// keystroke a round trip, and on a slow machine two of them straddle
// the page's debounce, so it saves half a word and the test sees two
// edits where it made one (CI runs 1183-1186). Type delivers its text
// as one input event; Fill replaces what was there the same way.
func TestTypeArrivesAsOneInput(t *testing.T) {
	server := actPages(t, map[string]string{"/": `<input id=q value="draft: "><textarea id=notes>old</textarea><div id=rich contenteditable>x</div>
<script nonce="` + FixtureNonce + `">
for (const el of document.querySelectorAll("input, textarea, [contenteditable]")) el.addEventListener("input", () => note(el.id + " " + (el.value ?? el.textContent)));
</script>`})
	ctx := NewBrowser(t)
	var q, notes, rich string
	if err := chromedp.Run(ctx, Open(server.URL),
		Type("#q", "hello world"),
		Fill("#notes", "new notes"),
		Fill("#rich", "rich text"),
		Fill("#q", ""),
		chromedp.Evaluate(`document.querySelector("#q").value`, &q),
		chromedp.Evaluate(`document.querySelector("#notes").value`, &notes),
		chromedp.Evaluate(`document.querySelector("#rich").textContent`, &rich),
	); err != nil {
		t.Fatal(err)
	}
	wantSeen(t, ctx, "q draft: hello world", "notes new notes", "rich rich text", "q ")
	if q != "" || notes != "new notes" || rich != "rich text" {
		t.Errorf("fields hold %q, %q, %q", q, notes, rich)
	}
}

// TestTypeFollowsAFieldSwappedBeforeTheText: the act focused the field,
// and the page replaced it before the text arrived — a refresh, a
// re-render on focus. Text lands in a page's live field or nowhere, so
// it is typed again into the field the page now shows.
func TestTypeFollowsAFieldSwappedBeforeTheText(t *testing.T) {
	server := actPages(t, map[string]string{"/": `<form><input id=q data-n=first><input id=other></form>
<script nonce="` + FixtureNonce + `">
document.addEventListener("input", (e) => note((e.target.dataset.n ?? e.target.id) + " " + e.target.value));
</script>`})
	ctx := NewBrowser(t)
	var once atomic.Bool
	swap := func(ctx context.Context) error {
		if once.Swap(true) {
			return nil
		}
		return eval(`(() => { const q = document.createElement("input"); q.id = "q"; q.dataset.n = "second"; document.querySelector("#q").replaceWith(q); document.querySelector("#other").focus(); })()`)(ctx)
	}
	if err := chromedp.Run(ctx, Open(server.URL), Type("#q", "hello", withBetween(swap))); err != nil {
		t.Fatal(err)
	}
	wantSeen(t, ctx, "second hello")
}

// TestPressStaysWithItsTarget: a key meant for one field never arrives
// in another the page moved focus to in between.
func TestPressStaysWithItsTarget(t *testing.T) {
	server := actPages(t, map[string]string{"/": `<input id=q><input id=other>
<script nonce="` + FixtureNonce + `">
document.addEventListener("keydown", (e) => note(e.target.id + " " + e.key));
</script>`})
	ctx := NewBrowser(t)
	var once atomic.Bool
	steal := func(ctx context.Context) error {
		if once.Swap(true) {
			return nil
		}
		return eval(`document.querySelector("#other").focus()`)(ctx)
	}
	if err := chromedp.Run(ctx, Open(server.URL), Press("#q", kb.Enter, withBetween(steal))); err != nil {
		t.Fatal(err)
	}
	wantSeen(t, ctx, "q Enter")
}

// TestSetValueIsAPersonsChoice: one evaluation sets the control and
// raises input and change, as choosing it does.
func TestSetValueIsAPersonsChoice(t *testing.T) {
	server := actPages(t, map[string]string{"/": `<select id=s><option>a<option>b</select><input id=c type=checkbox><input id=r type=range min=0 max=10 value=1>
<select id=m multiple><option>x<option>y<option>z</select>
<script nonce="` + FixtureNonce + `">
for (const type of ["input", "change"]) document.addEventListener(type, (e) => note(type + " " + e.target.id + " " + (e.target.type === "checkbox" ? e.target.checked : e.target.multiple ? [...e.target.selectedOptions].map(o => o.value) : e.target.value)));
</script>`})
	ctx := NewBrowser(t)
	if err := chromedp.Run(ctx, Open(server.URL),
		SetValue("#s", "b"),
		SetValue("#c", true),
		SetValue("#r", "7"),
		SetValue("#m", []string{"x", "z"}),
	); err != nil {
		t.Fatal(err)
	}
	wantSeen(t, ctx, "input s b", "change s b", "input c true", "change c true", "input r 7", "change r 7", "input m x,z", "change m x,z")
	err := chromedp.Run(ctx, SetValue("#s", "c", withTimeout(300*time.Millisecond)))
	if err == nil || !strings.Contains(err.Error(), `would not take the value "c"`) {
		t.Errorf("setting an option the select lacks: %v, want it refused", err)
	}
}

// TestSetFilesReachesTheLiveInput: setting files takes the browser two
// round trips on one node, and a page that swaps the input between them
// leaves the files on a node it threw away (CI run 2033). The act
// checks it reached the live input and tries again if not. The input is
// hidden, as a file input behind its styled label usually is.
func TestSetFilesReachesTheLiveInput(t *testing.T) {
	server := actPages(t, map[string]string{"/": `<form><label>Attach <input id=f type=file data-n=first hidden></label></form>
<script nonce="` + FixtureNonce + `">
document.addEventListener("change", (e) => note(e.target.dataset.n + " " + [...e.target.files].map(f => f.name)));
</script>`})
	file := filepath.Join(t.TempDir(), "brief.md")
	if err := os.WriteFile(file, []byte("# brief\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := NewBrowser(t)
	var once atomic.Bool
	swap := func(ctx context.Context) error {
		if once.Swap(true) {
			return nil
		}
		return eval(`(() => { const f = document.createElement("input"); f.type = "file"; f.id = "f"; f.hidden = true; f.dataset.n = "second"; document.querySelector("#f").replaceWith(f); })()`)(ctx)
	}
	if err := chromedp.Run(ctx, Open(server.URL), SetFiles("#f", []string{file}, withBetween(swap))); err != nil {
		t.Fatal(err)
	}
	got := seen(t, ctx)
	if len(got) == 0 || got[len(got)-1] != "second brief.md" {
		t.Errorf("the page saw %q, want the live input to end with the file", got)
	}
}

// TestHoverAndTapAreAPersonsToo: the mouse moves onto the element, and
// on an emulated phone a finger taps, which the browser turns into a
// click.
func TestHoverAndTapAreAPersonsToo(t *testing.T) {
	server := actPages(t, map[string]string{"/": `<meta name=viewport content="width=device-width"><button id=go>Go</button>
<script nonce="` + FixtureNonce + `">
const go = document.querySelector("#go");
go.addEventListener("pointerenter", (e) => note("enter " + e.pointerType));
go.addEventListener("click", (e) => note("click"));
go.addEventListener("pointerdown", (e) => note("down " + e.pointerType));
</script>`})
	ctx := NewBrowser(t)
	if err := chromedp.Run(ctx, Open(server.URL), Hover("#go")); err != nil {
		t.Fatal(err)
	}
	wantSeen(t, ctx, "enter mouse")
	if err := chromedp.Run(ctx, chromedp.Emulate(device.IPhone13), Open(server.URL), Tap("#go")); err != nil {
		t.Fatal(err)
	}
	got := seen(t, ctx)
	if len(got) < 2 || got[len(got)-1] != "click" || !strings.Contains(strings.Join(got, "|"), "down touch") {
		t.Errorf("the page saw %q, want a touch's pointerdown and then a click", got)
	}
}

// TestClickThatNavigatesEndsWhereItWent: a click that follows a link
// replaces the document the act would read its verdict from; that is
// the act having happened, and the next wait is in the new page.
func TestClickThatNavigatesEndsWhereItWent(t *testing.T) {
	server := actPages(t, map[string]string{
		"/":     `<a id=next href="/next">Next</a>`,
		"/next": `<main aria-busy=true><p id=where>loading</p></main><script nonce="` + FixtureNonce + `">setTimeout(() => { document.querySelector("#where").textContent = "next"; document.querySelector("main").removeAttribute("aria-busy"); }, 200);</script>`,
	})
	ctx := NewBrowser(t)
	var where string
	if err := chromedp.Run(ctx, Open(server.URL), Click("#next"), Settled(), Text("#where", &where)); err != nil {
		t.Fatal(err)
	}
	if where != "next" {
		t.Errorf("after the click the page shows %q, want %q", where, "next")
	}
}

// TestSettledNamesWhatStaysBusy: a page that never settles says which
// region keeps it busy.
func TestSettledNamesWhatStaysBusy(t *testing.T) {
	server := actPages(t, map[string]string{"/": `<section id=feed aria-busy=true>Loading</section>`})
	ctx := NewBrowser(t)
	err := chromedp.Run(ctx, chromedp.Navigate(server.URL), settled(300*time.Millisecond))
	if err == nil || !strings.Contains(err.Error(), `still busy: section#feed[aria-busy=true] "Loading"`) {
		t.Errorf("error = %v, want it to name the busy region", err)
	}
}

// TestTabsEmulateAFocusedWindow: every tab's page has the focus a
// person's tab has, which a headless window otherwise lacks (CI run
// 1193).
func TestTabsEmulateAFocusedWindow(t *testing.T) {
	server := actPages(t, map[string]string{"/": `<input id=q>`})
	first := NewBrowser(t)
	second := NewTab(t, first)
	for name, ctx := range map[string]context.Context{"first": first, "second": second} {
		var focused bool
		if err := chromedp.Run(ctx, Open(server.URL), chromedp.Evaluate(`document.hasFocus()`, &focused)); err != nil {
			t.Fatal(err)
		}
		if !focused {
			t.Errorf("the %s tab's page has no focus", name)
		}
	}
}

// TestThrottleSlowsThePage: ThrottleEnv makes a fast machine the slow
// builder. The same loop takes several times longer in a throttled tab;
// the bound is loose, since what matters is that the page slowed, not
// by exactly how much.
func TestThrottleSlowsThePage(t *testing.T) {
	server := actPages(t, map[string]string{"/": `<p>work</p>`})
	const work = `(() => { const start = performance.now(); let x = 0; for (let i = 0; i < 2e7; i++) x += Math.sqrt(i); return performance.now() - start + (x < 0 ? 1 : 0); })()`
	measure := func(ctx context.Context) float64 {
		var ms float64
		if err := chromedp.Run(ctx, Open(server.URL), chromedp.Evaluate(work, nil), chromedp.Evaluate(work, &ms)); err != nil {
			t.Fatal(err)
		}
		return ms
	}
	t.Setenv(ThrottleEnv, "")
	quick := measure(NewBrowser(t))
	t.Setenv(ThrottleEnv, "8")
	slow := measure(NewBrowser(t))
	if slow < 3*quick {
		t.Errorf("the loop took %.0fms throttled and %.0fms not, want the throttled run well over 3x slower", slow, quick)
	}
	t.Setenv(ThrottleEnv, "fast")
	if _, err := throttle(); err == nil {
		t.Errorf("%s=fast was accepted", ThrottleEnv)
	}
}

// TestPressIsAKeyboardsPress: a page that cancels a key's keydown
// cancels what the key would type, as it does under a person's keyboard.
// chromedp's kb sends a key's character as an event of its own, which
// Chrome inserts whatever the keydown's handler did, so the page saw an
// Enter it had refused (pkg/texteditor's reflow suite).
func TestPressIsAKeyboardsPress(t *testing.T) {
	server := actPages(t, map[string]string{"/": `<textarea id=t></textarea><textarea id=free></textarea>
<script nonce="` + FixtureNonce + `">
const t = document.querySelector("#t");
t.addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); note("refused Enter"); } });
</script>`})
	ctx := NewBrowser(t)
	var value, free string
	if err := chromedp.Run(ctx, Open(server.URL),
		Press("#t", "ab"+kb.Enter+"c"),
		Press("#free", "ab"+kb.Enter+"c"),
		Press("#free", "A", Modifiers(input.ModifierShift)),
		chromedp.Evaluate(`document.querySelector("#t").value`, &value),
		chromedp.Evaluate(`document.querySelector("#free").value`, &free),
	); err != nil {
		t.Fatal(err)
	}
	if value != "abc" {
		t.Errorf("the textarea holds %q, want %q: the refused Enter typed anyway", value, "abc")
	}
	if free != "ab\ncA" {
		t.Errorf("the free textarea holds %q, want %q", free, "ab\ncA")
	}
	wantSeen(t, ctx, "refused Enter")
}

// TestActsReachIntoScrolledContainers: a control inside a menu that
// scrolls on its own sits in the viewport but clipped by the menu, and
// a person scrolls the menu to it (music's Columns menu). A number field
// is filled whole: it has no caret a script can put after its value.
func TestActsReachIntoScrolledContainers(t *testing.T) {
	server := actPages(t, map[string]string{"/": `<div id=menu style="height:80px;overflow:auto">
<div style="height:200px"></div><button id=deep>Deep</button><div style="height:400px"></div></div>
<input id=n type=number value=4>
<script nonce="` + FixtureNonce + `">
document.querySelector("#deep").addEventListener("click", () => note("deep"));
</script>`})
	ctx := NewBrowser(t)
	var n string
	var scrolled bool
	if err := chromedp.Run(ctx, Open(server.URL), Click("#deep"), chromedp.Evaluate(`document.querySelector("#menu").scrollTop > 0`, &scrolled), Fill("#n", "42"), chromedp.Evaluate(`document.querySelector("#n").value`, &n)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, Type("#n", "2")); err == nil || !strings.Contains(err.Error(), "Fill it instead") {
		t.Errorf("typing after a number field's value: %v, want it refused, pointing at Fill", err)
	}
	wantSeen(t, ctx, "deep")
	if !scrolled {
		t.Error("the click reached the button without the menu scrolling to it")
	}
	if n != "42" {
		t.Errorf("the number field holds %q, want %q", n, "42")
	}
}

// TestAClickWhoseTargetGoesMidPressFails: a page that replaces the
// button between the press and the release — a refresh landing in a
// person's click — leaves Chrome nothing to click, and the click is
// lost. An act that pressed and saw no click says so, rather than
// reporting a click that never happened. A control that acts on its own
// press, replacing itself as it does (tagtime's suggestion chips act on
// mousedown), was pressed as a person presses it, and its act is done.
func TestAClickWhoseTargetGoesMidPressFails(t *testing.T) {
	server := actPages(t, map[string]string{
		"/":      `<button id=go>Go</button><script nonce="` + FixtureNonce + `">document.addEventListener("click", (e) => note("click " + e.target.id));</script>`,
		"/chips": `<div id=chips><span class=chip>#car</span></div>
<script nonce="` + FixtureNonce + `">
document.querySelector("#chips").addEventListener("mousedown", (e) => {
	if (!e.target.closest(".chip")) return;
	note("chose " + e.target.textContent);
	document.querySelector("#chips").innerHTML = '<span class=chip>#bus</span>';
});
</script>`,
	})
	ctx := NewBrowser(t)
	swap := eval(`(() => { const b = document.createElement("button"); b.id = "go"; b.textContent = "Go"; document.querySelector("#go").replaceWith(b); })()`)
	err := chromedp.Run(ctx, Open(server.URL), Click("#go", withMidPress(swap), withTimeout(5*time.Second)))
	if err == nil || !strings.Contains(err.Error(), "no click arrived") {
		t.Errorf("a click the page lost: %v, want it to fail saying no click arrived", err)
	}
	if err := chromedp.Run(ctx, Open(server.URL+"/chips"), Click(".chip", HasText("#car"))); err != nil {
		t.Fatalf("a chip that acts on its press: %v", err)
	}
	wantSeen(t, ctx, "chose #car")
}

// TestInputIntoARegionTurnedBusyWaits: the page starts replacing the
// field's region after the act aimed and before the text arrived — a
// refresh landing in the round trip. Text typed into the old field would
// be thrown away with it, so the screen refuses it, and the act types
// into the field the page settles on.
func TestInputIntoARegionTurnedBusyWaits(t *testing.T) {
	server := actPages(t, map[string]string{"/": `<form id=f><input id=q value="old"></form>
<script nonce="` + FixtureNonce + `">
document.addEventListener("input", (e) => note(e.target.value));
</script>`})
	ctx := NewBrowser(t)
	var once atomic.Bool
	busy := func(ctx context.Context) error {
		if once.Swap(true) {
			return nil
		}
		return eval(`(() => {
			const f = document.querySelector("#f");
			f.setAttribute("aria-busy", "true");
			setTimeout(() => { f.innerHTML = '<input id=q value="fresh">'; f.removeAttribute("aria-busy"); }, 300);
		})()`)(ctx)
	}
	var value string
	if err := chromedp.Run(ctx, Open(server.URL), Type("#q", " typed", withBetween(busy)), chromedp.Evaluate(`document.querySelector("#q").value`, &value)); err != nil {
		t.Fatal(err)
	}
	wantSeen(t, ctx, "fresh typed")
	if value != "fresh typed" {
		t.Errorf("the field holds %q, want %q", value, "fresh typed")
	}
}

// TestSetFilesOnceDeliveredIsDone: a page that takes the files and
// replaces its input with a fresh one — an upload that resets its
// picker — got the files; the act is done, not owed another try.
func TestSetFilesOnceDeliveredIsDone(t *testing.T) {
	server := actPages(t, map[string]string{"/": `<form><input id=f type=file></form>
<script nonce="` + FixtureNonce + `">
document.addEventListener("change", (e) => {
	note("upload " + [...e.target.files].map(f => f.name));
	const fresh = document.createElement("input"); fresh.type = "file"; fresh.id = "f";
	e.target.replaceWith(fresh);
});
</script>`})
	file := filepath.Join(t.TempDir(), "brief.md")
	if err := os.WriteFile(file, []byte("# brief\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := NewBrowser(t)
	if err := chromedp.Run(ctx, Open(server.URL), SetFiles("#f", []string{file})); err != nil {
		t.Fatal(err)
	}
	wantSeen(t, ctx, "upload brief.md")
}

// TestTextReadsWhatIsShown: Text reads what a person sees, so it waits
// for a hidden element to be shown, and fails on one that never is.
func TestTextReadsWhatIsShown(t *testing.T) {
	server := actPages(t, map[string]string{
		"/later": `<p id=answer hidden>Saved</p><script nonce="` + FixtureNonce + `">setTimeout(() => { document.querySelector("#answer").hidden = false; }, 300);</script>`,
		"/never": `<p id=answer hidden>Saved</p>`,
	})
	ctx := NewBrowser(t)
	var text string
	if err := chromedp.Run(ctx, chromedp.Navigate(server.URL+"/later"), Text("#answer", &text)); err != nil {
		t.Fatal(err)
	}
	if text != "Saved" {
		t.Errorf("read %q, want %q", text, "Saved")
	}
	err := chromedp.Run(ctx, chromedp.Navigate(server.URL+"/never"), Text("#answer", &text, withTimeout(300*time.Millisecond)))
	if err == nil || !strings.Contains(err.Error(), "is not visible") {
		t.Errorf("reading a hidden element: %v, want it refused as not visible", err)
	}
}
