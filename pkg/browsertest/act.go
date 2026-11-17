package browsertest

import (
	"context"
	_ "embed"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// actJS is the page's half of every act: an expression yielding the
// library it installs. guardTab installs it on every document before
// the page's own scripts; each act evaluates it again, which returns
// the installed library or installs it on a document that missed it.
//
//go:embed act.js
var actJS string

// The acts below are how a suite touches a page. Each names its target
// by a CSS selector — the first element in document order it matches —
// and does in one in-page evaluation what chromedp's node actions split
// across several round trips: find the element, wait until a person
// could act on it, and prepare the act. No node id outlives a round
// trip, so a page that swaps its content between a test's find and its
// act cannot leave the act holding a node the page threw away, which is
// the most common way a browser suite has flaked (see
// specs/pkg-browsertest.md § Flakes).
//
// Where the act is a person's input — a press, a keystroke, text — the
// input still crosses one round trip between the evaluation that aimed
// it and the browser delivering it. A screen act.js puts on every
// document closes that gap: it judges where the input actually landed,
// against what the selector names at that moment, and stops anything
// that landed elsewhere before the page hears it. The act then aims
// again. So an act either reaches what its selector names, or it
// reaches nothing and is retried, until the step's bound.
//
// "Could act on it" is what a person needs: the element is rendered,
// visible, has a size, is not disabled (nor inert, nor aria-disabled),
// is editable where the act types, and is not inside a region the page
// marks aria-busy — a region whose replacement is under way. A pointer
// act also needs the pixel it aims at to land on the element, scrolled
// into view, rather than on whatever covers it.
//
// Every act is bounded by StepTimeout and names itself and its selector
// in its error, with the page as it gave up; a suite need not wrap one
// in Step.

// An ActOption adjusts one act.
type ActOption func(*act)

// HasText narrows an act's selector to the elements whose text holds
// text: the first match in document order that does.
func HasText(text string) ActOption {
	return func(a *act) { a.spec.Text = &text }
}

// WhileBusy lets an act reach an element inside a region the page marks
// aria-busy. A test of what the page does with input that arrives while
// it is replacing something — a keystroke during a save, a press during
// a refresh — uses it; every other test waits for the region to settle,
// which is the default.
func WhileBusy() ActOption {
	return func(a *act) { a.spec.WhileBusy = true }
}

// Modifiers holds keys down through a pointer act or a key press: a
// shift-click, a ctrl+Enter.
func Modifiers(m input.Modifier) ActOption {
	return func(a *act) { a.mods = m }
}

// Clicks makes a pointer act press n times in a row at the same point,
// as a double or triple click does.
func Clicks(n int) ActOption {
	return func(a *act) { a.clicks = n }
}

// withBetween runs f after an act is aimed and before its input is
// sent. Unexported: it exists for the tests here to land a page's swap
// exactly in that gap.
func withBetween(f func(context.Context) error) ActOption {
	return func(a *act) { a.between = f }
}

// withMidPress runs f between a pointer act's press and its release.
// Unexported: it exists for the tests here to land a page's swap in a
// person's click.
func withMidPress(f func(context.Context) error) ActOption {
	return func(a *act) { a.midPress = f }
}

// withTimeout bounds an act at d instead of StepTimeout, for the tests
// here of an act that never becomes possible.
func withTimeout(d time.Duration) ActOption {
	return func(a *act) { a.timeout = d }
}

// act is one act, and Do performs it.
type act struct {
	verb    string
	sel     string
	spec    actSpec
	mods    input.Modifier
	clicks  int
	tap     bool
	keys    string
	text    string
	read    *string
	between  func(context.Context) error
	midPress func(context.Context) error
	timeout  time.Duration
}

// actSpec is what act.js reads: which act, and how it narrows and aims.
type actSpec struct {
	Kind      string  `json:"kind"`
	FX        float64 `json:"fx"`
	FY        float64 `json:"fy"`
	Text      *string `json:"text,omitzero"`
	WhileBusy bool    `json:"whileBusy"`
	Replace   bool    `json:"replace"`
	Value     any     `json:"value"`
}

func newAct(verb, sel, kind string, opts []ActOption) *act {
	a := &act{verb: verb, sel: sel, spec: actSpec{Kind: kind, FX: 0.5, FY: 0.5}, clicks: 1, timeout: StepTimeout}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Click clicks what sel names as a person with a mouse would: the
// pointer moves to a whole pixel inside it, scrolled into view, and
// presses and releases there. See the comment above for what it waits
// for and how it keeps a click from landing on anything else.
func Click(sel string, opts ...ActOption) chromedp.Action {
	return newAct("click", sel, "pointer", opts)
}

// ClickAt clicks at a point inside what sel names, given as fractions
// of its box: (0, 0) is its top left corner, (1, 1) its bottom right.
// The point is moved onto the nearest whole pixel inside the box, a
// pixel in from its edges, since Chromium rounds a fractional
// coordinate into whichever pixel it likes — the neighbour's, at an
// edge.
func ClickAt(sel string, fx, fy float64, opts ...ActOption) chromedp.Action {
	a := newAct("click", sel, "pointer", opts)
	a.spec.FX, a.spec.FY = fx, fy
	return a
}

// Tap taps what sel names as a finger would: a touch that starts and
// ends at a whole pixel inside it, which the browser turns into pointer
// and touch events and then a click. The page must be emulating a touch
// screen (chromedp.Emulate with a phone does) for the touch to arrive.
func Tap(sel string, opts ...ActOption) chromedp.Action {
	a := newAct("tap", sel, "pointer", opts)
	a.tap = true
	return a
}

// TapAt taps at a point inside what sel names, as ClickAt clicks.
func TapAt(sel string, fx, fy float64, opts ...ActOption) chromedp.Action {
	a := newAct("tap", sel, "pointer", opts)
	a.tap = true
	a.spec.FX, a.spec.FY = fx, fy
	return a
}

// Hover moves the mouse onto what sel names, at a whole pixel inside
// it, scrolled into view.
func Hover(sel string, opts ...ActOption) chromedp.Action {
	return newAct("hover over", sel, "hover", opts)
}

// HoverAt moves the mouse to a point inside what sel names, as ClickAt
// clicks.
func HoverAt(sel string, fx, fy float64, opts ...ActOption) chromedp.Action {
	a := newAct("hover over", sel, "hover", opts)
	a.spec.FX, a.spec.FY = fx, fy
	return a
}

// Type types text into what sel names — an input, a textarea, or an
// editable element — after what is there: it takes focus, the caret
// goes to the end of a text control's value (an editable element keeps
// its own caret), and the text arrives as one input event, as a paste
// or an input method delivers it. One event rather than a keystroke a
// round trip is the point: keystrokes a round trip apart straddle any
// debounce a page keys on its input — an autosave, a search — on a
// machine slow enough, and the page sees two edits where the test made
// one. A test of what a single key does uses Press.
func Type(sel, text string, opts ...ActOption) chromedp.Action {
	a := newAct("type into", sel, "type", opts)
	a.text = text
	return a
}

// Fill replaces the whole of what sel names holds with text, as Type
// types it: the content is selected, then the text arrives as one
// input event (or, for no text, a Backspace deletes it).
func Fill(sel, text string, opts ...ActOption) chromedp.Action {
	a := newAct("fill", sel, "type", opts)
	a.spec.Replace = true
	a.text = text
	return a
}

// Press gives what sel names focus and presses keys in it, a key at a
// time: chromedp's kb names the special keys (kb.Enter, kb.Escape,
// kb.ArrowDown). A key press that arrives anywhere else is stopped, and
// the press is tried again.
func Press(sel, keys string, opts ...ActOption) chromedp.Action {
	a := newAct("press keys in", sel, "key", opts)
	a.keys = keys
	return a
}

// SetValue sets the value of the control sel names as a person's choice
// would, in one evaluation: the value (checked, for a checkbox or radio
// given a bool; the selected options, for a multiple select given a
// []string), then an input and a change event. For a select, a range,
// a date — controls a person sets rather than types into.
func SetValue(sel string, value any, opts ...ActOption) chromedp.Action {
	a := newAct("set the value of", sel, "value", opts)
	a.spec.Value = value
	return a
}

// Text reads what sel names as a person sees it — its innerText — once
// it is visible, with a size, and outside any region the page marks
// aria-busy. Hidden content is read with chromedp.Evaluate.
func Text(sel string, text *string, opts ...ActOption) chromedp.Action {
	a := newAct("read", sel, "read", opts)
	a.read = text
	return a
}

// Visible waits until sel names an element a person can see — rendered,
// visible, with a size — outside any region the page marks aria-busy:
// the wait for something to have appeared, in the acts' terms, so
// HasText narrows it ("the saved notice that says None this week")
// where chromedp's WaitVisible took an XPath.
func Visible(sel string, opts ...ActOption) chromedp.Action {
	return newAct("wait to see", sel, "see", opts)
}

// verdict is what act.js's verdict reports: an outcome, and why.
type verdict struct {
	Outcome string `json:"outcome"`
	Why     string `json:"why"`
}

// prepared is what act.js's ready reports.
type prepared struct {
	Problem string  `json:"problem"`
	Token   int     `json:"token"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Framed  bool    `json:"framed"`
	Doc     string  `json:"doc"`
	Text    string  `json:"text"`
}

// Do performs the act, aiming again whenever its input landed
// somewhere other than what it meant, until its bound.
func (a *act) Do(ctx context.Context) error {
	deadline := within(ctx, a.timeout)
	ctx, cancel := context.WithTimeout(ctx, a.timeout+10*time.Second)
	defer cancel()
	why := ""
	for {
		left := time.Until(deadline)
		if left < time.Millisecond {
			break
		}
		var p prepared
		err := evaluate(ctx, actCall("ready", a.sel, a.spec, left.Milliseconds()), &p)
		if documentReplaced(err) {
			continue
		}
		if err != nil {
			return a.failed(ctx, err.Error())
		}
		if p.Problem != "" {
			why = p.Problem
			break
		}
		switch a.spec.Kind {
		case "read":
			*a.read = p.Text
			return nil
		case "value", "see":
			return nil
		}
		if a.between != nil {
			if err := a.between(ctx); err != nil {
				return a.failed(ctx, err.Error())
			}
		}
		if err := a.send(ctx, p); err != nil {
			return a.failed(ctx, err.Error())
		}
		var v verdict
		err = evaluate(ctx, actCall("verdict", p.Token, p.Doc), &v)
		if documentReplaced(err) {
			// The act is taking the page somewhere else: a link followed,
			// a form submitted. Input stopped by the screen goes nowhere.
			return nil
		}
		if err != nil {
			return a.failed(ctx, err.Error())
		}
		switch v.Outcome {
		case "delivered", "elsewhere":
			return nil
		case "nothing":
			if p.Framed || a.spec.Kind == "hover" {
				// A frame's events are its own window's, which the screen
				// here never hears; a pointer already resting where a
				// hover aims it raises no event to judge.
				return nil
			}
		case "lost":
			// Pressed again, the page would see a second press: a
			// partial act is the test's to read, not the act's to redo.
			return a.failed(ctx, v.Why)
		}
		why = fmt.Sprintf("its input %s", v.Why)
	}
	return a.failed(ctx, why)
}

// send delivers the act's input to the point or element it was aimed
// at.
func (a *act) send(ctx context.Context, p prepared) error {
	switch a.spec.Kind {
	case "pointer":
		if a.tap {
			for range a.clicks {
				if err := input.DispatchTouchEvent(input.TouchStart, []*input.TouchPoint{{X: p.X, Y: p.Y}}).WithModifiers(a.mods).Do(ctx); err != nil {
					return err
				}
				if err := input.DispatchTouchEvent(input.TouchEnd, []*input.TouchPoint{}).WithModifiers(a.mods).Do(ctx); err != nil {
					return err
				}
			}
			return nil
		}
		if err := input.DispatchMouseEvent(input.MouseMoved, p.X, p.Y).WithModifiers(a.mods).Do(ctx); err != nil {
			return err
		}
		for n := range a.clicks {
			for _, kind := range []input.MouseType{input.MousePressed, input.MouseReleased} {
				if kind == input.MouseReleased && a.midPress != nil {
					if err := a.midPress(ctx); err != nil {
						return err
					}
				}
				if err := input.DispatchMouseEvent(kind, p.X, p.Y).WithButton(input.Left).WithClickCount(int64(n + 1)).WithModifiers(a.mods).Do(ctx); err != nil {
					return err
				}
			}
		}
		return nil
	case "hover":
		return input.DispatchMouseEvent(input.MouseMoved, p.X, p.Y).WithModifiers(a.mods).Do(ctx)
	case "type":
		if a.text == "" {
			for _, k := range keystroke('\b', 0) {
				if err := k.Do(ctx); err != nil {
					return err
				}
			}
			return nil
		}
		return input.InsertText(a.text).Do(ctx)
	case "key":
		for _, r := range a.keys {
			for _, k := range keystroke(r, a.mods) {
				if err := k.Do(ctx); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return fmt.Errorf("no input for an act of kind %q", a.spec.Kind)
}

// keystroke is the key events a keyboard sends for r with mods held:
// a keydown carrying the key's text, and a keyup. chromedp's kb sends
// the text as a char event of its own instead, which Chrome inserts
// whatever the page did with the keydown, so a page that cancels a
// key's keydown — an editor refusing Enter — still got its character.
// Carried on the keydown, the text is the keydown's default action, as
// a person's key is, and cancelling the keydown cancels it. A key with
// a modifier other than Shift held types nothing: ctrl+a is a command.
func keystroke(r rune, mods input.Modifier) []*input.DispatchKeyEventParams {
	var down, up *input.DispatchKeyEventParams
	text := ""
	for _, k := range kb.Encode(r) {
		switch k.Type {
		case input.KeyDown, input.KeyRawDown:
			down = k
		case input.KeyChar:
			text = k.Text
		case input.KeyUp:
			up = k
		}
	}
	if mods&^input.ModifierShift != 0 {
		text = ""
	}
	down.Type, down.Text, down.UnmodifiedText = input.KeyRawDown, "", ""
	if text != "" {
		down.Type, down.Text, down.UnmodifiedText = input.KeyDown, text, text
	}
	down.Modifiers, up.Modifiers = mods, mods
	return []*input.DispatchKeyEventParams{down, up}
}

// failed names the act and what stopped it, and shows the page as it
// stood.
func (a *act) failed(ctx context.Context, why string) error {
	name := a.verb + " " + a.sel
	if a.spec.Text != nil {
		name += fmt.Sprintf(" holding %q", *a.spec.Text)
	}
	return fmt.Errorf("%s: %s%s", name, why, snapshot(ctx))
}

// SetFiles hands the file input sel names the files at paths, as a
// person's choice in a file picker does: the input's files are set and
// it raises input and change. The browser sets files on a node it is
// handed rather than on a selector, so the act holds the element for
// the one round trip that takes; the screen judges the input event the
// files raise, as it judges a press, so files that reached an input the
// page had already swapped out are tried again on the live one, and
// files the live input took are done, whatever the page does next.
func SetFiles(sel string, paths []string, opts ...ActOption) chromedp.Action {
	a := newAct("set files on", sel, "files", opts)
	return chromedp.ActionFunc(func(ctx context.Context) error {
		deadline := within(ctx, a.timeout)
		ctx, cancel := context.WithTimeout(ctx, a.timeout+10*time.Second)
		defer cancel()
		why := ""
		for {
			left := time.Until(deadline)
			if left < time.Millisecond {
				break
			}
			var held *runtime.RemoteObject
			expr := `(async () => { const r = await ` + actCall("ready", a.sel, a.spec, left.Milliseconds()) + `; return r.problem ?? r.element; })()`
			err := chromedp.Evaluate(expr, &held, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
				return p.WithAwaitPromise(true)
			}).Do(ctx)
			if documentReplaced(err) {
				continue
			}
			if err != nil {
				return a.failed(ctx, err.Error())
			}
			if held.ObjectID == "" {
				var problem string
				_ = json.Unmarshal(held.Value, &problem)
				why = problem
				break
			}
			var p prepared
			err = evaluate(ctx, actCall("armedAct"), &p)
			if err == nil && a.between != nil {
				err = a.between(ctx)
			}
			if err == nil {
				err = dom.SetFileInputFiles(paths).WithObjectID(held.ObjectID).Do(ctx)
			}
			var v verdict
			if err == nil {
				err = evaluate(ctx, actCall("verdict", p.Token, p.Doc), &v)
			}
			if err == nil && v.Outcome == "nothing" {
				// No event to judge — a page with its scripting off hears
				// none — so the files are where they belong if the input
				// they reached is still the live one.
				var owned bool
				owned, err = owns(ctx, held.ObjectID, a)
				if owned {
					v.Outcome = "delivered"
				}
			}
			_ = runtime.ReleaseObject(held.ObjectID).Do(ctx)
			if documentReplaced(err) {
				if v.Outcome != "" {
					return nil
				}
				continue
			}
			if err != nil {
				return a.failed(ctx, err.Error())
			}
			if v.Outcome == "delivered" || v.Outcome == "elsewhere" {
				return nil
			}
			why = "its files " + v.Why
		}
		return a.failed(ctx, why)
	})
}

// owns asks the page whether the element object names is still what
// a's selector names.
func owns(ctx context.Context, object runtime.RemoteObjectID, a *act) (bool, error) {
	res, exc, err := runtime.CallFunctionOn(`function(sel, o) { return (` + actJS + `).owns(this, sel, o); }`).
		WithObjectID(object).
		WithArguments([]*runtime.CallArgument{{Value: jsonValue(a.sel)}, {Value: jsonValue(a.spec)}}).
		WithReturnByValue(true).Do(ctx)
	if err == nil && exc != nil {
		err = exc
	}
	if err != nil {
		return false, err
	}
	return string(res.Value) == "true", nil
}

// Settled waits until the page has settled: the document has loaded,
// and nothing in it is marked aria-busy. The fleet's pages mark a
// region aria-busy while its replacement is under way — the catch-up a
// live page makes once its event stream opens, a refetch in flight —
// so a settled page is one whose content a test can read, and act on,
// without the page replacing it a moment later. See
// specs/pkg-browsertest.md § Settled.
//
// Like Poll it outlasts a document replaced while it waits.
func Settled() chromedp.Action {
	return settled(StepTimeout)
}

func settled(timeout time.Duration) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		deadline := within(ctx, timeout)
		ctx, cancel := context.WithTimeout(ctx, timeout+10*time.Second)
		defer cancel()
		var busy []string
		for {
			left := time.Until(deadline)
			if left < time.Millisecond {
				break
			}
			err := evaluate(ctx, actCall("settle", left.Milliseconds()), &busy)
			if documentReplaced(err) {
				continue
			}
			if err != nil {
				return fmt.Errorf("wait for the page to settle: %w%s", err, snapshot(ctx))
			}
			if len(busy) == 0 {
				return nil
			}
			break
		}
		return fmt.Errorf("wait for the page to settle: still busy: %s%s", strings.Join(busy, "; "), snapshot(ctx))
	})
}

// Open opens url and waits for the page to settle: the way a test
// arrives at a page. A test that means to watch a page before it
// settles — what it shows while its first refresh is under way —
// navigates with chromedp.Navigate instead.
func Open(url string) chromedp.Action {
	return chromedp.Tasks{
		Step("open "+url, chromedp.Navigate(url)),
		Settled(),
	}
}

// within is when a wait bounded by timeout gives up: timeout from now,
// or sooner, a little before ctx's own deadline — inside a Step, or near
// the end of the session's Budget — so the wait reports why the page
// wasn't ready rather than the bare deadline cutting it off.
func within(ctx context.Context, timeout time.Duration) time.Time {
	deadline := time.Now().Add(timeout)
	if outer, ok := ctx.Deadline(); ok && outer.Add(-2*time.Second).Before(deadline) {
		deadline = outer.Add(-2 * time.Second)
	}
	return deadline
}

// actCall is the expression calling act.js's method with args, each
// written as JSON.
func actCall(method string, args ...any) string {
	parts := make([]string, len(args))
	for i, arg := range args {
		parts[i] = string(jsonValue(arg))
	}
	return "(" + strings.TrimSpace(actJS) + ")." + method + "(" + strings.Join(parts, ", ") + ")"
}

func jsonValue(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("browsertest: %T as JSON: %v", v, err))
	}
	return b
}

// evaluate runs expr in the page, awaiting a promise it returns, and
// decodes its value into out.
func evaluate(ctx context.Context, expr string, out any) error {
	var raw []byte
	if err := chromedp.Evaluate(expr, &raw, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
		return p.WithAwaitPromise(true)
	}).Do(ctx); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("reading the page's answer %s: %w", raw, err)
	}
	return nil
}

// snapshot is the page as a failure left it, introduced for an error:
// read on the caller's context, briefly, since a page that cannot answer
// is itself the finding.
func snapshot(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	var page string
	if err := chromedp.Evaluate(pageSnapshot, &page).Do(ctx); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "\npage: unread (no answer)"
		}
		return fmt.Sprintf("\npage: unread (%v)", err)
	}
	return "\npage: " + page
}
