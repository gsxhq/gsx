// Package printer renders a (normalized) gsx AST back to canonical gsx source.
//
// Fprint assumes the AST has already been whitespace-normalized (via
// internal/wsnorm); gsx fmt does that before calling the printer. The printer's
// only job is to lay out the structure: it adds COSMETIC whitespace (newlines and
// tab indentation at block boundaries) that wsnorm.Normalize drops again on a
// re-parse, while preserving every significant byte of content. As a result the
// output is render-faithful (re-parsing + Normalize yields the same AST) and
// idempotent (printing an already-formatted file is byte-identical).
//
// It depends only on github.com/gsxhq/gsx/ast plus go/format, go/parser, go/token
// and the standard library.
package printer

import (
	"bytes"
	"fmt"
	goast "go/ast"
	"go/format"
	goparser "go/parser"
	gotoken "go/token"
	"io"
	"strings"

	"github.com/gsxhq/gsx/ast"
)

// Fprint writes the canonical gsx rendering of f to w.
func Fprint(w io.Writer, f *ast.File) error {
	var p printer
	p.file(f)
	if p.err != nil {
		return p.err
	}
	_, err := w.Write(p.buf.Bytes())
	return err
}

// printer accumulates output and the first encountered I/O-independent error.
type printer struct {
	buf bytes.Buffer
	err error
}

func (p *printer) ws(s string) { p.buf.WriteString(s) }

func (p *printer) indent(depth int) {
	for range depth {
		p.buf.WriteByte('\t')
	}
}

// file emits `package P` then each declaration separated by one blank line.
func (p *printer) file(f *ast.File) {
	p.ws("package ")
	p.ws(f.Package)
	p.ws("\n")
	for _, d := range f.Decls {
		p.ws("\n")
		p.decl(d)
	}
}

func (p *printer) decl(d ast.Decl) {
	switch v := d.(type) {
	case *ast.GoChunk:
		p.ws(fmtGoChunk(v.Src))
		p.ws("\n")
	case *ast.Component:
		p.component(v)
	default:
		p.err = fmt.Errorf("printer: unknown decl type %T", d)
	}
}

// component emits `component [recv ]Name(params) {` + body + `}`.
func (p *printer) component(c *ast.Component) {
	p.ws("component ")
	if c.Recv != "" {
		p.ws(fmtRecv(c.Recv))
		p.ws(" ")
	}
	p.ws(c.Name)
	p.ws("(")
	p.ws(fmtParams(c.Params))
	p.ws(") {")
	// A component body always breaks after `{` (like a Go func body): the closing
	// `}` sits on its own line and the body is indented. A BLOCK body puts each
	// child on its own line; an INLINE body (a surviving Text forces inline) goes on
	// a single indented line — never jammed onto the brace line. The added newlines
	// are cosmetic (Normalize drops them), so this stays render-faithful + idempotent.
	if len(c.Body) > 0 {
		if isBlockList(c.Body) {
			p.children(c.Body, 0, false)
		} else {
			p.ws("\n")
			p.indent(1)
			for _, n := range c.Body {
				p.markupInline(n, 1)
			}
			p.ws("\n")
		}
	}
	p.indent(0)
	p.ws("}\n")
}

// blockLevel reports whether a markup node forces a block layout when present in
// a children list.
func blockLevel(n ast.Markup) bool {
	switch n.(type) {
	case *ast.Element, *ast.Fragment, *ast.IfMarkup, *ast.ForMarkup,
		*ast.SwitchMarkup, *ast.GoBlock, *ast.Doctype, *ast.HTMLComment:
		return true
	default:
		return false
	}
}

// isBlockList implements the layout contract: a list lays out BLOCK iff it has
// at least one block-level child AND no surviving Text node; otherwise INLINE.
//
// Any surviving Text forces INLINE (breaking around it would alter rendering).
// A markup `//` is ordinary literal text — element content is literal (comments
// are tag-interior `//`/`/* */` or braced `{/* … */}`), so a Text never carries
// an "open" line comment that could swallow a sibling. No special case needed.
func isBlockList(nodes []ast.Markup) bool {
	hasBlock := false
	for _, n := range nodes {
		if _, ok := n.(*ast.Text); ok {
			return false
		}
		if blockLevel(n) {
			hasBlock = true
		}
	}
	return hasBlock
}

// children prints a children list between an already-emitted opener and a
// caller-emitted closer, applying the block/inline rule. parentDepth is the
// indentation depth of the parent's opening/closing line. When the list is block,
// each child sits on its own line at parentDepth+1 and the caller's closer must be
// re-indented to parentDepth. When inline (or empty), children are concatenated
// directly with no surrounding whitespace.
//
// preserve emits the children verbatim (used for pre/textarea/script/style): the
// Text values are written as-is with no added indentation.
func (p *printer) children(nodes []ast.Markup, parentDepth int, preserve bool) {
	if preserve {
		for _, n := range nodes {
			p.markupInline(n, parentDepth)
		}
		return
	}
	if !isBlockList(nodes) {
		for _, n := range nodes {
			p.markupInline(n, parentDepth)
		}
		return
	}
	// A block list never contains a surviving Text (isBlockList rejects any),
	// so each child is block-level and sits on its own indented line.
	for _, n := range nodes {
		p.ws("\n")
		p.indent(parentDepth + 1)
		p.markup(n, parentDepth+1)
	}
	p.ws("\n")
	p.indent(parentDepth)
}

// markup prints one node that begins at an already-indented position at the given
// depth (used for block-list children). depth is this node's own line depth.
func (p *printer) markup(n ast.Markup, depth int) {
	switch v := n.(type) {
	case *ast.Element:
		p.element(v, depth)
	case *ast.Fragment:
		p.fragment(v, depth)
	case *ast.IfMarkup:
		p.ifMarkup(v, depth)
	case *ast.ForMarkup:
		p.forMarkup(v, depth)
	case *ast.SwitchMarkup:
		p.switchMarkup(v, depth)
	case *ast.GoBlock:
		p.goBlock(v)
	case *ast.Doctype:
		p.ws(v.Text)
	case *ast.HTMLComment:
		p.ws("<!--")
		p.ws(v.Text)
		p.ws("-->")
	case *ast.Text:
		p.ws(v.Value)
	case *ast.Interp:
		p.interp(v)
	default:
		p.err = fmt.Errorf("printer: unknown markup type %T", n)
	}
}

// markupInline prints an inline-context node. depth is the depth of the
// surrounding line, used when a nested block list needs to re-indent its closer.
func (p *printer) markupInline(n ast.Markup, depth int) {
	p.markup(n, depth)
}

func (p *printer) element(e *ast.Element, depth int) {
	p.ws("<")
	p.ws(e.Tag)
	for _, a := range e.Attrs {
		p.ws(" ")
		p.attr(a, depth)
	}
	if e.Void && len(e.Children) == 0 {
		p.ws("/>")
		return
	}
	p.ws(">")
	if strings.EqualFold(e.Tag, "style") || strings.EqualFold(e.Tag, "script") {
		// <style> and <script> use @{ } holes (split distinctly from regular { }
		// markup interps); print them verbatim with the @{ } delimiter so the
		// formatted output re-parses to the same script/style interps.
		p.rawHoleChildren(e.Children)
	} else if isPreserveTag(e.Tag) {
		p.children(e.Children, depth, true)
	} else {
		p.children(e.Children, depth, false)
	}
	p.ws("</")
	p.ws(e.Tag)
	p.ws(">")
}

// rawHoleChildren prints the children of a <style> or <script> element: Text
// nodes are emitted verbatim and Interp nodes use the @{ } delimiter (distinct
// from the regular { } markup interp, so the re-parse recognizes them as
// style/script interps). Try ('?') and pipeline Stages are preserved faithfully
// so that the formatter never silently strips them — codegen will reject them
// with a clear error.
func (p *printer) rawHoleChildren(nodes []ast.Markup) {
	for _, n := range nodes {
		switch v := n.(type) {
		case *ast.Text:
			p.ws(v.Value)
		case *ast.Interp:
			p.ws("@{ ")
			p.ws(fmtExpr(v.Expr))
			if v.Try {
				p.ws("?")
			}
			for _, s := range v.Stages {
				p.ws(" |> ")
				p.pipeStage(s)
			}
			p.ws(" }")
		default:
			p.markup(n, 0)
		}
	}
}

func (p *printer) fragment(f *ast.Fragment, depth int) {
	p.ws("<>")
	p.children(f.Children, depth, false)
	p.ws("</>")
}

func (p *printer) interp(i *ast.Interp) {
	p.ws("{ ")
	p.ws(fmtExpr(i.Expr))
	if len(i.Stages) == 0 {
		if i.Try {
			p.ws("?")
		}
	} else {
		if i.Try {
			p.ws("?")
		}
		for _, s := range i.Stages {
			p.ws(" |> ")
			p.pipeStage(s)
		}
	}
	p.ws(" }")
}

func (p *printer) pipeStage(s ast.PipeStage) {
	p.ws(s.Name)
	if s.HasArgs {
		p.ws("(")
		p.ws(fmtArgs(s.Args))
		p.ws(")")
	}
	if s.Try {
		p.ws("?")
	}
}

func (p *printer) goBlock(b *ast.GoBlock) {
	p.ws("{{ ")
	p.ws(fmtStmts(b.Code))
	p.ws(" }}")
}

func (p *printer) ifMarkup(i *ast.IfMarkup, depth int) {
	p.ws("{ ")
	p.ifChain(i, depth)
	p.ws(" }")
}

// ifChain prints `if cond { Then }[ else …]` without the outer `{ ` / ` }`, so
// that else-if recursion stays on one logical construct.
func (p *printer) ifChain(i *ast.IfMarkup, depth int) {
	p.ws("if ")
	p.ws(fmtExpr(i.Cond))
	p.ws(" {")
	p.cfBody(i.Then, depth)
	p.ws("}")
	if len(i.Else) == 0 {
		return
	}
	// `else if` is exactly one *IfMarkup in Else (go/ast style).
	if len(i.Else) == 1 {
		if elseIf, ok := i.Else[0].(*ast.IfMarkup); ok {
			p.ws(" else ")
			p.ifChain(elseIf, depth)
			return
		}
	}
	p.ws(" else {")
	p.cfBody(i.Else, depth)
	p.ws("}")
}

func (p *printer) forMarkup(f *ast.ForMarkup, depth int) {
	p.ws("{ for ")
	p.ws(fmtClause(f.Clause))
	p.ws(" {")
	p.cfBody(f.Body, depth)
	p.ws("} }")
}

// cfBody prints a control-flow body between an already-emitted `{` and a
// caller-emitted `}`. For a block body it delegates to children (each child on
// its own line, closer re-indented). For an inline body it pads with single
// spaces — `{ <content> }` — so that an inline body beginning/ending with an
// Interp does not produce `{{`/`}}` (which would re-parse as a GoBlock).
func (p *printer) cfBody(nodes []ast.Markup, depth int) {
	if isBlockList(nodes) {
		p.children(nodes, depth, false)
		return
	}
	p.ws(" ")
	for _, n := range nodes {
		p.markupInline(n, depth)
	}
	p.ws(" ")
}

func (p *printer) switchMarkup(s *ast.SwitchMarkup, depth int) {
	p.ws("{ switch")
	if s.Tag != "" {
		p.ws(" ")
		p.ws(fmtExpr(s.Tag))
	}
	p.ws(" {")
	for _, c := range s.Cases {
		p.ws("\n")
		p.indent(depth + 1)
		if c.Default {
			p.ws("default:")
		} else {
			p.ws("case ")
			p.ws(fmtCaseList(c.List))
			p.ws(":")
		}
		p.caseBody(c.Body, depth+1)
	}
	p.ws("\n")
	p.indent(depth)
	p.ws("} }")
}

// caseBody prints a switch case arm's body. labelDepth is the indent depth of
// the `case …:` / `default:` line. Unlike children, it emits no trailing closer
// line: the following `case`/`}` supplies the next boundary. Block bodies put
// each child on its own line at labelDepth+1; inline bodies follow the colon.
func (p *printer) caseBody(nodes []ast.Markup, labelDepth int) {
	if len(nodes) == 0 {
		return
	}
	if !isBlockList(nodes) {
		for _, n := range nodes {
			p.markupInline(n, labelDepth)
		}
		return
	}
	for _, n := range nodes {
		p.ws("\n")
		p.indent(labelDepth + 1)
		p.markup(n, labelDepth+1)
	}
}

func (p *printer) attr(a ast.Attr, depth int) {
	switch v := a.(type) {
	case *ast.StaticAttr:
		p.ws(v.Name)
		p.ws(`="`)
		p.ws(v.Value)
		p.ws(`"`)
	case *ast.BoolAttr:
		p.ws(v.Name)
	case *ast.ExprAttr:
		p.ws(v.Name)
		p.ws("={")
		p.ws(fmtExpr(v.Expr))
		if len(v.Stages) == 0 {
			if v.Try {
				p.ws("?")
			}
		} else {
			if v.Try {
				p.ws("?")
			}
			for _, s := range v.Stages {
				p.ws(" |> ")
				p.pipeStage(s)
			}
		}
		p.ws("}")
	case *ast.SpreadAttr:
		p.ws("{...")
		p.ws(fmtExpr(v.Expr))
		p.ws("}")
	case *ast.ClassAttr:
		p.classAttr(v)
	case *ast.CondAttr:
		p.ws("{ ")
		p.condAttrChain(v, depth)
		p.ws(" }")
	case *ast.MarkupAttr:
		p.ws(v.Name)
		p.ws("={ ")
		p.markupAttrValue(v.Value, depth)
		p.ws(" }")
	default:
		p.err = fmt.Errorf("printer: unknown attr type %T", a)
	}
}

// markupAttrValue prints a named-slot markup list inside `name={ … }`. A slot
// lives on the attribute line, so it is printed inline regardless of the
// block/inline rule (its content carries no surrounding text, so an inline
// rendering re-parses + Normalizes to the same slot). Nested elements still lay
// out their own children per the rule via markup.
func (p *printer) markupAttrValue(nodes []ast.Markup, depth int) {
	for _, n := range nodes {
		p.markupInline(n, depth)
	}
}

func (p *printer) classAttr(c *ast.ClassAttr) {
	p.ws(c.Name)
	p.ws("={ ")
	for i, part := range c.Parts {
		if i > 0 {
			p.ws(", ")
		}
		p.ws(fmtExpr(part.Expr))
		if part.Cond != "" {
			p.ws(": ")
			p.ws(fmtExpr(part.Cond))
		}
	}
	p.ws(" }")
}

func (p *printer) condAttrChain(c *ast.CondAttr, depth int) {
	p.ws("if ")
	p.ws(fmtExpr(c.Cond))
	p.ws(" {")
	p.condAttrList(c.Then, depth)
	p.ws("}")
	if len(c.Else) == 0 {
		return
	}
	if len(c.Else) == 1 {
		if elseIf, ok := c.Else[0].(*ast.CondAttr); ok {
			p.ws(" else ")
			p.condAttrChain(elseIf, depth)
			return
		}
	}
	p.ws(" else {")
	p.condAttrList(c.Else, depth)
	p.ws("}")
}

// condAttrList prints attributes inside a conditional-attribute block, each
// separated and surrounded by single spaces: `{ a b }`.
func (p *printer) condAttrList(attrs []ast.Attr, depth int) {
	for _, a := range attrs {
		p.ws(" ")
		p.attr(a, depth)
	}
	if len(attrs) > 0 {
		p.ws(" ")
	}
}

// isPreserveTag mirrors wsnorm: pre/textarea/script/style keep their bodies
// verbatim with no added indentation.
func isPreserveTag(tag string) bool {
	switch strings.ToLower(tag) {
	case "pre", "textarea", "script", "style":
		return true
	}
	return false
}

// ---- Go-fragment formatting helpers ----------------------------------------
//
// Each helper canonicalizes a Go fragment via go/format by wrapping it into a
// valid compilation unit, formatting, and extracting the relevant span. On any
// formatting error (malformed Go that the gsx parser nonetheless accepted), the
// helper falls back to the trimmed verbatim source so fmt never fails on
// parseable gsx.

// fmtGoChunk formats a top-level Go declaration chunk (imports/types/funcs/etc.).
func fmtGoChunk(src string) string {
	out, err := format.Source([]byte(src))
	if err != nil {
		return strings.TrimSpace(src)
	}
	return strings.TrimSpace(string(out))
}

// fmtNode renders a go/ast node back to canonical Go source via go/format.Node,
// trimming surrounding whitespace. Used by the fragment helpers after they have
// parsed a wrapper and located the relevant sub-node. On any error it returns
// ("", false) so the caller can fall back to the verbatim source.
func fmtNode(fset *gotoken.FileSet, node any) (string, bool) {
	var b bytes.Buffer
	if err := format.Node(&b, fset, node); err != nil {
		return "", false
	}
	return strings.TrimSpace(b.String()), true
}

// parseWrapped parses a wrapped Go source string into its single declaration and
// returns the *ast.File. The wrapper always has exactly one top-level decl.
func parseWrapped(src string) (*goast.File, *gotoken.FileSet, bool) {
	fset := gotoken.NewFileSet()
	f, err := goparser.ParseFile(fset, "", src, 0)
	if err != nil || len(f.Decls) == 0 {
		return nil, nil, false
	}
	return f, fset, true
}

// fmtExpr formats a single Go expression by wrapping it as `var _ = (EXPR)` and
// extracting the parenthesized expression's inner operand.
func fmtExpr(src string) string {
	trimmed := strings.TrimSpace(src)
	if trimmed == "" {
		return ""
	}
	f, fset, ok := parseWrapped("package p\nvar _ = (" + trimmed + ")\n")
	if !ok {
		return trimmed
	}
	gd, ok := f.Decls[0].(*goast.GenDecl)
	if !ok || len(gd.Specs) == 0 {
		return trimmed
	}
	vs, ok := gd.Specs[0].(*goast.ValueSpec)
	if !ok || len(vs.Values) == 0 {
		return trimmed
	}
	expr := vs.Values[0]
	if pe, ok := expr.(*goast.ParenExpr); ok {
		expr = pe.X // unwrap our own protective parens
	}
	if out, ok := fmtNode(fset, expr); ok {
		return out
	}
	return trimmed
}

// fmtArgs formats a comma-separated Go argument list (pipeline stage args) via
// the call form `_f(ARGS)`, re-rendering each argument joined by ", ".
func fmtArgs(src string) string {
	trimmed := strings.TrimSpace(src)
	if trimmed == "" {
		return ""
	}
	f, fset, ok := parseWrapped("package p\nvar _ = _f(" + trimmed + ")\n")
	if !ok {
		return trimmed
	}
	gd, ok := f.Decls[0].(*goast.GenDecl)
	if !ok || len(gd.Specs) == 0 {
		return trimmed
	}
	vs, ok := gd.Specs[0].(*goast.ValueSpec)
	if !ok || len(vs.Values) == 0 {
		return trimmed
	}
	call, ok := vs.Values[0].(*goast.CallExpr)
	if !ok {
		return trimmed
	}
	parts := make([]string, 0, len(call.Args))
	for _, a := range call.Args {
		s, ok := fmtNode(fset, a)
		if !ok {
			return trimmed
		}
		parts = append(parts, s)
	}
	out := strings.Join(parts, ", ")
	if call.Ellipsis.IsValid() {
		out += "..."
	}
	return out
}

// fmtStmts formats a Go statement list (a {{ }} block) inside a func body. Single
// statements collapse to one line; multi-statement blocks keep gofmt's newlines
// (with the func-body indentation level stripped).
func fmtStmts(src string) string {
	trimmed := strings.TrimSpace(src)
	if trimmed == "" {
		return ""
	}
	wrapped := "package p\nfunc _m() {\n" + trimmed + "\n}\n"
	out, err := format.Source([]byte(wrapped))
	if err != nil {
		return trimmed
	}
	body, ok := extractFuncBody(string(out))
	if !ok {
		return trimmed
	}
	return body
}

// fmtParams formats a component parameter list via `func _m(PARAMS) {}`,
// extracting the gofmt-rendered field list (sans the outer parens).
func fmtParams(src string) string {
	trimmed := strings.TrimSpace(src)
	if trimmed == "" {
		return ""
	}
	f, fset, ok := parseWrapped("package p\nfunc _m(" + trimmed + ") {}\n")
	if !ok {
		return trimmed
	}
	fd, ok := f.Decls[0].(*goast.FuncDecl)
	if !ok || fd.Type.Params == nil {
		return trimmed
	}
	if out, ok := fmtFieldList(fset, fd.Type.Params); ok {
		return out
	}
	return trimmed
}

// fmtRecv formats a method receiver via `func (RECV) _m() {}`. Recv is stored
// including its parentheses, e.g. "(p UsersPage)"; the result is re-parenthesized.
func fmtRecv(src string) string {
	trimmed := strings.TrimSpace(src)
	if trimmed == "" {
		return ""
	}
	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "("), ")"))
	f, fset, ok := parseWrapped("package p\nfunc (" + inner + ") _m() {}\n")
	if !ok {
		return trimmed
	}
	fd, ok := f.Decls[0].(*goast.FuncDecl)
	if !ok || fd.Recv == nil {
		return trimmed
	}
	if out, ok := fmtFieldList(fset, fd.Recv); ok {
		return "(" + out + ")"
	}
	return trimmed
}

// fmtFieldList renders the fields of a *goast.FieldList (without the enclosing
// parentheses) joined by ", ".
func fmtFieldList(fset *gotoken.FileSet, fl *goast.FieldList) (string, bool) {
	if fl == nil {
		return "", true
	}
	parts := make([]string, 0, len(fl.List))
	for _, field := range fl.List {
		names := make([]string, 0, len(field.Names))
		for _, n := range field.Names {
			names = append(names, n.Name)
		}
		typ, ok := fmtNode(fset, field.Type)
		if !ok {
			return "", false
		}
		if len(names) == 0 {
			parts = append(parts, typ)
		} else {
			parts = append(parts, strings.Join(names, ", ")+" "+typ)
		}
	}
	return strings.Join(parts, ", "), true
}

// fmtClause formats a for/range clause via `for CLAUSE {}`, re-rendering the
// loop header (the text between `for ` and the body brace).
func fmtClause(src string) string {
	trimmed := strings.TrimSpace(src)
	if trimmed == "" {
		return ""
	}
	f, fset, ok := parseWrapped("package p\nfunc _m() {\nfor " + trimmed + " {\n}\n}\n")
	if !ok {
		return trimmed
	}
	fd, ok := f.Decls[0].(*goast.FuncDecl)
	if !ok || fd.Body == nil || len(fd.Body.List) == 0 {
		return trimmed
	}
	switch st := fd.Body.List[0].(type) {
	case *goast.ForStmt:
		return fmtForHeader(fset, st, trimmed)
	case *goast.RangeStmt:
		return fmtRangeHeader(fset, st, trimmed)
	default:
		return trimmed
	}
}

// fmtForHeader renders a three-clause / condition-only / infinite for header.
func fmtForHeader(fset *gotoken.FileSet, st *goast.ForStmt, fallback string) string {
	hasInitOrPost := st.Init != nil || st.Post != nil
	if !hasInitOrPost {
		if st.Cond == nil {
			return "" // bare `for {}` — gsx would not normally hit this
		}
		if s, ok := fmtNode(fset, st.Cond); ok {
			return s
		}
		return fallback
	}
	initS := ""
	if st.Init != nil {
		if s, ok := fmtNode(fset, st.Init); ok {
			initS = s
		} else {
			return fallback
		}
	}
	condS := ""
	if st.Cond != nil {
		if s, ok := fmtNode(fset, st.Cond); ok {
			condS = s
		} else {
			return fallback
		}
	}
	postS := ""
	if st.Post != nil {
		if s, ok := fmtNode(fset, st.Post); ok {
			postS = s
		} else {
			return fallback
		}
	}
	return initS + "; " + condS + "; " + postS
}

// fmtRangeHeader renders a range clause `[k[, v] :=|=] range X`.
func fmtRangeHeader(fset *gotoken.FileSet, st *goast.RangeStmt, fallback string) string {
	x, ok := fmtNode(fset, st.X)
	if !ok {
		return fallback
	}
	lhs := ""
	if st.Key != nil {
		k, ok := fmtNode(fset, st.Key)
		if !ok {
			return fallback
		}
		lhs = k
		if st.Value != nil {
			v, ok := fmtNode(fset, st.Value)
			if !ok {
				return fallback
			}
			lhs += ", " + v
		}
	}
	if lhs == "" {
		return "range " + x
	}
	return lhs + " " + st.Tok.String() + " range " + x
}

// fmtCaseList formats a switch case expression list via `switch { case LIST: }`,
// re-rendering each case expression joined by ", ".
func fmtCaseList(src string) string {
	trimmed := strings.TrimSpace(src)
	if trimmed == "" {
		return ""
	}
	f, fset, ok := parseWrapped("package p\nfunc _m() {\nswitch {\ncase " + trimmed + ":\n}\n}\n")
	if !ok {
		return trimmed
	}
	fd, ok := f.Decls[0].(*goast.FuncDecl)
	if !ok || fd.Body == nil || len(fd.Body.List) == 0 {
		return trimmed
	}
	sw, ok := fd.Body.List[0].(*goast.SwitchStmt)
	if !ok || sw.Body == nil || len(sw.Body.List) == 0 {
		return trimmed
	}
	cc, ok := sw.Body.List[0].(*goast.CaseClause)
	if !ok {
		return trimmed
	}
	parts := make([]string, 0, len(cc.List))
	for _, e := range cc.List {
		s, ok := fmtNode(fset, e)
		if !ok {
			return trimmed
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}

// extractFuncBody returns the contents of `func _m() {\n…\n}` with the func-body
// indentation level (one leading tab per line) stripped. A single-statement body
// collapses to that one line; multi-statement bodies keep gofmt's newlines.
func extractFuncBody(formatted string) (string, bool) {
	start := strings.Index(formatted, "{\n")
	if start < 0 {
		return "", false
	}
	end := strings.LastIndex(formatted, "}")
	if end < 0 || end <= start {
		return "", false
	}
	body := formatted[start+2 : end]
	body = strings.TrimRight(body, "\n")
	lines := strings.Split(body, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimPrefix(ln, "\t")
	}
	if len(lines) == 1 {
		return lines[0], true
	}
	return strings.Join(lines, "\n"), true
}
