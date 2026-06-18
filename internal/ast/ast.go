// Package ast defines the gsx syntax tree produced by the parser.
package ast

import "go/token"

// File is a parsed .gsx file.
type File struct {
	Package string
	PkgPos  token.Position
	Decls   []Decl
}

// Decl is a top-level declaration: opaque Go, or a component.
type Decl interface{ declNode() }

// GoChunk is a verbatim span of Go source (imports, types, consts, vars, funcs)
// copied through unchanged.
type GoChunk struct {
	Src string
	Pos token.Position
}

func (*GoChunk) declNode() {}

// Component is a `component [recv] Name(params) { body }` declaration.
type Component struct {
	Recv   string // e.g. "(p UsersPage)" or "(f *Form)"; "" if none
	Name   string
	Params string // raw param-list source, e.g. "title string, featured bool"; "" if none
	Body   []Node
	Pos    token.Position
}

func (*Component) declNode() {}

// Node is a markup node.
type Node interface{ node() }

// Element is an HTML element or a component tag (Tag may be dotted, e.g. "ui.Button").
type Element struct {
	Tag      string
	Void     bool // self-closing <tag/> or HTML void element
	Attrs    []Attr
	Children []Node
	Pos      token.Position
}

func (*Element) node() {}

// Fragment is <>…</> — siblings without a wrapper.
type Fragment struct {
	Children []Node
	Pos      token.Position
}

func (*Fragment) node() {}

// Text is literal character data between markup.
type Text struct {
	Value string
	Pos   token.Position
}

func (*Text) node() {}

// Interp is `{ expr }` (Try=false) or `{ expr? }` (Try=true).
type Interp struct {
	Expr string
	Try  bool
	Pos  token.Position
}

func (*Interp) node() {}

// Attr is one attribute on an element.
type Attr interface{ attr() }

// StaticAttr is name="value".
type StaticAttr struct{ Name, Value string }

func (*StaticAttr) attr() {}

// ExprAttr is name={expr} or name={expr?}.
type ExprAttr struct {
	Name, Expr string
	Try        bool
}

func (*ExprAttr) attr() {}

// BoolAttr is a bare attribute name (boolean true).
type BoolAttr struct{ Name string }

func (*BoolAttr) attr() {}

// SpreadAttr is {...expr}.
type SpreadAttr struct{ Expr string }

func (*SpreadAttr) attr() {}

// MarkupAttr is name={ <markup/> } — markup passed as an attribute value.
type MarkupAttr struct {
	Name  string
	Value []Node
}

func (*MarkupAttr) attr() {}
