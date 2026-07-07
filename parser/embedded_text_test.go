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

// TestBodyBacktickBackslashSubExpressionUnterminated documents a Task-2
// (unified go-expression scanner reroute) behavior change for a Go raw string
// that ends in a backslash, used as a sub-expression (not the whole `{ }`
// value) — e.g. “ `a\` “ + x. Before Task 2, this used to be misread by
// tryParseBodyEmbeddedInterp as a lone embedded literal (gsx's backtick-escape
// convention treats the trailing `\“ as an escaped backtick, so the literal
// scan ran off the end); the fix made it rewind and fall back to
// goDepth1End/goExprEnd, which — pre-Task-2 — used a plain, non-escape-aware
// bare-backtick byte scan and so correctly found the real `}` and parsed a
// plain *ast.Interp.
//
// Task 2 reroutes goDepth1End's guarded fast path onto scanGoExpr, whose
// backtick handling is uniformly gsx-escape-aware for ALL backticks (bare,
// `js`, and `css`) — by design (see parser/goexpr.go's scanGoExpr doc comment
// and docs/superpowers/plans/2026-07-07-unified-goexpr-scanner.md, which
// scopes the byte-identical guarantee to "tag-and-backtick-free fragments"
// and calls out an "@{`-in-raw-string compatibility change" as expected,
// to be finished/documented in Task 5/6). So the fallback ALSO now treats the
// trailing `\“ as an escape and runs off the end looking for a real closing
// backtick — same as tryParseBodyEmbeddedInterp's trial does — and there is no
// longer a differently-behaved fallback to rescue it: the whole `{ }` is
// reported unterminated. A bare backtick used as a Go sub-expression that ends
// in a literal backslash immediately before what would be its closing
// backtick is the one construct this reroute makes newly invalid; it is a
// known, accepted consequence of Task 2 (see task-2-report.md), not a bug in
// this test.
//
// The literal backslash-backtick can't appear in a Go raw string, so the
// source is built via concatenation.
func TestBodyBacktickBackslashSubExpressionUnterminated(t *testing.T) {
	lit := "`" + "a" + "\\" + "`" // literal bytes: ` a \ `
	src := "package p\ncomponent C(x string) { <p>{" + lit + " + x}</p> }\n"
	// Sanity-check the constructed bytes really are backtick, a, backslash,
	// backtick.
	if lit != "`a\\`" {
		t.Fatalf("lit = %q, want `a\\` (backtick a backslash backtick)", lit)
	}
	_, err := ParseFile(token.NewFileSet(), "in.gsx", []byte(src), 0)
	if err == nil {
		t.Fatalf("parse: want an error (unterminated `{`), got success")
	}
	if !strings.Contains(err.Error(), "unterminated `{`") {
		t.Fatalf("parse error = %q, want it to contain %q", err.Error(), "unterminated `{`")
	}
}

// TestBracedAttrBacktickBackslashSubExpressionUnterminated is the braced-attr
// sibling of TestBodyBacktickBackslashSubExpressionUnterminated: a Go raw
// string ending in a backslash, used as a sub-expression of `title={ … }`,
// now hits the same Task-2 backtick-escape-awareness reroute in
// parseBracedEmbeddedAttrValue's goExprEnd fallback and is reported
// unterminated rather than falling back to an ordinary ExprAttr. See that
// test's doc comment for the full explanation.
func TestBracedAttrBacktickBackslashSubExpressionUnterminated(t *testing.T) {
	lit := "`" + "a" + "\\" + "`" // literal bytes: ` a \ `
	src := "package p\ncomponent C(x string) { <span title={" + lit + " + x}>h</span> }\n"
	_, err := ParseFile(token.NewFileSet(), "in.gsx", []byte(src), 0)
	if err == nil {
		t.Fatalf("parse: want an error (unterminated `{`), got success")
	}
	if !strings.Contains(err.Error(), "unterminated `{`") {
		t.Fatalf("parse error = %q, want it to contain %q", err.Error(), "unterminated `{`")
	}
}

// TestBracedAttrBacktickBackslashSubExpressionClassUnterminated is the
// class/style sibling of TestBodyBacktickBackslashSubExpressionUnterminated:
// a Go raw string ending in a backslash, used as a sub-expression of
// `class={ … }`, now hits the same Task-2 reroute in the composed-attr path
// (goExprEnd, called before splitComposed/composedDelims ever run) and is
// reported unterminated rather than falling back to a composed *ast.ClassAttr.
// See TestBodyBacktickBackslashSubExpressionUnterminated's doc comment for the
// full explanation.
func TestBracedAttrBacktickBackslashSubExpressionClassUnterminated(t *testing.T) {
	lit := "`" + "a" + "\\" + "`" // literal bytes: ` a \ `
	src := "package p\ncomponent C(x string) { <span class={" + lit + " + x}>h</span> }\n"
	_, err := ParseFile(token.NewFileSet(), "in.gsx", []byte(src), 0)
	if err == nil {
		t.Fatalf("parse: want an error (unterminated `{` in class value), got success")
	}
	if !strings.Contains(err.Error(), "unterminated `{`") {
		t.Fatalf("parse error = %q, want it to contain %q", err.Error(), "unterminated `{`")
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
	src := "package p\ncomponent C() { <p>{" + lit + "}</p> }\n"
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
