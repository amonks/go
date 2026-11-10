// Package csp is the fleet's content security policy for scripts, as
// the proxy states it and the apps meet it: the header the proxy
// forwards a request's nonce in, the script-src naming that nonce, and
// what counts as a response already governing its own scripts. The
// proxy's response headers (the proxy spec's "Policy violation reports"
// section) are its one producer; pkg/serve reads the nonce for the
// apps' templates.
//
// A leaf, and published, so that any module — a published one's
// browser tests included — can state the policy without importing the
// proxy or the fleet's HTTP stack.
package csp

import "strings"

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
