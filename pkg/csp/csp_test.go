package csp

import "testing"

// Pinned whole, because each source is a decision ScriptPolicy's doc
// argues: no 'self' (media serves uploads from the origin typed by
// extension), 'strict-dynamic' (what a nonced script loads is trusted
// through it), 'wasm-unsafe-eval' (compiling wasm, and nothing else).
func TestScriptPolicy(t *testing.T) {
	got := ScriptPolicy("abc123")
	want := "script-src 'nonce-abc123' 'strict-dynamic' 'wasm-unsafe-eval'"
	if got != want {
		t.Fatalf("ScriptPolicy = %q, want %q", got, want)
	}
}

func TestGovernsScripts(t *testing.T) {
	for _, tc := range []struct {
		policy string
		want   bool
	}{
		{"", false},
		{"frame-ancestors 'self'", false},
		{"base-uri 'self'; frame-ancestors 'self'; form-action 'self'; object-src 'none'", false},
		{"sandbox", true},
		{"sandbox allow-scripts", true},
		{"default-src 'self'; script-src 'self' 'wasm-unsafe-eval'", true},
		{"default-src 'none'", true},
		{"script-src 'none'", true},
		{"  Script-Src 'self'", true},
		// Any ASCII whitespace ends the name.
		{"default-src\t'self'", true},
		{"sandbox\tallow-scripts", true},
		// Part of the script decision is not all of it: the fleet's policy
		// still covers the rest, intersecting with this one.
		{"script-src-elem 'self'", false},
		{"script-src-attr 'none'", false},
		// A directive the browser does not know governs nothing.
		{"script-src-typo 'none'", false},
		{"script-srcx 'none'", false},
		// A second policy, as a comma-joined header carries it.
		{"frame-ancestors 'self', sandbox", true},
		// A directive's name, not text elsewhere in the policy.
		{"img-src https://example.com/script-src/", false},
		{"report-uri /sandbox", false},
	} {
		if got := GovernsScripts(tc.policy); got != tc.want {
			t.Errorf("GovernsScripts(%q) = %v, want %v", tc.policy, got, tc.want)
		}
	}
}

// The fleet's policy beside a response's own: the directives always,
// and the script policy unless the response already governs scripts —
// the rule the proxy and pkg/browsertest share.
func TestPolicy(t *testing.T) {
	const directives = "base-uri 'self'; frame-ancestors 'self'; form-action 'self'; object-src 'none'"
	if Directives != directives {
		t.Fatalf("Directives = %q, want %q", Directives, directives)
	}
	for _, tc := range []struct {
		own, want string
	}{
		{"", directives + "; " + ScriptPolicy("n")},
		{"frame-ancestors 'none'", directives + "; " + ScriptPolicy("n")},
		{"sandbox", directives},
		{"default-src 'self'; script-src 'self' 'wasm-unsafe-eval'", directives},
	} {
		if got := Policy("n", tc.own); got != tc.want {
			t.Errorf("Policy(n, %q) = %q, want %q", tc.own, got, tc.want)
		}
	}
}

// A content-only domain's policy: framed by the account domains named,
// posting nowhere, and no script policy of the fleet's — every byte
// such a domain serves carries its own sandbox.
func TestContentPolicy(t *testing.T) {
	got := ContentPolicy("https://ss.cx", "https://www.ss.cx")
	want := "base-uri 'self'; frame-ancestors 'self' https://ss.cx https://www.ss.cx; form-action 'none'; object-src 'none'"
	if got != want {
		t.Errorf("ContentPolicy = %q, want %q", got, want)
	}
	if got := ContentPolicy(); got != "base-uri 'self'; frame-ancestors 'self'; form-action 'none'; object-src 'none'" {
		t.Errorf("ContentPolicy() = %q", got)
	}
}
