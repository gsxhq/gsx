// internal/cssfmt/cssfmt_test.go
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

func TestFormatSingleRule(t *testing.T) {
	got := fmtCSS(t, ".a{color:red;background:blue}")
	want := ".a {\n\tcolor: red;\n\tbackground: blue;\n}\n"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestFormatAddsTrailingSemicolon(t *testing.T) {
	got := fmtCSS(t, ".a{color:red}")
	if !strings.Contains(got, "color: red;") {
		t.Fatalf("missing normalized declaration:\n%s", got)
	}
}

func TestFormatSelectorList(t *testing.T) {
	got := fmtCSS(t, "h1,h2 , h3{margin:0}")
	if !strings.HasPrefix(got, "h1, h2, h3 {") {
		t.Fatalf("selector list not normalized:\n%s", got)
	}
}

func TestFormatPseudoClassColonNotSpaced(t *testing.T) {
	// A selector colon (pseudo-class) must stay attached — ".a:hover", never
	// ".a: hover" (which would be a different, broken selector). renderInline
	// injects no space around ':'; only layoutDecl spaces the declaration colon.
	got := fmtCSS(t, ".a:hover{color:red}")
	if !strings.HasPrefix(got, ".a:hover {") {
		t.Fatalf("pseudo-class colon was spaced/mangled:\n%s", got)
	}
}

func TestFormatNestedAtRule(t *testing.T) {
	got := fmtCSS(t, "@media (min-width:600px){.a{color:red}}")
	// renderInline injects NO space around ':' — the at-rule prelude keeps
	// "min-width:600px" as-is (correct + safe). The declaration colon inside the
	// nested rule IS spaced ("color: red"), because layoutDecl adds it.
	want := "@media (min-width:600px) {\n\t.a {\n\t\tcolor: red;\n\t}\n}\n"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestFormatAtRuleStatement(t *testing.T) {
	got := fmtCSS(t, `@import "x.css";`)
	if strings.TrimSpace(got) != `@import "x.css";` {
		t.Fatalf("at-rule statement not preserved:\n%s", got)
	}
}

func TestFormatPreservesComment(t *testing.T) {
	got := fmtCSS(t, "/* hi */ .a{color:red}")
	if !strings.Contains(got, "/* hi */") {
		t.Fatalf("comment dropped:\n%s", got)
	}
}

func TestFormatPreservesSentinel(t *testing.T) {
	got := fmtCSS(t, ".a{color:__gsxhole_0_}")
	if !strings.Contains(got, "__gsxhole_0_") {
		t.Fatalf("sentinel mangled:\n%s", got)
	}
}

func TestFormatRejectsUnbalanced(t *testing.T) {
	if _, err := Format([]byte(".a{color:red"), 80); err == nil {
		t.Fatal("expected error for unbalanced braces")
	}
}

func TestFormatIdempotent(t *testing.T) {
	once := fmtCSS(t, ".a{color:red;background:blue}h1,h2{margin:0}")
	twice := fmtCSS(t, once)
	if once != twice {
		t.Fatalf("not idempotent:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}
