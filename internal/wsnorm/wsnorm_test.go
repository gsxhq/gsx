package wsnorm

import (
	"go/token"
	"reflect"
	"testing"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/parser"
)

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
		// Internal run collapse.
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

// --- helpers for AST-level tests ---

func parse(t *testing.T, body string) *ast.File {
	t.Helper()
	src := "package p\n\ncomponent C() {\n" + body + "\n}\n"
	f, err := parser.ParseFile(token.NewFileSet(), "t.gsx", src, 0)
	if err != nil {
		t.Fatalf("parse error: %v\nsrc:\n%s", err, src)
	}
	return f
}

// collectText returns every Text node's Value, in traversal order.
func collectText(f *ast.File) []string {
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if t, ok := n.(*ast.Text); ok {
			out = append(out, t.Value)
		}
		return true
	})
	return out
}

func TestNormalizeBlockIndentationRemoved(t *testing.T) {
	f := parse(t, "<div>\n  <p>a</p>\n  <span>b</span>\n</div>")
	Normalize(f)
	got := collectText(f)
	// "a" and "b" survive; all indentation Text dropped.
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

func TestNormalizeInlineSpaceKept(t *testing.T) {
	// The parser emits the inline trailing space after "a" as Text "a "; wsnorm
	// must preserve that single significant space (no newline at the edge).
	f := parse(t, "a <b>y</b>")
	Normalize(f)
	got := collectText(f)
	want := []string{"a ", "y"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

func TestNormalizeNewlineEdgeDropped(t *testing.T) {
	// The parser emits Text "y\n" after <b>x</b>; wsnorm drops the trailing
	// newline edge (cosmetic indentation before the closing brace) → "y".
	f := parse(t, "<b>x</b>\ny")
	Normalize(f)
	got := collectText(f)
	want := []string{"x", "y"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

func TestNormalizeTrailingInlineBeforeInterp(t *testing.T) {
	f := parse(t, "Hello,   {name}")
	Normalize(f)
	got := collectText(f)
	want := []string{"Hello, "}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

// --- Preserve contexts ---

func TestPreservePre(t *testing.T) {
	f := parse(t, "<pre>  a\n  b</pre>")
	Normalize(f)
	got := collectText(f)
	want := []string{"  a\n  b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

func TestPreserveTextarea(t *testing.T) {
	f := parse(t, "<textarea>\n x \n</textarea>")
	Normalize(f)
	got := collectText(f)
	want := []string{"\n x \n"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

func TestPreserveScript(t *testing.T) {
	f := parse(t, "<script>\n let x=1;\n</script>")
	Normalize(f)
	got := collectText(f)
	want := []string{"\n let x=1;\n"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

func TestPreserveStyle(t *testing.T) {
	f := parse(t, "<style>\n a{}\n</style>")
	Normalize(f)
	got := collectText(f)
	want := []string{"\n a{}\n"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

// A <pre> wrapping nested elements + indentation → all inner whitespace preserved
// (nested-preserve flag stays on through descendants).
func TestPreserveNested(t *testing.T) {
	f := parse(t, "<pre>\n  <code>\n    x\n  </code>\n</pre>")
	Normalize(f)
	got := collectText(f)
	want := []string{"\n  ", "\n    x\n  ", "\n"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

// --- MarkupAttr slot ---

func TestMarkupAttrSlotNormalized(t *testing.T) {
	f := parse(t, "<Panel header={ <h1>\n  Hi \n</h1> }/>")
	Normalize(f)
	got := collectText(f)
	// "\n  Hi \n" → "Hi" (newline edges dropped).
	want := []string{"Hi"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

// A <pre> inside a MarkupAttr slot is preserved (slot is fresh context, but the
// pre tag turns preserve back on within the slot).
func TestMarkupAttrSlotPreservesPre(t *testing.T) {
	f := parse(t, "<Panel header={ <pre>  a\n  b</pre> }/>")
	Normalize(f)
	got := collectText(f)
	want := []string{"  a\n  b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

// --- Control flow ---

func TestControlFlowForBodyNormalized(t *testing.T) {
	// Inside <li>: the parser yields Text "\n " before {x} and " \n" after it;
	// both are all-whitespace-with-newline → dropped (cosmetic indentation).
	f := parse(t, "{ for _, x := range xs {\n  <li>\n {x} \n</li>\n} }")
	Normalize(f)
	got := collectText(f)
	want := []string(nil)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

// A control-flow body whose markup carries real content + indentation: the
// indentation collapses but content survives, proving the for body is walked.
func TestControlFlowForBodyContentSurvives(t *testing.T) {
	f := parse(t, "{ for _, x := range xs {\n  <li>item   {x}</li>\n} }")
	Normalize(f)
	got := collectText(f)
	// "item   " → "item " (internal/trailing inline run collapsed to one space).
	want := []string{"item "}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

// --- Idempotence at the AST level: twice == once ---

func TestNormalizeIdempotentAST(t *testing.T) {
	bodies := []string{
		"<div>\n  <p>a</p>\n  <span>b</span>\n</div>",
		"<b>x</b> y",
		"Hello,   {name}",
		"<pre>  a\n  b</pre>",
		"<Panel header={ <h1>\n  Hi \n</h1> }/>",
		"{ for _, x := range xs {\n  <li>\n {x} \n</li>\n} }",
		"<div>foo   bar\n  baz</div>",
	}
	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			f := parse(t, body)
			Normalize(f)
			once := collectText(f)
			Normalize(f)
			twice := collectText(f)
			if !reflect.DeepEqual(once, twice) {
				t.Fatalf("not idempotent:\n once=%#v\ntwice=%#v", once, twice)
			}
		})
	}
}

// --- Coverage: paths correct but previously untested (per review) ---

// TestSwitchBodyNormalized proves SwitchMarkup case AND default bodies are walked.
func TestSwitchBodyNormalized(t *testing.T) {
	f := parse(t, "{ switch n {\ncase 1:\n  <p>one   {n}</p>\ndefault:\n  <p>many   {n}</p>\n} }")
	Normalize(f)
	got := collectText(f)
	// Each branch's indentation drops; "one   "/"many   " collapse to one space.
	want := []string{"one ", "many "}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

// TestCondAttrNestedSlotNormalized proves normalizeAttrs recurses a CondAttr's
// branches to reach a MarkupAttr slot, which is then normalized.
func TestCondAttrNestedSlotNormalized(t *testing.T) {
	f := parse(t, "<Panel { if on { header={ <h1>\n  Hi \n</h1> } } }/>")
	Normalize(f)
	got := collectText(f)
	want := []string{"Hi"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

// TestPreserveTagUppercase proves isPreserveTag is case-insensitive (<PRE>).
func TestPreserveTagUppercase(t *testing.T) {
	f := parse(t, "<PRE>  a\n  b</PRE>")
	Normalize(f)
	got := collectText(f)
	want := []string{"  a\n  b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

func TestStyleInterpPreserved(t *testing.T) {
	// A <style> body with an interp must pass through wsnorm untouched: Text
	// verbatim, Interp intact.
	src := "package p\n\ncomponent C(w int) {\n\t<style>.a {\n\t\twidth: @{ w }px;\n\t}</style>\n}\n"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "t.gsx", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	Normalize(f)
	style := f.Decls[0].(*ast.Component).Body[0].(*ast.Element)
	var sawInterp bool
	for _, c := range style.Children {
		switch v := c.(type) {
		case *ast.Text:
			if v.Value == "" {
				t.Errorf("Normalize produced empty Text node in <style> body")
			}
		case *ast.Interp:
			sawInterp = true
			if v.Expr != "w" {
				t.Fatalf("interp Expr = %q, want w", v.Expr)
			}
		}
	}
	if !sawInterp {
		t.Fatal("interp was lost from <style> body after Normalize")
	}
	// The leading whitespace inside <style> must be preserved (not collapsed).
	if txt, ok := style.Children[0].(*ast.Text); !ok || txt.Value != ".a {\n\t\twidth: " {
		t.Fatalf("child0 = %#v, want verbatim leading CSS text", style.Children[0])
	}
}

// --- ast.GoWithElements (elements embedded in Go-expression position) ---

// TestGoWithElementsNormalized confirms an element sitting in Go-expression
// position (e.g. `var help = <div>...</div>`, parsed as *ast.GoWithElements,
// not *ast.Component) gets the SAME JSX whitespace collapsing as a component
// body element: cosmetic block indentation is dropped, inline runs collapse
// to a single space. Before this case existed, Normalize only walked
// f.Decls for *ast.Component, silently skipping GoWithElements — a gap that
// left an embedded element's Text children un-normalized (see fmt's
// idempotence contract in internal/printer).
func TestGoWithElementsNormalized(t *testing.T) {
	src := "package p\n\nvar help = <div>\n  <p>a</p>\n  <span>b</span>\n</div>\n"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "t.gsx", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	Normalize(f)
	got := collectText(f)
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

// TestGoWithElementsPreserveTag confirms the preserve-tags rule (pre/textarea/
// script/style keep whitespace verbatim) also applies inside a
// GoWithElements-embedded element, not just inside a component body.
func TestGoWithElementsPreserveTag(t *testing.T) {
	src := "package p\n\nvar help = <pre>  a\n  b</pre>\n"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "t.gsx", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	Normalize(f)
	got := collectText(f)
	want := []string{"  a\n  b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

// TestNormalizeFragmentExpressionValue confirms a fragment sitting in
// Go-expression position (e.g. `var x = <>...</>`, parsed as a
// *ast.Fragment Go part of a GoWithElements decl, not a Component body) gets
// the SAME JSX whitespace collapsing as a fragment in a component body: its
// inter-element cosmetic whitespace text nodes normalize identically either
// way. Before the GoWithElements branch's type switch grew a *ast.Fragment
// case, this fragment's children were never normalized at all (the branch
// only matched *ast.Element), leaving a real fmt idempotence bug for
// expression-position fragments.
func TestNormalizeFragmentExpressionValue(t *testing.T) {
	exprSrc := "package p\n\nvar x = <>  <a>{ v }</a>  </>\n"
	fset := token.NewFileSet()
	exprFile, err := parser.ParseFile(fset, "t.gsx", exprSrc, 0)
	if err != nil {
		t.Fatal(err)
	}
	Normalize(exprFile)
	got := collectText(exprFile)

	bodyFile := parse(t, "<>  <a>{ v }</a>  </>")
	Normalize(bodyFile)
	want := collectText(bodyFile)

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expression-position fragment text nodes = %#v, want (same as component-body fragment) %#v", got, want)
	}
	// The wrapping whitespace around <a>{ v }</a> is all-whitespace with no
	// newline, so it collapses to a single inline space rather than being
	// dropped outright — same rule normalizeText applies everywhere else.
	// (The `{ v }` interpolation itself is an *ast.Interp, not *ast.Text, so
	// it never shows up in collectText's output.)
	wantText := []string{" ", " "}
	if !reflect.DeepEqual(got, wantText) {
		t.Fatalf("text nodes = %#v, want %#v", got, wantText)
	}
}
