package wsnorm_test

import (
	"go/token"
	"reflect"
	"testing"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/wsnorm"
	"github.com/gsxhq/gsx/parser"
)

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
	wsnorm.Normalize(f)
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
	wsnorm.Normalize(f)
	got := collectText(f)
	want := []string{"a ", "y"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

func TestNormalizeRootProseBoundary(t *testing.T) {
	// Root prose has the same inferred separator as text inside an element.
	// The trailing newline before the component closing brace remains layout.
	f := parse(t, "<b>x</b>\ny")
	wsnorm.Normalize(f)
	got := collectText(f)
	want := []string{"x", " y"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

func TestNormalizeTrailingInlineBeforeInterp(t *testing.T) {
	f := parse(t, "Hello,   {name}")
	wsnorm.Normalize(f)
	got := collectText(f)
	want := []string{"Hello, "}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

// --- Preserve contexts ---

func TestPreservePre(t *testing.T) {
	f := parse(t, "<pre>  a\n  b</pre>")
	wsnorm.Normalize(f)
	got := collectText(f)
	want := []string{"  a\n  b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

func TestPreserveTextarea(t *testing.T) {
	f := parse(t, "<textarea>\n x \n</textarea>")
	wsnorm.Normalize(f)
	got := collectText(f)
	want := []string{"\n x \n"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

func TestPreserveScript(t *testing.T) {
	f := parse(t, "<script>\n let x=1;\n</script>")
	wsnorm.Normalize(f)
	got := collectText(f)
	want := []string{"\n let x=1;\n"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

func TestPreserveStyle(t *testing.T) {
	f := parse(t, "<style>\n a{}\n</style>")
	wsnorm.Normalize(f)
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
	wsnorm.Normalize(f)
	got := collectText(f)
	want := []string{"\n  ", "\n    x\n  ", "\n"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

// --- MarkupAttr slot ---

func TestMarkupAttrSlotNormalized(t *testing.T) {
	f := parse(t, "<Panel header={ <h1>\n  Hi \n</h1> }/>")
	wsnorm.Normalize(f)
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
	wsnorm.Normalize(f)
	got := collectText(f)
	want := []string{"  a\n  b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

// --- MarkerRegion ---

// TestNormalizeMarkerRegionChildrenNormalized mirrors
// TestNormalizeBlockIndentationRemoved but with a `<?start …> … <?end>`
// MarkerRegion wrapper instead of a <div>: proves normalizeMarkup's
// *ast.MarkerRegion case (added alongside the *ast.Fragment case it sits
// next to) walks Children the same way a Fragment/Element does. Before that
// case existed, a region's children fell through to the untouched default
// case, so the source indentation around a region's block-level children
// rendered LITERALLY into the HTML instead of collapsing like every other
// non-preserved children list.
func TestNormalizeMarkerRegionChildrenNormalized(t *testing.T) {
	f := parse(t, "<?start name=\"x\">\n  <p>a</p>\n  <span>b</span>\n<?end>")
	wsnorm.Normalize(f)
	got := collectText(f)
	// "a" and "b" survive; all indentation Text dropped — same result as the
	// <div> case.
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

// --- Control flow ---

func TestControlFlowForBodyNormalized(t *testing.T) {
	// Inside <li>: the parser yields Text "\n " before {x} and " \n" after it;
	// both are all-whitespace-with-newline → dropped (cosmetic indentation).
	f := parse(t, "{ for _, x := range xs {\n  <li>\n {x} \n</li>\n} }")
	wsnorm.Normalize(f)
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
	wsnorm.Normalize(f)
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
		"<?start name=\"x\">\n  <p>a</p>\n  <span>b</span>\n<?end>",
	}
	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			f := parse(t, body)
			wsnorm.Normalize(f)
			once := collectText(f)
			wsnorm.Normalize(f)
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
	wsnorm.Normalize(f)
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
	wsnorm.Normalize(f)
	got := collectText(f)
	want := []string{"Hi"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

// TestPreserveTagUppercase proves IsPreserveTag is case-insensitive (<PRE>).
func TestPreserveTagUppercase(t *testing.T) {
	f := parse(t, "<PRE>  a\n  b</PRE>")
	wsnorm.Normalize(f)
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
	wsnorm.Normalize(f)
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
// not *ast.Component) gets the same source whitespace normalization as a component
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
	wsnorm.Normalize(f)
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
	wsnorm.Normalize(f)
	got := collectText(f)
	want := []string{"  a\n  b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text nodes = %#v, want %#v", got, want)
	}
}

// TestNormalizeFragmentExpressionValue confirms a fragment sitting in
// Go-expression position (e.g. `var x = <>...</>`, parsed as a
// *ast.Fragment Go part of a GoWithElements decl, not a Component body) gets
// the same source whitespace normalization as a fragment in a component body: its
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
	wsnorm.Normalize(exprFile)
	got := collectText(exprFile)

	bodyFile := parse(t, "<>  <a>{ v }</a>  </>")
	wsnorm.Normalize(bodyFile)
	want := collectText(bodyFile)

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expression-position fragment text nodes = %#v, want (same as component-body fragment) %#v", got, want)
	}
	// The wrapping whitespace around <a>{ v }</a> is all-whitespace with no
	// newline, so it collapses to a single inline space rather than being
	// dropped outright — same source normalization rule applies everywhere else.
	// (The `{ v }` interpolation itself is an *ast.Interp, not *ast.Text, so
	// it never shows up in collectText's output.)
	wantText := []string{" ", " "}
	if !reflect.DeepEqual(got, wantText) {
		t.Fatalf("text nodes = %#v, want %#v", got, wantText)
	}
}

func TestNormalizeProseBoundaryRules(t *testing.T) {
	cases := []struct {
		name, body string
		want       []string
	}{
		{"component prose", "<p>Hello\n<Link>world</Link></p>", []string{"Hello ", "world"}},
		{"image between prose", "<p>Read\n<img/>\nmore</p>", []string{"Read ", " more"}},
		{"block between prose", "<p>Read\n<div>box</div>\nmore</p>", []string{"Read ", "box", " more"}},
		{"empty fragment", "<p>Hello\n<></>!</p>", []string{"Hello ", "!"}},
		{"empty expression", "<p>Hello\n{\"\"}!</p>", []string{"Hello ", "!"}},
		{"explicit expression spaces", "<p>{\"  \"}\n%</p>", []string{" %"}},
		{"outer newlines", "<p>\n hello\n</p>", []string{"hello"}},
		{"initial comment", "<p>{/* comment */}\nhello</p>", []string{"hello"}},
		{"final comment", "<p>hello\n{/* comment */}</p>", []string{"hello"}},
		{"button prose", "<p>Click\n<button>here</button></p>", []string{"Click ", "here"}},
		{"label prose", "<p>Click\n<label>here</label></p>", []string{"Click ", "here"}},
		{"output prose", "<p>Read\n<output>value</output></p>", []string{"Read ", "value"}},
		{"map prose", "<p>Read\n<map>value</map></p>", []string{"Read ", "value"}},
		{"empty button", "<p>Click\n<button/></p>", []string{"Click "}},
		{"input sibling", "<p>Click\n<input/></p>", []string{"Click "}},
		{"image sibling", "<p>Click\n<img/></p>", []string{"Click "}},
		{"svg sibling", "<p>Click\n<svg>icon</svg></p>", []string{"Click ", "icon"}},
		{"fragment sibling with branch", "<p>Hello\n<>{ if ok { <b>…</b> } }<div>block</div></><i>word</i></p>", []string{"Hello ", "…", "block", "word"}},
		{"fragment sibling with explicit branch space", "<p>Hello\n<>{ if ok { {\" \"} } }<div>block</div></><i>word</i></p>", []string{"Hello ", "block", "word"}},
		{"joined percent", "<p>{value}%</p>", []string{"%"}},
		{"joined punctuation", "<p>Hello<em>world</em>!</p>", []string{"Hello", "world", "!"}},
		{"text before tag", "<p>Hello\n <b>world</b></p>", []string{"Hello ", "world"}},
		{"sibling layout", "<p><span>A</span>\n<span>B</span></p>", []string{"A", "B"}},
		{"percent", "<p>{value}\n %</p>", []string{" %"}},
		{"text after tag", "<p><b>x</b>\ny</p>", []string{"x", " y"}},
		{"ellipsis", "<p>{value}\n …</p>", []string{" …"}},
		{"ascii ellipsis", "<p>{value}\n ...</p>", []string{" ..."}},
		{"entity gap internal", "<p>a&nbsp;\n b</p>", []string{"a&nbsp; b"}},
		{"entity gap split", "<p>a&nbsp;\n{\"b\"}</p>", []string{"a&nbsp; "}},
		{"expression gap split", "<p>{\"a\\u00a0\"}\n b</p>", []string{" b"}},
		{"entity gap on right", "<p>{\"a\"}\n &nbsp;b</p>", []string{" &nbsp;b"}},
		{"explicit space beside entity", "<p>a&nbsp; {\"b\"}</p>", []string{"a&nbsp; "}},
		{"explicit inline", "<p>{value} %</p>", []string{" %"}},
		{"comment-separated newline node", "<p>Hello{/* first */}\n{/* second */}world</p>", []string{"Hello", " ", "world"}},
		{"comment-separated inline node", "<p>Hello{/* first */} {/* second */}world</p>", []string{"Hello", " ", "world"}},
		{"inline run across comment", "<p>a {/* comment */} b</p>", []string{"a ", "b"}},
		{"newline run across comment", "<p>a\n{/* comment */}\n b</p>", []string{"a ", "b"}},
		{"multiple comment gaps", "<p>a{/* first */} \n {/* second */} b</p>", []string{"a", " ", "b"}},
		{"comment tag layout", "<p><b>x</b>{/* first */}\n{/* second */}<i>y</i></p>", []string{"x", "y"}},
		{"comment expression layout", "<p>{value}{/* first */}\n{/* second */}{value}</p>", nil},
		{"outer comment indentation", "<p>\n{/* comment */} a </p>", []string{"a "}},
		{"trailing comment newline", "<p>a {/* comment */}\n</p>", []string{"a"}},
		{"html comment first gap", "<p>Hello<!-- first -->\n<!-- second -->world</p>", []string{"Hello", " ", "world"}},
		{"go first gap", "<p>Hello{{ _ = value }}\nworld</p>", []string{"Hello", " world"}},
		{"comments", "<p>Hello\n{/* comment */}\nworld</p>", []string{"Hello ", "world"}},
		{"punctuation alternatives", "<p>Hello\n{ if ok { . } else { ! } }</p>", []string{"Hello ", ".", "!"}},
		{"opening alternatives", "<p>{ if ok { ( } else { [ } }\nworld</p>", []string{"(", "[", " world"}},
		{"optional punctuation", "<p>Hello\n{ if ok { . } }!</p>", []string{"Hello ", ".", "!"}},
		{"branch word", "<p>Hello\n{ if ok { word } else { ! } }</p>", []string{"Hello ", "word", "!"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := parse(t, c.body)
			wsnorm.Normalize(f)
			if got := collectText(f); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %#v, want %#v", got, c.want)
			}
			wsnorm.Normalize(f)
			if got := collectText(f); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("second pass %#v, want %#v", got, c.want)
			}
		})
	}
}

func TestNormalizeBranchContentOutcomes(t *testing.T) {
	values := []string{".", "!", "…", "%", "word", "(", "&nbsp;"}
	for _, a := range values {
		for _, b := range values {
			t.Run(a+"/"+b, func(t *testing.T) {
				f := parse(t, "<p>Hello\n{ if ok { "+a+" } else { "+b+" } }</p>")
				wsnorm.Normalize(f)
				want := "Hello "
				if got := collectText(f)[0]; got != want {
					t.Fatalf("first text %q want %q", got, want)
				}
				once := collectText(f)
				wsnorm.Normalize(f)
				if twice := collectText(f); !reflect.DeepEqual(once, twice) {
					t.Fatalf("not idempotent: %#v -> %#v", once, twice)
				}
			})
		}
	}
}
