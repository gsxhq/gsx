package wsnorm

import "testing"

// This file holds white-box tests of normalizeText, wsnorm's unexported
// per-Text rule — the only reason this package needs an in-package (not
// wsnorm_test) test file. Everything else lives in wsnorm_test.go as a
// black-box test of the exported API: parser/markup.go now imports wsnorm
// (for the shared IsPreserveTag predicate), so an in-package test file that
// also imported parser (for its .gsx-parsing helper) would create an import
// cycle in the test binary.

// --- normalizeText table (the load-bearing per-text rule) ---

func TestNormalizeText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		out  string
		keep bool
	}{
		// All-whitespace with newline → DROP (cosmetic indentation).
		{"all-ws newline", "\n  ", "", false},
		{"all-ws CR", "\r\n\t", "", false},
		{"all-ws just newline", "\n", "", false},
		// All-whitespace without newline → single inline space.
		{"all-ws space", " ", " ", true},
		{"all-ws spaces", "   ", " ", true},
		{"all-ws tabs", "\t\t", " ", true},
		// Leading inline run (no newline) → one leading space.
		{"lead inline space", " world", " world", true},
		{"lead inline tab", "\tworld", " world", true},
		// Leading newline edge → no space.
		{"lead newline", "\nworld", "world", true},
		{"lead newline+indent", "\n  world", "world", true},
		// Trailing inline run (no newline) → one trailing space.
		{"trail inline space", "Hello,   ", "Hello, ", true},
		{"trail inline tab", "Hello\t", "Hello ", true},
		// Trailing newline edge → no space.
		{"trail newline", "world\n", "world", true},
		{"trail newline+indent", "world\n  ", "world", true},
		// Inline and newline runs preserve the same authored separator.
		{"internal percent", "42\n %", "42 %", true},
		{"internal explicit percent", "42 %", "42 %", true},
		{"internal punctuation", "Hello\n !", "Hello !", true},
		{"internal opening", "(\n word", "( word", true},
		{"internal ellipsis", "word\n …", "word …", true},
		{"internal period ellipsis", "word\n ...", "word ...", true},
		{"internal percent entity", "42\n &#37;", "42 &#37;", true},
		{"internal opening entity", "&lpar;\n word", "&lpar; word", true},
		{"internal ellipsis entity", "word\n &hellip;", "word &hellip;", true},
		{"internal entity gap left", "a&nbsp;\n b", "a&nbsp; b", true},
		{"internal entity gap right", "a\n &nbsp;b", "a &nbsp;b", true},
		{"internal Unicode gap", "a\u00a0\n b", "a\u00a0 b", true},
		{"internal explicit entity gap", "a&nbsp; b", "a&nbsp; b", true},
		{"internal collapse", "foo   bar", "foo bar", true},
		{"internal tabs", "foo\t\tbar", "foo bar", true},
		{"internal newline", "foo\nbar", "foo bar", true},
		// Multi-line join (lines trimmed, joined by one space, edges dropped).
		{"multi-line join", "\n  a\n  b\n", "a b", true},
		// Both edges inline.
		{"both inline edges", "  x  ", " x ", true},
		// Content-only unchanged.
		{"content only", "hello", "hello", true},
		{"content with single internal space", "a b", "a b", true},
		// Empty string: not all-whitespace by our rule? Empty has no newline and is
		// all-whitespace vacuously; treat as the no-newline all-ws → " ".
		// (Parser never emits empty Text; documented behavior.)
		{"empty", "", " ", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, keep := normalizeText(tc.in)
			if out != tc.out || keep != tc.keep {
				t.Fatalf("normalizeText(%q) = (%q, %v), want (%q, %v)", tc.in, out, keep, tc.out, tc.keep)
			}
		})
	}
}

// normalizeText must be idempotent on its own output (when kept).
func TestNormalizeTextIdempotent(t *testing.T) {
	inputs := []string{
		"\n  ", " ", "   ", "\t", " world", "\nworld", "Hello,   ",
		"world\n", "foo   bar", "\n  a\n  b\n", "  x  ", "hello",
	}
	for _, in := range inputs {
		out, keep := normalizeText(in)
		if !keep {
			continue
		}
		out2, keep2 := normalizeText(out)
		if !keep2 || out2 != out {
			t.Fatalf("normalizeText not idempotent: %q → %q → (%q, keep=%v)", in, out, out2, keep2)
		}
	}
}
