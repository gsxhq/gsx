// internal/jsfmt/jsfmt_test.go
package jsfmt

import (
	"strings"
	"testing"
)

func fmtJS(t *testing.T, in string) string {
	t.Helper()
	out, err := Format([]byte(in), 80)
	if err != nil {
		t.Fatalf("Format(%q) error: %v", in, err)
	}
	return string(out)
}

func TestReindentsToTabs(t *testing.T) {
	in := "function f() {\n      const x = 1;\n   if (x) {\nreturn x;\n   }\n}"
	want := "function f() {\n\tconst x = 1;\n\tif (x) {\n\t\treturn x;\n\t}\n}"
	if got := fmtJS(t, in); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestKeepsNewlinesNoReflow(t *testing.T) {
	// A one-line body is NOT reflowed (ASI-preserving: line structure untouched).
	in := "const x=1;const y=2"
	if got := fmtJS(t, in); got != in {
		t.Fatalf("must not reflow/reformat a one-liner: got %q", got)
	}
}

func TestPreservesBlankLines(t *testing.T) {
	in := "a();\n\nb();"
	if got := fmtJS(t, in); got != in {
		t.Fatalf("blank line not preserved: got %q", got)
	}
}

func TestTemplateLiteralInteriorUntouched(t *testing.T) {
	in := "const t = `line1\n   line2`;"
	got := fmtJS(t, in)
	if !strings.Contains(got, "`line1\n   line2`") {
		t.Fatalf("template literal interior was re-indented:\n%s", got)
	}
}

func TestRegexNotMislexedAsDivision(t *testing.T) {
	// `/re/g` after `=` is a regex; must pass through verbatim, not break.
	in := "const re = /a\\/b/g;\nconst q = a / b;"
	got := fmtJS(t, in)
	if !strings.Contains(got, "/a\\/b/g") || !strings.Contains(got, "a / b") {
		t.Fatalf("regex/division mishandled:\n%s", got)
	}
}

func TestCommentInteriorUntouched(t *testing.T) {
	in := "function f() {\n\t/* a\n  b */\n\tx();\n}"
	got := fmtJS(t, in)
	if !strings.Contains(got, "/* a\n  b */") {
		t.Fatalf("block comment interior re-indented:\n%s", got)
	}
}

func TestIdempotent(t *testing.T) {
	once := fmtJS(t, "function f(){\n   const x=1\nif(x){\nreturn x\n}\n}")
	twice := fmtJS(t, once)
	if once != twice {
		t.Fatalf("not idempotent:\n--- once ---\n%q\n--- twice ---\n%q", once, twice)
	}
}

func TestTokenSignatureIgnoresWhitespace(t *testing.T) {
	// The two inputs must differ ONLY in whitespace — the re-indenter never
	// adds or removes a semicolon, so the signature (correctly) does not
	// normalize the optional one. Both have `return x;`.
	a := TokenSignature([]byte("const x=1;function f(){return x;}"))
	b := TokenSignature([]byte("const x = 1;\nfunction f() {\n\treturn x;\n}"))
	if a != b {
		t.Fatalf("whitespace changed the signature:\n%q\n%q", a, b)
	}
}

func TestNonLFLineTerminatorsNotDropped(t *testing.T) {
	// A lone \r, U+2028, U+2029 must NOT be dropped (that would fuse tokens and
	// break ASI). Each must become a real line break in the output.
	for _, in := range []string{"a = 1\rb = 2", "a = 1 b = 2", "a = 1 b = 2"} {
		got := fmtJS(t, in)
		if strings.Contains(got, "1b") || strings.Contains(got, "12") {
			t.Fatalf("line terminator dropped, tokens fused: %q -> %q", in, got)
		}
		if !strings.Contains(got, "a = 1\nb = 2") {
			t.Fatalf("expected a line break preserved (as \\n): %q -> %q", in, got)
		}
	}
}

func TestCRLFNormalizedToLF(t *testing.T) {
	got := fmtJS(t, "a = 1\r\nb = 2")
	if got != "a = 1\nb = 2" {
		t.Fatalf("CRLF not normalized to a single LF: %q", got)
	}
}
