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
	case *Doctype:
		v.span = s
	case *HTMLComment:
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
	case *GoBlock:
		v.span = s
	case *IfMarkup:
		v.span = s
	case *ForMarkup:
		v.span = s
	case *SwitchMarkup:
		v.span = s
	case *CaseClause:
		v.span = s
	case *CondAttr:
		v.span = s
	case *ClassAttr:
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
	Recv      string // e.g. "(p UsersPage)" or "(f *Form)"; "" if none
	Name      string
	NamePos   token.Pos // position of the first char of Name in source
	Params    string    // raw param-list source, e.g. "title string, featured bool"; "" if none
	ParamsPos token.Pos // position of the first char of Params in source (after `(` + ws); NoPos if no params
	Body      []Markup
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

// Doctype is an HTML `<!DOCTYPE …>` declaration. Text holds the full source
// including the `<!` and `>` delimiters (e.g. "<!DOCTYPE html>"); it renders
// verbatim.
type Doctype struct {
	span
	Text string
}

func (*Doctype) markupNode() {}

// HTMLComment is an HTML `<!-- … -->` comment. Text holds the inner text between
// the `<!--` and `-->` delimiters; unlike source-only `{/* */}` comments, HTML
// comments are PRESERVED and render verbatim (they can be meaningful, e.g. htmx
// or conditional comments).
type HTMLComment struct {
	span
	Text string
}

func (*HTMLComment) markupNode() {}

// Interp is `{ expr }`. When Stages is non-empty, Expr is the pipeline seed and
// Stages are applied left-to-right (`seed |> s0 |> s1 …`). A `(T, error)` Expr is
// auto-unwrapped at codegen (the error propagates out of the enclosing Render);
// there is no try-marker.
type Interp struct {
	span
	Expr   string
	Stages []PipeStage
	// ExprPos is the position of the first non-whitespace character of the
	// interpolation's inner expression in the source file (i.e. where Expr
	// starts before trimming). It is token.NoPos when unavailable. Used by
	// codegen to emit compensated //line directives so type errors map to the
	// exact source column of the expression rather than the '{' opener, and by
	// the LSP to map a cursor onto the expression for go-to-definition.
	ExprPos token.Pos
	// JSCtx is set by internal/jsx for Interps inside a <script>; JSCtxNone otherwise.
	JSCtx JSCtx
}

func (*Interp) markupNode() {}

// JSCtx is the JavaScript context an Interp inside a <script> was classified
// into (set by internal/jsx). 0 (JSCtxNone) for non-script interps.
type JSCtx uint8

const (
	JSCtxNone JSCtx = iota
	JSCtxValue
	JSCtxString
	JSCtxTemplate
	JSCtxRegexp
)

// PipeStage is one `|> name` / `|> name(args)` filter in a pipeline. It is a
// plain value, not a Node. HasArgs distinguishes `f` (bare → f(x)) from `f()`
// (parameterized → f()(x)).
type PipeStage struct {
	Name    string
	Args    string
	HasArgs bool
}

// StaticAttr is name="value".
type StaticAttr struct {
	span
	Name, Value string
}

func (*StaticAttr) attrNode() {}

// ExprAttr is name={expr}. Stages mirrors Interp.Stages for a pipelined
// attribute value (`name={ seed |> s0 … }`). A `(T, error)` expr is auto-unwrapped
// at codegen (the error propagates out of the enclosing Render).
type ExprAttr struct {
	span
	Name, Expr string
	ExprPos    token.Pos // position of the first char of Expr in source (for go-to-definition)
	Stages     []PipeStage
}

func (*ExprAttr) attrNode() {}

// BoolAttr is a bare attribute name (boolean true).
type BoolAttr struct {
	span
	Name string
}

func (*BoolAttr) attrNode() {}

// SpreadAttr is { expr... }. When Stages is non-empty, Expr is the pipeline seed
// and Stages are applied left-to-right (`{ seed |> s0 |> s1 ... }`), mirroring
// Interp.Stages — the lowered result is the spread/splat subject.
type SpreadAttr struct {
	span
	Expr   string
	Stages []PipeStage
}

func (*SpreadAttr) attrNode() {}

// MarkupAttr is name={ <markup/> } — markup passed as an attribute value.
type MarkupAttr struct {
	span
	Name  string
	Value []Markup
}

func (*MarkupAttr) attrNode() {}

// JSAttr is a JS-context attribute whose quoted value contains @{ } holes:
// name="… @{ expr } …" (e.g. x-data, onclick). Segments are Text and Interp,
// like a <script> body. Set only for attrjs.IsJSAttr names with ≥1 hole;
// hole-free JS attributes stay StaticAttr.
type JSAttr struct {
	span
	Name     string
	Segments []Markup
}

func (*JSAttr) attrNode() {}

// GoBlock is `{{ stmt }}` — a Go-statement escape hatch in a component body.
// Code is the trimmed Go source between the `{{` and `}}` delimiters.
type GoBlock struct {
	span
	Code string
}

func (*GoBlock) markupNode() {}

// IfMarkup is `{ if Cond { Then } [else if … | else { Else }] }`.
// An `else if` is stored as Else = []Markup{<*IfMarkup>} (go/ast style); a plain
// `else` puts its body in Else; no else clause leaves Else nil.
type IfMarkup struct {
	span
	Cond string
	Then []Markup
	Else []Markup
}

func (*IfMarkup) markupNode() {}

// ForMarkup is `{ for Clause { Body } }`. Clause is the raw Go for/range clause.
type ForMarkup struct {
	span
	Clause string
	Body   []Markup
}

func (*ForMarkup) markupNode() {}

// SwitchMarkup is `{ switch Tag { Cases } }`. Tag is "" for a tagless switch.
type SwitchMarkup struct {
	span
	Tag   string
	Cases []*CaseClause
}

func (*SwitchMarkup) markupNode() {}

// CaseClause is one `case List:` or `default:` arm of a SwitchMarkup. It is a
// Node (for Inspect and positions) but is neither Markup nor Attr. List is the
// raw Go case expression(s); Default is true for the `default:` arm (List == "").
type CaseClause struct {
	span
	List    string
	Default bool
	Body    []Markup
}

// CondAttr is an in-tag `{ if Cond { Then } [else …] }` conditional attribute.
// Then and Else are attribute lists; an `else if` is Else = []Attr{<*CondAttr>}.
type CondAttr struct {
	span
	Cond string
	Then []Attr
	Else []Attr
}

func (*CondAttr) attrNode() {}

// ClassPart is one contribution in a composable class/style list: an
// unconditional Expr, or Expr emitted when Cond is true. Cond == "" → always.
// When Stages is non-empty, Expr is the pipeline seed and Stages are applied
// left-to-right (`seed |> s0 |> s1 ...`), mirroring Interp.Stages; the guard Cond
// is NEVER piped. It is a plain value, not a Node.
type ClassPart struct {
	Expr   string
	Cond   string
	Stages []PipeStage
}

// ClassAttr is `class={ … }` / `style={ … }` — a composable contribution list.
// Name is "class" or "style".
type ClassAttr struct {
	span
	Name  string
	Parts []ClassPart
}

func (*ClassAttr) attrNode() {}

// Inspect traverses the AST in depth-first order, calling f for each node.
// If f returns false, Inspect does not recurse into that node's children.
// After recursing into children, Inspect calls f(nil) for go/ast parity.
// Children by type:
//   - *File: each Decl
//   - *Component: each Body markup node
//   - *Element: each Attr, then each Child
//   - *Fragment: each Child
//   - *MarkupAttr: each Value markup node
//   - *IfMarkup: each Then and Else markup node
//   - *ForMarkup: each Body markup node
//   - *SwitchMarkup: each CaseClause
//   - *CaseClause: each Body markup node
//   - *CondAttr: each Then and Else attr node
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
	case *IfMarkup:
		for _, m := range n.Then {
			Inspect(m, f)
		}
		for _, m := range n.Else {
			Inspect(m, f)
		}
	case *ForMarkup:
		for _, m := range n.Body {
			Inspect(m, f)
		}
	case *SwitchMarkup:
		for _, c := range n.Cases {
			Inspect(c, f)
		}
	case *CaseClause:
		for _, m := range n.Body {
			Inspect(m, f)
		}
	case *CondAttr:
		for _, a := range n.Then {
			Inspect(a, f)
		}
		for _, a := range n.Else {
			Inspect(a, f)
		}
		// GoBlock, ClassAttr: leaves (ClassParts are not Nodes)
	}
	f(nil)
}
