// Package csp is the fleet's content security policy, as the proxy
// states it and the apps meet it: the directives every page carries,
// the header the proxy forwards a request's nonce in, the script-src
// naming that nonce, and what counts as a response already governing
// its own scripts. The proxy's response headers (the proxy spec's
// "Policy violation reports" section) are its one producer, pkg/serve
// reads the nonce for the apps' templates, and pkg/browsertest runs
// every page a browser suite loads under it.
//
// A leaf, and published, so that any module — a published one's
// browser tests included — can state the policy without importing the
// proxy or the fleet's HTTP stack.
package csp

import "strings"

// Directives are the four the fleet states for every page beside its
// script policy, each closing a real hole and none of them able to
// break a page in this fleet — checked rather than assumed, which is
// the only reason they could be turned on at once.
//
// Checked over two corpora, because the tree is not all of what the
// fleet renders. The source answers for the apps; the other corpus is
// the blog, the fleet's one page that renders HTML it did not author,
// from a file on fs, unsandboxed (apps/homepage/render.go's
// html.WithUnsafe). Everything else served off fs leaves through
// pkg/fsview under its file policy: file-viewer HTML allows scripts in an opaque
// origin; other types remain inert. Scanning the posts
// turned up exactly one casualty: a 2013 project post carries a form
// posting to http://lyrics.gy, which form-action 'self' refuses. It
// was already dead three ways over — the service is gone, the name is
// an alias_of monks.co now, and a browser blocks a mixed-content form
// submission from an https page regardless — so the directive stays
// and the post keeps its dead button.
//
// The directives:
//
//   - base-uri 'self' earns its place first, and its real argument is
//     ScriptPolicy rather than the markup. Every chrome
//     emits a <base href> (pkg/opsui, pkg/templib, pkg/gameui, and the
//     apps with layouts of their own) and nearly every URL the fleet
//     writes is relative to it. A nonce-based script-src is trivially
//     bypassed without this directive: inject a <base> naming your own
//     host, and every relative <script src> on the page resolves there
//     instead — carrying the nonce along, which authorizes your copy.
//     base-uri is a precondition for a nonce policy, not a small win
//     beside it. Every base in the tree is a same-origin path, so
//     'self' admits all of them.
//
//   - frame-ancestors 'self' is the clickjacking gate, which the fleet
//     has never had in any form — nothing sets X-Frame-Options either.
//     ss.cx has things worth tricking someone into clicking: cc's
//     start-a-session buttons, the directory's delegation form, the
//     proxy's own election controls. 'self' rather than 'none' because
//     a same-origin framer can already read the page it would frame,
//     so 'none' buys nothing over 'self' and costs whatever
//     same-origin embed somebody adds later.
//
//   - form-action 'self' keeps an injected form from posting what it
//     collected somewhere else. No form in the tree names a
//     cross-origin action. The improvement widget submits to cc on
//     its current origin, on domains where cc permits that operation.
//
//   - object-src 'none' retires the plugin vectors. There is no
//     <object> or <embed> anywhere in the fleet.
//
// Absent: style-src, and it is worth saying that it is absent on
// purpose rather than pending. A nonce covers a <style> block but
// never a style="…" attribute, of which the fleet has a couple of
// hundred, and the vector it closes is much weaker than the one
// script-src closes. It is where policies like this one usually die.
const Directives = "base-uri 'self'; frame-ancestors 'self'; form-action 'self'; object-src 'none'"

// Policy is the policy the fleet states for a page beside the
// response's own, own (empty when the response states none; several
// comma-joined): Directives, and ScriptPolicy with the request's nonce
// unless own already governs scripts. A browser enforces both policies,
// and two policies intersect, so this one can only ever subtract from
// what the response allowed itself. No report-uri: where reports go is
// the stater's to say.
func Policy(nonce, own string) string {
	if GovernsScripts(own) {
		return Directives
	}
	return Directives + "; " + ScriptPolicy(nonce)
}

// ContentPolicy is the policy a content-only domain states for every
// response instead of Policy: frames may have the content origin itself
// or one of ancestors — the account origins that embed its previews —
// as an ancestor, and forms post nowhere. It carries no script policy,
// because every byte a content domain serves carries its own sandbox,
// which keeps the file out of any application's origin even where an
// application may frame it.
func ContentPolicy(ancestors ...string) string {
	var frame strings.Builder
	frame.WriteString("'self'")
	for _, a := range ancestors {
		frame.WriteString(" " + a)
	}
	return "base-uri 'self'; frame-ancestors " + frame.String() + "; form-action 'none'; object-src 'none'"
}

// NonceHeader carries the per-request nonce the proxy named in the
// response's ScriptPolicy, so the app can put it on every script tag it
// renders. The proxy mints one per request; a Monks- name, which both
// of its listeners strip off every caller, so a backend reading it is
// reading the proxy's assertion. A request that did not come through
// the proxy carries none.
const NonceHeader = "Monks-Csp-Nonce"

// ScriptPolicy is the script-src the proxy states for every page it
// serves, naming the request's nonce.
//
// 'strict-dynamic' and no 'self', because 'self' would be a hole in
// this fleet specifically: media serves stored bytes from ss.cx typed
// by file extension, so a script-src naming 'self' would let an
// injected <script src> point at an uploaded .js file and run it as the
// origin. With 'strict-dynamic' every script tag in the markup needs
// the nonce, and an upload has no way to get one; what a nonced script
// loads in turn — a script element it creates, a worker, a blob:
// worker — is trusted through it.
//
// 'wasm-unsafe-eval' permits compiling WebAssembly and nothing else: no
// eval, no new Function, no string timer. Compiling takes script that
// is already running, which is what the nonce decides, so it adds no
// way for an injection to start. piano-alone's player is wasm, and
// Google Maps compiles a module in Chrome (map); both lose a working
// page without it.
func ScriptPolicy(nonce string) string {
	return "script-src 'nonce-" + nonce + "' 'strict-dynamic' 'wasm-unsafe-eval'"
}

// GovernsScripts reports whether a policy already says what may run as
// script, all of it — a sandbox, a script-src, or a default-src — in
// which case the proxy states no ScriptPolicy beside it. A
// script-src-elem or script-src-attr alone governs one kind of script
// and leaves the rest to ScriptPolicy, which then intersects with it,
// and a misspelled directive governs nothing, as the browser reads it. Every stored byte the fleet serves answers under pkg/fsview's
// sandbox, and fsview's PDF viewer states its own script-src for the
// immutable page, same-origin modules and worker the nonce cannot
// cover.
//
// The test is the directives and not the presence of a policy. A
// response stating a policy about something else entirely —
// frame-ancestors on one route, say — has said nothing about scripts,
// and treating any policy as an answer would hand it a silent opt-out
// from the fleet's script policy in exchange for an unrelated header.
// Several policies, as a comma-joined header carries them, answer if
// any one does.
func GovernsScripts(policy string) bool {
	for one := range strings.SplitSeq(policy, ",") {
		for directive := range strings.SplitSeq(one, ";") {
			// A directive's name runs to the first ASCII whitespace, a tab
			// as much as a space, and is matched case-insensitively
			// (CSP3 § 2.2.1, parse a serialized CSP).
			fields := strings.FieldsFunc(directive, isASCIIWhitespace)
			if len(fields) == 0 {
				continue
			}
			switch strings.ToLower(fields[0]) {
			case "sandbox", "default-src", "script-src":
				return true
			}
		}
	}
	return false
}

// isASCIIWhitespace is the Infra standard's ASCII whitespace, which is
// what separates a CSP directive's name from its value.
func isASCIIWhitespace(r rune) bool {
	switch r {
	case '\t', '\n', '\f', '\r', ' ':
		return true
	}
	return false
}
