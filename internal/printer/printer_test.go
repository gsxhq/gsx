package printer

import (
	"bytes"
	"fmt"
	"go/token"
	"reflect"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/pretty"
	"github.com/gsxhq/gsx/parser"
)

// fmtSource parses and prints src, returning the canonical output.
func fmtSource(t *testing.T, src string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "test.gsx", src, 0)
	if err != nil {
		t.Fatalf("parse error: %v\nsrc:\n%s", err, src)
	}

	var b strings.Builder
	if err := Fprint(&b, f, 80, pretty.DefaultTabWidth); err != nil {
		t.Fatalf("Fprint error: %v", err)
	}
	return b.String()
}

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

	var b bytes.Buffer
	if err := Fprint(&b, f, 80, pretty.DefaultTabWidth); err != nil {
		t.Fatalf("print: %v", err)
	}
	got := b.String()
	if got != want {
		t.Fatalf("format mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if !reflect.DeepEqual(contentAST(t, src), contentAST(t, want)) {
		t.Fatalf("assertFormat: formatting changed the content AST (not faithful)\nsrc:\n%s\nwant:\n%s", src, want)
	}
	// Idempotence: formatting want again must yield want.
	again := fmtSource(t, want)
	if again != want {
		t.Fatalf("assertFormat: not idempotent:\n--- want ---\n%s\n--- again ---\n%s", want, again)
	}
}

func TestElementBlock(t *testing.T) {
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
	src := `package p
component C() {
	<p>a <b>x</b> b</p>
}`
	want := `package p

component C() {
	<p>a <b>x</b> b</p>
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
	>{ children }</div>
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
	>x</div>
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
	>x</div>
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

// TestEmbeddedDquoteAttrPreserved pins that a `"`-delimited literal (the
// escape-hatch used when the content contains a backtick) round-trips as
// `"`-delimited — the printer must NOT rewrite it to the backtick form, which
// would put a bare backtick inside a backtick literal and fail to re-parse.
func TestEmbeddedDquoteAttrPreserved(t *testing.T) {
	src := `package p
component C(n string) {
	<a t=js"x=` + "`" + `@{n}` + "`" + `">go</a>
}`
	want := `package p

component C(n string) {
	<a t=js"x=` + "`" + `@{n}` + "`" + `">go</a>
}
`
	checkFormat(t, src, want)
}

// TestEmbeddedDquoteBodyAndEscapePreserved pins delimiter faithfulness for the
// body form {f"…"} and for the `\"` escape (a literal quote inside a `"`
// literal), both idempotent under re-format.
func TestEmbeddedDquoteBodyAndEscapePreserved(t *testing.T) {
	src := `package p
component C(n string) {
	<p title=f"say \"@{n}\"">{f"row-@{n}"}</p>
}`
	want := `package p

component C(n string) {
	<p title=f"say \"@{n}\"">{f"row-@{n}"}</p>
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

func TestEmbeddedAttrBracedLiteralPreservesBraces(t *testing.T) {
	// The braced form must round-trip: on a component tag a braced js`/css`
	// literal binds a declared prop while the bare form falls through to the
	// Attrs bag, so stripping the braces would change the meaning.
	src := `package p
component C(id string) {
	<button @click={js` + "`" + `save(@{ id })` + "`" + `}>Save</button>
}`
	want := `package p

component C(id string) {
	<button @click={js` + "`" + `save(@{id})` + "`" + `}>Save</button>
}
`
	checkFormat(t, src, want)
}

func TestEmbeddedAttrMultilineReindented(t *testing.T) {
	src := `package p
component C(open bool) {
	<div x-data=js` + "`" + `
		{ open: @{ open } }
	` + "`" + `>x</div>
}`
	want := "package p\n\ncomponent C(open bool) {\n\t<div\n\t\tx-data=js`\n\t\t\t{ open: @{open} }\n\t\t`\n\t>x</div>\n}\n"
	checkFormat(t, src, want)
}

func TestElementAttrsMultilinePreserved(t *testing.T) {
	broken := "package p\n\ncomponent C() {\n\t<a\n\t\thref=\"/help\"\n\t\tclass=\"x\"\n\t>?</a>\n}\n"
	brokenWant := "package p\n\ncomponent C() {\n\t<a\n\t\thref=\"/help\"\n\t\tclass=\"x\"\n\t>?</a>\n}\n"
	checkFormat(t, broken, brokenWant)

	inline := "package p\n\ncomponent C() {\n\t<a href=\"/help\" class=\"x\">?</a>\n}\n"
	checkFormat(t, inline, inline)
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
	want := "package p\n\ncomponent C(color string, hidden bool) {\n\t<div\n\t\tstyle={ \"display:none\": hidden, css`color:@{color};content:\"\\`\"` }\n\t>x</div>\n}\n"
	checkFormat(t, src, want)
}

func TestEmbeddedTextLiteralLangPrefix(t *testing.T) {
	if got := embeddedLangName(ast.EmbeddedText); got != "f" {
		t.Fatalf("embeddedLangName(ast.EmbeddedText) = %q, want \"f\"", got)
	}
	if got := embeddedLangName(ast.EmbeddedJS); got != "js" {
		t.Fatalf("embeddedLangName(ast.EmbeddedJS) = %q, want \"js\"", got)
	}
	if got := embeddedLangName(ast.EmbeddedCSS); got != "css" {
		t.Fatalf("embeddedLangName(ast.EmbeddedCSS) = %q, want \"css\"", got)
	}
}

func TestEmbeddedTextLiteralRoundTrip(t *testing.T) {
	// f`-prefixed EmbeddedText: interpolation opt-in, and a literal `@{` in the
	// source (escaped as `\@{`) must round-trip as literal text rather than
	// turning into a hole.
	src := `package p
component C(v string) {
	<span class=f` + "`" + `badge-@{v} \@{lit}` + "`" + `>x</span>
}`
	want := `package p

component C(v string) {
	<span class=f` + "`" + `badge-@{v} \@{lit}` + "`" + `>x</span>
}
`
	checkFormat(t, src, want)
}

func TestBodyEmbeddedInterpRoundTrip(t *testing.T) {
	// A body backtick literal `{`…@{expr}…`}` prints in its literal form
	// (preserved, not canonicalized to interleaved Text/Interp nodes).
	src := `package p
component C(id string, n int) {
	<p>{f` + "`" + `row-@{ id }-@{n}` + "`" + `}</p>
}`
	want := `package p

component C(id string, n int) {
	<p>{f` + "`" + `row-@{id}-@{n}` + "`" + `}</p>
}
`
	checkFormat(t, src, want)
}

func TestBodyEmbeddedInterpWholePipeRoundTrip(t *testing.T) {
	// A whole-literal pipeline after the closing backtick prints trailing
	// ` |> stage`.
	src := `package p
component C(id string) {
	<p>{f` + "`" + `row-@{ id }` + "`" + ` |> upper}</p>
}`
	want := `package p

component C(id string) {
	<p>{f` + "`" + `row-@{id}` + "`" + ` |> upper}</p>
}
`
	checkFormat(t, src, want)
}

func TestBodyEmbeddedInterpEscapedHoleRoundTrip(t *testing.T) {
	// A literal `\@{` inside a body embedded literal re-escapes and round-trips
	// as literal text rather than turning into a hole.
	src := `package p
component C(id string) {
	<p>{f` + "`" + `row-@{id} \@{lit}` + "`" + `}</p>
}`
	want := `package p

component C(id string) {
	<p>{f` + "`" + `row-@{id} \@{lit}` + "`" + `}</p>
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
	<div class={f` + "`" + `btn-@{ v }` + "`" + ` |> upper}>x</div>
}`
	want := `package p

component C(v string) {
	<div class={f` + "`" + `btn-@{v}` + "`" + ` |> upper}>x</div>
}
`
	checkFormat(t, src, want)
}

func TestIfElseIfElse(t *testing.T) {
	src := `package p
component C() {
	<div>
		{ if a { <p>A</p> } else if b { <p>B</p> } else { <p>C</p> } }
	</div>
}`
	want := `package p

component C() {
	<div>
		{ if a { <p>A</p> } else if b { <p>B</p> } else { <p>C</p> } }
	</div>
}
`
	checkFormat(t, src, want)
}

func TestForMarkup(t *testing.T) {
	src := `package p
component C() {
	<ul>
		{ for _, it := range items { <li>{it.Name}</li> } }
	</ul>
}`
	want := `package p

component C() {
	<ul>
		{ for _, it := range items { <li>{ it.Name }</li> } }
	</ul>
}
`
	checkFormat(t, src, want)
}

func TestSwitchMarkup(t *testing.T) {
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
	// The author wrote the fragment children on their own lines; that vertical
	// layout is preserved (the line break after `<>` forces the group open).
	src := `package p
component C() {
	<>
		<p>a</p>
		<p>b</p>
	</>
}`
	want := `package p

component C() {
	<>
		<p>a</p>
		<p>b</p>
	</>
}
`
	checkFormat(t, src, want)
}

func TestFragmentInlineStaysInline(t *testing.T) {
	// Authored on one line and fits: the fragment stays collapsed.
	src := `package p
component C() {
	<><p>a</p><p>b</p></>
}`
	want := `package p

component C() {
	<><p>a</p><p>b</p></>
}
`
	checkFormat(t, src, want)
}

// TestPrintFragmentExpressionValue confirms a fragment sitting in
// Go-expression position (e.g. `var list = <><a/><b/></>`, parsed as a
// GoWithElements Go part rather than a Component body) prints via the same
// p.fragment path as a component-body fragment, instead of hitting
// goWithElements' default "unknown Go-expression part type" failure.
func TestPrintFragmentExpressionValue(t *testing.T) {
	src := "package v\n\nvar list = <><a/><b/></>\n"
	want := "package v\n\nvar list = <><a/><b/></>\n"
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
		<p>a <b>x</b> b</p>
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
			>{ props.Category.Name }</a>
		} }
	</p>
}
`
	assertFormat(t, src, want)
}

func TestCfBodyEdgeUnsafeStaysFaithful(t *testing.T) {
	src := `package p
component C() {
	{ if veryLongConditionNameThatWouldForceTheEnclosingGroupToBreakAcrossEightyColumns { some text } }
}`
	out, err := printSource(t, src)
	if err != nil {
		t.Fatalf("print: %v", err)
	}
	if !reflect.DeepEqual(contentAST(t, src), contentAST(t, out)) {
		t.Fatalf("cfBody break changed content AST (unfaithful):\n%s", out)
	}
	// Idempotent.
	out2, err := printSource(t, out)
	if err != nil {
		t.Fatalf("reprint: %v", err)
	}
	if out != out2 {
		t.Fatalf("not idempotent:\n--- once ---\n%s\n--- twice ---\n%s", out, out2)
	}
}

func TestBlockShowsHierarchy(t *testing.T) {
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
	>{ children }{ name }</a>
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

// format80 parses and Fprintfs src at width 80, returning the
// canonical output. Used by comment-fidelity tests that need a bytes.Buffer.
func format80(t *testing.T, src string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "c.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var b bytes.Buffer
	if err := Fprint(&b, f, 80, pretty.DefaultTabWidth); err != nil {
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
	>Submit</form>
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
	>{ children }</a>
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
	>{ children }</p>
}
`
	assertFormat(t, src, want)
}

func TestComponentBodyEdgeUnsafeStaysFaithful(t *testing.T) {
	src := "package p\n\ncomponent C() { foo }"
	out, err := printSource(t, src)
	if err != nil {
		t.Fatalf("printSource: %v", err)
	}
	want := contentAST(t, src)
	got := contentAST(t, out)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("not faithful:\n  src=%q\n  out=%q", src, out)
	}
	out2, err := printSource(t, out)
	if err != nil {
		t.Fatalf("printSource(out2): %v", err)
	}
	if out != out2 {
		t.Fatalf("not idempotent:\n  out =%q\n  out2=%q", out, out2)
	}
}

func TestSwitchArmEdgeUnsafeStaysFaithful(t *testing.T) {
	src := "package p\n\ncomponent C() {\n{ switch k { case \"a\": foo } }\n}"
	out, err := printSource(t, src)
	if err != nil {
		t.Fatalf("printSource: %v", err)
	}
	want := contentAST(t, src)
	got := contentAST(t, out)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("not faithful:\n  src=%q\n  out=%q", src, out)
	}
	out2, err := printSource(t, out)
	if err != nil {
		t.Fatalf("printSource(out2): %v", err)
	}
	if out != out2 {
		t.Fatalf("not idempotent:\n  out =%q\n  out2=%q", out, out2)
	}
}

func TestTouchingBlockChildStaysTouching(t *testing.T) {
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
	>x</span>
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
	>x</span>
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
// attribute-value path (goFieldDoc): multiline() previously re-indented the
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
	src := "package p\ncomponent C() {\n\t<p>{// hidden\n\t}Visible</p>\n}\n"
	want := "package p\n\ncomponent C() {\n\t<p>{// hidden\n\t\t}Visible</p>\n}\n"
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

// --- author line-break preservation (control-flow bodies + element children) ---
// When the source places a line break immediately after the opening delimiter,
// the body stays block-formatted even though it is inline-only and would fit.

func TestIfInlineBodyPreservesAuthorLineBreak(t *testing.T) {
	src := "package p\ncomponent C(viewing *int) {\n\t<div>\n\t\t{ if viewing != nil {\n\t\t\t{ *viewing }\n\t\t} }\n\t</div>\n}\n"
	want := "package p\n\ncomponent C(viewing *int) {\n\t<div>\n\t\t{ if viewing != nil {\n\t\t\t{ *viewing }\n\t\t} }\n\t</div>\n}\n"
	checkFormat(t, src, want)
}

func TestIfInlineBodyStaysInlineWhenAuthorInline(t *testing.T) {
	// The <div> still breaks structurally (the `if` is a block child), but the
	// author-inline if body is NOT expanded — only author line breaks are honored.
	src := "package p\ncomponent C(viewing *int) {\n\t<div>{ if viewing != nil { { *viewing } } }</div>\n}\n"
	want := "package p\n\ncomponent C(viewing *int) {\n\t<div>{ if viewing != nil { { *viewing } } }</div>\n}\n"
	checkFormat(t, src, want)
}

func TestIfElseInlineBodyPreservesAuthorLineBreak(t *testing.T) {
	src := "package p\ncomponent C(x int) {\n\t<div>\n\t\t{ if x > 0 {\n\t\t\t{ x }\n\t\t} else {\n\t\t\t{ -x }\n\t\t} }\n\t</div>\n}\n"
	want := "package p\n\ncomponent C(x int) {\n\t<div>\n\t\t{ if x > 0 {\n\t\t\t{ x }\n\t\t} else {\n\t\t\t{ -x }\n\t\t} }\n\t</div>\n}\n"
	checkFormat(t, src, want)
}

func TestForInlineBodyPreservesAuthorLineBreak(t *testing.T) {
	src := "package p\ncomponent C(items []string) {\n\t<ul>\n\t\t{ for _, it := range items {\n\t\t\t{ it }\n\t\t} }\n\t</ul>\n}\n"
	want := "package p\n\ncomponent C(items []string) {\n\t<ul>\n\t\t{ for _, it := range items {\n\t\t\t{ it }\n\t\t} }\n\t</ul>\n}\n"
	checkFormat(t, src, want)
}

func TestElementInlineChildrenPreserveAuthorLineBreak(t *testing.T) {
	src := "package p\ncomponent C(name string) {\n\t<span>\n\t\t{ name }\n\t</span>\n}\n"
	want := "package p\n\ncomponent C(name string) {\n\t<span>\n\t\t{ name }\n\t</span>\n}\n"
	checkFormat(t, src, want)
}

func TestElementInlineChildrenStayInlineWhenAuthorInline(t *testing.T) {
	src := "package p\ncomponent C(name string) {\n\t<span>{ name }</span>\n}\n"
	want := "package p\n\ncomponent C(name string) {\n\t<span>{ name }</span>\n}\n"
	checkFormat(t, src, want)
}

func TestCfBodyEdgesTrimmedThenBreaksOnAuthorNewline(t *testing.T) {
	src := "package p\ncomponent C(x bool) {\n\t<p>a{ if x {\n\ttext  } }b</p>\n}\n"
	want := "package p\n\ncomponent C(x bool) {\n\t<p>a{ if x {\n\t\t\ttext\n\t\t} }b</p>\n}\n"
	checkFormat(t, src, want)
}

func TestFragmentInlineChildrenPreserveAuthorLineBreak(t *testing.T) {
	src := "package p\ncomponent C(a, b string) {\n\t<>\n\t\t{ a }{ b }\n\t</>\n}\n"
	want := "package p\n\ncomponent C(a, b string) {\n\t<>\n\t\t{ a }{ b }\n\t</>\n}\n"
	checkFormat(t, src, want)
}

func TestInlineContentStaysInline(t *testing.T) {
	// The paragraph that motivated this work: <code> must not explode even
	// though the glued tail overflows; literal prose retains its word gaps.
	checkFormat(t,
		"package p\n\ncomponent C() {\n\t<p>\n\t\tthe CLI vendors real <code>.gsx</code> source into your own module, so what you build against is code you own\n\t</p>\n}\n",
		// Width 80 cannot reflow literal element content.
		"package p\n\ncomponent C() {\n\t<p>\n\t\tthe CLI vendors real <code>.gsx</code> source into your own module, so what you build against is code you own\n\t</p>\n}\n")
}

func TestInlineOnlyOneLinerStays(t *testing.T) {
	// An all-inline children list no longer forces the parent open.
	src := "package p\n\ncomponent C() {\n\t<p>vendors real <code>.gsx</code> source.</p>\n}\n"
	checkFormat(t, src, src)
}

func TestBlockChildDoesNotAddSeparators(t *testing.T) {
	checkFormat(t,
		"package p\n\ncomponent C() {\n\t<div><span>a</span> <div>b</div></div>\n}\n",
		"package p\n\ncomponent C() {\n\t<div><span>a</span> <div>b</div></div>\n}\n")
}

func TestSpacingInterpGlue(t *testing.T) {
	checkFormat(t,
		"package p\n\ncomponent C() {\n\t<p>\n\t\tcaller-class-merge work as documented in the guide here (see{ \" \" }\n\t\t<a\n\t\t\thref=\"/docs/theming\"\n\t\t\tclass=\"underline underline-offset-4\"\n\t\t>Theming</a>)\n\t</p>\n}\n",
		"package p\n\ncomponent C() {\n\t<p>\n\t\tcaller-class-merge work as documented in the guide here (see{ \" \" }\n\t\t<a\n\t\t\thref=\"/docs/theming\"\n\t\t\tclass=\"underline underline-offset-4\"\n\t\t>Theming</a>)\n\t</p>\n}\n")
}

func TestSwitchArmBodyPreservesAuthorLineBreak(t *testing.T) {
	src := `package p
component C(kind string) {
	<div>
		{ switch kind {
		case "warn":
			<b>warning</b>
		case "info":
			<i>info</i>
		} }
	</div>
}`
	want := `package p

component C(kind string) {
	<div>
		{ switch kind {
		case "warn":
			<b>warning</b>
		case "info":
			<i>info</i>
		} }
	</div>
}
`
	checkFormat(t, src, want)
}

// TestSwitchCaseAlignsWithSwitch pins the gofmt-style arm alignment: `case`/
// `default` labels sit at the `{ switch` line's level, bodies one deeper.
// Markup switch is Go syntax with Go case expressions, so it indents like the
// same switch written in an attribute expression (which gofmt aligns) rather
// than like a JavaScript switch (which prettier, and our brace-depth reindent
// of js`/<script> bodies, put one level deeper).
func TestSwitchCaseAlignsWithSwitch(t *testing.T) {
	src := `package p
component C(kind string) {
	<div>
		{ switch kind {
		case "warn":
			<b>warning</b>
		default:
			<i>info</i>
		} }
	</div>
}`
	checkFormat(t, src, `package p

component C(kind string) {
	<div>
		{ switch kind {
		case "warn":
			<b>warning</b>
		default:
			<i>info</i>
		} }
	</div>
}
`)
}

// TestSwitchArmBodyStaysInlineWhenAuthorInline is the inline counterpart: a
// case body written after the colon on the same line stays inline (only
// author line breaks are honored, matching the *Multiline convention
// elsewhere). The leading space after `:` is trimmed like any control-flow
// body's brace-interior edge (trimBodyEdges) — same as today.
func TestSwitchArmBodyStaysInlineWhenAuthorInline(t *testing.T) {
	src := `package p
component C(kind string) {
	<div>
		{ switch kind {
		case "warn": <b>w</b>
		case "info": <i>i</i>
		} }
	</div>
}`
	want := `package p

component C(kind string) {
	<div>
		{ switch kind {
		case "warn": <b>w</b>
		case "info": <i>i</i>
		} }
	</div>
}
`
	checkFormat(t, src, want)
}

func TestSiblingLeavesPreserveAuthorLineBreak(t *testing.T) {
	src := `package p
component C() {
	<div>
		<img src="/a.png" alt="a"/>
		<br/>
		<input type="text"/>
	</div>
}`
	want := `package p

component C() {
	<div>
		<img src="/a.png" alt="a"/>
		<br/>
		<input type="text"/>
	</div>
}
`
	checkFormat(t, src, want)
}

func TestSiblingOneLinerStaysOneLine(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<div><em>a</em><em>b</em></div>\n}\n"
	checkFormat(t, src, src)
}

func TestMixedProsePreservesAuthoredWhitespace(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<p>\n\t\tThis is a long paragraph that explains something important about the code and\n\t\thow it works in practice with <code>example</code> shown inline for readers.\n\t</p>\n}\n"
	want := "package p\n\ncomponent C() {\n\t<p>\n\t\tThis is a long paragraph that explains something important about the code and\n\t\thow it works in practice with <code>example</code> shown inline for readers.\n\t</p>\n}\n"
	checkFormat(t, src, want)
}

func TestComponentTopLevelBodySiblingsPreserveAuthorLineBreak(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<b>a</b>\n\t<br/>\n}\n"
	checkFormat(t, src, src)
}

// TestIfBodySiblingsPreserveAuthorLineBreak covers control-flow bodies
// (parseControlBody → parseMarkupUntilCloseWS), the second missed context.
func TestIfBodySiblingsPreserveAuthorLineBreak(t *testing.T) {
	src := `package p
component C(x bool) {
	<div>
		{ if x {
			<b>a</b>
			<br/>
		} }
	</div>
}`
	want := "package p\n\ncomponent C(x bool) {\n\t<div>\n\t\t{ if x {\n\t\t\t<b>a</b>\n\t\t\t<br/>\n\t\t} }\n\t</div>\n}\n"
	checkFormat(t, src, want)
}

func TestIfBodySiblingsStayInlineWhenAuthorInline(t *testing.T) {
	src := "package p\n\ncomponent C(x bool) {\n\t<div>\n\t\t{ if x { <b>a</b><br/> } }\n\t</div>\n}\n"
	checkFormat(t, src, src)
}

// TestForBodySiblingsPreserveAuthorLineBreak covers the for-body half of
// parseControlBody → parseMarkupUntilCloseWS.
func TestForBodySiblingsPreserveAuthorLineBreak(t *testing.T) {
	src := `package p
component C(items []string) {
	<ul>
		{ for _, it := range items {
			<b>{ it }</b>
			<br/>
		} }
	</ul>
}`
	want := "package p\n\ncomponent C(items []string) {\n\t<ul>\n\t\t{ for _, it := range items {\n\t\t\t<b>{ it }</b>\n\t\t\t<br/>\n\t\t} }\n\t</ul>\n}\n"
	checkFormat(t, src, want)
}

// TestCaseArmInteriorSiblingPreservesAuthorLineBreak is the third missed
// context: CaseClause.BodyMultiline only protects the break right after the
// colon — the joint BETWEEN two sibling leaves inside the arm's own body is a
// separate joint that parseCaseBody's loop must stamp itself.
func TestCaseArmInteriorSiblingPreservesAuthorLineBreak(t *testing.T) {
	src := `package p
component C(kind string) {
	<div>
		{ switch kind {
		case "warn":
			<b>w</b>
			<br/>
		} }
	</div>
}`
	want := "package p\n\ncomponent C(kind string) {\n\t<div>\n\t\t{ switch kind {\n\t\tcase \"warn\":\n\t\t\t<b>w</b>\n\t\t\t<br/>\n\t\t} }\n\t</div>\n}\n"
	checkFormat(t, src, want)
}

// TestCaseArmInteriorSiblingStaysInlineWhenAuthorInline is the inline
// counterpart: siblings written on the same line as the colon, with no break
// between them either, all stay glued on the arm's one line.
func TestCaseArmInteriorSiblingStaysInlineWhenAuthorInline(t *testing.T) {
	src := "package p\n\ncomponent C(kind string) {\n\t<div>\n\t\t{ switch kind {\n\t\tcase \"warn\": <b>w</b><br/>\n\t\t} }\n\t</div>\n}\n"
	checkFormat(t, src, src)
}

// --- 2026-07-25 amendment: the formatter must not create case labels ---
//
// switchArmCount parses src and returns the number of Cases in the first
// SwitchMarkup found in decl "C"'s body (searching one level into Element
// children, enough for these tests' `<div>{ switch ... } }</div>` shape).
func switchArmCount(t *testing.T, src string) int {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "count.gsx", src, 0)
	if err != nil {
		t.Fatalf("parse error: %v\nsrc:\n%s", err, src)
	}
	var find func(nodes []ast.Markup) *ast.SwitchMarkup
	find = func(nodes []ast.Markup) *ast.SwitchMarkup {
		for _, n := range nodes {
			switch v := n.(type) {
			case *ast.SwitchMarkup:
				return v
			case *ast.Element:
				if s := find(v.Children); s != nil {
					return s
				}
			}
		}
		return nil
	}
	for _, d := range f.Decls {
		c, ok := d.(*ast.Component)
		if !ok {
			continue
		}
		if s := find(c.Body); s != nil {
			return len(s.Cases)
		}
	}
	t.Fatalf("no SwitchMarkup found\nsrc:\n%s", src)
	return 0
}

// proseWrapCaseSource builds a switch with exactly two real arms — `case
// "a":` and `default:` — where the "a" arm's body is n bare "x" words
// followed by a bare occurrence of keyword (either "case 1: nope" or
// "default: wins"), all on one physical source line. Neither occurrence is a
// real label: "case 1: nope" isn't at line start in the source, and there is
// already a real `default:` arm later, so a naive parse producing a THIRD arm
// here would itself be a bug distinct from the ones under test — switchArmCount
// on the UNFORMATTED src is asserted to be 2 by the caller.
func proseWrapCaseSource(n int, keyword string) string {
	words := make([]string, n)
	for i := range words {
		words[i] = "x"
	}
	prose := strings.Join(words, " ")
	var tail string
	if keyword == "case" {
		tail = "case 1: nope"
	} else {
		tail = "default: wins"
	}
	return "package views\n\ncomponent C(k string) {\n\t<div>{ switch k {\n\tcase \"a\":\n\t\t" +
		prose + " " + tail + "\n\tdefault:\n\t\t<i>y</i>\n\t} }</div>\n}\n"
}

// assertFormatNeverCreatesLabel is the shared TDD assertion for both repro
// shapes (c112: mid-line "case 1:"; d108: mid-line "default:"): formatting
// must not change the arm count, and — since a bond only forbids a break, it
// never changes the render — the formatted output must re-format to a
// byte-identical fixed point (idempotence), matching checkFormat elsewhere.
func assertFormatNeverCreatesLabel(t *testing.T, n int, keyword string) {
	t.Helper()
	src := proseWrapCaseSource(n, keyword)
	if got := switchArmCount(t, src); got != 2 {
		t.Fatalf("n=%d keyword=%s: fixture itself has %d arms, want 2 (bad fixture)", n, keyword, got)
	}
	formatted := fmtSource(t, src)
	if got := switchArmCount(t, formatted); got != 2 {
		t.Errorf("n=%d keyword=%s: gsx fmt changed the arm count to %d (formatter created a label)\n--- formatted ---\n%s",
			n, keyword, got, formatted)
	}
	again := fmtSource(t, formatted)
	if again != formatted {
		t.Errorf("n=%d keyword=%s: fmt is not idempotent\n--- pass1 ---\n%s\n--- pass2 ---\n%s", n, keyword, formatted, again)
	}
}

func TestCaseBodyProseWrapNeverCreatesCaseLabel(t *testing.T) {
	assertFormatNeverCreatesLabel(t, 36, "case")
}

// TestCaseBodyProseWrapNeverCreatesDefaultLabel is d108's exact repro: the
// "default:" variant, which additionally made `gsx generate` fail outright
// ("multiple defaults") since the source already had a real default arm.
// n=34 is the analogous exact-boundary word count for this keyword's length.
func TestCaseBodyProseWrapNeverCreatesDefaultLabel(t *testing.T) {
	assertFormatNeverCreatesLabel(t, 34, "default")
}

// TestCaseBodyProseWrapNeverCreatesLabelSweep is the reviewer-requested sweep:
// varying prose length across the wrap boundary rather than pinning one
// length (the adversarial review found 12 of 84 lengths broke — a single
// length is not enough coverage for a greedy word-wrap interaction). Covers
// both keywords over two wrap cycles' worth of lengths.
func TestCaseBodyProseWrapNeverCreatesLabelSweep(t *testing.T) {
	for _, keyword := range []string{"case", "default"} {
		for n := 1; n <= 100; n++ {
			t.Run(fmt.Sprintf("%s/n=%d", keyword, n), func(t *testing.T) {
				assertFormatNeverCreatesLabel(t, n, keyword)
			})
		}
	}
}

func TestCaseBodyContentNeverCreatesLabel(t *testing.T) {
	for n := 1; n <= 100; n++ {
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			words := make([]string, n)
			for i := range words {
				words[i] = "x"
			}
			prose := strings.Join(words, " ")
			src := "package views\n\ncomponent C(k string) {\n\t<div>{ switch k {\n\tcase \"a\":\n\t\t<div>y</div>\n\t\t" +
				prose + " default: wins\n\tdefault:\n\t\t<i>z</i>\n\t} }</div>\n}\n"
			if got := switchArmCount(t, src); got != 2 {
				t.Fatalf("fixture itself has %d arms, want 2 (bad fixture)", got)
			}
			formatted := fmtSource(t, src)
			if got := switchArmCount(t, formatted); got != 2 {
				t.Errorf("gsx fmt changed the arm count to %d (formatter created a label)\n--- formatted ---\n%s", got, formatted)
			}
		})
	}
}

// TestCaseBodyNestedElementPreservesProse pins the scanner's case-body scope:
// line-start "default:" inside an element is text, not a switch label. Long
// literal prose remains intact at every formatter width, and is idempotent.
func TestCaseBodyNestedElementPreservesProse(t *testing.T) {
	words := make([]string, 40)
	for i := range words {
		words[i] = "x"
	}
	prose := strings.Join(words, " ")
	src := "package views\n\ncomponent C(k string) {\n\t<div>{ switch k {\n\tcase \"a\":\n\t\t<p>" +
		prose + " default: wins</p>\n\tdefault:\n\t\t<i>y</i>\n\t} }</div>\n}\n"
	formatted := fmtSource(t, src)
	again := fmtSource(t, formatted)
	if again != formatted {
		t.Errorf("fmt is not idempotent\n--- pass1 ---\n%s\n--- pass2 ---\n%s", formatted, again)
	}
	if !strings.Contains(formatted, "default: wins") {
		t.Errorf("element-child prose was altered, want \"default: wins\" preserved verbatim somewhere\n%s", formatted)
	}
}

// TestDecorativeParenStaysFaithful pins that the parens fmt adds around a
// multi-line Go-expression element (and keeps around an author-wrapped one)
// are not a change of meaning: codegen strips them, and so must the
// faithfulness normalizer.
func TestDecorativeParenStaysFaithful(t *testing.T) {
	for _, src := range []string{
		"package p\n\nvar x = <span>\n\ta\n</span>\n",
		"package p\n\nvar x = <span>\n\ta\n\t<b>b</b>\n</span>\n",
		"package p\n\nvar x = (<span>a</span>)\n",
		"package p\n\nvar (\n\tx = <span>\n\t\ta\n\t</span>\n)\n",
	} {
		out, err := printSource(t, src)
		if err != nil {
			t.Fatalf("printSource: %v", err)
		}
		if !reflect.DeepEqual(contentAST(t, src), contentAST(t, out)) {
			t.Errorf("not faithful:\n  src=%q\n  out=%q", src, out)
		}
	}
}
