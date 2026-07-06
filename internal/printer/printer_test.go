package printer

import (
	"bytes"
	"go/token"
	"reflect"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/wsnorm"
	"github.com/gsxhq/gsx/parser"
)

// fmtSource parses, normalizes, and prints src, returning the canonical output.
func fmtSource(t *testing.T, src string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "test.gsx", src, 0)
	if err != nil {
		t.Fatalf("parse error: %v\nsrc:\n%s", err, src)
	}
	wsnorm.Normalize(f)
	var b strings.Builder
	if err := Fprint(&b, f, 80); err != nil {
		t.Fatalf("Fprint error: %v", err)
	}
	return b.String()
}

// checkFormat asserts the canonical output equals want, and that printing is
// idempotent (re-parse + Normalize + Fprint of the output is byte-identical).
func checkFormat(t *testing.T, src, want string) {
	t.Helper()
	got := fmtSource(t, src)
	if got != want {
		t.Errorf("format mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	// Idempotence: formatting the output again must be byte-identical.
	again := fmtSource(t, got)
	if again != got {
		t.Errorf("not idempotent:\n--- pass1 ---\n%s\n--- pass2 ---\n%s", got, again)
	}
}

func assertFormat(t *testing.T, src, want string) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "c.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	wsnorm.Normalize(f)
	var b bytes.Buffer
	if err := Fprint(&b, f, 80); err != nil {
		t.Fatalf("print: %v", err)
	}
	got := b.String()
	if got != want {
		t.Fatalf("format mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	// Faithfulness: formatting must not change the normalized AST.
	if !reflect.DeepEqual(normalizedAST(t, src), normalizedAST(t, want)) {
		t.Fatalf("assertFormat: formatting changed the normalized AST (not faithful)\nsrc:\n%s\nwant:\n%s", src, want)
	}
	// Idempotence: formatting want again must yield want.
	again := fmtSource(t, want)
	if again != want {
		t.Fatalf("assertFormat: not idempotent:\n--- want ---\n%s\n--- again ---\n%s", want, again)
	}
}

func TestElementBlock(t *testing.T) {
	// Block-level children always break to show hierarchy — structural breaking.
	src := `package p
component C() {
	<div>
		<p>a</p>
		<span>b</span>
	</div>
}`
	want := `package p

component C() {
	<div>
		<p>a</p>
		<span>b</span>
	</div>
}
`
	checkFormat(t, src, want)
}

func TestSpreadPipelineParenthesized(t *testing.T) {
	// A piped spread prints parenthesized so the trailing `...` reads as the spread
	// marker on the whole pipeline. The bare input canonicalizes to the
	// parenthesized form, and re-formatting is idempotent — which also proves the
	// parser round-trips the parenthesized form back to the same seed + stages.
	src := `package p
component C(extra gsx.Attrs) {
	<div { extra |> withTitle("hi")... }></div>
}`
	want := `package p

component C(extra gsx.Attrs) {
	<div { (extra |> withTitle("hi"))... }></div>
}
`
	checkFormat(t, src, want)
}

func TestElementInlineOnlyInterp(t *testing.T) {
	src := `package p
component C() {
	<title>{title}</title>
}`
	want := `package p

component C() {
	<title>{ title }</title>
}
`
	checkFormat(t, src, want)
}

func TestElementInlineTextAndElement(t *testing.T) {
	// Text + element child: the segment is glued (edge-safe) and breaks to show
	// the block child; all nodes stay on one indented line together.
	src := `package p
component C() {
	<p>a <b>x</b> b</p>
}`
	want := `package p

component C() {
	<p>
		a <b>x</b> b
	</p>
}
`
	checkFormat(t, src, want)
}

func TestVoidElement(t *testing.T) {
	src := `package p
component C() {
	<br/>
	<img src="x.png"/>
}`
	want := `package p

component C() {
	<br/>
	<img src="x.png"/>
}
`
	// The outer body has two void elements (block-level, no Text) → block.
	checkFormat(t, src, want)
}

func TestInterpPipeline(t *testing.T) {
	src := `package p
component C() {
	<div>{ items |> reverse |> take(3) }</div>
}`
	want := `package p

component C() {
	<div>{ items |> reverse |> take(3) }</div>
}
`
	checkFormat(t, src, want)
}

func TestAttrKinds(t *testing.T) {
	// Five attrs total 85 chars flat — exceeds 76 remaining at depth 1 — so the
	// opening-tag group breaks. When the tag breaks, children also break to their
	// own indented line. Output is faithful+idempotent.
	src := `package p
component C() {
	<div id="main" hidden class={ "card", "active": isActive } data-x={ val } { rest... }>{children}</div>
}`
	want := `package p

component C() {
	<div
		id="main"
		hidden
		class={ "card", "active": isActive }
		data-x={val}
		{ rest... }
	>
		{ children }
	</div>
}
`
	checkFormat(t, src, want)
}

func TestValueSwitchPrintsUnbracedCaseValues(t *testing.T) {
	src := `package p
component C(v int) {
	<div class={ switch v { case 1: "green" default: "gray" } }>x</div>
}`
	want := `package p

component C(v int) {
	<div class={ switch v { case 1: "green" default: "gray" } }>x</div>
}
`
	checkFormat(t, src, want)
}

func TestValueSwitchBreaksWhenOverWidth(t *testing.T) {
	src := `package p
component C(v int) {
	<div class={ switch v { case 1: "green-green-green-green-green-green-green" default: "gray-gray-gray-gray-gray-gray-gray" } }>x</div>
}`
	want := `package p

component C(v int) {
	<div
		class={
			switch v {
			case 1:
				"green-green-green-green-green-green-green"
			default:
				"gray-gray-gray-gray-gray-gray-gray"
			}
		}
	>
		x
	</div>
}
`
	checkFormat(t, src, want)
}

func TestCondAttr(t *testing.T) {
	// A CondAttr always forces the opening-tag group to break (BreakParent inside
	// attrDoc for CondAttr). When the tag breaks, children also break to their own
	// indented line. Output is faithful+idempotent.
	src := `package p
component C() {
	<div { if active { class="on" } else { class="off" } }>x</div>
}`
	want := `package p

component C() {
	<div
		{ if active {
			class="on"
		} else {
			class="off"
		} }
	>
		x
	</div>
}
`
	checkFormat(t, src, want)
}

func TestMarkupAttr(t *testing.T) {
	src := `package p
component C() {
	<Panel header={ <h1>Hi</h1> }>x</Panel>
}`
	want := `package p

component C() {
	<Panel header={ <h1>Hi</h1> }>x</Panel>
}
`
	checkFormat(t, src, want)
}

func TestJSAttr(t *testing.T) {
	// Quoted attrs that contain @{ } are plain static attrs.
	src := `package p
component C(tab string) {
	<div x-data="{ tab: @{ tab }, open: false }">x</div>
}`
	want := `package p

component C(tab string) {
	<div x-data="{ tab: @{ tab }, open: false }">x</div>
}
`
	checkFormat(t, src, want)
}

func TestEmbeddedAttrLiteral(t *testing.T) {
	src := `package p
component C(tab string) {
	<div x-data=js` + "`" + `{ tab: @{tab}, open: false }` + "`" + ` style=css` + "`" + `color:@{tab}` + "`" + `>x</div>
}`
	want := `package p

component C(tab string) {
	<div x-data=js` + "`" + `{ tab: @{tab}, open: false }` + "`" + ` style=css` + "`" + `color:@{tab}` + "`" + `>x</div>
}
`
	checkFormat(t, src, want)
}

func TestEmbeddedAttrDirectOptionalBraceLiteral(t *testing.T) {
	src := `package p
component C(id string) {
	<button @click=js` + "`" + `save(@{ id })` + "`" + `>Save</button>
}`
	want := `package p

component C(id string) {
	<button @click=js` + "`" + `save(@{id})` + "`" + `>Save</button>
}
`
	checkFormat(t, src, want)
}

func TestEmbeddedAttrBracedOptionalBraceLiteral(t *testing.T) {
	src := `package p
component C(id string) {
	<button @click={js` + "`" + `save(@{ id })` + "`" + `}>Save</button>
}`
	want := `package p

component C(id string) {
	<button @click=js` + "`" + `save(@{id})` + "`" + `>Save</button>
}
`
	checkFormat(t, src, want)
}

func TestEmbeddedAttrMultilinePreservesBody(t *testing.T) {
	src := `package p
component C(open bool) {
	<div x-data=js` + "`" + `
		{ open: @{ open } }
	` + "`" + `>x</div>
}`
	want := `package p

component C(open bool) {
	<div x-data=js` + "`" + `
		{ open: @{open} }
	` + "`" + `>x</div>
}
`
	checkFormat(t, src, want)
}

func TestEmbeddedAttrEscapedBacktick(t *testing.T) {
	src := `package p
component C() {
	<div data-x=js` + "`" + `a\` + "`" + `b` + "`" + `>x</div>
}`
	want := `package p

component C() {
	<div data-x=js` + "`" + `a\` + "`" + `b` + "`" + `>x</div>
}
`
	checkFormat(t, src, want)
}

func TestComposedStyleCSSLiteralPart(t *testing.T) {
	src := `package p
component C(color string, hidden bool) {
	<div style={ "display:none": hidden, css` + "`" + `color:@{ color };content:"\` + "`" + `"` + "`" + ` }>x</div>
}`
	want := `package p

component C(color string, hidden bool) {
	<div style={ "display:none": hidden, css` + "`" + `color:@{color};content:"\` + "`" + `"` + "`" + ` }>
		x
	</div>
}
`
	checkFormat(t, src, want)
}

func TestEmbeddedTextLiteralNoLangPrefix(t *testing.T) {
	if got := embeddedLangName(ast.EmbeddedText); got != "" {
		t.Fatalf("embeddedLangName(ast.EmbeddedText) = %q, want empty string", got)
	}
	if got := embeddedLangName(ast.EmbeddedJS); got != "js" {
		t.Fatalf("embeddedLangName(ast.EmbeddedJS) = %q, want \"js\"", got)
	}
	if got := embeddedLangName(ast.EmbeddedCSS); got != "css" {
		t.Fatalf("embeddedLangName(ast.EmbeddedCSS) = %q, want \"css\"", got)
	}
}

func TestEmbeddedTextLiteralRoundTrip(t *testing.T) {
	// Bare-backtick EmbeddedText: no lang prefix, and a literal `@{` in the
	// source (escaped as `\@{`) must round-trip as literal text rather than
	// turning into a hole.
	src := `package p
component C(v string) {
	<span class=` + "`" + `badge-@{v} \@{lit}` + "`" + `>x</span>
}`
	want := `package p

component C(v string) {
	<span class=` + "`" + `badge-@{v} \@{lit}` + "`" + `>x</span>
}
`
	checkFormat(t, src, want)
}

func TestBodyEmbeddedInterpRoundTrip(t *testing.T) {
	// A body backtick literal `{`…@{expr}…`}` prints in its literal form
	// (preserved, not canonicalized to interleaved Text/Interp nodes).
	src := `package p
component C(id string, n int) {
	<p>{` + "`" + `row-@{ id }-@{n}` + "`" + `}</p>
}`
	want := `package p

component C(id string, n int) {
	<p>{` + "`" + `row-@{id}-@{n}` + "`" + `}</p>
}
`
	checkFormat(t, src, want)
}

func TestBodyEmbeddedInterpWholePipeRoundTrip(t *testing.T) {
	// A whole-literal pipeline after the closing backtick prints trailing
	// ` |> stage`.
	src := `package p
component C(id string) {
	<p>{` + "`" + `row-@{ id }` + "`" + ` |> upper}</p>
}`
	want := `package p

component C(id string) {
	<p>{` + "`" + `row-@{id}` + "`" + ` |> upper}</p>
}
`
	checkFormat(t, src, want)
}

func TestBodyEmbeddedInterpEscapedHoleRoundTrip(t *testing.T) {
	// A literal `\@{` inside a body embedded literal re-escapes and round-trips
	// as literal text rather than turning into a hole.
	src := `package p
component C(id string) {
	<p>{` + "`" + `row-@{id} \@{lit}` + "`" + `}</p>
}`
	want := `package p

component C(id string) {
	<p>{` + "`" + `row-@{id} \@{lit}` + "`" + `}</p>
}
`
	checkFormat(t, src, want)
}

func TestAttrEmbeddedWholePipeBracedRoundTrip(t *testing.T) {
	// A whole-literal pipeline on a braced embedded attribute value must keep
	// its braces on print — the direct/unbraced attr=`…` form never parses a
	// trailing `|>`, so dropping the braces would make the output unparseable.
	src := `package p
component C(v string) {
	<div class={` + "`" + `btn-@{ v }` + "`" + ` |> upper}>x</div>
}`
	want := `package p

component C(v string) {
	<div class={` + "`" + `btn-@{v}` + "`" + ` |> upper}>x</div>
}
`
	checkFormat(t, src, want)
}

func TestIfElseIfElse(t *testing.T) {
	// An if-else-if-else with block children in each arm: the if body has
	// block-level children so each arm breaks, and the containing <div>
	// shows hierarchy too.
	src := `package p
component C() {
	<div>
		{ if a { <p>A</p> } else if b { <p>B</p> } else { <p>C</p> } }
	</div>
}`
	want := `package p

component C() {
	<div>
		{ if a {
			<p>A</p>
		} else if b {
			<p>B</p>
		} else {
			<p>C</p>
		} }
	</div>
}
`
	checkFormat(t, src, want)
}

func TestForMarkup(t *testing.T) {
	// A for-range body with a block-level child always breaks to show hierarchy.
	// The containing <ul> also breaks because it has a block-level child (the for).
	src := `package p
component C() {
	<ul>
		{ for _, it := range items { <li>{it.Name}</li> } }
	</ul>
}`
	want := `package p

component C() {
	<ul>
		{ for _, it := range items {
			<li>{ it.Name }</li>
		} }
	</ul>
}
`
	checkFormat(t, src, want)
}

func TestSwitchMarkup(t *testing.T) {
	// Switch arms with block-level children always break to show hierarchy.
	// The containing <div> also breaks because it has a block-level child (switch).
	src := `package p
component C() {
	<div>
		{ switch kind {
		case "a":
			<p>A</p>
		default:
			<p>D</p>
		} }
	</div>
}`
	want := `package p

component C() {
	<div>
		{ switch kind {
			case "a":
				<p>A</p>
			default:
				<p>D</p>
		} }
	</div>
}
`
	checkFormat(t, src, want)
}

func TestFragment(t *testing.T) {
	// Two short paragraph children fit on one line: fragment Group collapses.
	src := `package p
component C() {
	<>
		<p>a</p>
		<p>b</p>
	</>
}`
	want := `package p

component C() {
	<><p>a</p><p>b</p></>
}
`
	checkFormat(t, src, want)
}

func TestGoBlock(t *testing.T) {
	src := `package p
component C() {
	{{ heading:="Reports" }}
	<h1>{heading}</h1>
}`
	want := `package p

component C() {
	{{ heading := "Reports" }}
	<h1>{ heading }</h1>
}
`
	checkFormat(t, src, want)
}

func TestGoChunk(t *testing.T) {
	src := `package p

import "fmt"

type Item struct{ ID, Name string }

component C() {
	<p>{fmt.Sprint(1)}</p>
}`
	want := `package p

import "fmt"

type Item struct{ ID, Name string }

component C() {
	<p>{ fmt.Sprint(1) }</p>
}
`
	checkFormat(t, src, want)
}

func TestComponentRecvParams(t *testing.T) {
	src := `package p
component (page UsersPage) Render(title string,featured bool) {
	<h1>{title}</h1>
}`
	want := `package p

component (page UsersPage) Render(title string, featured bool) {
	<h1>{ title }</h1>
}
`
	checkFormat(t, src, want)
}

func TestComponentTypeParams(t *testing.T) {
	src := `package p
component EditCheckbox[T bool|pgtype.Bool](value T) {
	<input checked={value}/>
}
component (page Page) Field[T any](value T) {
	<EditCheckbox[pgtype.Bool] checked={value} />
}`
	want := `package p

component EditCheckbox[T bool | pgtype.Bool](value T) {
	<input checked={value}/>
}

component (page Page) Field[T any](value T) {
	<EditCheckbox[pgtype.Bool] checked={value}/>
}
`
	checkFormat(t, src, want)
}

func TestPreVerbatim(t *testing.T) {
	src := "package p\ncomponent C() {\n\t<pre>  line1\n    line2  </pre>\n}"
	want := "package p\n\ncomponent C() {\n\t<pre>  line1\n    line2  </pre>\n}\n"
	checkFormat(t, src, want)
}

func TestTextareaVerbatim(t *testing.T) {
	src := "package p\ncomponent C() {\n\t<textarea>  hi  </textarea>\n}"
	want := "package p\n\ncomponent C() {\n\t<textarea>  hi  </textarea>\n}\n"
	checkFormat(t, src, want)
}

func TestNestedBlockInline(t *testing.T) {
	// Outer block has block-level children (two <p> elements) → always breaks.
	// Inner <p> with text+element glued segment also breaks to show its hierarchy.
	src := `package p
component C() {
	<div>
		<p>a <b>x</b> b</p>
		<p>plain</p>
	</div>
}`
	want := `package p

component C() {
	<div>
		<p>
			a <b>x</b> b
		</p>
		<p>plain</p>
	</div>
}
`
	checkFormat(t, src, want)
}

func TestDoctypeAndComment(t *testing.T) {
	src := `package p
component C() {
	<!DOCTYPE html>
	<!-- hi -->
	<div>x</div>
}`
	want := `package p

component C() {
	<!DOCTYPE html>
	<!-- hi -->
	<div>x</div>
}
`
	checkFormat(t, src, want)
}

func TestNullaryStaysEmpty(t *testing.T) {
	got := fmtSource(t, "package p\ncomponent C() {\n\t<br/>\n}")
	if !strings.Contains(got, "component C() {") {
		t.Errorf("nullary () not preserved:\n%s", got)
	}
}

func TestStyleInterpFormat(t *testing.T) {
	src := "package p\n\ncomponent C(w int) {\n\t<style>.a{width:@{ w }px}</style>\n}\n"
	// CSS is re-indented only (no reflow): a minified one-liner stays one line.
	want := "package p\n\ncomponent C(w int) {\n\t<style>\n\t\t.a{width:@{ w }px}\n\t</style>\n}\n"
	checkFormat(t, src, want)
}

func TestStyleInterpFormatPreservesPipeline(t *testing.T) {
	// @{ x |> upper } in a <style> block must round-trip exactly — the printer
	// must not silently discard pipeline stages.
	// CSS is re-indented only (no reflow): a minified one-liner stays one line.
	src := "package p\n\ncomponent C(x string) {\n\t<style>.a{color:@{ x |> upper }}</style>\n}\n"
	want := "package p\n\ncomponent C(x string) {\n\t<style>\n\t\t.a{color:@{ x |> upper }}\n\t</style>\n}\n"
	checkFormat(t, src, want)
}

func TestBlockBreaksMixedTextControlFlow(t *testing.T) {
	// The reported bug: a long <p> with text + interp + an if must break at the
	// safe boundary (Interp|IfMarkup), keeping "· <a>…</a>" glued by its space.
	// Canonical output: interp content gains spaces ({ expr }), ExprAttr has none
	// ({expr}), and the if-body breaks because the flat rendering exceeds 80 cols.
	src := `package p
component C() {
	<p class="text-sm text-slate-500">
		by {props.Author.Username}
		{ if props.Category.Slug != "" {
			· <a class="hover:underline" href={ categoryPage{} |> url("slug", props.Category.Slug) }>{props.Category.Name}</a>
		} }
	</p>
}`
	want := `package p

component C() {
	<p class="text-sm text-slate-500">
		by { props.Author.Username }
		{ if props.Category.Slug != "" {
			· <a
				class="hover:underline"
				href={categoryPage{} |> url("slug", props.Category.Slug)}
			>
				{ props.Category.Name }
			</a>
		} }
	</p>
}
`
	assertFormat(t, src, want)
}

func TestCfBodyEdgeUnsafeStaysFaithful(t *testing.T) {
	// A control-flow body that is a single space-bearing Text must stay flat
	// even when the enclosing if is long: breaking would absorb the significant
	// leading/trailing spaces and change the normalized AST.
	src := `package p
component C() {
	{ if veryLongConditionNameThatWouldForceTheEnclosingGroupToBreakAcrossEightyColumns { some text } }
}`
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatalf("print: %v", err)
	}
	// Faithful: formatting must not change the normalized AST.
	if !reflect.DeepEqual(normalizedAST(t, src), normalizedAST(t, out)) {
		t.Fatalf("cfBody break changed normalized AST (unfaithful):\n%s", out)
	}
	// Idempotent.
	out2, err := normPrint(t, out)
	if err != nil {
		t.Fatalf("reprint: %v", err)
	}
	if out != out2 {
		t.Fatalf("not idempotent:\n--- once ---\n%s\n--- twice ---\n%s", out, out2)
	}
}

func TestBlockShowsHierarchy(t *testing.T) {
	// A block-containing element always breaks to show hierarchy regardless of
	// whether the content fits within 80 columns — structural breaking.
	src := `package p
component C() {
	<div>
		<p>plain</p>
	</div>
}`
	want := `package p

component C() {
	<div>
		<p>plain</p>
	</div>
}
`
	assertFormat(t, src, want)
}

func TestAttrWrapOnConditionalAttr(t *testing.T) {
	// A CondAttr forces the opening tag to break, one attr per line, > alone;
	// the forced tag-break also forces breakable children onto their own lines.
	// Two Interp children (no space between) form two segments → breakable.
	src := `package p
component C(p Props) {
	<a { if p.ID != "" { id={ p.ID } } } href={ p.Href } class={ buttonClass(p) } { p.Attributes... }>{ children }{ name }</a>
}`
	want := `package p

component C(p Props) {
	<a
		{ if p.ID != "" {
			id={p.ID}
		} }
		href={p.Href}
		class={ buttonClass(p) }
		{ p.Attributes... }
	>
		{ children }
		{ name }
	</a>
}
`
	assertFormat(t, src, want)
}

func TestAttrStayInlineWhenShort(t *testing.T) {
	src := `package p
component C() {
	<a href="/x" class="b">go</a>
}`
	want := `package p

component C() {
	<a href="/x" class="b">go</a>
}
`
	assertFormat(t, src, want)
}

// format80 parses, normalizes, and Fprintfs src at width 80, returning the
// canonical output. Used by comment-fidelity tests that need a bytes.Buffer.
func format80(t *testing.T, src string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "c.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	wsnorm.Normalize(f)
	var b bytes.Buffer
	if err := Fprint(&b, f, 80); err != nil {
		t.Fatalf("print: %v", err)
	}
	return b.String()
}

func TestAttrValueMultilinePreservesComment(t *testing.T) {
	src := `package p
component C(p Props) {
	<p class={ utils.TwMerge(
		// keep this comment
		"text-[0.8rem] font-medium",
		p.Class,
	) }>x</p>
}`
	// The comment must survive, and the long call must stay multi-line.
	got := format80(t, src)
	if !strings.Contains(got, "// keep this comment") {
		t.Fatalf("comment dropped:\n%s", got)
	}
	if !strings.Contains(got, "utils.TwMerge(\n") {
		t.Fatalf("expr not multi-line:\n%s", got)
	}
	// Args must be indented one level deeper than the call (templ-style), not
	// flattened flush with it.
	callIndent := leadingTabs(lineContaining(got, "utils.TwMerge("))
	argIndent := leadingTabs(lineContaining(got, "// keep this comment"))
	if argIndent != callIndent+1 {
		t.Fatalf("args should be one tab deeper than the call: callIndent=%d argIndent=%d\n%s", callIndent, argIndent, got)
	}
	// Idempotence: re-formatting is a fixed point.
	if again := format80(t, got); again != got {
		t.Fatalf("not idempotent:\n--- once ---\n%s\n--- twice ---\n%s", got, again)
	}
}

func lineContaining(s, sub string) string {
	for ln := range strings.SplitSeq(s, "\n") {
		if strings.Contains(ln, sub) {
			return ln
		}
	}
	return ""
}

func leadingTabs(line string) int {
	n := 0
	for n < len(line) && line[n] == '\t' {
		n++
	}
	return n
}

// TestPrintWidthControlsWrap verifies that width governs ATTRIBUTE wrapping:
// a long attribute list (exceeding 80 cols) causes the opening tag to wrap
// with one attribute per line; short attributes stay on one line.
func TestPrintWidthControlsWrap(t *testing.T) {
	// Long attrs: opening tag exceeds 80 cols → attrs wrap, one per line.
	// When the tag wraps, children also break to their own indented line.
	src := `package p
component C() {
	<form action="/submit" method="post" class="space-y-4 mt-6" id="contact-form" novalidate>Submit</form>
}`
	want := `package p

component C() {
	<form
		action="/submit"
		method="post"
		class="space-y-4 mt-6"
		id="contact-form"
		novalidate
	>
		Submit
	</form>
}
`
	assertFormat(t, src, want)

	// Short attrs: stay inline.
	shortSrc := `package p
component C() {
	<a href="/x" class="b">link</a>
}`
	shortGot := fmtSource(t, shortSrc)
	if !strings.Contains(shortGot, `<a href="/x" class="b">link</a>`) {
		t.Fatalf("short attrs should stay inline:\n%s", shortGot)
	}
}

// TestDSButtonAcceptance verifies the ds/button pattern: CondAttr forces the
// opening tag to break (one attr per line); when the tag breaks, children also
// break to their own indented line. Strengthened assertFormat enforces
// faithfulness + idempotence.
func TestDSButtonAcceptance(t *testing.T) {
	src := `package ds

component Button(p Props) {
	<a { if p.ID != "" { id={ p.ID } } } href={ p.Href } class={ buttonClass(p) } { p.Attributes... }>{ children }</a>
}`
	want := `package ds

component Button(p Props) {
	<a
		{ if p.ID != "" {
			id={p.ID}
		} }
		href={p.Href}
		class={ buttonClass(p) }
		{ p.Attributes... }
	>
		{ children }
	</a>
}
`
	assertFormat(t, src, want)
}

// TestDSFormMessageAcceptance verifies the ds/form-message pattern: CondAttr
// forces tag break; a multi-line class value (utils.TwMerge) renders with
// gofmt's own indentation under the ExprAttr; when the tag breaks, children
// also break to their own indented line. Faithfulness + idempotence enforced.
func TestDSFormMessageAcceptance(t *testing.T) {
	src := `package ds

component Message(p MessageProps) {
	<p { if p.ID != "" { id={ p.ID } } } class={ utils.TwMerge(
		"text-[0.8rem] font-medium",
		messageVariantClass(p.Variant),
		p.Class,
	) } { p.Attributes... }>{ children }</p>
}`
	want := `package ds

component Message(p MessageProps) {
	<p
		{ if p.ID != "" {
			id={p.ID}
		} }
		class={
			utils.TwMerge(
				"text-[0.8rem] font-medium",
				messageVariantClass(p.Variant),
				p.Class,
			)
		}
		{ p.Attributes... }
	>
		{ children }
	</p>
}
`
	assertFormat(t, src, want)
}

func TestComponentBodyEdgeUnsafeStaysFaithful(t *testing.T) {
	src := "package p\n\ncomponent C() { foo }"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatalf("normPrint: %v", err)
	}
	want := normalizedAST(t, src)
	got := normalizedAST(t, out)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("not faithful:\n  src=%q\n  out=%q", src, out)
	}
	out2, err := normPrint(t, out)
	if err != nil {
		t.Fatalf("normPrint(out2): %v", err)
	}
	if out != out2 {
		t.Fatalf("not idempotent:\n  out =%q\n  out2=%q", out, out2)
	}
}

func TestSwitchArmEdgeUnsafeStaysFaithful(t *testing.T) {
	src := "package p\n\ncomponent C() {\n{ switch k { case \"a\": foo } }\n}"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatalf("normPrint: %v", err)
	}
	want := normalizedAST(t, src)
	got := normalizedAST(t, out)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("not faithful:\n  src=%q\n  out=%q", src, out)
	}
	out2, err := normPrint(t, out)
	if err != nil {
		t.Fatalf("normPrint(out2): %v", err)
	}
	if out != out2 {
		t.Fatalf("not idempotent:\n  out =%q\n  out2=%q", out, out2)
	}
}

func TestSingleBlockChildBreaks(t *testing.T) {
	// A container whose only child is a block-level `for` must put it on its own
	// line — the `<div>`/`</div>` must not jam onto the for lines.
	src := `package p
component C(props P) {
	<div class="space-y-4">
		{ for _, post := range props.Posts {
			<PostCard p={post}/>
		} }
	</div>
}`
	want := `package p

component C(props P) {
	<div class="space-y-4">
		{ for _, post := range props.Posts {
			<PostCard p={post}/>
		} }
	</div>
}
`
	assertFormat(t, src, want)
}

func TestShortAttrsStayInlineWithLongChildren(t *testing.T) {
	// Short attrs on the opening tag must NOT wrap just because the children are
	// long/multi-line — the attr group decides independently of children.
	src := `package p
component C(props P) {
	<ul class="flex flex-wrap gap-2">
		{ for _, c := range props.Categories {
			<li>{ c.Name }</li>
		} }
	</ul>
}`
	want := `package p

component C(props P) {
	<ul class="flex flex-wrap gap-2">
		{ for _, c := range props.Categories {
			<li>{ c.Name }</li>
		} }
	</ul>
}
`
	assertFormat(t, src, want)
}

func TestDocCommentStaysAttachedToComponent(t *testing.T) {
	// A comment directly above `component` (no blank line in source) is a doc
	// comment and must stay attached — no blank line inserted.
	src := `package p

// Doc comment for C.
component C() {
	<div></div>
}`
	want := `package p

// Doc comment for C.
component C() {
	<div></div>
}
`
	assertFormat(t, src, want)
}

func TestBlankLineBeforeComponentPreserved(t *testing.T) {
	// A blank line between preceding code and `component` in source is kept.
	src := `package p

type T struct{}

component C() {
	<div></div>
}`
	want := `package p

type T struct{}

component C() {
	<div></div>
}
`
	assertFormat(t, src, want)
}

func TestPackageDocCommentPreserved(t *testing.T) {
	// The doc comment above `package` must survive formatting (it was being
	// dropped: the parser discarded everything before the package keyword).
	src := `// Package foo exports the shared widgets.
// Second line of the package doc.
package foo

component C() {
	<div></div>
}`
	want := `// Package foo exports the shared widgets.
// Second line of the package doc.
package foo

component C() {
	<div></div>
}
`
	assertFormat(t, src, want)
}

// TestAttrWSNormalization proves that the formatter auto-strips whitespace around
// '=' in all attribute value forms. The printer reconstructs attributes from the
// AST (which never stores '=' whitespace), so no printer code change is needed —
// this test just confirms the automatic normalization is real.
func TestAttrWSNormalization(t *testing.T) {
	// Source has spaces around '=' in two attribute forms: static string and
	// brace expression. The formatted output must emit canonical no-space form
	// (id="tip", data-x={val}) regardless of spacing in the input.
	src := `package p

component C() {
	<div id = "tip" data-x = {val}></div>
}`
	want := `package p

component C() {
	<div id="tip" data-x={val}></div>
}
`
	checkFormat(t, src, want)
}

func TestOrderedAttrsEmptyBagFormatting(t *testing.T) {
	// An empty {{ }} literal must format as `name={{ }}` (single interior space),
	// not `name={{  }}` (two interior spaces). Also verifies idempotence.
	src := `package p

import "github.com/gsxhq/gsx"

component C(attrs gsx.OrderedAttrs) {
	<div></div>
}

component Page() {
	<C attrs={{ }}/>
}`
	want := `package p

import "github.com/gsxhq/gsx"

component C(attrs gsx.OrderedAttrs) {
	<div></div>
}

component Page() {
	<C attrs={{ }}/>
}
`
	checkFormat(t, src, want)
}

func TestClassMapWraps(t *testing.T) {
	// A composed class map wider than 80 cols must break one entry per line,
	// not weld every entry onto one indented line.
	src := `package p
component C(v int) {
	<span class={ "base-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "green-bbbbbbbbbbbbbbbbbbbbbbbb": v == 1, "gray-cccccccccccccccccccccccc": v != 1 }>x</span>
}`
	want := `package p

component C(v int) {
	<span
		class={
			"base-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"green-bbbbbbbbbbbbbbbbbbbbbbbb": v == 1,
			"gray-cccccccccccccccccccccccc": v != 1
		}
	>
		x
	</span>
}
`
	assertFormat(t, src, want)
}

func TestValueFormSwitchLayout(t *testing.T) {
	src := `package p
component C(v int) {
	<span class={ "base", switch v { case 1: "green-aaaaaaaaaaaaaaaaaaaaaaaaaaaa" default: "gray-bbbbbbbbbbbbbbbbbbbbbbbb" } }>x</span>
}`
	want := `package p

component C(v int) {
	<span
		class={
			"base",
			switch v {
			case 1:
				"green-aaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			default:
				"gray-bbbbbbbbbbbbbbbbbbbbbbbb"
			}
		}
	>
		x
	</span>
}
`
	assertFormat(t, src, want)
}

func TestValueFormIfInline(t *testing.T) {
	src := `package p
component C(b bool) {
	<i class={ "x", if b { "on" } else { "off" } }>y</i>
}`
	want := `package p

component C(b bool) {
	<i class={ "x", if b { "on" } else { "off" } }>y</i>
}
`
	assertFormat(t, src, want)
}

// TestInterpMultilineExprReindents pins the fix for multi-line Go expressions
// inside a `{ }` interpolation: gofmt renders the expression relative to column
// 0, and the printer must re-indent every continuation line to the surrounding
// markup depth (previously the lines were emitted verbatim at column 0/1).
func TestInterpMultilineExprReindents(t *testing.T) {
	src := `package p
component C() {
	<div>
		<span>
			{ toc([]Item{
	{ID: "a", Label: "A"},
			{ID: "b", Label: "B"},
}) }
		</span>
	</div>
}`
	want := `package p

component C() {
	<div>
		<span>
			{ toc([]Item{
				{ID: "a", Label: "A"},
				{ID: "b", Label: "B"},
			}) }
		</span>
	</div>
}
`
	checkFormat(t, src, want)
}

// TestInterpRawStringVerbatim pins raw-string safety in interpolations: the
// interior lines of a multi-line raw string literal are part of the string's
// VALUE, so re-indenting them would change program behavior. They must pass
// through byte-for-byte.
func TestInterpRawStringVerbatim(t *testing.T) {
	src := `package p
component C() {
	<div>
		{ f(` + "`line1\nline2\n  line3`" + `) }
	</div>
}`
	want := `package p

component C() {
	<div>
		{ f(` + "`line1\nline2\n  line3`" + `) }
	</div>
}
`
	checkFormat(t, src, want)
}

// TestAttrExprRawStringVerbatim pins the same raw-string safety on the
// attribute-value path (fmtExprDoc): multiline() previously re-indented the
// raw string's interior lines, silently changing the attribute's value.
func TestAttrExprRawStringVerbatim(t *testing.T) {
	src := `package p
component C() {
	<div data-x={ f(` + "`line1\nline2`" + `) }>x</div>
}`
	got := fmtSource(t, src)
	if !strings.Contains(got, "`line1\nline2`") {
		t.Fatalf("raw string content was altered by formatting:\n%s", got)
	}
	again := fmtSource(t, got)
	if again != got {
		t.Fatalf("not idempotent:\n--- pass1 ---\n%s\n--- pass2 ---\n%s", got, again)
	}
}

// TestGoBlockRawStringVerbatim pins raw-string safety inside {{ }} Go blocks,
// which share multiline() with the interpolation and attribute paths.
func TestGoBlockRawStringVerbatim(t *testing.T) {
	src := `package p
component C() {
	<div>
		{{ q := ` + "`select 1\nfrom t`" + ` }}
		{ q }
	</div>
}`
	got := fmtSource(t, src)
	if !strings.Contains(got, "`select 1\nfrom t`") {
		t.Fatalf("raw string content was altered by formatting:\n%s", got)
	}
	again := fmtSource(t, got)
	if again != got {
		t.Fatalf("not idempotent:\n--- pass1 ---\n%s\n--- pass2 ---\n%s", got, again)
	}
}

// --- source comment preservation (// /* */ {/* */} {// }) ---

func TestFmtAttrOwnLineComment(t *testing.T) {
	// Comment on its own source line stays on its own line.
	src := "package p\ncomponent C(name string) {\n\t<input\n\ttype=\"checkbox\"\n\t// hello\n\tid={name} />\n}\n"
	want := "package p\n\ncomponent C(name string) {\n\t<input\n\t\ttype=\"checkbox\"\n\t\t// hello\n\t\tid={name}\n\t/>\n}\n"
	checkFormat(t, src, want)
}

func TestFmtAttrTrailingComment(t *testing.T) {
	// A comment on the same source line as an attr stays glued to that line.
	src := "package p\ncomponent C(name string) {\n\t<input id={name} // note\n\tvalue=\"x\" />\n}\n"
	want := "package p\n\ncomponent C(name string) {\n\t<input\n\t\tid={name} // note\n\t\tvalue=\"x\"\n\t/>\n}\n"
	checkFormat(t, src, want)
}

func TestFmtAttrBlockCommentInline(t *testing.T) {
	// A /* */ block comment may stay inline when the tag fits.
	src := "package p\ncomponent C() {\n\t<div /* note */ id={x}></div>\n}\n"
	want := "package p\n\ncomponent C() {\n\t<div /* note */ id={x}></div>\n}\n"
	checkFormat(t, src, want)
}

func TestFmtAttrBracedCommentCanonicalizesToBare(t *testing.T) {
	// A braced {/* */} in attribute position canonicalizes to the bare form.
	src := "package p\ncomponent C() {\n\t<div {/* note */} id={x}></div>\n}\n"
	want := "package p\n\ncomponent C() {\n\t<div /* note */ id={x}></div>\n}\n"
	checkFormat(t, src, want)
}

func TestFmtContentBlockComment(t *testing.T) {
	src := "package p\ncomponent C() {\n\t<p>{/* hidden */}Visible</p>\n}\n"
	want := "package p\n\ncomponent C() {\n\t<p>{/* hidden */}Visible</p>\n}\n"
	checkFormat(t, src, want)
}

func TestFmtContentLineComment(t *testing.T) {
	// The {// } line form must print with } on its own line (a one-line
	// {// text } would let the // swallow the closing brace on reparse).
	src := "package p\ncomponent C() {\n\t<p>{// hidden\n\t}Visible</p>\n}\n"
	want := "package p\n\ncomponent C() {\n\t<p>\n\t\t{// hidden\n\t\t}\n\t\tVisible\n\t</p>\n}\n"
	checkFormat(t, src, want)
}

func TestFmtCondAttrComment(t *testing.T) {
	src := "package p\ncomponent C(on bool) {\n\t<input { if on {\n\t// enabled\n\tchecked\n\t} } />\n}\n"
	want := "package p\n\ncomponent C(on bool) {\n\t<input\n\t\t{ if on {\n\t\t\t// enabled\n\t\t\tchecked\n\t\t} }\n\t/>\n}\n"
	checkFormat(t, src, want)
}

func TestFmtCommentsIdempotentMixed(t *testing.T) {
	src := "package p\ncomponent C(name string) {\n\t<input\n\t\ttype=\"checkbox\"\n\t\t// a\n\t\tid={name} // b\n\t\t/* c */\n\t\tvalue=\"x\"\n\t/>\n\t<p>{/* d */}Hi</p>\n}\n"
	once := fmtSource(t, src)
	twice := fmtSource(t, once)
	if once != twice {
		t.Fatalf("not idempotent:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
	if !strings.Contains(once, "// a") || !strings.Contains(once, "// b") ||
		!strings.Contains(once, "/* c */") || !strings.Contains(once, "{/* d */}") {
		t.Fatalf("a comment was dropped:\n%s", once)
	}
}
