package browsertest

import (
	"context"
	"fmt"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
)

// PressWithoutScript presses what sel names on a page that runs no
// script (Emulation.setScriptExecutionDisabled), where the acts can't:
// their screen is script the page runs, so there it hears nothing, and
// an act reads its input as never arriving — or, when the press
// navigates, as arriving only if the new document beat its look. The
// press is sent raw, aimed as an act aims: a whole pixel inside the
// target, scrolled into view and hit-tested in the one evaluation that
// finds it. What the press does — a link followed, a form sent — is the
// test's to wait for, with PollWithoutScript.
func PressWithoutScript(sel string) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		var at struct {
			X, Y    float64
			Problem string
		}
		if err := evaluate(ctx, fmt.Sprintf(`(function (sel) {
			const el = document.querySelector(sel);
			if (!el) return { Problem: sel + ' matches nothing' };
			el.scrollIntoView({ block: 'center', inline: 'center', behavior: 'instant' });
			const r = el.getBoundingClientRect();
			const x = Math.floor(r.left + r.width / 2), y = Math.floor(r.top + r.height / 2);
			const hit = document.elementFromPoint(x, y);
			if (hit !== el && !el.contains(hit)) return { Problem: sel + ' is covered at (' + x + ', ' + y + ') by ' + (hit ? hit.outerHTML.slice(0, 80) : 'nothing') };
			return { X: x, Y: y };
		})(%s)`, jsonValue(sel)), &at); err != nil {
			return fmt.Errorf("press %s without script: %w", sel, err)
		}
		if at.Problem != "" {
			return fmt.Errorf("press without script: %s", at.Problem)
		}
		for _, kind := range []input.MouseType{input.MouseMoved, input.MousePressed, input.MouseReleased} {
			ev := input.DispatchMouseEvent(kind, at.X, at.Y)
			if kind != input.MouseMoved {
				ev = ev.WithButton(input.Left).WithClickCount(1)
			}
			if err := ev.Do(ctx); err != nil {
				return fmt.Errorf("press %s without script: %w", sel, err)
			}
		}
		return nil
	})
}

// PollWithoutScript is Poll for a page that runs no script. The page's
// timers don't run there, so Poll's in-page wait, whose first look can
// come before a navigation lands or before the parser reaches what expr
// reads, never looks again; this one looks from Go every 50ms, under
// StepTimeout, in the live document as every act does. expr holds only
// once the document is complete; a throw, or an evaluation a
// navigation interrupts, counts as not yet. A stall names the
// expression, its last error, and the page as it was left.
func PollWithoutScript(expr string) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		deadline := within(ctx, StepTimeout)
		check := fmt.Sprintf(`(() => { try { return document.readyState === "complete" && !!(%s); } catch (e) { return String(e); } })()`, expr)
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		var last string
		for {
			var held any
			switch err := evaluate(ctx, check, &held); {
			case err != nil:
				last = err.Error()
			case held == true:
				return nil
			default:
				if thrown, ok := held.(string); ok {
					last = "it threw " + thrown
				} else {
					last = ""
				}
			}
			if time.Now().After(deadline) {
				err := fmt.Errorf("wait without script for %s: %w", expr, chromedp.ErrPollingTimeout)
				if last != "" {
					err = fmt.Errorf("%w (last: %s)", err, last)
				}
				return fmt.Errorf("%w%s", err, snapshot(ctx))
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("wait without script for %s: %w", expr, ctx.Err())
			case <-tick.C:
			}
		}
	})
}
