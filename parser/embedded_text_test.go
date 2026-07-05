package parser

import (
	"go/token"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

// firstEmbeddedAttr walks f and returns the first *ast.EmbeddedAttr found,
// failing the test if none is present.
func firstEmbeddedAttr(t *testing.T, f *ast.File) *ast.EmbeddedAttr {
	t.Helper()
	var found *ast.EmbeddedAttr
	ast.Inspect(f, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		if ea, ok := n.(*ast.EmbeddedAttr); ok {
			found = ea
			return false
		}
		return true
	})
	if found == nil {
		t.Fatalf("no *ast.EmbeddedAttr found in file")
	}
	return found
}

func TestParseEmbeddedTextAttr(t *testing.T) {
	src := "package p\ncomponent C(v string) { <span class=`badge-@{v} x`>h</span> }\n"
	f, err := ParseFile(token.NewFileSet(), "in.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ea := firstEmbeddedAttr(t, f)
	if ea.Lang != ast.EmbeddedText {
		t.Fatalf("Lang = %d, want EmbeddedText (%d)", ea.Lang, ast.EmbeddedText)
	}
	// segments: Text("badge-"), Interp(v), Text(" x")
	if len(ea.Segments) != 3 {
		t.Fatalf("segments = %d, want 3: %#v", len(ea.Segments), ea.Segments)
	}
	if _, ok := ea.Segments[1].(*ast.Interp); !ok {
		t.Fatalf("segment[1] = %T, want *ast.Interp", ea.Segments[1])
	}
}

func TestParseEmbeddedTextBraced(t *testing.T) {
	src := "package p\ncomponent C(v string) { <span class={`badge-@{v}`}>h</span> }\n"
	f, err := ParseFile(token.NewFileSet(), "in.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ea := firstEmbeddedAttr(t, f); ea.Lang != ast.EmbeddedText {
		t.Fatalf("Lang = %d, want EmbeddedText", ea.Lang)
	}
}

// embeddedText concatenates all *ast.Text segment values in ea, in order.
func embeddedText(ea *ast.EmbeddedAttr) string {
	var b strings.Builder
	for _, s := range ea.Segments {
		if t, ok := s.(*ast.Text); ok {
			b.WriteString(t.Value)
		}
	}
	return b.String()
}

func TestEmbeddedTextEscapedHole(t *testing.T) {
	src := "package p\ncomponent C(v string) { <span data-x=`lit \\@{ not a hole } @{v}`>h</span> }\n"
	f, err := ParseFile(token.NewFileSet(), "in.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ea := firstEmbeddedAttr(t, f)
	// exactly one real hole (@{v}); the \@{ is literal text
	holes := 0
	for _, s := range ea.Segments {
		if _, ok := s.(*ast.Interp); ok {
			holes++
		}
	}
	if holes != 1 {
		t.Fatalf("holes = %d, want 1 (\\@{ must be literal)", holes)
	}
	// literal text must contain "@{ not a hole }" with the backslash removed
	got := embeddedText(ea)
	if strings.Contains(got, "\\@{") {
		t.Fatalf("literal text %q still contains the escaping backslash; \\@{ must unescape to @{", got)
	}
	if want := "lit @{ not a hole } "; got != want {
		t.Fatalf("literal text = %q, want %q", got, want)
	}
}

// firstEmbeddedInterp walks f and returns the first *ast.EmbeddedInterp found,
// failing the test if none is present.
func firstEmbeddedInterp(t *testing.T, f *ast.File) *ast.EmbeddedInterp {
	t.Helper()
	var found *ast.EmbeddedInterp
	ast.Inspect(f, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		if ei, ok := n.(*ast.EmbeddedInterp); ok {
			found = ei
			return false
		}
		return true
	})
	if found == nil {
		t.Fatalf("no *ast.EmbeddedInterp found in file")
	}
	return found
}

// hasEmbeddedInterp reports whether f contains any *ast.EmbeddedInterp node.
func hasEmbeddedInterp(f *ast.File) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		if _, ok := n.(*ast.EmbeddedInterp); ok {
			found = true
			return false
		}
		return true
	})
	return found
}

func TestParseBodyEmbeddedInterp(t *testing.T) {
	src := "package p\ncomponent C(id string, n int) { <p>{`row-@{id}-@{n}`}</p> }\n"
	f, err := ParseFile(token.NewFileSet(), "in.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ei := firstEmbeddedInterp(t, f) // add helper: walk via ast.Inspect for *ast.EmbeddedInterp
	// segments: Text("row-"), Interp(id), Text("-"), Interp(n)
	if len(ei.Segments) != 4 {
		t.Fatalf("segments=%d want 4: %#v", len(ei.Segments), ei.Segments)
	}
	if len(ei.Stages) != 0 {
		t.Fatalf("stages=%d want 0", len(ei.Stages))
	}
}

func TestParseBodyEmbeddedInterpPipe(t *testing.T) {
	src := "package p\ncomponent C(id string) { <p>{`row-@{id}` |> upper}</p> }\n"
	f, err := ParseFile(token.NewFileSet(), "in.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ei := firstEmbeddedInterp(t, f)
	if len(ei.Stages) != 1 || ei.Stages[0].Name != "upper" {
		t.Fatalf("stages=%v want [upper]", ei.Stages)
	}
}

func TestBodyBacktickSubExpressionStaysGo(t *testing.T) {
	// a backtick that is NOT the whole { } value stays a Go raw string.
	src := "package p\ncomponent C(x string) { <p>{`a` + x}</p> }\n"
	f, err := ParseFile(token.NewFileSet(), "in.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// must NOT be an EmbeddedInterp — it's an ordinary Interp with Expr "`a` + x"
	if hasEmbeddedInterp(f) {
		t.Fatalf("`a` + x must stay a Go expression, not EmbeddedInterp")
	}
}

// TestParseBodyEmbeddedInterpEscapedBacktick pins a regression: a lone body
// literal containing an ODD number of gsx-escaped backticks (backslash then
// backtick) used to desync goExprEnd's naive Go-syntax backtick matching,
// producing a spurious "unterminated `{`" error on valid syntax. The literal
// is now bounded by parseEmbeddedAttrLiteral (which understands the gsx
// backslash-backtick escape) instead of goExprEnd.
//
// A literal backtick can't appear in a Go raw string, so the source is built
// via concatenation: "`" for a bare backtick and "\\`" for the gsx escape (a
// double-quoted string containing one backslash then a backtick).
func TestParseBodyEmbeddedInterpEscapedBacktick(t *testing.T) {
	lit := "`" + "x" + "\\`" + " " + "`" // literal bytes: ` x \ ` <space> `
	src := "package p\ncomponent C() { <p>{" + lit + "}</p> }\n"
	f, err := ParseFile(token.NewFileSet(), "in.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ei := firstEmbeddedInterp(t, f)
	if len(ei.Stages) != 0 {
		t.Fatalf("stages=%d want 0: %#v", len(ei.Stages), ei.Stages)
	}
	if len(ei.Segments) != 1 {
		t.Fatalf("segments=%d want 1: %#v", len(ei.Segments), ei.Segments)
	}
	txt, ok := ei.Segments[0].(*ast.Text)
	if !ok {
		t.Fatalf("segment[0] = %T, want *ast.Text", ei.Segments[0])
	}
	if want := "x` "; txt.Value != want {
		t.Fatalf("text = %q, want %q", txt.Value, want)
	}
}

// TestParseEmbeddedAttrBracedEscapedBacktickPipe is the braced-attr sibling of
// TestParseBodyEmbeddedInterpEscapedBacktick: an escaped backtick plus a
// trailing whole-literal `|>` pipeline. parseBracedEmbeddedAttrValue used to
// bound the whole `{ … }` region with goExprEnd, which desyncs on the odd
// escaped backtick; it now only Go-scans the post-literal stages tail.
func TestParseEmbeddedAttrBracedEscapedBacktickPipe(t *testing.T) {
	lit := "`" + "a" + "\\`" + " " + "`" // literal bytes: ` a \ ` <space> `
	src := "package p\ncomponent C() { <span class={" + lit + " |> upper}>h</span> }\n"
	f, err := ParseFile(token.NewFileSet(), "in.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ea := firstEmbeddedAttr(t, f)
	if ea.Lang != ast.EmbeddedText {
		t.Fatalf("Lang = %d, want EmbeddedText", ea.Lang)
	}
	if len(ea.Stages) != 1 || ea.Stages[0].Name != "upper" {
		t.Fatalf("stages=%v want [upper]", ea.Stages)
	}
	if want := "a` "; embeddedText(ea) != want {
		t.Fatalf("text = %q, want %q", embeddedText(ea), want)
	}
}

func TestParseEmbeddedAttrBracedPipe(t *testing.T) {
	src := "package p\ncomponent C(v string) { <span class={`badge-@{v}` |> upper}>h</span> }\n"
	f, err := ParseFile(token.NewFileSet(), "in.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ea := firstEmbeddedAttr(t, f)
	if ea.Lang != ast.EmbeddedText {
		t.Fatalf("Lang = %d, want EmbeddedText", ea.Lang)
	}
	if len(ea.Stages) != 1 || ea.Stages[0].Name != "upper" {
		t.Fatalf("stages=%v want [upper]", ea.Stages)
	}
}
