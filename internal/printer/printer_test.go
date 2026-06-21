package printer

import (
	"go/token"
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
	if err := Fprint(&b, f); err != nil {
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

func TestInterpTryAndPipeline(t *testing.T) {
	src := `package p
component C() {
	<div>{ x? }</div>
	<div>{ items |> reverse |> take(3) }</div>
}`
	want := `package p

component C() {
	<div>{ x? }</div>
	<div>{ items |> reverse |> take(3) }</div>
}
`
	checkFormat(t, src, want)
}

func TestAttrKinds(t *testing.T) {
	src := `package p
component C() {
	<div id="main" hidden class={ "card", "active": isActive } data-x={ val? } {...rest}>{children}</div>
}`
	want := `package p

component C() {
	<div id="main" hidden class={ "card", "active": isActive } data-x={val?} {...rest}>{ children }</div>
}
`
	checkFormat(t, src, want)
}

func TestCondAttr(t *testing.T) {
	src := `package p
component C() {
	<div { if active { class="on" } else { class="off" } }>x</div>
}`
	want := `package p

component C() {
	<div { if active { class="on" } else { class="off" } }>x</div>
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
	// Outer block (two elements), inner inline (text+element).
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
