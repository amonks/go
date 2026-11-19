package browsertest

import (
	"encoding/json/v2"
	"fmt"
)

// A form that asks before it submits says so with data-confirm, on the
// form or on its submit button, and the fleet's one gate asks:
// pkg/confirm's listener, rendered by the page's layout as the script
// with id confirm-gate-script. A layout that renders such a form
// without the gate fails silently: nothing asks, the form just posts,
// and no test of the page's own notices unless it set out to. So the
// guard fails the test of any page that holds one without the gate:
// when the document has parsed, when it has loaded, and when it submits
// one (a form a script inserted later is caught there).
//
// The gate counts once it is live, not once it is present: the guard
// runs ahead of the page's scripts and watches the window's
// addEventListener, and the gate is the script with that id putting a
// submit listener on the window in the capture phase, as pkg/confirm's
// does. A script that only carries the id — rendered empty, refused by
// the page's policy, or doing something else — asks nothing, and the
// page fails as if it had none.
//
// What counts is what the gate reads: a form's data-confirm, and a
// submit button's that has a form to submit. A button outside any form
// is someone else's to ask about (backupd's data-api buttons ask
// through their own script).

// UngatedConfirm is a page holding a form that asks before it submits
// (data-confirm) without the gate that asks.
type UngatedConfirm struct {
	// Document is the page, and Question the first such form's.
	Document string
	Question string
}

// Error makes an UngatedConfirm the failure the guard reports.
func (u UngatedConfirm) Error() string {
	return fmt.Sprintf("browsertest: %s has a form that asks %q before it submits (data-confirm), but no live gate that asks it (pkg/confirm's #confirm-gate-script, its submit listener on the window): its layout renders confirm.Script() first in the head", u.Document, u.Question)
}

// gateBinding is the CDP binding gateListener reports a page through.
const gateBinding = "__browsertestUngatedConfirm"

// gateListener is installed on every document before its own scripts,
// as violationListener is, and checks the page for a data-confirm form
// without the live gate at each moment the package doc names. It
// watches for the gate by standing in for the window's
// addEventListener, which it hands every call on to unchanged.
const gateListener = `(() => {
	const send = globalThis.` + gateBinding + `;
	if (typeof send !== "function") return;
	delete globalThis.` + gateBinding + `;
	let sent = false, live = false;
	const add = EventTarget.prototype.addEventListener;
	const capturing = options => options === true || (typeof options === "object" && options !== null && options.capture === true);
	Object.defineProperty(window, "addEventListener", {
		configurable: true,
		writable: true,
		value: function addEventListener(type, listener, options) {
			const script = document.currentScript;
			if (this === window && type === "submit" && capturing(options) && script !== null && script.id === "confirm-gate-script") live = true;
			return add.call(this, type, listener, options);
		},
	});
	const asks = el => el instanceof HTMLFormElement ||
		((el instanceof HTMLButtonElement || el instanceof HTMLInputElement) && el.form !== null &&
			(el.type === "submit" || el.type === "image"));
	const check = within => {
		if (sent || live) return;
		const el = within.find(asks);
		if (!el) return;
		sent = true;
		send(JSON.stringify({document: document.URL, question: el.dataset.confirm}));
	};
	const all = () => [...document.querySelectorAll('[data-confirm]:not([data-confirm=""])')];
	addEventListener("DOMContentLoaded", () => check(all()));
	addEventListener("load", () => check(all()));
	addEventListener("submit", e => check([e.submitter, e.target].filter(el => el && el.dataset && el.dataset.confirm)), true);
})()`

// ungated reads a page gateListener reported, and fails the test for
// it once.
func (g *guard) ungated(payload string) {
	var u UngatedConfirm
	if err := json.Unmarshal([]byte(payload), &u, json.MatchCaseInsensitiveNames(true)); err != nil {
		g.t.Errorf("browsertest: an ungated-confirm report the guard cannot read (%v): %q", err, payload)
		return
	}
	key := "ungated\x00" + u.Document
	g.mu.Lock()
	seen, closed := g.reported[key], g.closed
	g.reported[key] = true
	g.mu.Unlock()
	if seen || closed {
		return
	}
	g.failed(u)
}
