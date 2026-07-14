package cssfmt

import (
	"strings"
	"testing"
)

func fmtCSS(t *testing.T, in string) string {
	t.Helper()
	out, err := Format([]byte(in), 80)
	if err != nil {
		t.Fatalf("Format(%q) error: %v", in, err)
	}
	return string(out)
}

func TestReindentFixesIndentation(t *testing.T) {
	in := ".a {\n      color: red;\n  background: blue;\n}"
	want := ".a {\n\tcolor: red;\n\tbackground: blue;\n}"
	if got := fmtCSS(t, in); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestOneLinerStaysOneLine(t *testing.T) {
	// A minified rule is NOT reflowed — only its (absent) indentation is touched.
	in := ".a{color:red;background:blue}"
	if got := fmtCSS(t, in); got != in {
		t.Fatalf("one-liner must not be reflowed: got %q", got)
	}
}

func TestNoBlankLinesInvented(t *testing.T) {
	in := ".a {\n\tcolor: red;\n}\n.b {\n\tmargin: 0;\n}"
	got := fmtCSS(t, in)
	if strings.Contains(got, "}\n\n.b") {
		t.Fatalf("a blank line was invented between rules:\n%s", got)
	}
}

func TestExistingBlankLinesPreserved(t *testing.T) {
	in := ".a {\n\tcolor: red;\n}\n\n.b {\n\tmargin: 0;\n}"
	if got := fmtCSS(t, in); got != in {
		t.Fatalf("existing blank line not preserved:\n%s", got)
	}
}

func TestNestedAtRuleIndents(t *testing.T) {
	in := "@media (min-width: 600px) {\n.a {\ncolor: red;\n}\n}"
	want := "@media (min-width: 600px) {\n\t.a {\n\t\tcolor: red;\n\t}\n}"
	if got := fmtCSS(t, in); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestMultiLineCommentInteriorUntouched(t *testing.T) {
	in := ".a {\n\t/* keep\n   me */\n\tcolor: red;\n}"
	got := fmtCSS(t, in)
	if !strings.Contains(got, "/* keep\n   me */") {
		t.Fatalf("multi-line comment interior was re-indented:\n%s", got)
	}
}

func TestSentinelPreserved(t *testing.T) {
	in := ".a {\ncolor: __gsxhole_0_;\n}"
	got := fmtCSS(t, in)
	if !strings.Contains(got, "__gsxhole_0_") {
		t.Fatalf("sentinel mangled:\n%s", got)
	}
}

func TestUnterminatedStringErrors(t *testing.T) {
	// A tokenizer error (unterminated string) → error → caller falls back verbatim.
	if _, err := Format([]byte(".a{content:\"oops}"), 80); err == nil {
		t.Fatal("expected error on unterminated string")
	}
}

// realWorldCSS are hand/editor-formatted CSS bodies (tab-indented, the shapes
// seen in the structpages examples and component styles). Re-indenting
// correctly-indented CSS must reproduce it exactly, including nested @media
// (whose `( … )` media-feature parens must NOT add indentation — brace-only).
var realWorldCSS = []string{
	".widget {\n\tpadding: 1rem;\n\tborder: 1px solid #ddd;\n}\n.widget h3 {\n\tmargin-top: 0;\n}",
	"@media (min-width: 600px) {\n\t.a {\n\t\tcolor: red;\n\t}\n}",
	".a {\n\twidth: calc(100% - 10px);\n\tbackground: url(x.png);\n}",
	".grid {\n\tdisplay: grid;\n\tgrid-template-columns: repeat(auto-fit, minmax(300px, 1fr));\n}",
}

func TestRealWorldCSSReproducedExactly(t *testing.T) {
	for i, src := range realWorldCSS {
		got := fmtCSS(t, src)
		if got != src {
			t.Errorf("case %d: re-indenting already-correct real CSS changed it:\n--- want (input) ---\n%s\n--- got ---\n%s", i, src, got)
		}
		if again := fmtCSS(t, got); again != got {
			t.Errorf("case %d: not idempotent", i)
		}
	}
}

func TestFormatIdempotent(t *testing.T) {
	once := fmtCSS(t, ".a {\n   color: red;\n}\n.b{margin:0}")
	twice := fmtCSS(t, once)
	if once != twice {
		t.Fatalf("not idempotent:\n--- once ---\n%q\n--- twice ---\n%q", once, twice)
	}
}

// TokenSignature is retained as the CSS faithfulness oracle.
func TestTokenSignatureIgnoresWhitespace(t *testing.T) {
	if TokenSignature([]byte("h1,h2{margin:0}")) != TokenSignature([]byte("h1, h2 {\n\tmargin: 0;\n}\n")) {
		t.Fatal("whitespace/optional-semicolon changed the signature")
	}
}

func TestCSSLoneCRIsLineBreakNotFusion(t *testing.T) {
	// A lone \r between rules must act as a line break, never fuse tokens.
	got := fmtCSS(t, ".a{}\r.b{}")
	if strings.Contains(got, "}.b") || !strings.Contains(got, "}\n.b") {
		t.Fatalf("lone CR mishandled in CSS: %q", got)
	}
}

func TestFormatLinesBlockCommentOneLine(t *testing.T) {
	// A multi-line /* … */ comment must be a SINGLE logical line.
	src := ".a {\n/* multi\nline\ncomment */\ncolor: red;\n}"
	lines, ok := FormatLines([]byte(src), 80)
	if !ok {
		t.Fatal("ok=false")
	}
	found := false
	for _, ln := range lines {
		if strings.Contains(ln, "/* multi") {
			if !strings.Contains(ln, "line") || !strings.Contains(ln, "comment */") {
				t.Fatalf("comment split across lines: %q", ln)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("comment line not found in %q", lines)
	}
}
