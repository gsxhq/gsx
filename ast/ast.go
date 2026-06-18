// Package ast defines the gsx syntax tree produced by the parser.
package ast

import "go/token"

// span records the start and end positions of a node within a token.FileSet.
// Embed span in every concrete node to satisfy the Node interface automatically.
// The fields are unexported; positions are exposed only via Pos() and End().
type span struct {
	start token.Pos
	end   token.Pos
}

// Pos returns the position of the first character of the node.
func (s span) Pos() token.Pos { return s.start }

// End returns the position one past the last character of the node.
func (s span) End() token.Pos { return s.end }

// Node is the universal base interface for every AST node.
// All concrete types (File, GoChunk, Component, Element, Fragment, Text,
// Interp, StaticAttr, ExprAttr, BoolAttr, SpreadAttr, MarkupAttr) implement Node
// by embedding span.
type Node interface {
	Pos() token.Pos
	End() token.Pos
}

// SetSpan sets the position span on a concrete node pointer. It is provided so
// that the parser package (which cannot touch unexported fields of span directly)
// can record positions after constructing a node.
func SetSpan(n Node, start, end token.Pos) {
	s := span{start: start, end: end}
	switch v := n.(type) {
	case *File:
		v.span = s
	case *GoChunk:
		v.span = s
	case *Component:
		v.span = s
	case *Element:
		v.span = s
	case *Fragment:
		v.span = s
	case *Text:
		v.span = s
	case *Interp:
		v.span = s
	case *StaticAttr:
		v.span = s
	case *ExprAttr:
		v.span = s
	case *BoolAttr:
		v.span = s
	case *SpreadAttr:
		v.span = s
	case *MarkupAttr:
		v.span = s
	}
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
	span
	Package string
	Decls   []Decl
}

// GoChunk is a verbatim span of Go source (imports, types, consts, vars, funcs)
// copied through unchanged.
type GoChunk struct {
	span
	Src string
}

func (*GoChunk) declNode() {}

// Component is a `component [recv] Name(params) { body }` declaration.
type Component struct {
	span
	Recv   string // e.g. "(p UsersPage)" or "(f *Form)"; "" if none
	Name   string
	Params string // raw param-list source, e.g. "title string, featured bool"; "" if none
	Body   []Markup
}

func (*Component) declNode() {}

// Element is an HTML element or a component tag (Tag may be dotted, e.g. "ui.Button").
type Element struct {
	span
	Tag      string
	Void     bool // self-closing <tag/> or HTML void element
	Attrs    []Attr
	Children []Markup
}

func (*Element) markupNode() {}

// Fragment is <>…</> — siblings without a wrapper.
type Fragment struct {
	span
	Children []Markup
}

func (*Fragment) markupNode() {}

// Text is literal character data between markup.
type Text struct {
	span
	Value string
}

func (*Text) markupNode() {}

// Interp is `{ expr }` (Try=false) or `{ expr? }` (Try=true).
type Interp struct {
	span
	Expr string
	Try  bool
}

func (*Interp) markupNode() {}

// StaticAttr is name="value".
type StaticAttr struct {
	span
	Name, Value string
}

func (*StaticAttr) attrNode() {}

// ExprAttr is name={expr} or name={expr?}.
type ExprAttr struct {
	span
	Name, Expr string
	Try        bool
}

func (*ExprAttr) attrNode() {}

// BoolAttr is a bare attribute name (boolean true).
type BoolAttr struct {
	span
	Name string
}

func (*BoolAttr) attrNode() {}

// SpreadAttr is {...expr}.
type SpreadAttr struct {
	span
	Expr string
}

func (*SpreadAttr) attrNode() {}

// MarkupAttr is name={ <markup/> } — markup passed as an attribute value.
type MarkupAttr struct {
	span
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
