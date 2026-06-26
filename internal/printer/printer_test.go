package printer

import (
	"bytes"
	"go/token"
	"reflect"
	"strings"
	"testing"

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
	if got := b.String(); got != want {
		t.Fatalf("format mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestElementBlock(t *testing.T) {
	// Two short children fit on one line in the new width-aware layout: the
	// <div> Group collapses because <div><p>a</p><span>b</span></div> < 80 cols.
	src := `package p
component C() {
	<div>
		<p>a</p>
		<span>b</span>
	</div>
}`
	want := `package p

component C() {
	<div><p>a</p><span>b</span></div>
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
	// Text + element child → inline-verbatim (a surviving Text forbids block).
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
	// opening-tag group breaks. Children (`{ children }`) are non-breakable (one
	// Interp segment), so they sit inline after `>`. Output is faithful+idempotent.
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

func TestCondAttr(t *testing.T) {
	// A CondAttr always forces the opening-tag group to break (BreakParent inside
	// attrDoc for CondAttr). Children "x" are non-breakable, so they sit inline
	// after `>`. Output is faithful+idempotent.
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
	// Two JSAttrs — flat tag is 71 chars but tag+child+close = 78 chars; at depth
	// 1 (position 4) the total 82 > 80 so the opening-tag group breaks. Children
	// "x" are non-breakable, so they sit inline after `>`. Faithful+idempotent.
	src := `package p
component C(tab string) {
	<div x-data="{ tab: @{ tab }, open: false }" onclick="alert(@{ tab })">x</div>
}`
	want := `package p

component C(tab string) {
	<div
		x-data="{ tab: @{ tab }, open: false }"
		onclick="alert(@{ tab })"
	>x</div>
}
`
	checkFormat(t, src, want)
}

func TestIfElseIfElse(t *testing.T) {
	// Short if-else-if-else fits on one line: the Group collapses to flat
	// because the whole expression is < 80 cols at depth 1.
	src := `package p
component C() {
	<div>
		{ if a { <p>A</p> } else if b { <p>B</p> } else { <p>C</p> } }
	</div>
}`
	want := `package p

component C() {
	<div>{ if a { <p>A</p> } else if b { <p>B</p> } else { <p>C</p> } }</div>
}
`
	checkFormat(t, src, want)
}

func TestForMarkup(t *testing.T) {
	// Short for-range with one child fits on one line at depth 1 (< 80 cols).
	src := `package p
component C() {
	<ul>
		{ for _, it := range items { <li>{it.Name}</li> } }
	</ul>
}`
	want := `package p

component C() {
	<ul>{ for _, it := range items { <li>{ it.Name }</li> } }</ul>
}
`
	checkFormat(t, src, want)
}

func TestSwitchMarkup(t *testing.T) {
	// Switch always uses HardLine so it never collapses. Short case bodies (one
	// non-breakable element) follow the colon on the same line. The <div> renders
	// inline (one child, not breakable) wrapping the switch block.
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
	<div>{ switch kind {
		case "a":<p>A</p>
		default:<p>D</p>
	} }</div>
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
	// Outer block (two elements), inner inline (text+element). Both fit flat.
	// Width-aware: <div><p>a <b>x</b> b</p><p>plain</p></div> < 80 cols.
	src := `package p
component C() {
	<div>
		<p>a <b>x</b> b</p>
		<p>plain</p>
	</div>
}`
	want := `package p

component C() {
	<div><p>a <b>x</b> b</p><p>plain</p></div>
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
	want := "package p\n\ncomponent C(w int) {\n\t<style>.a{width:@{ w }px}</style>\n}\n"
	checkFormat(t, src, want)
}

func TestStyleInterpFormatPreservesPipeline(t *testing.T) {
	// @{ x |> upper } in a <style> block must round-trip exactly — the printer
	// must not silently discard pipeline stages.
	src := "package p\n\ncomponent C(x string) {\n\t<style>.a{color:@{ x |> upper }}</style>\n}\n"
	want := "package p\n\ncomponent C(x string) {\n\t<style>.a{color:@{ x |> upper }}</style>\n}\n"
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
			>{ props.Category.Name }</a>
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

func TestShortBlockCollapsesToOneLine(t *testing.T) {
	// "true Prettier": a short block structure that fits 80 cols lays out flat.
	src := `package p
component C() {
	<div>
		<p>plain</p>
	</div>
}`
	want := `package p

component C() {
	<div><p>plain</p></div>
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
	// Idempotence: re-formatting is a fixed point.
	if again := format80(t, got); again != got {
		t.Fatalf("not idempotent:\n--- once ---\n%s\n--- twice ---\n%s", got, again)
	}
}
