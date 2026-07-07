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
	src := "package p\ncomponent C(v string) { <span class=f`badge-@{v} x`>h</span> }\n"
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
	src := "package p\ncomponent C(v string) { <span class={f`badge-@{v}`}>h</span> }\n"
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
	src := "package p\ncomponent C(v string) { <span data-x=f`lit \\@{ not a hole } @{v}`>h</span> }\n"
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
	src := "package p\ncomponent C(id string, n int) { <p>{f`row-@{id}-@{n}`}</p> }\n"
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
	src := "package p\ncomponent C(id string) { <p>{f`row-@{id}` |> upper}</p> }\n"
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
	src := "package p\ncomponent C() { <p>{f" + lit + "}</p> }\n"
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
	src := "package p\ncomponent C() { <span class={f" + lit + " |> upper}>h</span> }\n"
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

// TestBodyBacktickBackslashSubExpressionStaysGo pins the fix for a parser
// regression: a Go raw string that ends in a backslash, used as a
// sub-expression (not the whole `{ }` value), used to be misread by
// tryParseBodyEmbeddedInterp as a lone embedded literal. gsx's backtick-escape
// convention treats the trailing `\“ as an escaped backtick, so the literal
// scan runs off the end and used to surface "unterminated embedded attribute
// literal" instead of falling back to an ordinary Go expression. It must now
// rewind and parse as a plain *ast.Interp with Expr “ `a\` + x “.
//
// The literal backslash-backtick can't appear in a Go raw string, so the
// source is built via concatenation.
func TestBodyBacktickBackslashSubExpressionStaysGo(t *testing.T) {
	lit := "`" + "a" + "\\" + "`" // literal bytes: ` a \ `
	src := "package p\ncomponent C(x string) { <p>{" + lit + " + x}</p> }\n"
	// Sanity-check the constructed bytes really are backtick, a, backslash,
	// backtick.
	if lit != "`a\\`" {
		t.Fatalf("lit = %q, want `a\\` (backtick a backslash backtick)", lit)
	}
	f, err := ParseFile(token.NewFileSet(), "in.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if hasEmbeddedInterp(f) {
		t.Fatalf("`a\\` + x must stay a Go expression, not EmbeddedInterp")
	}
	// Confirm it parsed as an ordinary *ast.Interp with the expected Expr.
	var interp *ast.Interp
	ast.Inspect(f, func(n ast.Node) bool {
		if interp != nil {
			return false
		}
		if in, ok := n.(*ast.Interp); ok {
			interp = in
			return false
		}
		return true
	})
	if interp == nil {
		t.Fatalf("no *ast.Interp found in file")
	}
	if want := lit + " + x"; interp.Expr != want {
		t.Fatalf("Expr = %q, want %q", interp.Expr, want)
	}
}

// TestBracedAttrBacktickBackslashSubExpressionStaysGo is the braced-attr
// sibling of TestBodyBacktickBackslashSubExpressionStaysGo: a Go raw string
// ending in a backslash, used as a sub-expression of `title={ … }`, must parse
// as an ordinary ExprAttr rather than erroring out of
// parseBracedEmbeddedAttrValue. A plain (non class/style) attribute name is used
// so the fallback yields ExprAttr — class/style route through parseComposedAttr
// instead (see TestBracedAttrBacktickBackslashSubExpressionClassComposes).
func TestBracedAttrBacktickBackslashSubExpressionStaysGo(t *testing.T) {
	lit := "`" + "a" + "\\" + "`" // literal bytes: ` a \ `
	src := "package p\ncomponent C(x string) { <span title={" + lit + " + x}>h</span> }\n"
	f, err := ParseFile(token.NewFileSet(), "in.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var expr *ast.ExprAttr
	ast.Inspect(f, func(n ast.Node) bool {
		if expr != nil {
			return false
		}
		if ea, ok := n.(*ast.ExprAttr); ok {
			expr = ea
			return false
		}
		return true
	})
	if expr == nil {
		t.Fatalf("no *ast.ExprAttr found in file (want the title attr parsed as an ordinary Go expression)")
	}
	if want := lit + " + x"; expr.Expr != want {
		t.Fatalf("Expr = %q, want %q", expr.Expr, want)
	}
}

// TestBracedAttrBacktickBackslashSubExpressionClassComposes pins the class/style
// dispatch in parseBracedEmbeddedAttrValue's fallback: when a class value starts
// with a backtick but is actually a Go sub-expression (so it is NOT a lone gsx
// literal), it must fall back to parseComposedAttr and remain a *ast.ClassAttr —
// the node the fallthrough/forwarding merge machinery recognizes — not a plain
// ExprAttr (which would silently drop the component's own class when a caller
// forwards class via an attrs bag).
func TestBracedAttrBacktickBackslashSubExpressionClassComposes(t *testing.T) {
	lit := "`" + "a" + "\\" + "`" // literal bytes: ` a \ `
	src := "package p\ncomponent C(x string) { <span class={" + lit + " + x}>h</span> }\n"
	f, err := ParseFile(token.NewFileSet(), "in.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var cls *ast.ClassAttr
	ast.Inspect(f, func(n ast.Node) bool {
		if cls != nil {
			return false
		}
		if ca, ok := n.(*ast.ClassAttr); ok {
			cls = ca
			return false
		}
		return true
	})
	if cls == nil {
		t.Fatalf("no *ast.ClassAttr found (want the class attr parsed as a composed ClassAttr so it merges with forwarded bag classes)")
	}
	if cls.Name != "class" {
		t.Fatalf("ClassAttr.Name = %q, want %q", cls.Name, "class")
	}
}

// TestBodyEscapedBacktickLiteralStillEmbedded is the control for
// TestBodyBacktickBackslashSubExpressionStaysGo: a lone literal whose gsx
// backtick-escape genuinely terminates (an escaped backtick followed by more
// literal text and a real closing backtick) must still parse as
// EmbeddedInterp, not fall back to Go. Distinguishing case: the escape here is
// followed by further literal content and a true closing backtick, so the
// literal closes cleanly — unlike the `\` + x case above, which runs off the
// end.
func TestBodyEscapedBacktickLiteralStillEmbedded(t *testing.T) {
	lit := "`" + "x" + "\\`" + " " + "`" // literal bytes: ` x \ ` <space> `
	src := "package p\ncomponent C() { <p>{f" + lit + "}</p> }\n"
	f, err := ParseFile(token.NewFileSet(), "in.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ei := firstEmbeddedInterp(t, f)
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

// TestBareBacktickAttrValueStaysGoString pins the "one rule": an unbraced bare
// backtick attribute value is a plain Go raw string (ExprAttr whose Expr is the
// raw literal, backticks included), NOT an interpolating EmbeddedText literal —
// interpolation is opt-in behind the f`/js`/css` prefix. A `@{` inside is now
// ordinary literal text, not a hole.
func TestBareBacktickAttrValueStaysGoString(t *testing.T) {
	src := "package p\ncomponent C() { <div data-x=`row-@{id}`>h</div> }\n"
	f, err := ParseFile(token.NewFileSet(), "in.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var ea *ast.ExprAttr
	ast.Inspect(f, func(n ast.Node) bool {
		if ea != nil {
			return false
		}
		if a, ok := n.(*ast.ExprAttr); ok {
			ea = a
			return false
		}
		return true
	})
	if ea == nil {
		t.Fatalf("no *ast.ExprAttr found; bare `…` attr value must parse as a plain Go string")
	}
	if want := "`row-@{id}`"; ea.Expr != want {
		t.Fatalf("Expr = %q, want %q (raw string, no @{ } hole processing)", ea.Expr, want)
	}
}

// TestBareBacktickClassAttrValueComposes pins that an unbraced bare backtick on
// class/style is a plain Go string routed through the composed ClassAttr (so the
// forwarding/merge machinery still recognizes the component's contribution),
// never an EmbeddedAttr.
func TestBareBacktickClassAttrValueComposes(t *testing.T) {
	src := "package p\ncomponent C() { <span class=`p-4 flex`>h</span> }\n"
	f, err := ParseFile(token.NewFileSet(), "in.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var ca *ast.ClassAttr
	ast.Inspect(f, func(n ast.Node) bool {
		if ca != nil {
			return false
		}
		if a, ok := n.(*ast.ClassAttr); ok {
			ca = a
			return false
		}
		return true
	})
	if ca == nil {
		t.Fatalf("no *ast.ClassAttr found; bare `…` class value must compose")
	}
	if len(ca.Parts) != 1 || ca.Parts[0].Expr != "`p-4 flex`" {
		t.Fatalf("Parts = %#v, want one Expr part `p-4 flex`", ca.Parts)
	}
}

// TestBareBacktickBodyStaysGoString pins the body counterpart: a lone bare
// backtick in `{ }` is a plain Go raw string (*ast.Interp), never an
// EmbeddedInterp.
func TestBareBacktickBodyStaysGoString(t *testing.T) {
	src := "package p\ncomponent C() { <p>{`row-@{id}`}</p> }\n"
	f, err := ParseFile(token.NewFileSet(), "in.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if hasEmbeddedInterp(f) {
		t.Fatalf("bare `…` in body must stay a Go raw string, not EmbeddedInterp")
	}
	var in *ast.Interp
	ast.Inspect(f, func(n ast.Node) bool {
		if in != nil {
			return false
		}
		if i, ok := n.(*ast.Interp); ok {
			in = i
			return false
		}
		return true
	})
	if in == nil {
		t.Fatalf("no *ast.Interp found")
	}
	if want := "`row-@{id}`"; in.Expr != want {
		t.Fatalf("Expr = %q, want %q", in.Expr, want)
	}
}

func TestParseEmbeddedAttrBracedPipe(t *testing.T) {
	src := "package p\ncomponent C(v string) { <span class={f`badge-@{v}` |> upper}>h</span> }\n"
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

// TestParseEmbeddedAttrBracedJSPipeRejected pins the parser-level gate closing
// a latent codegen gap: a whole-literal `|> f` pipeline is only meaningful on
// a plain-text “ `…` “ literal, since the codegen only ever consumes Stages
// for EmbeddedText. Without this gate, a pipe on a js`…` literal parsed
// cleanly (Stages set) and would be silently dropped at emit — wrong output,
// no error. It must now be a parse error instead.
func TestParseEmbeddedAttrBracedJSPipeRejected(t *testing.T) {
	src := "package p\ncomponent C() { <span class={js`x` |> upper}>h</span> }\n"
	_, err := ParseFile(token.NewFileSet(), "in.gsx", []byte(src), 0)
	if err == nil {
		t.Fatalf("parse succeeded, want error rejecting whole-literal |> on a js`` literal")
	}
	if want := "whole-literal pipelines are only supported on plain `…` attribute literals, not js``/css``"; !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want it to contain %q", err, want)
	}
}

// TestParseEmbeddedAttrBracedCSSPipeRejected is the css“ sibling of
// TestParseEmbeddedAttrBracedJSPipeRejected.
func TestParseEmbeddedAttrBracedCSSPipeRejected(t *testing.T) {
	src := "package p\ncomponent C() { <span class={css`color:red` |> upper}>h</span> }\n"
	_, err := ParseFile(token.NewFileSet(), "in.gsx", []byte(src), 0)
	if err == nil {
		t.Fatalf("parse succeeded, want error rejecting whole-literal |> on a css`` literal")
	}
	if want := "whole-literal pipelines are only supported on plain `…` attribute literals, not js``/css``"; !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want it to contain %q", err, want)
	}
}

// TestParseEmbeddedAttrBracedJSNoPipeStillParses is the control for
// TestParseEmbeddedAttrBracedJSPipeRejected: a js“ literal WITHOUT a trailing
// pipe is unaffected by the gate and still parses as a plain EmbeddedAttr with
// no Stages.
func TestParseEmbeddedAttrBracedJSNoPipeStillParses(t *testing.T) {
	src := "package p\ncomponent C() { <span class={js`x`}>h</span> }\n"
	f, err := ParseFile(token.NewFileSet(), "in.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ea := firstEmbeddedAttr(t, f)
	if ea.Lang != ast.EmbeddedJS {
		t.Fatalf("Lang = %d, want EmbeddedJS", ea.Lang)
	}
	if len(ea.Stages) != 0 {
		t.Fatalf("stages=%v want none", ea.Stages)
	}
}
