package parser

import (
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

// nonWhitespaceChildren filters out *ast.Text nodes that are entirely
// whitespace, so tests can assert the meaningful node sequence without
// hard-coding incidental indentation.
func nonWhitespaceChildren(nodes []ast.Markup) []ast.Markup {
	var out []ast.Markup
	for _, n := range nodes {
		if t, ok := n.(*ast.Text); ok && strings.TrimSpace(t.Value) == "" {
			continue
		}
		out = append(out, n)
	}
	return out
}

// firstComment finds the first *ast.Comment in a node slice, failing the test
// if none is present.
func firstComment(t *testing.T, nodes []ast.Markup) *ast.Comment {
	t.Helper()
	for _, n := range nodes {
		if c, ok := n.(*ast.Comment); ok {
			return c
		}
	}
	t.Fatalf("no *ast.Comment found in %#v", nodes)
	return nil
}

// containsCommentNode reports whether a Comment node appears anywhere in the
// subtree rooted at nodes (recursing into Element children only — sufficient
// for the pre/textarea verbatim fixtures below).
func containsCommentNode(nodes []ast.Markup) bool {
	for _, n := range nodes {
		switch v := n.(type) {
		case *ast.Comment:
			return true
		case *ast.Element:
			if containsCommentNode(v.Children) {
				return true
			}
		}
	}
	return false
}

func TestBareCommentBetweenTags(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<div>\n\t\t<span>a</span>\n\t\t// note\n\t\t<span>b</span>\n\t</div>\n}\n"
	f := parseStringT(t, src)
	comp := f.Decls[0].(*ast.Component)
	div := comp.Body[0].(*ast.Element)
	if div.Tag != "div" {
		t.Fatalf("body[0] tag = %q, want div", div.Tag)
	}
	kids := nonWhitespaceChildren(div.Children)
	if len(kids) != 3 {
		t.Fatalf("got %d non-whitespace children, want 3: %#v", len(kids), kids)
	}
	span0, ok := kids[0].(*ast.Element)
	if !ok || span0.Tag != "span" {
		t.Fatalf("child0 = %#v, want span element", kids[0])
	}
	c, ok := kids[1].(*ast.Comment)
	if !ok || !c.Bare || c.Block || c.Text != "note" {
		t.Fatalf("child1 = %#v, want Comment{Bare:true, Text:\"note\"}", kids[1])
	}
	span1, ok := kids[2].(*ast.Element)
	if !ok || span1.Tag != "span" {
		t.Fatalf("child2 = %#v, want span element", kids[2])
	}
}

func TestBareCommentSplitsTextRun(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<p>\n\t\thello\n\t\t// note\n\t\tworld\n\t</p>\n}\n"
	f := parseStringT(t, src)
	comp := f.Decls[0].(*ast.Component)
	p := comp.Body[0].(*ast.Element)
	if len(p.Children) != 3 {
		t.Fatalf("got %d children, want 3: %#v", len(p.Children), p.Children)
	}
	t0, ok := p.Children[0].(*ast.Text)
	if !ok || t0.Value != "\n\t\thello\n\t\t" {
		t.Fatalf("child0 = %#v, want Text(%q)", p.Children[0], "\n\t\thello\n\t\t")
	}
	c, ok := p.Children[1].(*ast.Comment)
	if !ok || !c.Bare || c.Block || c.Text != "note" {
		t.Fatalf("child1 = %#v, want Comment{Bare:true, Text:\"note\"}", p.Children[1])
	}
	t1, ok := p.Children[2].(*ast.Text)
	if !ok || t1.Value != "\n\t\tworld\n\t" {
		t.Fatalf("child2 = %#v, want Text(%q)", p.Children[2], "\n\t\tworld\n\t")
	}
}

func TestMidLineSlashesStayText(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<p>hello // world</p>\n}\n"
	f := parseStringT(t, src)
	comp := f.Decls[0].(*ast.Component)
	p := comp.Body[0].(*ast.Element)
	if len(p.Children) != 1 {
		t.Fatalf("got %d children, want 1: %#v", len(p.Children), p.Children)
	}
	txt, ok := p.Children[0].(*ast.Text)
	if !ok || txt.Value != "hello // world" {
		t.Fatalf("child0 = %#v, want Text(\"hello // world\")", p.Children[0])
	}
}

func TestTagOnSameLineStaysText(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<p>// hi</p>\n}\n"
	f := parseStringT(t, src)
	comp := f.Decls[0].(*ast.Component)
	p := comp.Body[0].(*ast.Element)
	if len(p.Children) != 1 {
		t.Fatalf("got %d children, want 1: %#v", len(p.Children), p.Children)
	}
	txt, ok := p.Children[0].(*ast.Text)
	if !ok || txt.Value != "// hi" {
		t.Fatalf("child0 = %#v, want Text(\"// hi\")", p.Children[0])
	}
}

func TestPreSubtreeVerbatim(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<pre><code>\n// display this\nx\n</code></pre>\n}\n"
	f := parseStringT(t, src)
	comp := f.Decls[0].(*ast.Component)
	pre := comp.Body[0].(*ast.Element)
	if pre.Tag != "pre" {
		t.Fatalf("body[0] tag = %q, want pre", pre.Tag)
	}
	if containsCommentNode(pre.Children) {
		t.Fatalf("found *ast.Comment under <pre>, want none: %#v", pre.Children)
	}
	if len(pre.Children) != 1 {
		t.Fatalf("pre.Children = %#v, want 1 <code> element", pre.Children)
	}
	code, ok := pre.Children[0].(*ast.Element)
	if !ok || code.Tag != "code" {
		t.Fatalf("pre.Children[0] = %#v, want <code>", pre.Children[0])
	}
	if len(code.Children) != 1 {
		t.Fatalf("code.Children = %#v, want 1 verbatim Text", code.Children)
	}
	txt, ok := code.Children[0].(*ast.Text)
	if !ok || txt.Value != "\n// display this\nx\n" {
		t.Fatalf("code.Children[0] = %#v, want verbatim Text", code.Children[0])
	}
}

func TestTextareaVerbatim(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<textarea>\n// literal\n</textarea>\n}\n"
	f := parseStringT(t, src)
	comp := f.Decls[0].(*ast.Component)
	ta := comp.Body[0].(*ast.Element)
	if ta.Tag != "textarea" {
		t.Fatalf("body[0] tag = %q, want textarea", ta.Tag)
	}
	if containsCommentNode(ta.Children) {
		t.Fatalf("found *ast.Comment under <textarea>, want none: %#v", ta.Children)
	}
	if len(ta.Children) != 1 {
		t.Fatalf("ta.Children = %#v, want 1 verbatim Text", ta.Children)
	}
	txt, ok := ta.Children[0].(*ast.Text)
	if !ok || txt.Value != "\n// literal\n" {
		t.Fatalf("ta.Children[0] = %#v, want verbatim Text", ta.Children[0])
	}
}

func TestBareCommentInControlBody(t *testing.T) {
	src := "package p\n\ncomponent C(xs []string) {\n\t<ul>\n\t\t{ for _, x := range xs {\n\t\t\t// per-item note\n\t\t\t<li>{x}</li>\n\t\t} }\n\t</ul>\n}\n"
	f := parseStringT(t, src)
	comp := f.Decls[0].(*ast.Component)
	ul := comp.Body[0].(*ast.Element)
	kids := nonWhitespaceChildren(ul.Children)
	if len(kids) != 1 {
		t.Fatalf("got %d non-whitespace children of <ul>, want 1: %#v", len(kids), kids)
	}
	forNode, ok := kids[0].(*ast.ForMarkup)
	if !ok {
		t.Fatalf("child0 = %#v, want *ast.ForMarkup", kids[0])
	}
	c := firstComment(t, forNode.Body)
	if !c.Bare || c.Block || c.Text != "per-item note" {
		t.Fatalf("comment = %#v, want Comment{Bare:true, Text:\"per-item note\"}", c)
	}
}

func TestBareCommentInCaseBody(t *testing.T) {
	src := "package p\n\ncomponent C(n int) {\n\t<div>\n\t\t{ switch n {\n\t\tcase 1:\n\t\t\t// one\n\t\t\t<b>1</b>\n\t\t} }\n\t</div>\n}\n"
	f := parseStringT(t, src)
	comp := f.Decls[0].(*ast.Component)
	div := comp.Body[0].(*ast.Element)
	kids := nonWhitespaceChildren(div.Children)
	if len(kids) != 1 {
		t.Fatalf("got %d non-whitespace children of <div>, want 1: %#v", len(kids), kids)
	}
	sw, ok := kids[0].(*ast.SwitchMarkup)
	if !ok {
		t.Fatalf("child0 = %#v, want *ast.SwitchMarkup", kids[0])
	}
	if len(sw.Cases) != 1 {
		t.Fatalf("got %d cases, want 1: %#v", len(sw.Cases), sw.Cases)
	}
	c := firstComment(t, sw.Cases[0].Body)
	if !c.Bare || c.Block || c.Text != "one" {
		t.Fatalf("comment = %#v, want Comment{Bare:true, Text:\"one\"}", c)
	}
}

func TestEmptyBareComment(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<div>\n\t\t<span>a</span>\n\t\t//\n\t\t<span>b</span>\n\t</div>\n}\n"
	f := parseStringT(t, src)
	comp := f.Decls[0].(*ast.Component)
	div := comp.Body[0].(*ast.Element)
	kids := nonWhitespaceChildren(div.Children)
	if len(kids) != 3 {
		t.Fatalf("got %d non-whitespace children, want 3: %#v", len(kids), kids)
	}
	c, ok := kids[1].(*ast.Comment)
	if !ok || !c.Bare || c.Block || c.Text != "" {
		t.Fatalf("child1 = %#v, want Comment{Bare:true, Text:\"\"}", kids[1])
	}
}

func TestCRLFLineStart(t *testing.T) {
	src := "package p\r\n\r\ncomponent C() {\r\n\t<div>\r\n\t\t<span>a</span>\r\n\t\t// note\r\n\t\t<span>b</span>\r\n\t</div>\r\n}\r\n"
	f := parseStringT(t, src)
	comp := f.Decls[0].(*ast.Component)
	div := comp.Body[0].(*ast.Element)
	kids := nonWhitespaceChildren(div.Children)
	if len(kids) != 3 {
		t.Fatalf("got %d non-whitespace children, want 3: %#v", len(kids), kids)
	}
	c, ok := kids[1].(*ast.Comment)
	if !ok || !c.Bare || c.Block || c.Text != "note" {
		t.Fatalf("child1 = %#v, want Comment{Bare:true, Text:\"note\"} (no trailing CR)", kids[1])
	}
}

// TestBareCommentInSlotInsidePreserve pins that preserveDepth does not leak
// across the `name={ … }` markup-attribute expression boundary: a slot value
// lexically nested inside a <pre> subtree is its own fresh non-preserve
// context (mirroring wsnorm's normalizeAttrs — internal/wsnorm/wsnorm.go),
// so a bare `//` inside the slot IS recognized as a comment, while <pre>'s
// own direct text children stay verbatim (no comment recognition).
func TestBareCommentInSlotInsidePreserve(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<pre>\n// verbatim\n<div attr={\n\t<span>a</span>\n\t// note\n\t<span>b</span>\n}></div>\n</pre>\n}\n"
	f := parseStringT(t, src)
	comp := f.Decls[0].(*ast.Component)
	pre := comp.Body[0].(*ast.Element)
	if pre.Tag != "pre" {
		t.Fatalf("body[0] tag = %q, want pre", pre.Tag)
	}

	// pre's own direct text children remain verbatim: no Comment node, and
	// the literal "// verbatim" text survives.
	var div *ast.Element
	for _, n := range pre.Children {
		if e, ok := n.(*ast.Element); ok && e.Tag == "div" {
			div = e
			break
		}
	}
	if div == nil {
		t.Fatalf("pre.Children = %#v, want a <div> child", pre.Children)
	}
	sawVerbatim := false
	for _, n := range pre.Children {
		if c, isComment := n.(*ast.Comment); isComment {
			t.Fatalf("found *ast.Comment %#v as a direct child of <pre>, want verbatim text", c)
		}
		if t2, isText := n.(*ast.Text); isText && strings.Contains(t2.Value, "// verbatim") {
			sawVerbatim = true
		}
	}
	if !sawVerbatim {
		t.Fatalf("pre.Children = %#v, want a verbatim Text containing %q", pre.Children, "// verbatim")
	}

	// The slot value (div's attr={...}) IS a fresh context: the bare `//`
	// inside it is recognized as Comment{Bare:true}.
	if len(div.Attrs) != 1 {
		t.Fatalf("div.Attrs = %#v, want 1 attr", div.Attrs)
	}
	ma, ok := div.Attrs[0].(*ast.MarkupAttr)
	if !ok {
		t.Fatalf("div.Attrs[0] = %#v, want *ast.MarkupAttr", div.Attrs[0])
	}
	c := firstComment(t, ma.Value)
	if !c.Bare || c.Block || c.Text != "note" {
		t.Fatalf("slot comment = %#v, want Comment{Bare:true, Text:\"note\"}", c)
	}
}

func TestFileStartLine(t *testing.T) {
	// Line-start scan hitting offset 0 (no preceding newline) must not panic.
	// A // at file start is Go code territory (package clause), so just assert
	// a normal file still parses.
	src := "package p\n\ncomponent C() {\n\t<div></div>\n}\n"
	f := parseStringT(t, src)
	comp := f.Decls[0].(*ast.Component)
	if len(comp.Body) == 0 {
		t.Fatalf("expected non-empty component body")
	}
}
