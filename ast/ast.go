// Package ast defines the gsx syntax tree produced by the parser.
package ast

import "go/token"

// Span records the start and end positions of a node within a token.FileSet.
// Embed Span in every concrete node to satisfy the Node interface automatically.
type Span struct {
	Start  token.Pos
	Finish token.Pos
}

// Pos returns the position of the first character of the node.
func (s Span) Pos() token.Pos { return s.Start }

// End returns the position one past the last character of the node.
func (s Span) End() token.Pos { return s.Finish }

// Node is the universal base interface for every AST node.
// All concrete types (File, GoChunk, Component, Element, Fragment, Text,
// Interp, StaticAttr, ExprAttr, BoolAttr, SpreadAttr, MarkupAttr) implement Node
// by embedding Span.
type Node interface {
	Pos() token.Pos
	End() token.Pos
}

// Markup is the interface for markup nodes (Element, Fragment, Text, Interp).
// It refines Node with a sealed marker. This replaces the old "Node" markup interface.
type Markup interface {
	Node
	markupNode()
}

// Decl is a top-level declaration: opaque Go source or a component.
type Decl interface {
	Node
	declNode()
}

// Attr is one attribute on an element.
type Attr interface {
	Node
	attrNode()
}

// File is a parsed .gsx file.
type File struct {
	Span
	Package string
	Decls   []Decl
}

// GoChunk is a verbatim span of Go source (imports, types, consts, vars, funcs)
// copied through unchanged.
type GoChunk struct {
	Span
	Src string
}

func (*GoChunk) declNode() {}

// Component is a `component [recv] Name(params) { body }` declaration.
type Component struct {
	Span
	Recv   string // e.g. "(p UsersPage)" or "(f *Form)"; "" if none
	Name   string
	Params string // raw param-list source, e.g. "title string, featured bool"; "" if none
	Body   []Markup
}

func (*Component) declNode() {}

// Element is an HTML element or a component tag (Tag may be dotted, e.g. "ui.Button").
type Element struct {
	Span
	Tag      string
	Void     bool // self-closing <tag/> or HTML void element
	Attrs    []Attr
	Children []Markup
}

func (*Element) markupNode() {}

// Fragment is <>…</> — siblings without a wrapper.
type Fragment struct {
	Span
	Children []Markup
}

func (*Fragment) markupNode() {}

// Text is literal character data between markup.
type Text struct {
	Span
	Value string
}

func (*Text) markupNode() {}

// Interp is `{ expr }` (Try=false) or `{ expr? }` (Try=true).
type Interp struct {
	Span
	Expr string
	Try  bool
}

func (*Interp) markupNode() {}

// StaticAttr is name="value".
type StaticAttr struct {
	Span
	Name, Value string
}

func (*StaticAttr) attrNode() {}

// ExprAttr is name={expr} or name={expr?}.
type ExprAttr struct {
	Span
	Name, Expr string
	Try        bool
}

func (*ExprAttr) attrNode() {}

// BoolAttr is a bare attribute name (boolean true).
type BoolAttr struct {
	Span
	Name string
}

func (*BoolAttr) attrNode() {}

// SpreadAttr is {...expr}.
type SpreadAttr struct {
	Span
	Expr string
}

func (*SpreadAttr) attrNode() {}

// MarkupAttr is name={ <markup/> } — markup passed as an attribute value.
type MarkupAttr struct {
	Span
	Name  string
	Value []Markup
}

func (*MarkupAttr) attrNode() {}

// Inspect traverses the AST in depth-first order, calling f for each node.
// If f returns false, Inspect does not recurse into that node's children.
// After recursing into children, Inspect calls f(nil) for go/ast parity.
// Children by type:
//   - *File: each Decl
//   - *Component: each Body markup node
//   - *Element: each Attr, then each Child
//   - *Fragment: each Child
//   - *MarkupAttr: each Value markup node
//   - all other nodes: leaves (no children)
func Inspect(node Node, f func(Node) bool) {
	if !f(node) {
		return
	}
	switch n := node.(type) {
	case *File:
		for _, d := range n.Decls {
			Inspect(d, f)
		}
	case *Component:
		for _, m := range n.Body {
			Inspect(m, f)
		}
	case *Element:
		for _, a := range n.Attrs {
			Inspect(a, f)
		}
		for _, c := range n.Children {
			Inspect(c, f)
		}
	case *Fragment:
		for _, c := range n.Children {
			Inspect(c, f)
		}
	case *MarkupAttr:
		for _, m := range n.Value {
			Inspect(m, f)
		}
	// GoChunk, Text, Interp, StaticAttr, ExprAttr, BoolAttr, SpreadAttr: leaves
	}
	f(nil)
}
