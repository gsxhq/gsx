package parser

import (
	"go/token"
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
	if n.Expr != "user.Name" || n.Try {
		t.Fatalf("got %+v", n)
	}
	if p.src[p.i:] != "rest" {
		t.Fatalf("cursor at %q", p.src[p.i:])
	}
}

func TestParseInterpTry(t *testing.T) {
	p := testParser(`{ route.URL(ctx)? }`)
	n, err := p.parseInterp()
	if err != nil {
		t.Fatal(err)
	}
	if n.Expr != "route.URL(ctx)" || !n.Try {
		t.Fatalf("got %+v", n)
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
	p := testParser(`class="card" id={x} disabled {...rest} data-y={z?}>`)
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
	if a, ok := attrs[4].(*ast.ExprAttr); !ok || a.Name != "data-y" || !a.Try {
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
	// {...expr}, { ...expr }, and {...  expr  } must all parse as one spread.
	for _, src := range []string{`{...rest}>`, `{ ...rest }>`, `{...  rest  }>`} {
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
