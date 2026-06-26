// Package printer renders a (normalized) gsx AST back to canonical gsx source.
//
// Fprint assumes the AST has already been whitespace-normalized (via
// internal/wsnorm); gsx fmt does that first. The printer builds a width-aware
// pretty.Doc describing the layout, then renders it: cosmetic newlines and tab
// indentation are added only at whitespace-safe boundaries (which wsnorm drops
// on a re-parse), so the output is render-faithful and idempotent.
//
// It depends on github.com/gsxhq/gsx/ast, internal/pretty, plus go/format,
// go/parser, go/token and the standard library.
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
	"github.com/gsxhq/gsx/internal/pretty"
)

// Fprint writes the canonical gsx rendering of f to w, wrapping lists that
// exceed width columns. width <= 0 uses pretty's default (80).
func Fprint(w io.Writer, f *ast.File, width int) error {
	var p printer
	doc := p.file(f)
	if p.err != nil {
		return p.err
	}
	_, err := io.WriteString(w, pretty.Print(doc, width))
	return err
}

// printer accumulates the first I/O-independent error encountered while
// building the document.
type printer struct {
	err error
}

func (p *printer) fail(format string, args ...any) pretty.Doc {
	if p.err == nil {
		p.err = fmt.Errorf(format, args...)
	}
	return pretty.Text("")
}

// file emits `package P` then each declaration separated by one blank line.
// Each declaration already ends with a trailing HardLine, so no extra newline
// is appended here.
func (p *printer) file(f *ast.File) pretty.Doc {
	parts := []pretty.Doc{pretty.Text("package "), pretty.Text(f.Package), pretty.HardLine}
	for _, d := range f.Decls {
		parts = append(parts, pretty.HardLine, p.decl(d))
	}
	return pretty.Concat(parts...)
}

func (p *printer) decl(d ast.Decl) pretty.Doc {
	switch v := d.(type) {
	case *ast.GoChunk:
		return pretty.Concat(multiline(fmtGoChunk(v.Src)), pretty.HardLine)
	case *ast.Component:
		return p.component(v)
	default:
		return p.fail("printer: unknown decl type %T", d)
	}
}

// component emits `component [recv ]Name(params) {` + body + `}`. The body
// always breaks after `{` (like a Go func body): a block body puts each segment
// on its own line; an inline body sits on one indented line; the closing `}`
// sits on its own line.
func (p *printer) component(c *ast.Component) pretty.Doc {
	head := []pretty.Doc{pretty.Text("component ")}
	if c.Recv != "" {
		head = append(head, pretty.Text(fmtRecv(c.Recv)), pretty.Text(" "))
	}
	head = append(head,
		pretty.Text(c.Name), pretty.Text("("), pretty.Text(fmtParams(c.Params)), pretty.Text(") {"))

	body := pretty.Text("")
	if len(c.Body) > 0 {
		inner, _ := p.childrenInner(c.Body)
		body = pretty.Concat(pretty.Indent(pretty.Concat(pretty.HardLine, inner)))
	}
	return pretty.Concat(pretty.Concat(head...), body, pretty.HardLine, pretty.Text("}"), pretty.HardLine)
}

// childrenInner builds the inline content of a children list (the segments,
// joined by SoftLine at safe boundaries when breakable) and reports whether the
// list is breakable. For preserved subtrees use childrenPreserve instead.
func (p *printer) childrenInner(nodes []ast.Markup) (doc pretty.Doc, breakable bool) {
	segs, breakable := segmentChildren(nodes)
	parts := make([]pretty.Doc, 0, len(segs)*2)
	for i, s := range segs {
		if i > 0 {
			parts = append(parts, pretty.SoftLine)
		}
		parts = append(parts, p.segment(s))
	}
	return pretty.Concat(parts...), breakable
}

// segment renders one glued run on a single (flat) line.
func (p *printer) segment(s segment) pretty.Doc {
	parts := make([]pretty.Doc, 0, len(s.nodes))
	for _, n := range s.nodes {
		parts = append(parts, p.markup(n))
	}
	return pretty.Concat(parts...)
}

// element renders <tag attrs>children</tag>.
func (p *printer) element(e *ast.Element) pretty.Doc {
	open := []pretty.Doc{pretty.Text("<"), pretty.Text(e.Tag)}
	for _, a := range e.Attrs {
		open = append(open, pretty.Text(" "), pretty.Text(attrInline(a)))
	}
	openTag := pretty.Concat(open...)

	if e.Void && len(e.Children) == 0 {
		return pretty.Concat(openTag, pretty.Text("/>"))
	}
	close := pretty.Concat(pretty.Text("</"), pretty.Text(e.Tag), pretty.Text(">"))

	if strings.EqualFold(e.Tag, "style") || strings.EqualFold(e.Tag, "script") {
		return pretty.Concat(openTag, pretty.Text(">"), p.rawHoleChildren(e.Children), close)
	}
	if isPreserveTag(e.Tag) {
		return pretty.Concat(openTag, pretty.Text(">"), p.childrenPreserve(e.Children), close)
	}

	inner, breakable := p.childrenInner(e.Children)
	if !breakable {
		return pretty.Concat(openTag, pretty.Text(">"), inner, close)
	}
	body := pretty.Concat(pretty.Indent(pretty.Concat(pretty.SoftLine, inner)), pretty.SoftLine)
	return pretty.Group(pretty.Concat(openTag, pretty.Text(">"), body, close))
}

// childrenPreserve emits pre/textarea bodies verbatim (no added indentation).
func (p *printer) childrenPreserve(nodes []ast.Markup) pretty.Doc {
	parts := make([]pretty.Doc, 0, len(nodes))
	for _, n := range nodes {
		parts = append(parts, p.markup(n))
	}
	return pretty.Concat(parts...)
}

// markup dispatches one node to its Doc builder.
func (p *printer) markup(n ast.Markup) pretty.Doc {
	switch v := n.(type) {
	case *ast.Element:
		return p.element(v)
	case *ast.Fragment:
		return p.fragment(v)
	case *ast.IfMarkup:
		return p.ifMarkup(v)
	case *ast.ForMarkup:
		return p.forMarkup(v)
	case *ast.SwitchMarkup:
		return p.switchMarkup(v)
	case *ast.GoBlock:
		return p.goBlock(v)
	case *ast.Doctype:
		return pretty.Text(v.Text)
	case *ast.HTMLComment:
		return pretty.Concat(pretty.Text("<!--"), pretty.Text(v.Text), pretty.Text("-->"))
	case *ast.Text:
		return pretty.Text(v.Value)
	case *ast.Interp:
		return p.interp(v)
	default:
		return p.fail("printer: unknown markup type %T", n)
	}
}

func (p *printer) fragment(f *ast.Fragment) pretty.Doc {
	inner, breakable := p.childrenInner(f.Children)
	if !breakable {
		return pretty.Concat(pretty.Text("<>"), inner, pretty.Text("</>"))
	}
	body := pretty.Concat(pretty.Indent(pretty.Concat(pretty.SoftLine, inner)), pretty.SoftLine)
	return pretty.Group(pretty.Concat(pretty.Text("<>"), body, pretty.Text("</>")))
}

func (p *printer) interp(i *ast.Interp) pretty.Doc {
	parts := []pretty.Doc{pretty.Text("{ "), pretty.Text(fmtExpr(i.Expr))}
	for _, s := range i.Stages {
		parts = append(parts, pretty.Text(" |> "), pretty.Text(pipeStageStr(s)))
	}
	parts = append(parts, pretty.Text(" }"))
	return pretty.Concat(parts...)
}

func pipeStageStr(s ast.PipeStage) string {
	if s.HasArgs {
		return s.Name + "(" + fmtArgs(s.Args) + ")"
	}
	return s.Name
}

func (p *printer) goBlock(b *ast.GoBlock) pretty.Doc {
	return pretty.Concat(pretty.Text("{{ "), multiline(fmtStmts(b.Code)), pretty.Text(" }}"))
}

// ifMarkup renders `{ if cond { … }[ else …] }` as a group: short → one line,
// long → block body.
func (p *printer) ifMarkup(i *ast.IfMarkup) pretty.Doc {
	return pretty.Group(pretty.Concat(pretty.Text("{ "), p.ifChain(i), pretty.Text(" }")))
}

func (p *printer) ifChain(i *ast.IfMarkup) pretty.Doc {
	parts := []pretty.Doc{pretty.Text("if "), pretty.Text(fmtExpr(i.Cond)), pretty.Text(" {"), p.cfBody(i.Then), pretty.Text("}")}
	if len(i.Else) == 0 {
		return pretty.Concat(parts...)
	}
	if len(i.Else) == 1 {
		if elseIf, ok := i.Else[0].(*ast.IfMarkup); ok {
			parts = append(parts, pretty.Text(" else "), p.ifChain(elseIf))
			return pretty.Concat(parts...)
		}
	}
	parts = append(parts, pretty.Text(" else {"), p.cfBody(i.Else), pretty.Text("}"))
	return pretty.Concat(parts...)
}

func (p *printer) forMarkup(f *ast.ForMarkup) pretty.Doc {
	return pretty.Group(pretty.Concat(
		pretty.Text("{ for "), pretty.Text(fmtClause(f.Clause)), pretty.Text(" {"), p.cfBody(f.Body), pretty.Text("} }")))
}

// cfBody renders a control-flow body between an already-emitted `{` and a
// caller-emitted `}`. Always uses Line (not Text) so the enclosing Group can
// break even when children form a single non-breakable segment: flat mode →
// `{ … }` (Line renders as " "); break mode → newline-indented. This is
// correct for both short bodies (Group fits → collapses) and long bodies (Group
// doesn't fit → breaks). Indent is always applied so break-mode content is one
// tab deeper than the surrounding `{`/`}`.
func (p *printer) cfBody(nodes []ast.Markup) pretty.Doc {
	if len(nodes) == 0 {
		return pretty.Text("")
	}
	inner, _ := p.childrenInner(nodes)
	// A break inserts newline+indent right after `{` and before `}`. If the
	// body's first child leads with a significant space, or its last child
	// trails with one, that break would absorb the space and change the
	// normalized AST. Keep such bodies flat (single-space padded), matching the
	// edge guard segmentChildren already enforces for element children.
	if leadsWithSpace(nodes[0]) || trailsWithSpace(nodes[len(nodes)-1]) {
		return pretty.Concat(pretty.Text(" "), inner, pretty.Text(" "))
	}
	return pretty.Concat(pretty.Indent(pretty.Concat(pretty.Line, inner)), pretty.Line)
}

// switchMarkup always breaks (cases on their own lines) via HardLine.
func (p *printer) switchMarkup(s *ast.SwitchMarkup) pretty.Doc {
	head := []pretty.Doc{pretty.Text("{ switch")}
	if s.Tag != "" {
		head = append(head, pretty.Text(" "), pretty.Text(fmtExpr(s.Tag)))
	}
	head = append(head, pretty.Text(" {"))

	caseParts := make([]pretty.Doc, 0, len(s.Cases))
	for _, c := range s.Cases {
		label := pretty.Text("default:")
		if !c.Default {
			label = pretty.Concat(pretty.Text("case "), pretty.Text(fmtCaseList(c.List)), pretty.Text(":"))
		}
		caseParts = append(caseParts, pretty.HardLine, pretty.Concat(label, p.caseBody(c.Body)))
	}
	return pretty.Concat(
		pretty.Concat(head...),
		pretty.Indent(pretty.Concat(caseParts...)),
		pretty.HardLine, pretty.Text("} }"))
}

// caseBody renders a switch arm. Block → each segment on its own line (one
// deeper than the `case`); inline → follows the colon.
func (p *printer) caseBody(nodes []ast.Markup) pretty.Doc {
	if len(nodes) == 0 {
		return pretty.Text("")
	}
	inner, breakable := p.childrenInner(nodes)
	if !breakable {
		return inner
	}
	return pretty.Indent(pretty.Concat(pretty.HardLine, inner))
}

// multiline turns a possibly multi-line Go fragment into a Doc: lines are
// joined with HardLine so the engine re-indents continuation lines to the
// current level (and any multi-line fragment forces its enclosing group to
// break). A single-line fragment is a plain Text.
func multiline(s string) pretty.Doc {
	if !strings.Contains(s, "\n") {
		return pretty.Text(s)
	}
	lines := strings.Split(s, "\n")
	parts := make([]pretty.Doc, 0, len(lines)*2)
	for i, ln := range lines {
		if i > 0 {
			parts = append(parts, pretty.HardLine)
		}
		parts = append(parts, pretty.Text(ln))
	}
	return pretty.Concat(parts...)
}

// --- attributes (inline for now; real wrapping is a later task) -------------

// attrInline renders an attribute to its single-line gsx text, exactly as the
// pre-IR printer did. (Multi-line attribute layout is added later.)
func attrInline(a ast.Attr) string {
	var b strings.Builder
	writeAttrInline(&b, a)
	return b.String()
}

func writeAttrInline(b *strings.Builder, a ast.Attr) {
	switch v := a.(type) {
	case *ast.StaticAttr:
		b.WriteString(v.Name)
		b.WriteString(`="`)
		b.WriteString(v.Value)
		b.WriteString(`"`)
	case *ast.BoolAttr:
		b.WriteString(v.Name)
	case *ast.ExprAttr:
		b.WriteString(v.Name)
		b.WriteString("={")
		b.WriteString(fmtExpr(v.Expr))
		for _, s := range v.Stages {
			b.WriteString(" |> ")
			b.WriteString(pipeStageStr(s))
		}
		b.WriteString("}")
	case *ast.SpreadAttr:
		b.WriteString("{ ")
		if len(v.Stages) > 0 {
			b.WriteString("(")
			b.WriteString(fmtExpr(v.Expr))
			for _, s := range v.Stages {
				b.WriteString(" |> ")
				b.WriteString(pipeStageStr(s))
			}
			b.WriteString(")... }")
		} else {
			b.WriteString(fmtExpr(v.Expr))
			b.WriteString("... }")
		}
	case *ast.ClassAttr:
		b.WriteString(v.Name)
		b.WriteString("={ ")
		for i, part := range v.Parts {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(fmtExpr(part.Expr))
			for _, s := range part.Stages {
				b.WriteString(" |> ")
				b.WriteString(pipeStageStr(s))
			}
			if part.Cond != "" {
				b.WriteString(": ")
				b.WriteString(fmtExpr(part.Cond))
			}
		}
		b.WriteString(" }")
	case *ast.CondAttr:
		b.WriteString("{ ")
		writeCondAttrChain(b, v)
		b.WriteString(" }")
	case *ast.MarkupAttr:
		b.WriteString(v.Name)
		b.WriteString("={ ")
		for _, n := range v.Value {
			b.WriteString(markupInlineString(n))
		}
		b.WriteString(" }")
	case *ast.JSAttr:
		b.WriteString(v.Name)
		b.WriteString(`="`)
		writeRawHoleString(b, v.Segments)
		b.WriteString(`"`)
	default:
		// Attribute types are AST-defined and enumerable; an unrecognized type
		// here is a programming error, not user input — skip it explicitly.
	}
}

func writeCondAttrChain(b *strings.Builder, c *ast.CondAttr) {
	b.WriteString("if ")
	b.WriteString(fmtExpr(c.Cond))
	b.WriteString(" {")
	writeCondAttrList(b, c.Then)
	b.WriteString("}")
	if len(c.Else) == 0 {
		return
	}
	if len(c.Else) == 1 {
		if elseIf, ok := c.Else[0].(*ast.CondAttr); ok {
			b.WriteString(" else ")
			writeCondAttrChain(b, elseIf)
			return
		}
	}
	b.WriteString(" else {")
	writeCondAttrList(b, c.Else)
	b.WriteString("}")
}

func writeCondAttrList(b *strings.Builder, attrs []ast.Attr) {
	for _, a := range attrs {
		b.WriteString(" ")
		writeAttrInline(b, a)
	}
	if len(attrs) > 0 {
		b.WriteString(" ")
	}
}

// markupInlineString renders a markup node to its flat gsx text (used inside
// attribute slots, which always lay out inline). It reuses the Doc builder and
// prints it flat at a very wide margin so no Line ever breaks.
func markupInlineString(n ast.Markup) string {
	var p printer
	return pretty.Print(p.markup(n), 1<<30)
}

// rawHoleChildren renders <style>/<script> children: Text verbatim, Interp with
// the @{ } delimiter. Pipeline Stages are preserved faithfully.
func (p *printer) rawHoleChildren(nodes []ast.Markup) pretty.Doc {
	var b strings.Builder
	writeRawHoleString(&b, nodes)
	return pretty.Text(b.String())
}

func writeRawHoleString(b *strings.Builder, nodes []ast.Markup) {
	for _, n := range nodes {
		switch v := n.(type) {
		case *ast.Text:
			b.WriteString(v.Value)
		case *ast.Interp:
			b.WriteString("@{ ")
			b.WriteString(fmtExpr(v.Expr))
			for _, s := range v.Stages {
				b.WriteString(" |> ")
				b.WriteString(pipeStageStr(s))
			}
			b.WriteString(" }")
		default:
			b.WriteString(markupInlineString(n))
		}
	}
}

// isPreserveTag mirrors wsnorm: pre/textarea/script/style keep bodies verbatim.
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
