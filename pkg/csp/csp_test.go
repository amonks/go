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
