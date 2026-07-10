// Package ast defines the gsx syntax tree produced by the parser.
package ast

import (
	"go/token"
	"strings"
)

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
// Interp, StaticAttr, ExprAttr, BoolAttr, SpreadAttr, MarkupAttr, EmbeddedAttr)
// implement Node by embedding span.
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
	case *GoWithElements:
		v.span = s
	case *GoText:
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
	case *EmbeddedAttr:
		v.span = s
	case *EmbeddedInterp:
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
	case *ClassPart:
		v.span = s
	case *OrderedAttrsAttr:
		v.span = s
	case *OrderedPair:
		v.span = s
	case *ValueArm:
		v.span = s
	case *ValueIf:
		v.span = s
	case *ValueSwitch:
		v.span = s
	case *ValueSwitchCase:
		v.span = s
	case *ValueCF:
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

// GoPart is one piece of a GoWithElements (or an Interp.Embedded split): a raw
// run of Go source text (GoText), an embedded element (*Element), an embedded
// fragment (*Fragment), or an interpolating f`/js`/css` literal
// (*EmbeddedInterp, which lowers to a Go string value). It refines Node with a
// sealed marker, mirroring Markup/Decl/Attr.
type GoPart interface {
	Node
	goPartNode()
}

// File is a parsed .gsx file.
type File struct {
	span
	// Doc is the verbatim comment block that precedes the `package` clause (the
	// package doc comment), or "" if there is none. It is preserved across
	// formatting; the parser captures everything before the `package` keyword.
	Doc     string
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

// GoWithElements is a run of Go source that has one or more gsx elements
// embedded directly in expression position (e.g. `x := <div/>`), where a plain
// GoChunk (verbatim, element-free Go text) would otherwise apply. Parts holds
// the Go text and element literals in source order; concatenating each part's
// source (GoText.Src, or an element's own source span) reproduces the
// original text. GoWithElements is used as a top-level file item, replacing
// the GoChunk that would otherwise span this range — it is produced only by
// the file-level decl loop (parser/file.go's splitGoElements), never nested
// inside a component body or another Go-expression construct.
type GoWithElements struct {
	span
	Parts []GoPart
}

func (*GoWithElements) declNode() {}

// GoText is a raw run of Go source text inside a GoWithElements — the Go code
// before, after, or between embedded elements. It may be empty (e.g. two
// elements back to back with no Go code between them). GoText satisfies
// GoPart, alongside *Element.
type GoText struct {
	span
	Src string
}

func (GoText) goPartNode() {}

// Component is a `component [recv] Name[typeParams](params) { body }` declaration.
type Component struct {
	span
	Recv          string    // e.g. "(p UsersPage)" or "(f *Form)"; "" if none
	RecvPos       token.Pos // position of the receiver's opening `(` in source; NoPos if no receiver
	Name          string
	NamePos       token.Pos // position of the first char of Name in source
	TypeParams    string    // raw type-param-list source, e.g. "T any"; "" if none
	TypeParamsPos token.Pos // position of the first char of TypeParams in source (after `[` + ws); NoPos if none
	Params        string    // raw param-list source, e.g. "title string, featured bool"; "" if none
	ParamsPos     token.Pos // position of the first char of Params in source (after `(` + ws); NoPos if no params
	Body          []Markup
}

func (*Component) declNode() {}

// Element is an HTML element or a component tag (Tag may be dotted, e.g. "ui.Button").
type Element struct {
	span
	Tag         string
	TypeArgs    string    // raw type-arg-list source, e.g. "int, string"; "" if none
	TypeArgsPos token.Pos // position of the first char of TypeArgs in source (after `[` + ws); NoPos if none
	Void        bool      // self-closing <tag/> or HTML void element
	Attrs       []Attr
	Children    []Markup
	// CloseNamePos is the position of the first char of the name in the closing
	// tag (the "Card" in "</Card>"); token.NoPos for void/self-closing elements
	// (which have no closing tag). Tooling (LSP go-to-definition) uses it so a
	// cursor on the closing tag resolves like the opening tag.
	CloseNamePos token.Pos
	// ChildrenMultiline records that the source placed a line break immediately
	// after the opening tag's `>`; the formatter preserves that vertical layout,
	// keeping inline-only children block-formatted instead of collapsing them.
	ChildrenMultiline bool
	// AttrsMultiline records that the source placed a line break inside the
	// opening tag — in the whitespace between the tag name and an attribute,
	// between two attributes, or before the closing `>`/`/>`. The formatter
	// preserves that vertical layout, keeping the attribute list block-formatted
	// (one attribute per line, `>` alone) instead of collapsing it onto one line.
	// A line break occurring inside an attribute's value does not set this; that
	// is the value's own layout, not a request to break the list. Elements with
	// no attributes never set it — there is no list to break.
	AttrsMultiline bool
}

func (*Element) markupNode() {}
func (*Element) goPartNode() {}

// Fragment is <>…</> — siblings without a wrapper.
type Fragment struct {
	span
	Children []Markup
	// ChildrenMultiline records that the source placed a line break immediately
	// after the `<>`; the formatter preserves that vertical layout.
	ChildrenMultiline bool
}

func (*Fragment) markupNode() {}
func (*Fragment) goPartNode() {}

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

// Comment is a source-only content comment: `{/* text */}` or `{// text }`
// between child nodes. Unlike HTMLComment it is NOT rendered — codegen drops it,
// the formatter preserves it. (A bare `//` in text content is literal Text, not
// a comment; only the braced forms are comments in content position.)
type Comment struct {
	span
	Text  string
	Block bool // true = /* */, false = //
}

func (*Comment) markupNode() {}

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
	// Embedded holds the seed expression (Expr) split at its operand-position
	// <tag>/<> element literals into interleaved GoText and *Element/*Fragment
	// parts — e.g. `wrap(<b/>)` → [GoText("wrap("), *Element, GoText(")")].
	// It is nil when Expr contains no embedded element (the common case) and is
	// populated ONLY by codegen's analysis pass (buildSkeleton), never by the
	// parser — so the printer and the fmt faithfulness harness (which re-parse)
	// never observe it, and Expr remains the single verbatim round-trip source.
	// The analysis and emit passes share these parsed nodes (the type-checker
	// resolves each embedded value's interps against the enclosing scope, and
	// emit lowers each part to its inline gsx.Func(...) value) so resolved types
	// key on the SAME node pointers.
	Embedded []GoPart
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
	NamePos token.Pos // position of the first char of Name in source
	ArgsPos token.Pos // position of the first char of Args (after `(`); NoPos when !HasArgs
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
	Expr string
	// ExprPos is the position of the first char of Expr in source, for LSP
	// cursor mapping. It is NoPos when Expr's text differs from the source
	// bytes (a parenthesized pipeline unwrapped by the parser).
	ExprPos token.Pos
	Stages  []PipeStage
}

func (*SpreadAttr) attrNode() {}

// MarkupAttr is name={ <markup/> } — markup passed as an attribute value.
type MarkupAttr struct {
	span
	Name  string
	Value []Markup
}

func (*MarkupAttr) attrNode() {}

type EmbeddedLang uint8

const (
	EmbeddedJS EmbeddedLang = iota + 1
	EmbeddedCSS
	EmbeddedText // interpolating plain-text literal: name=f`…@{expr}…`, HTML-attribute-escaped
)

// EmbeddedAttr is an embedded-language attribute value:
//
//	name=js`…@{expr}…`, name={js`…`}, name=css`…`, name={css`…`},
//	name=f`…@{expr}…`  (EmbeddedText — plain, HTML-attribute-escaped), name={f`…`}.
//
// Interpolation is opt-in behind the f`/js`/css` prefix; a bare `…` attribute
// value is a plain Go raw string (ExprAttr/ClassAttr), never an EmbeddedAttr.
// Segments contain *Text and *Interp only. Stages is the optional whole-literal
// pipeline applied to the assembled string: name={f`…` |> f}.
type EmbeddedAttr struct {
	span
	Name     string
	Lang     EmbeddedLang
	Segments []Markup
	Stages   []PipeStage
	// DoubleQuoted records the literal's delimiter so the printer round-trips
	// it: false is the backtick form (name=f`…`), true is the `"`-delimited
	// escape-hatch form (name=f"…") used when the content contains a backtick.
	// Both forms are semantically identical; only the boundary char and which
	// char is `\`-escaped inside differ.
	DoubleQuoted bool
}

func (*EmbeddedAttr) attrNode() {}

// EmbeddedInterp is an interpolating f`…` literal used as a body/child
// expression: {f`…@{expr}…`} or {f`…` |> f}. Segments contain *Text and *Interp
// only; Stages is the optional whole-literal pipeline applied to the assembled
// string. Always plain-text (HTML-text-escaped) — no js/css lang in body, and a
// bare `…` in body position is a plain Go raw string, not an EmbeddedInterp.
type EmbeddedInterp struct {
	span
	Segments []Markup
	Stages   []PipeStage
	// DoubleQuoted records the delimiter: false is {f`…`}, true is {f"…"}. See
	// EmbeddedAttr.DoubleQuoted.
	DoubleQuoted bool
}

func (*EmbeddedInterp) markupNode() {}

// goPartNode lets an interpolating f-backtick literal ride in Go-expression
// position — as a Part of a top-level GoWithElements (a var initializer such as
// f-backtick-hello-at-name) or in an interp's Interp.Embedded split (a call arg
// such as wrap of an f-backtick literal) — where it lowers to a Go string value
// (embeddedValueExpr), interleaved in source order with the GoText and
// element/fragment parts.
func (*EmbeddedInterp) goPartNode() {}

// GoBlock is `{{ stmt }}` — a Go-statement escape hatch in a component body.
// Code is the trimmed Go source between the `{{` and `}}` delimiters.
type GoBlock struct {
	span
	Code    string
	CodePos token.Pos // first char of Code text in source (NoPos if unavailable)
}

func (*GoBlock) markupNode() {}

// IfMarkup is `{ if Cond { Then } [else if … | else { Else }] }`.
// An `else if` is stored as Else = []Markup{<*IfMarkup>} (go/ast style); a plain
// `else` puts its body in Else; no else clause leaves Else nil.
type IfMarkup struct {
	span
	Cond    string
	CondPos token.Pos // first char of Cond text in source (NoPos if unavailable)
	Then    []Markup
	Else    []Markup
	// ThenMultiline/ElseMultiline record that the source placed a line break
	// immediately after the then/else body's opening `{`. The formatter preserves
	// that vertical layout (keeping an inline-only body block-formatted) instead of
	// collapsing it to one line. ElseMultiline is meaningful only for a plain
	// `else { … }`; an `else if` is a nested IfMarkup carrying its own ThenMultiline.
	ThenMultiline bool
	ElseMultiline bool
}

func (*IfMarkup) markupNode() {}

// ForMarkup is `{ for Clause { Body } }`. Clause is the raw Go for/range clause.
type ForMarkup struct {
	span
	Clause    string
	ClausePos token.Pos // first char of Clause text in source (NoPos if unavailable)
	Body      []Markup
	// BodyMultiline records that the source placed a line break immediately after
	// the body's opening `{`; the formatter preserves that vertical layout.
	BodyMultiline bool
}

func (*ForMarkup) markupNode() {}

// SwitchMarkup is `{ switch Tag { Cases } }`. Tag is "" for a tagless switch.
type SwitchMarkup struct {
	span
	Tag    string
	TagPos token.Pos // first char of Tag in source (NoPos for a tagless switch)
	Cases  []*CaseClause
}

func (*SwitchMarkup) markupNode() {}

// CaseClause is one `case List:` or `default:` arm of a SwitchMarkup. It is a
// Node (for Inspect and positions) but is neither Markup nor Attr. List is the
// raw Go case expression(s); Default is true for the `default:` arm (List == "").
type CaseClause struct {
	span
	List    string
	ListPos token.Pos // first char of List in source (NoPos for `default:`)
	Default bool
	Body    []Markup
}

// CondAttr is an in-tag `{ if Cond { Then } [else …] }` conditional attribute.
// Then and Else are attribute lists; an `else if` is Else = []Attr{<*CondAttr>}.
type CondAttr struct {
	span
	Cond    string
	CondPos token.Pos // first char of Cond text in source (NoPos if unavailable)
	Then    []Attr
	Else    []Attr
}

func (*CondAttr) attrNode() {}

// ClassPart is one contribution in a composable class/style list: an
// unconditional Expr, Expr emitted when Cond is true, an explicit CSS literal
// inside style={...}, or a value-form if/switch. Cond == "" → always.
// When Stages is non-empty, Expr is the pipeline seed and Stages are applied
// left-to-right (`seed |> s0 |> s1 ...`), mirroring Interp.Stages; the guard Cond
// is NEVER piped. It is a Node (span embedded) so *ClassPart can be keyed in the
// resolved map for (T, error) auto-unwrap on unconditional plain parts.
// When CSSSegments != nil, this is style={ ..., css`...` }; Expr/Cond/Stages/CF
// are unused. When CF != nil, this is a value-form if/switch; Expr/Cond/Stages
// and CSSSegments are unused.
type ClassPart struct {
	span
	Expr string
	// ExprPos is the position of the first char of Expr (the pipe seed) in
	// source (NoPos for CSS-literal and value-form parts, which have no Expr).
	ExprPos token.Pos
	Cond    string
	// CondPos is the position of the first char of the `: cond` guard text in
	// source (NoPos when Cond == "").
	CondPos     token.Pos
	Stages      []PipeStage
	CSSSegments []Markup
	// CSSDoubleQuoted records the delimiter of a composed CSS literal part
	// (style={ css`…` } vs style={ css"…" }). See EmbeddedAttr.DoubleQuoted.
	CSSDoubleQuoted bool
	CF              *ValueCF
}

// ClassAttr is `class={ … }` / `style={ … }` — a composable contribution list.
// Name is "class" or "style".
type ClassAttr struct {
	span
	Name  string
	Parts []ClassPart
}

func (*ClassAttr) attrNode() {}

// ValueArm is one produced value in a value-form if/switch inside a class/style
// list — a Go string expression with an optional pipeline. It is a Node (for
// type harvest + diagnostics) but neither Markup nor Attr.
type ValueArm struct {
	span
	Expr    string
	ExprPos token.Pos // first char of Expr (the pipe seed) in source
	Stages  []PipeStage
}

// ValueIf is the value-producing `if Cond { Then } [else if … | else { Else }]`
// usable inside class/style. Then is always set; the tail is either ElseIf
// (an `else if` chain) or Else (a final `else { … }`), or neither.
type ValueIf struct {
	span
	Cond    string
	CondPos token.Pos
	Then    *ValueArm
	ElseIf  *ValueIf
	Else    *ValueArm
}

// ValueSwitch is the value-producing `switch [Tag] { case … default … }`.
// Tag is "" for a tagless switch.
type ValueSwitch struct {
	span
	Tag    string
	TagPos token.Pos // first char of Tag in source (NoPos for a tagless switch)
	Cases  []*ValueSwitchCase
}

// ValueSwitchCase is one `case List:` / `default:` arm of a ValueSwitch. List is
// the raw Go case expression(s); Default is true for `default:` (List == "").
type ValueSwitchCase struct {
	span
	List    string
	ListPos token.Pos // first char of List in source (NoPos for `default:`)
	Default bool
	Value   *ValueArm
}

// ValueCF is the value-form control-flow attached to a ClassPart. Exactly one of
// If/Switch is non-nil.
type ValueCF struct {
	span
	If     *ValueIf
	Switch *ValueSwitch
}

// OrderedPair is one "key": value pair of an OrderedAttrsAttr. Key is the
// unquoted attribute name (string-literal key, already unquoted). Value is the
// raw Go expression source. span covers the value expression (start = first
// non-space char after the colon, end = last char of the value), so *OrderedPair
// satisfies ast.Node and can be used as a resolved-map key.
type OrderedPair struct {
	span
	Key   string
	Value string
}

// OrderedAttrsAttr is name={{ "k1": v1, "k2": v2 }} — an ordered attribute bag
// literal in attribute-value position. It lowers to a gsx.OrderedAttrs{…}
// composite literal bound to the matched prop field. Distinct from a body GoBlock
// ({{ stmt }}); the two never share a parse position.
type OrderedAttrsAttr struct {
	span
	Name  string
	Pairs []OrderedPair
}

func (*OrderedAttrsAttr) attrNode() {}

// CommentAttr is a source-only comment in an element's attribute list: bare
// `// text` / `/* text */`, or a braced comment-only `{/* */}` / `{// }`. It is
// never rendered (codegen ignores it); the formatter preserves it. Braced forms
// canonicalize to bare on output, so no "braced" flag is retained.
type CommentAttr struct {
	span
	Text     string // inner text, delimiters and wrapping braces stripped, trimmed
	Block    bool   // true = /* */, false = //
	Trailing bool   // true = same source line as the previous attribute
}

func (*CommentAttr) attrNode() {}

// Inspect traverses the AST in depth-first order, calling f for each node.
// If f returns false, Inspect does not recurse into that node's children.
// After recursing into children, Inspect calls f(nil) for go/ast parity.
// Children by type:
//   - *File: each Decl
//   - *GoWithElements: each Part (GoText leaves; *Element and *Fragment recurse)
//   - *Component: each Body markup node
//   - *Element: each Attr, then each Child
//   - *Fragment: each Child
//   - *MarkupAttr: each Value markup node
//   - *IfMarkup: each Then and Else markup node
//   - *ForMarkup: each Body markup node
//   - *SwitchMarkup: each CaseClause
//   - *CaseClause: each Body markup node
//   - *CondAttr: each Then and Else attr node
//   - *ClassAttr: each *ClassPart (Parts slice walked by pointer)
//   - *ClassPart: CF (if non-nil)
//   - *ValueCF: If or Switch (whichever is non-nil)
//   - *ValueIf: Then, ElseIf (if non-nil), Else (if non-nil)
//   - *ValueSwitch: each CaseClause
//   - *ValueSwitchCase: Value arm
//   - *ValueArm: leaf
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
	case *GoWithElements:
		for _, part := range n.Parts {
			Inspect(part, f)
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
	case *EmbeddedAttr:
		for _, m := range n.Segments {
			Inspect(m, f)
		}
	case *EmbeddedInterp:
		for _, m := range n.Segments {
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
	case *OrderedAttrsAttr:
		for i := range n.Pairs {
			Inspect(&n.Pairs[i], f)
		}
	case *ClassAttr:
		for i := range n.Parts {
			Inspect(&n.Parts[i], f)
		}
	case *ClassPart:
		for _, m := range n.CSSSegments {
			Inspect(m, f)
		}
		if n.CF != nil {
			Inspect(n.CF, f)
		}
	case *ValueCF:
		if n.If != nil {
			Inspect(n.If, f)
		}
		if n.Switch != nil {
			Inspect(n.Switch, f)
		}
	case *ValueIf:
		Inspect(n.Then, f)
		if n.ElseIf != nil {
			Inspect(n.ElseIf, f)
		}
		if n.Else != nil {
			Inspect(n.Else, f)
		}
	case *ValueSwitch:
		for _, c := range n.Cases {
			Inspect(c, f)
		}
	case *ValueSwitchCase:
		Inspect(n.Value, f)
	case *ValueArm:
		// leaf
		// GoBlock and OrderedPair are also leaves with no child nodes.
	}
	f(nil)
}

// IsComponentTag reports whether a tag names a component (uppercase first
// letter or dotted, e.g. ui.Button) rather than an HTML element. Single
// source of truth for the parser (type-arg admission) and codegen (call
// lowering) — the two MUST agree or type args get rejected on tags codegen
// lowers as components.
func IsComponentTag(tag string) bool {
	if tag == "" {
		return false
	}
	if strings.Contains(tag, ".") {
		return true
	}
	return tag[0] >= 'A' && tag[0] <= 'Z'
}
