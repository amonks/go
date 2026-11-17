package browsertest

import (
	"encoding/json/v2"
	"fmt"
)

// A form that asks before it submits says so with data-confirm, on the
// form or on its submit button, and the fleet's one gate asks:
// pkg/confirm's listener, rendered by the page's layout as the script
// with id monks-confirm-script. A layout that renders such a form
// without the gate fails silently: nothing asks, the form just posts,
// and no test of the page's own notices unless it set out to. So the
// guard fails the test of any page that holds one without the gate:
// when the document has parsed, when it has loaded, and when it submits
// one (a form a script inserted later is caught there).
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
	return fmt.Sprintf("browsertest: %s has a form that asks %q before it submits (data-confirm), but not the gate that asks it (pkg/confirm's #monks-confirm-script): its layout renders confirm.Script() first in the head", u.Document, u.Question)
}

// gateBinding is the CDP binding gateListener reports a page through.
const gateBinding = "__browsertestUngatedConfirm"

// gateListener is installed on every document before its own scripts,
// as violationListener is, and checks the page for a data-confirm form
// without the gate at each moment the package doc names.
const gateListener = `(() => {
	const send = globalThis.` + gateBinding + `;
	if (typeof send !== "function") return;
	delete globalThis.` + gateBinding + `;
	let sent = false;
	const asks = el => el instanceof HTMLFormElement ||
		((el instanceof HTMLButtonElement || el instanceof HTMLInputElement) && el.form !== null &&
			(el.type === "submit" || el.type === "image"));
	const check = within => {
		if (sent || document.getElementById("monks-confirm-script")) return;
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
