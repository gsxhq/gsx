package parser

import (
	"go/token"
	"reflect"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

// parseStringT parses a full .gsx source string and fails the test on error.
func parseStringT(t *testing.T, src string) *ast.File {
	t.Helper()
	file, err := ParseFile(token.NewFileSet(), "test.gsx", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func TestParseInterp(t *testing.T) {
	p := testParser(`{ user.Name }rest`)
	n, err := p.parseInterp()
	if err != nil {
		t.Fatal(err)
	}
	if n.Expr != "user.Name" {
		t.Fatalf("got %+v", n)
	}
	if p.src[p.i:] != "rest" {
		t.Fatalf("cursor at %q", p.src[p.i:])
	}
}

// The `?` try-marker was removed: a trailing `?` is now a parse error (gsx
// auto-unwraps (T, error)). Confirm the marker is rejected, not silently parsed.
func TestParseInterpTryRejected(t *testing.T) {
	p := testParser(`{ route.URL(ctx)? }`)
	if _, err := p.parseInterp(); err == nil {
		t.Fatal("expected a parse error for the removed `?` try-marker, got nil")
	}
}

func TestParseText(t *testing.T) {
	p := testParser("hello world<div>")
	n := p.parseText()
	if n.Value != "hello world" {
		t.Fatalf("got %q", n.Value)
	}
	if p.peek() != '<' {
		t.Fatalf("cursor at %q", p.src[p.i:])
	}
}

func TestParseAttrs(t *testing.T) {
	p := testParser(`class="card" id={x} disabled { rest... } data-y={z}>`)
	attrs, err := p.parseAttrs()
	if err != nil {
		t.Fatal(err)
	}
	if len(attrs) != 5 {
		t.Fatalf("got %d attrs: %#v", len(attrs), attrs)
	}
	if a, ok := attrs[0].(*ast.StaticAttr); !ok || a.Name != "class" || a.Value != "card" {
		t.Fatalf("attr0 = %#v", attrs[0])
	}
	if a, ok := attrs[1].(*ast.ExprAttr); !ok || a.Name != "id" || a.Expr != "x" {
		t.Fatalf("attr1 = %#v", attrs[1])
	}
	if a, ok := attrs[2].(*ast.BoolAttr); !ok || a.Name != "disabled" {
		t.Fatalf("attr2 = %#v", attrs[2])
	}
	if a, ok := attrs[3].(*ast.SpreadAttr); !ok || a.Expr != "rest" {
		t.Fatalf("attr3 = %#v", attrs[3])
	}
	if a, ok := attrs[4].(*ast.ExprAttr); !ok || a.Name != "data-y" || a.Expr != "z" {
		t.Fatalf("attr4 = %#v", attrs[4])
	}
	if p.peek() != '>' {
		t.Fatalf("cursor at %q", p.src[p.i:])
	}
}

func TestParseSelfClosing(t *testing.T) {
	p := testParser(`<img src="x.png"/>`)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if el.Tag != "img" || !el.Void || len(el.Attrs) != 1 {
		t.Fatalf("got %#v", el)
	}
}

func TestParseDottedComponentTag(t *testing.T) {
	p := testParser(`<ui.Button variant="primary"></ui.Button>`)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if el.Tag != "ui.Button" || el.Void || len(el.Attrs) != 1 {
		t.Fatalf("got %#v", el)
	}
}

func TestParseChildrenNested(t *testing.T) {
	p := testParser(`<div class="card"><h2>{title}</h2>text</div>`)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	div := n.(*ast.Element)
	if len(div.Children) != 2 {
		t.Fatalf("got %d children: %#v", len(div.Children), div.Children)
	}
	h2 := div.Children[0].(*ast.Element)
	if h2.Tag != "h2" {
		t.Fatalf("child0 = %#v", h2)
	}
	if _, ok := h2.Children[0].(*ast.Interp); !ok {
		t.Fatalf("h2 child = %#v", h2.Children[0])
	}
	if txt := div.Children[1].(*ast.Text); txt.Value != "text" {
		t.Fatalf("child1 = %#v", div.Children[1])
	}
}

func TestParseMarkupAttr(t *testing.T) {
	p := testParser(`<Panel header={ <h1>Hi</h1> }></Panel>`)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	ma := el.Attrs[0].(*ast.MarkupAttr)
	if ma.Name != "header" || len(ma.Value) != 1 {
		t.Fatalf("got %#v", ma)
	}
	if ma.Value[0].(*ast.Element).Tag != "h1" {
		t.Fatalf("markup attr value = %#v", ma.Value[0])
	}
}

func TestMarkupAttrWithApostrophe(t *testing.T) {
	// C1: apostrophe inside a markup-attribute value must parse.
	p := testParser(`<Panel header={ <h1>Today's news</h1> }></Panel>`)
	n, err := p.parseElement()
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	el := n.(*ast.Element)
	ma, ok := el.Attrs[0].(*ast.MarkupAttr)
	if !ok {
		t.Fatalf("attr0 = %T, want *ast.MarkupAttr", el.Attrs[0])
	}
	h1 := ma.Value[0].(*ast.Element)
	if h1.Tag != "h1" {
		t.Fatalf("markup attr value = %#v", ma.Value)
	}
	var txt *ast.Text
	for _, c := range h1.Children {
		if t2, ok := c.(*ast.Text); ok {
			txt = t2
		}
	}
	if txt == nil || !strings.Contains(txt.Value, "Today's") {
		t.Fatalf("h1 children = %#v, want text containing apostrophe", h1.Children)
	}
}

func TestParseChildrenMismatch(t *testing.T) {
	p := testParser(`<div>hi</span>`)
	_, err := p.parseElement()
	if err == nil {
		t.Fatalf("expected mismatched-close-tag error, got nil")
	}
	if !strings.Contains(err.Error(), "mismatched close tag") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseSpreadWhitespace(t *testing.T) {
	// { rest... }, {rest...}, and {  rest  ...  } must all parse as one spread.
	for _, src := range []string{`{ rest... }>`, `{rest...}>`, `{  rest  ...  }>`} {
		p := testParser(src)
		attrs, err := p.parseAttrs()
		if err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		if len(attrs) != 1 {
			t.Fatalf("%q: got %d attrs: %#v", src, len(attrs), attrs)
		}
		sa, ok := attrs[0].(*ast.SpreadAttr)
		if !ok || sa.Expr != "rest" {
			t.Fatalf("%q: got %#v", src, attrs[0])
		}
	}
}

func TestParseSpreadPipeline(t *testing.T) {
	// The canonical parenthesized piped-spread form and the bare form parse to the
	// SAME seed + stages; a parenthesized NON-pipeline keeps its parens; a plain
	// spread has no stages.
	cases := []struct {
		src       string
		wantSeed  string
		wantStage string // first stage name, "" if no stages
	}{
		{`{ (a |> upper)... }>`, "a", "upper"},
		{`{ a |> upper... }>`, "a", "upper"},
		{`{ (a |> trim |> upper)... }>`, "a", "trim"},
		{`{ rest... }>`, "rest", ""},
		{`{ (x)... }>`, "(x)", ""},
	}
	for _, tc := range cases {
		p := testParser(tc.src)
		attrs, err := p.parseAttrs()
		if err != nil {
			t.Fatalf("%q: %v", tc.src, err)
		}
		sa, ok := attrs[0].(*ast.SpreadAttr)
		if !ok {
			t.Fatalf("%q: not a SpreadAttr: %#v", tc.src, attrs[0])
		}
		if sa.Expr != tc.wantSeed {
			t.Errorf("%q: seed = %q, want %q", tc.src, sa.Expr, tc.wantSeed)
		}
		if tc.wantStage == "" {
			if len(sa.Stages) != 0 {
				t.Errorf("%q: got %d stages, want 0", tc.src, len(sa.Stages))
			}
		} else if len(sa.Stages) == 0 || sa.Stages[0].Name != tc.wantStage {
			t.Errorf("%q: stages = %#v, want first %q", tc.src, sa.Stages, tc.wantStage)
		}
	}
}

func TestTagTrailingLineComment(t *testing.T) {
	// `<div id={x} // trailing\n class="y">` → div with exactly two attrs (ExprAttr id, StaticAttr class)
	p := testParser("<div id={x} // trailing\n class=\"y\"></div>")
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Attrs) != 2 {
		t.Fatalf("got %d attrs, want 2: %#v", len(el.Attrs), el.Attrs)
	}
	if a, ok := el.Attrs[0].(*ast.ExprAttr); !ok || a.Name != "id" || a.Expr != "x" {
		t.Fatalf("attr0 = %#v, want ExprAttr{id, x}", el.Attrs[0])
	}
	if a, ok := el.Attrs[1].(*ast.StaticAttr); !ok || a.Name != "class" || a.Value != "y" {
		t.Fatalf("attr1 = %#v, want StaticAttr{class, y}", el.Attrs[1])
	}
}

func TestTagOwnLineComment(t *testing.T) {
	// `<div\n // own line\n id={x}>` → div with one attr id
	p := testParser("<div\n // own line\n id={x}></div>")
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Attrs) != 1 {
		t.Fatalf("got %d attrs, want 1: %#v", len(el.Attrs), el.Attrs)
	}
	if a, ok := el.Attrs[0].(*ast.ExprAttr); !ok || a.Name != "id" || a.Expr != "x" {
		t.Fatalf("attr0 = %#v, want ExprAttr{id, x}", el.Attrs[0])
	}
}

func TestTagBlockComment(t *testing.T) {
	// `<div /* note */ id={x}>` → div with one attr id
	p := testParser("<div /* note */ id={x}></div>")
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Attrs) != 1 {
		t.Fatalf("got %d attrs, want 1: %#v", len(el.Attrs), el.Attrs)
	}
	if a, ok := el.Attrs[0].(*ast.ExprAttr); !ok || a.Name != "id" || a.Expr != "x" {
		t.Fatalf("attr0 = %#v, want ExprAttr{id, x}", el.Attrs[0])
	}
}

func TestContentIsLiteral(t *testing.T) {
	// CRITICAL: text between > and < or { is verbatim; // and /* */ are NOT stripped.
	src := `<a>http://example.com // and /* this */ stay literal</a>`
	p := testParser(src)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Children) != 1 {
		t.Fatalf("got %d children, want 1: %#v", len(el.Children), el.Children)
	}
	txt, ok := el.Children[0].(*ast.Text)
	if !ok {
		t.Fatalf("child is %T, want *ast.Text", el.Children[0])
	}
	want := "http://example.com // and /* this */ stay literal"
	if txt.Value != want {
		t.Fatalf("text value = %q, want %q", txt.Value, want)
	}
}

func TestBracedContentComment(t *testing.T) {
	// {/* comment with <tags> and a } brace */} is skipped; "keep" remains as Text.
	// goExprEnd handles the } inside the comment (scanner-aware).
	src := `<div>{/* a content comment with <tags> and a } brace */}keep</div>`
	p := testParser(src)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Children) != 1 {
		t.Fatalf("got %d children, want 1: %#v", len(el.Children), el.Children)
	}
	txt, ok := el.Children[0].(*ast.Text)
	if !ok {
		t.Fatalf("child is %T, want *ast.Text", el.Children[0])
	}
	if txt.Value != "keep" {
		t.Fatalf("text value = %q, want %q", txt.Value, "keep")
	}
}

func TestBracedLineComment(t *testing.T) {
	// {// line comment\n} is skipped; "x" remains as Text.
	src := "<div>{// just a line comment\n}x</div>"
	p := testParser(src)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Children) != 1 {
		t.Fatalf("got %d children, want 1: %#v", len(el.Children), el.Children)
	}
	txt, ok := el.Children[0].(*ast.Text)
	if !ok {
		t.Fatalf("child is %T, want *ast.Text", el.Children[0])
	}
	if txt.Value != "x" {
		t.Fatalf("text value = %q, want %q", txt.Value, "x")
	}
}

func TestUnterminatedTagBlockComment(t *testing.T) {
	// `<div /* oops>` → parseElement returns an error mentioning "unterminated block comment"
	p := testParser("<div /* oops>")
	_, err := p.parseElement()
	if err == nil {
		t.Fatal("expected error for unterminated block comment, got nil")
	}
	if !strings.Contains(err.Error(), "unterminated block comment") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseIfSimple(t *testing.T) {
	p := testParser(`{ if ok { <b>yes</b> } }`)
	node, _, err := p.parseBraceNode()
	if err != nil {
		t.Fatal(err)
	}
	n, ok := node.(*ast.IfMarkup)
	if !ok {
		t.Fatalf("got %T, want *ast.IfMarkup", node)
	}
	if n.Cond != "ok" {
		t.Fatalf("Cond = %q", n.Cond)
	}
	if len(n.Then) != 1 || n.Then[0].(*ast.Element).Tag != "b" {
		t.Fatalf("Then = %#v", n.Then)
	}
	if n.Else != nil {
		t.Fatalf("Else should be nil, got %#v", n.Else)
	}
}

func TestParseIfElse(t *testing.T) {
	p := testParser(`{ if a { <b>1</b> } else { <i>2</i> } }`)
	node, _, err := p.parseBraceNode()
	if err != nil {
		t.Fatal(err)
	}
	n := node.(*ast.IfMarkup)
	if len(n.Else) != 1 || n.Else[0].(*ast.Element).Tag != "i" {
		t.Fatalf("Else = %#v", n.Else)
	}
}

func TestParseIfElseIfChain(t *testing.T) {
	p := testParser(`{ if a { <x/> } else if b { <y/> } else { <z/> } }`)
	node, _, err := p.parseBraceNode()
	if err != nil {
		t.Fatal(err)
	}
	n := node.(*ast.IfMarkup)
	if n.Cond != "a" {
		t.Fatalf("Cond = %q", n.Cond)
	}
	if len(n.Else) != 1 {
		t.Fatalf("expected else-if chain, Else = %#v", n.Else)
	}
	elseIf, ok := n.Else[0].(*ast.IfMarkup)
	if !ok {
		t.Fatalf("Else[0] = %T, want *ast.IfMarkup", n.Else[0])
	}
	if elseIf.Cond != "b" {
		t.Fatalf("else-if Cond = %q", elseIf.Cond)
	}
	if len(elseIf.Else) != 1 || elseIf.Else[0].(*ast.Element).Tag != "z" {
		t.Fatalf("final else = %#v", elseIf.Else)
	}
}

func TestParseIfWithInterpAndText(t *testing.T) {
	p := testParser(`{ if it.Active { <strong>{it.Name}</strong> } else { {it.Name} } }`)
	node, _, err := p.parseBraceNode()
	if err != nil {
		t.Fatal(err)
	}
	n := node.(*ast.IfMarkup)
	strong := n.Then[0].(*ast.Element)
	if strong.Children[0].(*ast.Interp).Expr != "it.Name" {
		t.Fatalf("then interp = %#v", strong.Children[0])
	}
	// else body has whitespace text + interp; find the interp
	var elseInterp *ast.Interp
	for _, m := range n.Else {
		if in, ok := m.(*ast.Interp); ok {
			elseInterp = in
		}
	}
	if elseInterp == nil || elseInterp.Expr != "it.Name" {
		t.Fatalf("else interp = %#v", n.Else)
	}
}

var _ = ast.Text{}

func TestParseGoBlock(t *testing.T) {
	// {{ … }} at child level becomes a GoBlock with trimmed Code; trailing text remains.
	p := testParser("{{ x := f() }}rest<")
	node, skipped, err := p.parseBraceNode()
	if err != nil {
		t.Fatal(err)
	}
	if skipped {
		t.Fatal("GoBlock should not be skipped")
	}
	gb, ok := node.(*ast.GoBlock)
	if !ok {
		t.Fatalf("got %T, want *ast.GoBlock", node)
	}
	if gb.Code != "x := f()" {
		t.Fatalf("Code = %q, want %q", gb.Code, "x := f()")
	}
	if p.src[p.i:] != "rest<" {
		t.Fatalf("cursor at %q, want %q", p.src[p.i:], "rest<")
	}
}

func TestParseGoBlockNestedBraces(t *testing.T) {
	// Inner Go braces (composite literal, if-block) must not end the {{ }} early.
	p := testParser("{{ if err != nil { return err }; m := map[string]int{} }}")
	node, _, err := p.parseBraceNode()
	if err != nil {
		t.Fatal(err)
	}
	gb := node.(*ast.GoBlock)
	want := "if err != nil { return err }; m := map[string]int{}"
	if gb.Code != want {
		t.Fatalf("Code = %q, want %q", gb.Code, want)
	}
}

func TestGoBlockInComponentBody(t *testing.T) {
	// End-to-end: a {{ }} between markup siblings in a component body.
	src := `package p
component C() {
	<div>
		{{ initials := f(name) }}
		<span>{initials}</span>
	</div>
}`
	file := parseStringT(t, src)
	comp := file.Decls[0].(*ast.Component)
	div := comp.Body[0].(*ast.Element)
	var sawGoBlock bool
	for _, c := range div.Children {
		if gb, ok := c.(*ast.GoBlock); ok {
			sawGoBlock = true
			if gb.Code != "initials := f(name)" {
				t.Fatalf("Code = %q", gb.Code)
			}
		}
	}
	if !sawGoBlock {
		t.Fatal("no GoBlock found in component body children")
	}
}

func TestParseForRange(t *testing.T) {
	p := testParser(`{ for i, it := range items { <li>{it.Name}</li> } }`)
	node, _, err := p.parseBraceNode()
	if err != nil {
		t.Fatal(err)
	}
	n, ok := node.(*ast.ForMarkup)
	if !ok {
		t.Fatalf("got %T, want *ast.ForMarkup", node)
	}
	if n.Clause != "i, it := range items" {
		t.Fatalf("Clause = %q", n.Clause)
	}
	li := n.Body[0].(*ast.Element)
	if li.Tag != "li" {
		t.Fatalf("body = %#v", n.Body)
	}
}

func TestParseForCStyle(t *testing.T) {
	p := testParser(`{ for i := 0; i < n; i++ { <x/> } }`)
	node, _, err := p.parseBraceNode()
	if err != nil {
		t.Fatal(err)
	}
	n := node.(*ast.ForMarkup)
	if n.Clause != "i := 0; i < n; i++" {
		t.Fatalf("Clause = %q", n.Clause)
	}
}

func TestParseForWithGoBlockInside(t *testing.T) {
	p := testParser(`{ for i := range xs { {{ v := g(i) }}<a>{v}</a> } }`)
	node, _, err := p.parseBraceNode()
	if err != nil {
		t.Fatal(err)
	}
	n := node.(*ast.ForMarkup)
	var sawGoBlock bool
	for _, m := range n.Body {
		if _, ok := m.(*ast.GoBlock); ok {
			sawGoBlock = true
		}
	}
	if !sawGoBlock {
		t.Fatalf("expected a GoBlock inside the for body, got %#v", n.Body)
	}
}

func TestParseSwitch(t *testing.T) {
	src := `{ switch kind {
		case "warning":
			<span>warn</span>
		case "error":
			<span>err</span>
		default:
			<span>info</span>
		} }`
	p := testParser(src)
	node, _, err := p.parseBraceNode()
	if err != nil {
		t.Fatal(err)
	}
	n, ok := node.(*ast.SwitchMarkup)
	if !ok {
		t.Fatalf("got %T, want *ast.SwitchMarkup", node)
	}
	if n.Tag != "kind" {
		t.Fatalf("Tag = %q", n.Tag)
	}
	if len(n.Cases) != 3 {
		t.Fatalf("got %d cases, want 3: %#v", len(n.Cases), n.Cases)
	}
	if n.Cases[0].List != `"warning"` || n.Cases[0].Default {
		t.Fatalf("case0 = %#v", n.Cases[0])
	}
	if !n.Cases[2].Default || n.Cases[2].List != "" {
		t.Fatalf("case2 (default) = %#v", n.Cases[2])
	}
	if n.Cases[1].Body[0].(*ast.Element).Tag != "span" {
		t.Fatalf("case1 body = %#v", n.Cases[1].Body)
	}
}

func TestParseSwitchTagless(t *testing.T) {
	src := `{ switch {
		case x > 0:
			<a/>
		} }`
	p := testParser(src)
	node, _, err := p.parseBraceNode()
	if err != nil {
		t.Fatal(err)
	}
	n := node.(*ast.SwitchMarkup)
	if n.Tag != "" {
		t.Fatalf("Tag = %q, want empty", n.Tag)
	}
	if n.Cases[0].List != "x > 0" {
		t.Fatalf("case list = %q", n.Cases[0].List)
	}
}

func TestParseCondAttr(t *testing.T) {
	// One-off conditional attribute on a tag.
	p := testParser(`<input { if id != "" { id={id} } }/>`)
	node, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := node.(*ast.Element)
	if len(el.Attrs) != 1 {
		t.Fatalf("got %d attrs, want 1: %#v", len(el.Attrs), el.Attrs)
	}
	ca, ok := el.Attrs[0].(*ast.CondAttr)
	if !ok {
		t.Fatalf("attr0 = %T, want *ast.CondAttr", el.Attrs[0])
	}
	if ca.Cond != `id != ""` {
		t.Fatalf("Cond = %q", ca.Cond)
	}
	if len(ca.Then) != 1 {
		t.Fatalf("Then = %#v", ca.Then)
	}
	ea, ok := ca.Then[0].(*ast.ExprAttr)
	if !ok || ea.Name != "id" || ea.Expr != "id" {
		t.Fatalf("Then[0] = %#v", ca.Then[0])
	}
}

func TestParseCondAttrBool(t *testing.T) {
	p := testParser(`<input { if required { required } }/>`)
	node, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	ca := node.(*ast.Element).Attrs[0].(*ast.CondAttr)
	if _, ok := ca.Then[0].(*ast.BoolAttr); !ok {
		t.Fatalf("Then[0] = %T, want *ast.BoolAttr", ca.Then[0])
	}
}

func TestParseCondAttrWithOtherAttrs(t *testing.T) {
	// Conditional attr composes with normal attrs and a spread on one element.
	p := testParser(`<button type="button" { if on { disabled } } { rest... }>x</button>`)
	node, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := node.(*ast.Element)
	if len(el.Attrs) != 3 {
		t.Fatalf("got %d attrs, want 3: %#v", len(el.Attrs), el.Attrs)
	}
	if _, ok := el.Attrs[0].(*ast.StaticAttr); !ok {
		t.Fatalf("attr0 = %T", el.Attrs[0])
	}
	if _, ok := el.Attrs[1].(*ast.CondAttr); !ok {
		t.Fatalf("attr1 = %T", el.Attrs[1])
	}
	if _, ok := el.Attrs[2].(*ast.SpreadAttr); !ok {
		t.Fatalf("attr2 = %T", el.Attrs[2])
	}
}

func TestParseComposedClass(t *testing.T) {
	src := `<a class={
		"group flex gap-x-3",
		variantClass(v),
		"bg-active": isActive,
		"text-muted": !isActive,
		class,
	}></a>`
	p := testParser(src)
	node, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := node.(*ast.Element)
	ca, ok := el.Attrs[0].(*ast.ClassAttr)
	if !ok {
		t.Fatalf("attr0 = %T, want *ast.ClassAttr", el.Attrs[0])
	}
	if ca.Name != "class" {
		t.Fatalf("Name = %q", ca.Name)
	}
	want := []ast.ClassPart{
		{Expr: `"group flex gap-x-3"`},
		{Expr: `variantClass(v)`},
		{Expr: `"bg-active"`, Cond: "isActive"},
		{Expr: `"text-muted"`, Cond: "!isActive"},
		{Expr: `class`},
	}
	if !reflect.DeepEqual(ca.Parts, want) {
		t.Fatalf("Parts:\n got %#v\nwant %#v", ca.Parts, want)
	}
}

func TestParseComposedStyleSingle(t *testing.T) {
	// style={ … } with one part; no trailing comma.
	p := testParser(`<div style={ "color: red" }></div>`)
	node, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	ca := node.(*ast.Element).Attrs[0].(*ast.ClassAttr)
	if ca.Name != "style" || len(ca.Parts) != 1 || ca.Parts[0].Expr != `"color: red"` {
		t.Fatalf("got %#v", ca.Parts)
	}
}

func TestComposedColonInsideBracketsIsOneExpr(t *testing.T) {
	// A ':' inside a Go index/slice expr must NOT split expr:cond.
	p := testParser(`<a class={ m[k], s[1:2] }></a>`)
	node, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	ca := node.(*ast.Element).Attrs[0].(*ast.ClassAttr)
	want := []ast.ClassPart{{Expr: "m[k]"}, {Expr: "s[1:2]"}}
	if !reflect.DeepEqual(ca.Parts, want) {
		t.Fatalf("Parts = %#v, want %#v", ca.Parts, want)
	}
}

func TestNonClassBraceStaysExprAttr(t *testing.T) {
	// A non-class/style attribute with a brace value is still an ExprAttr.
	p := testParser(`<input value={x}/>`)
	node, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := node.(*ast.Element).Attrs[0].(*ast.ExprAttr); !ok {
		t.Fatalf("attr0 = %T, want *ast.ExprAttr", node.(*ast.Element).Attrs[0])
	}
}

func TestParseForRangeSliceLiteral(t *testing.T) {
	// I2: ranging over a bare composite literal — the literal's '{' must NOT be
	// taken as the body brace.
	p := testParser(`{ for _, v := range []int{1, 2} { <a>{v}</a> } }`)
	node, _, err := p.parseBraceNode()
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	n, ok := node.(*ast.ForMarkup)
	if !ok {
		t.Fatalf("got %T, want *ast.ForMarkup", node)
	}
	if n.Clause != "_, v := range []int{1, 2}" {
		t.Fatalf("Clause = %q", n.Clause)
	}
	if n.Body[0].(*ast.Element).Tag != "a" {
		t.Fatalf("body = %#v", n.Body)
	}
}

func TestParseForRangeMapLiteral(t *testing.T) {
	p := testParser(`{ for k := range map[string]int{"a": 1} { <i>{k}</i> } }`)
	node, _, err := p.parseBraceNode()
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	n := node.(*ast.ForMarkup)
	if n.Clause != `k := range map[string]int{"a": 1}` {
		t.Fatalf("Clause = %q", n.Clause)
	}
}

func TestParseIfParenComposite(t *testing.T) {
	// Paren-wrapped composite in an if condition still resolves to the body brace.
	p := testParser(`{ if (struct{ Ok bool }{Ok: true}).Ok { <y/> } }`)
	node, _, err := p.parseBraceNode()
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	n := node.(*ast.IfMarkup)
	if n.Then[0].(*ast.Element).Tag != "y" {
		t.Fatalf("then = %#v", n.Then)
	}
}

func TestParseInterpPipeline(t *testing.T) {
	f := parseStringT(t, "package p\ncomponent C() { <p>{ name |> upper |> truncate(20) }</p> }\n")
	var interp *ast.Interp
	ast.Inspect(f, func(n ast.Node) bool {
		if i, ok := n.(*ast.Interp); ok {
			interp = i
		}
		return true
	})
	if interp == nil {
		t.Fatal("no Interp found")
	}
	if interp.Expr != "name" {
		t.Errorf("seed = %q, want \"name\"", interp.Expr)
	}
	want := []ast.PipeStage{{Name: "upper"}, {Name: "truncate", Args: "20", HasArgs: true}}
	if !reflect.DeepEqual(interp.Stages, want) {
		t.Errorf("stages = %#v, want %#v", interp.Stages, want)
	}
}

func TestParseAttrPipeline(t *testing.T) {
	f := parseStringT(t, "package p\ncomponent C() { <a href={ u |> absolute }>x</a> }\n")
	var ea *ast.ExprAttr
	ast.Inspect(f, func(n ast.Node) bool {
		if a, ok := n.(*ast.ExprAttr); ok {
			ea = a
		}
		return true
	})
	if ea == nil || ea.Expr != "u" || len(ea.Stages) != 1 || ea.Stages[0].Name != "absolute" {
		t.Fatalf("ExprAttr pipeline not parsed: %#v", ea)
	}
}

// --- DOCTYPE -----------------------------------------------------------------

func TestParseDoctype(t *testing.T) {
	p := testParser(`<!DOCTYPE html>rest`)
	node, err := p.parseElement()
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	d, ok := node.(*ast.Doctype)
	if !ok {
		t.Fatalf("got %T, want *ast.Doctype", node)
	}
	if d.Text != "<!DOCTYPE html>" {
		t.Fatalf("Text = %q", d.Text)
	}
	if p.src[p.i:] != "rest" {
		t.Fatalf("cursor at %q", p.src[p.i:])
	}
}

func TestParseDoctypeCaseInsensitive(t *testing.T) {
	p := testParser(`<!doctype HTML>`)
	node, err := p.parseElement()
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	d, ok := node.(*ast.Doctype)
	if !ok {
		t.Fatalf("got %T, want *ast.Doctype", node)
	}
	if d.Text != "<!doctype HTML>" {
		t.Fatalf("Text = %q", d.Text)
	}
}

func TestParseDoctypeUnterminated(t *testing.T) {
	p := testParser(`<!DOCTYPE html`)
	_, err := p.parseElement()
	if err == nil {
		t.Fatal("expected error for unterminated DOCTYPE")
	}
	if !strings.Contains(err.Error(), "unterminated") {
		t.Fatalf("error = %v", err)
	}
}

// --- HTML comments -----------------------------------------------------------

func TestParseHTMLComment(t *testing.T) {
	p := testParser(`<!-- keep me -->rest`)
	node, err := p.parseElement()
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	c, ok := node.(*ast.HTMLComment)
	if !ok {
		t.Fatalf("got %T, want *ast.HTMLComment", node)
	}
	if c.Text != " keep me " {
		t.Fatalf("Text = %q", c.Text)
	}
	if p.src[p.i:] != "rest" {
		t.Fatalf("cursor at %q", p.src[p.i:])
	}
}

func TestParseHTMLCommentWithMarkupLikeBody(t *testing.T) {
	p := testParser(`<!-- <div> {x} not parsed -->`)
	node, err := p.parseElement()
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	c := node.(*ast.HTMLComment)
	if c.Text != " <div> {x} not parsed " {
		t.Fatalf("Text = %q", c.Text)
	}
}

func TestParseHTMLCommentUnterminated(t *testing.T) {
	p := testParser(`<!-- never closed`)
	_, err := p.parseElement()
	if err == nil {
		t.Fatal("expected error for unterminated comment")
	}
	if !strings.Contains(err.Error(), "unterminated") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseBangUnknown(t *testing.T) {
	p := testParser(`<!bogus>`)
	_, err := p.parseElement()
	if err == nil {
		t.Fatal("expected error for `<!` followed by unknown")
	}
}

// --- raw-text elements -------------------------------------------------------

func TestParseScriptRaw(t *testing.T) {
	p := testParser(`<script>const a = {x: 1}; if (a < b) {} // <div></script>after`)
	node, err := p.parseElement()
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	el := node.(*ast.Element)
	if el.Tag != "script" {
		t.Fatalf("Tag = %q", el.Tag)
	}
	if len(el.Children) != 1 {
		t.Fatalf("children = %#v", el.Children)
	}
	txt := el.Children[0].(*ast.Text)
	if txt.Value != `const a = {x: 1}; if (a < b) {} // <div>` {
		t.Fatalf("raw = %q", txt.Value)
	}
	if p.src[p.i:] != "after" {
		t.Fatalf("cursor at %q", p.src[p.i:])
	}
}

func TestParseStyleRaw(t *testing.T) {
	p := testParser(`<style>.a { color: red } .b > .c {}</style>`)
	node, err := p.parseElement()
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	el := node.(*ast.Element)
	if el.Tag != "style" {
		t.Fatalf("Tag = %q", el.Tag)
	}
	txt := el.Children[0].(*ast.Text)
	if txt.Value != ".a { color: red } .b > .c {}" {
		t.Fatalf("raw = %q", txt.Value)
	}
}

func TestParseScriptCaseInsensitiveClose(t *testing.T) {
	p := testParser(`<SCRIPT>raw</SCRIPT>`)
	node, err := p.parseElement()
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	el := node.(*ast.Element)
	if el.Children[0].(*ast.Text).Value != "raw" {
		t.Fatalf("raw = %q", el.Children[0])
	}
}

func TestParseScriptWithAttr(t *testing.T) {
	p := testParser(`<script nonce={n}>var x = 1;</script>`)
	node, err := p.parseElement()
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	el := node.(*ast.Element)
	if len(el.Attrs) != 1 {
		t.Fatalf("attrs = %#v", el.Attrs)
	}
	if el.Children[0].(*ast.Text).Value != "var x = 1;" {
		t.Fatalf("raw = %q", el.Children[0])
	}
}

func TestParseScriptSelfClosing(t *testing.T) {
	p := testParser(`<script src="x.js"/>after`)
	node, err := p.parseElement()
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	el := node.(*ast.Element)
	if !el.Void || len(el.Children) != 0 {
		t.Fatalf("got %#v", el)
	}
	if p.src[p.i:] != "after" {
		t.Fatalf("cursor at %q", p.src[p.i:])
	}
}

func TestParseScriptUnterminated(t *testing.T) {
	p := testParser(`<script>never closed`)
	_, err := p.parseElement()
	if err == nil {
		t.Fatal("expected error for unterminated raw element")
	}
	if !strings.Contains(err.Error(), "unterminated") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseScriptEmpty(t *testing.T) {
	p := testParser(`<script></script>`)
	node, err := p.parseElement()
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	el := node.(*ast.Element)
	if len(el.Children) != 0 {
		t.Fatalf("children = %#v", el.Children)
	}
}

func TestStyleInterpolation(t *testing.T) {
	src := `<style>.a{width:@{w}px}</style>`
	p := testParser(src)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Children) != 3 {
		t.Fatalf("got %d children, want 3: %#v", len(el.Children), el.Children)
	}
	if txt, ok := el.Children[0].(*ast.Text); !ok || txt.Value != ".a{width:" {
		t.Fatalf("child0 = %#v, want Text \".a{width:\"", el.Children[0])
	}
	if in, ok := el.Children[1].(*ast.Interp); !ok || in.Expr != "w" {
		t.Fatalf("child1 = %#v, want Interp{Expr:w}", el.Children[1])
	}
	if txt, ok := el.Children[2].(*ast.Text); !ok || txt.Value != "px}" {
		t.Fatalf("child2 = %#v, want Text \"px}\"", el.Children[2])
	}
}

func TestScriptAtBraceTriggers(t *testing.T) {
	// <script> now interpolates @{ … } just like <style>.
	// "let x=@{ y }" → Text{"let x="}, Interp{Expr:"y"}, Text{""}
	// The trailing empty segment is NOT emitted because flush only appends when
	// end > segStart (markup.go:672-678); after the interp segStart advances to
	// the start of "</script>", so flush(p.i) finds end==segStart → no Text.
	src := `<script>let x=@{ y }</script>`
	p := testParser(src)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Children) != 2 {
		t.Fatalf("got %d children, want 2: %#v", len(el.Children), el.Children)
	}
	if txt, ok := el.Children[0].(*ast.Text); !ok || txt.Value != "let x=" {
		t.Fatalf("child0 = %#v, want Text \"let x=\"", el.Children[0])
	}
	if in, ok := el.Children[1].(*ast.Interp); !ok || in.Expr != "y" {
		t.Fatalf("child1 = %#v, want Interp{Expr:\"y\"}", el.Children[1])
	}
}

func TestScriptBareAtIsLiteral(t *testing.T) {
	// A bare '@' not immediately followed by '{' stays literal in <script>.
	src := `<script>a @ b</script>`
	p := testParser(src)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Children) != 1 {
		t.Fatalf("got %d children, want 1 (all literal): %#v", len(el.Children), el.Children)
	}
	if txt, ok := el.Children[0].(*ast.Text); !ok || txt.Value != "a @ b" {
		t.Fatalf("child0 = %#v, want Text \"a @ b\"", el.Children[0])
	}
}

func TestScriptNoInterpolationWithTrailingText(t *testing.T) {
	// Interp in the middle of script content: trailing text IS emitted.
	src := `<script>var x = @{y};</script>`
	p := testParser(src)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Children) != 3 {
		t.Fatalf("got %d children, want 3: %#v", len(el.Children), el.Children)
	}
	if txt, ok := el.Children[0].(*ast.Text); !ok || txt.Value != "var x = " {
		t.Fatalf("child0 = %#v, want Text \"var x = \"", el.Children[0])
	}
	if in, ok := el.Children[1].(*ast.Interp); !ok || in.Expr != "y" {
		t.Fatalf("child1 = %#v, want Interp{Expr:\"y\"}", el.Children[1])
	}
	if txt, ok := el.Children[2].(*ast.Text); !ok || txt.Value != ";" {
		t.Fatalf("child2 = %#v, want Text \";\"", el.Children[2])
	}
}

func TestStyleBareDollarIsLiteral(t *testing.T) {
	// A '$' not immediately followed by '{' stays raw, as do bare { } #.
	src := `<style>.a{c:$x; #d{} }</style>`
	p := testParser(src)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Children) != 1 {
		t.Fatalf("got %d children, want 1 (all literal): %#v", len(el.Children), el.Children)
	}
	if txt := el.Children[0].(*ast.Text); txt.Value != ".a{c:$x; #d{} }" {
		t.Fatalf("text = %q", txt.Value)
	}
}

func TestStyleBareAtIsLiteral(t *testing.T) {
	// A bare '@' not immediately followed by '{' stays literal (mirrors the trigger's
	// {-adjacency requirement): at-rules (@media), bare @, and @ident are all CSS text.
	src := `<style>@media (x){ .a{c:1} } a@b @ c</style>`
	p := testParser(src)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Children) != 1 {
		t.Fatalf("got %d children, want 1 (all literal): %#v", len(el.Children), el.Children)
	}
	if txt := el.Children[0].(*ast.Text); txt.Value != `@media (x){ .a{c:1} } a@b @ c` {
		t.Fatalf("text = %q, want verbatim incl. bare @ / @media", txt.Value)
	}
}

func TestStyleUnterminatedInterp(t *testing.T) {
	// Note: brief listed `@{w}` (with closing brace) which is valid; corrected to
	// `@{w` (no closing brace) so the interpolation is genuinely unterminated.
	src := `<style>.a{w:@{w</style>`
	p := testParser(src)
	if _, err := p.parseElement(); err == nil {
		t.Fatal("expected an error for unterminated interpolation, got nil")
	}
}

func TestStyleAtBraceTriggers(t *testing.T) {
	src := `<style>.a{width:@{w}px}</style>`
	p := testParser(src)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Children) != 3 {
		t.Fatalf("got %d children, want 3: %#v", len(el.Children), el.Children)
	}
	if in, ok := el.Children[1].(*ast.Interp); !ok || in.Expr != "w" {
		t.Fatalf("child1 = %#v, want Interp{Expr:w}", el.Children[1])
	}
}

func TestStyleDollarBraceIsNowLiteral(t *testing.T) {
	// After the migration, ${ … } inside <style> is plain CSS text, not an interp.
	src := `<style>.a{content:"${ w }"}</style>`
	p := testParser(src)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	el := n.(*ast.Element)
	if len(el.Children) != 1 {
		t.Fatalf("got %d children, want 1 (all literal): %#v", len(el.Children), el.Children)
	}
	if txt := el.Children[0].(*ast.Text); txt.Value != `.a{content:"${ w }"}` {
		t.Fatalf("text = %q, want it verbatim incl. ${ }", txt.Value)
	}
}

// parseSingleElemAttrs parses `<div ...>` and returns the element's attrs.
func parseSingleElemAttrs(t *testing.T, src string) []ast.Attr {
	t.Helper()
	p := testParser(src)
	n, err := p.parseElement()
	if err != nil {
		t.Fatal(err)
	}
	return n.(*ast.Element).Attrs
}

func TestJSAttrSplitsHoles(t *testing.T) {
	attrs := parseSingleElemAttrs(t, `<div x-data="{ a: @{ x } }"></div>`)
	if len(attrs) != 1 {
		t.Fatalf("got %d attrs, want 1: %#v", len(attrs), attrs)
	}
	a, ok := attrs[0].(*ast.JSAttr)
	if !ok || a.Name != "x-data" {
		t.Fatalf("attr0 = %#v, want JSAttr{x-data}", attrs[0])
	}
	if len(a.Segments) != 3 {
		t.Fatalf("got %d segments: %#v", len(a.Segments), a.Segments)
	}
	if txt, ok := a.Segments[0].(*ast.Text); !ok || txt.Value != "{ a: " {
		t.Fatalf("seg0 = %#v, want Text %q", a.Segments[0], "{ a: ")
	}
	if in, ok := a.Segments[1].(*ast.Interp); !ok || in.Expr != "x" {
		t.Fatalf("seg1 = %#v, want Interp{x}", a.Segments[1])
	}
	if txt, ok := a.Segments[2].(*ast.Text); !ok || txt.Value != " }" {
		t.Fatalf("seg2 = %#v, want Text %q", a.Segments[2], " }")
	}
}

func TestJSAttrOnclick(t *testing.T) {
	attrs := parseSingleElemAttrs(t, `<div onclick="alert(@{ n })"></div>`)
	a, ok := attrs[0].(*ast.JSAttr)
	if !ok || a.Name != "onclick" {
		t.Fatalf("attr0 = %#v, want JSAttr{onclick}", attrs[0])
	}
	if len(a.Segments) != 3 {
		t.Fatalf("got %d segments: %#v", len(a.Segments), a.Segments)
	}
	if txt, ok := a.Segments[0].(*ast.Text); !ok || txt.Value != "alert(" {
		t.Fatalf("seg0 = %#v, want Text %q", a.Segments[0], "alert(")
	}
	if in, ok := a.Segments[1].(*ast.Interp); !ok || in.Expr != "n" {
		t.Fatalf("seg1 = %#v, want Interp{n}", a.Segments[1])
	}
	if txt, ok := a.Segments[2].(*ast.Text); !ok || txt.Value != ")" {
		t.Fatalf("seg2 = %#v, want Text %q", a.Segments[2], ")")
	}
}

func TestNonJSAttrHoleStaysLiteral(t *testing.T) {
	attrs := parseSingleElemAttrs(t, `<div title="a@{b}"></div>`)
	a, ok := attrs[0].(*ast.StaticAttr)
	if !ok || a.Name != "title" || a.Value != "a@{b}" {
		t.Fatalf("attr0 = %#v, want StaticAttr{title, a@{b}}", attrs[0])
	}
}

func TestJSAttrNoHoleStaysStatic(t *testing.T) {
	attrs := parseSingleElemAttrs(t, `<div x-data="{ open: false }"></div>`)
	a, ok := attrs[0].(*ast.StaticAttr)
	if !ok || a.Name != "x-data" || a.Value != "{ open: false }" {
		t.Fatalf("attr0 = %#v, want StaticAttr{x-data, { open: false }}", attrs[0])
	}
}

func TestJSAttrHoleWithInnerStringLiteral(t *testing.T) {
	attrs := parseSingleElemAttrs(t, `<div x-data="{ s: @{ "v" } }"></div>`)
	a, ok := attrs[0].(*ast.JSAttr)
	if !ok || a.Name != "x-data" {
		t.Fatalf("attr0 = %#v, want JSAttr{x-data}", attrs[0])
	}
	if len(a.Segments) != 3 {
		t.Fatalf("got %d segments: %#v", len(a.Segments), a.Segments)
	}
	in, ok := a.Segments[1].(*ast.Interp)
	if !ok || in.Expr != `"v"` {
		t.Fatalf("seg1 = %#v, want Interp{\"v\"}", a.Segments[1])
	}
	if txt, ok := a.Segments[2].(*ast.Text); !ok || txt.Value != " }" {
		t.Fatalf("seg2 = %#v, want trailing Text %q", a.Segments[2], " }")
	}
}
