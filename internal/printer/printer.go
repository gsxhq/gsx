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
	goscanner "go/scanner"
	gotoken "go/token"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/cssfmt"
	"github.com/gsxhq/gsx/internal/goexprshape"
	"github.com/gsxhq/gsx/internal/jsfmt"
	"github.com/gsxhq/gsx/internal/pretty"
	"github.com/gsxhq/gsx/internal/rawfmt"
)

// Fprint writes the canonical gsx rendering of f to w, wrapping lists that
// exceed width columns. width <= 0 uses pretty's default (80).
func Fprint(w io.Writer, f *ast.File, width int) error {
	return FprintWith(w, f, width, defaultCSSFormatter(width), defaultJSFormatter(width))
}

// FprintWith is Fprint with explicit CSS and JS formatters for <style>/<script>
// bodies. A nil formatter leaves that body verbatim.
func FprintWith(w io.Writer, f *ast.File, width int, cssFmt, jsFmt rawfmt.Formatter) error {
	p := printer{cssFmt: cssFmt, jsFmt: jsFmt}
	doc := p.file(f)
	if p.err != nil {
		return p.err
	}
	_, err := io.WriteString(w, pretty.Print(doc, width))
	return err
}

// defaultCSSFormatter binds the built-in cssfmt to the print width.
func defaultCSSFormatter(width int) rawfmt.Formatter {
	return func(src []byte) ([]byte, error) { return cssfmt.Format(src, width) }
}

func defaultJSFormatter(width int) rawfmt.Formatter {
	return func(src []byte) ([]byte, error) { return jsfmt.Format(src, width) }
}

// printer accumulates the first I/O-independent error encountered while
// building the document.
type printer struct {
	err    error
	cssFmt rawfmt.Formatter // nil → no CSS formatting (style stays verbatim)
	jsFmt  rawfmt.Formatter
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
	// The package clause, optionally preceded by its doc comment. Routing the
	// doc + clause through go/format keeps the comment attached and canonical.
	pkgClause := "package " + f.Package
	if f.Doc != "" {
		pkgClause = fmtGoChunk(f.Doc + "\n" + pkgClause)
	}
	parts := []pretty.Doc{multiline(pkgClause), pretty.HardLine}
	for i, d := range f.Decls {
		// Each declaration is preceded by a blank line, EXCEPT when the previous
		// declaration is a Go chunk whose source runs directly into this one (a
		// doc comment sitting immediately above a `component`, with no blank line
		// between them in the source): such a comment stays attached, gofmt-style.
		blank := true
		if i > 0 {
			switch prev := f.Decls[i-1].(type) {
			case *ast.GoChunk:
				if !endsWithBlankLine(prev.Src) {
					blank = false
				}
			case *ast.GoWithElements:
				// Mirror the GoChunk check above using the trailing GoText part's
				// own source (the verbatim Go text after the last embedded
				// element, up to this next decl) — same "did the source have a
				// blank line here" question GoChunk answers via its own Src.
				if last, ok := prev.Parts[len(prev.Parts)-1].(ast.GoText); ok && !endsWithBlankLine(last.Src) {
					blank = false
				}
			}
		}
		if blank {
			parts = append(parts, pretty.HardLine)
		}
		parts = append(parts, p.decl(d))
	}
	return pretty.Concat(parts...)
}

// endsWithBlankLine reports whether src's trailing whitespace contains a blank
// line (two or more newlines) — i.e. the source had a blank line between this Go
// chunk and whatever follows it. Trailing spaces/tabs on the last line are
// ignored. A single trailing newline (the chunk runs straight into the next
// declaration) returns false.
func endsWithBlankLine(src string) bool {
	s := strings.TrimRight(src, " \t")
	return strings.HasSuffix(s, "\n\n")
}

func (p *printer) decl(d ast.Decl) pretty.Doc {
	switch v := d.(type) {
	case *ast.GoChunk:
		return pretty.Concat(multiline(fmtGoChunk(v.Src)), pretty.HardLine)
	case *ast.GoWithElements:
		return pretty.Concat(p.goWithElements(v), pretty.HardLine)
	case *ast.Component:
		return p.component(v)
	default:
		return p.fail("printer: unknown decl type %T", d)
	}
}

// goWithElements renders a *ast.GoWithElements decl: Go source text
// interleaved with one or more gsx elements sitting in expression position
// (e.g. `var help = <a href={u}>{ label }</a>`). Each GoText part is an
// INCOMPLETE Go fragment — the Go code before, after, or between embedded
// elements (e.g. "var help = ", or "" between two adjacent elements) — so,
// unlike a GoChunk, it cannot be routed through fmtGoChunk/go-format: that
// function requires a syntactically complete compilation unit, and a bare
// "var help = " isn't one.
//
// The parts are NOT relayed verbatim, though. fmtGoExprParts first restores
// syntactic completeness — standing a placeholder identifier in for each gsx
// value — so go/format can lay the Go text out exactly as it would any other
// top-level Go; the formatted text comes back re-split into GoText parts. Only
// when go/format rejects the substituted source (Go the gsx parser accepted but
// Go's own parser does not) do the original, unformatted parts flow through.
//
// Each resulting GoText part is then printed via multiline (which lays out
// embedded newlines at the CURRENT indent; at this decl's top-level position
// that indent is zero, so a multi-line fragment's own indentation — carried as
// literal bytes inside each line's Text — reproduces unchanged) and each
// *ast.Element part goes through the ordinary element printer, the exact same
// one every other element in the file is printed with.
//
// Only the outermost edges are trimmed, mirroring fmtGoChunk's TrimSpace: the
// leading whitespace of the FIRST part and the trailing whitespace of the
// LAST part are the blank-line padding between this decl and its neighbors
// (file's own blank-line-separator logic re-derives that padding, exactly as
// it does for a GoChunk's leading/trailing whitespace).
func (p *printer) goWithElements(v *ast.GoWithElements) pretty.Doc {
	parts := v.Parts
	formatted, shapes, ok := p.fmtGoExprParts(parts)
	if ok {
		parts = formatted
	}
	// partResult maps each value's classification (shapes is indexed by value
	// order) onto its parts index, so a GoText run can look at its NEIGHBORING
	// value (not just the one shapeIdx has reached) to decide whether to strip
	// a pre-existing decorative paren.
	partResult := make([]goexprshape.Result, len(parts))
	shapeIdx := 0
	for i, part := range parts {
		if _, ok := part.(ast.GoText); ok {
			continue
		}
		if shapeIdx < len(shapes) {
			partResult[i] = shapes[shapeIdx]
		}
		shapeIdx++
	}
	// eligible reports whether parts[i] is an *Element/*Fragment in a position
	// safe to visually wrap in (...) — the only case that ever carries a
	// decorative paren. Never an EmbeddedInterp (f`...` literal), which codegen
	// doesn't lower into a closure and so has no matching strip step (see
	// internal/codegen's emit.go GoWithElements case).
	eligible := func(i int) bool {
		switch parts[i].(type) {
		case *ast.Element, *ast.Fragment:
			return partResult[i].Shape == goexprshape.ParenWrap
		default:
			return false
		}
	}
	// decoratedParen reports whether parts[i] is ALSO currently sitting inside
	// a real paren in this source (not just eligible for one) — e.g. a `var
	// (…)` group's own closing paren can immediately follow an unwrapped,
	// eligible value with no relation to it, and must be left alone.
	decoratedParen := func(i int) bool {
		return eligible(i) && partResult[i].Wrapped
	}

	last := len(parts) - 1
	docs := make([]pretty.Doc, 0, len(parts))
	precedingGoText := ""
	for i, part := range parts {
		if gt, ok := part.(ast.GoText); ok {
			src := trimGoTextEdges(gt.Src, i == 0, i == last)
			if i > 0 && decoratedParen(i-1) {
				src = goexprshape.StripLeadingParen(src)
			}
			if i < last && decoratedParen(i+1) {
				src = goexprshape.StripTrailingParen(src)
			}
			if src != "" {
				precedingGoText = src
			}
			if src == "" {
				continue
			}
			docs = append(docs, multiline(src))
			continue
		}
		doc, ok := p.goExprValue(part)
		if !ok {
			return p.fail("printer: unknown Go-expression part type %T", part)
		}
		// A gsx value starts partway along its Go line (`n := <div>`), and the Go
		// text before it is literal bytes inside a Text doc — invisible to the
		// pretty printer, whose indent level for this decl is zero. realTabDepth
		// recovers the real Go nesting depth from that literal text so children
		// and the closing tag/paren land at the right tab depth regardless.
		depth := realTabDepth(precedingGoText)
		if eligible(i) {
			doc = parenWrapDoc(doc)
		}
		docs = append(docs, indentN(depth, doc))
	}
	return pretty.Concat(docs...)
}

// parenWrapDoc wraps doc in "(" ")" that render only when doc breaks — either
// because it genuinely can't fit on one line (author-forced multi-line
// content) or because the line is too wide. Mirrors the element printer's own
// opening-tag/children Group+SoftLine shape (see the element method):
// SoftLine never forces a break, so Group's forced flag reduces to whatever
// doc itself carries, and IfBreak's branches are Text-only (never
// Line/HardLine) so the parens never spuriously force the group to break.
//
// The parens this emits are purely cosmetic for the .gsx source: codegen
// strips the matching literal "(" / ")" out of the surrounding GoText before
// splicing in the element's lowered closure (see emit.go), so they never
// reach the generated .x.go and can't trip Go's automatic semicolon insertion
// on the closure's own trailing "}" / ")".
func parenWrapDoc(doc pretty.Doc) pretty.Doc {
	return pretty.Group(pretty.Concat(
		pretty.IfBreak(pretty.Text("("), pretty.Text("")),
		pretty.Indent(pretty.Concat(pretty.SoftLine, doc)),
		pretty.SoftLine,
		pretty.IfBreak(pretty.Text(")"), pretty.Text("")),
	))
}

// indentN wraps d in n ordinary Indent levels — used to bring a value's
// break-indentation up to the real Go nesting depth its preceding GoText sits
// at, which the printer's own indent counter (always 0 for a GoWithElements
// decl) cannot see on its own. See realTabDepth.
func indentN(n int, d pretty.Doc) pretty.Doc {
	for range n {
		d = pretty.Indent(d)
	}
	return d
}

// realTabDepth returns the leading-tab count on the last line of
// precedingGoText — the real Go indentation depth the next gsx value sits at,
// which the enclosing GoWithElements decl's own indent counter (always 0,
// since its surrounding Go text is emitted as literal bytes) cannot see.
func realTabDepth(precedingGoText string) int {
	line := precedingGoText
	if i := strings.LastIndexByte(line, '\n'); i >= 0 {
		line = line[i+1:]
	}
	n := 0
	for n < len(line) && line[n] == '\t' {
		n++
	}
	return n
}

// trimGoTextEdges trims leading whitespace from src when first is true, and
// trailing whitespace when last is true; src is returned unchanged when both
// are false (an internal GoText part, between two elements). Shared by the
// printer (goWithElements above) and the printer test suite's Go-fragment
// canonicalization (canonGo), so both sides of the faithfulness comparison
// treat a GoWithElements decl's outer edges identically.
func trimGoTextEdges(src string, first, last bool) string {
	if first {
		src = strings.TrimLeft(src, " \t\n\r")
	}
	if last {
		src = strings.TrimRight(src, " \t\n\r")
	}
	return src
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
	head = append(head, pretty.Text(c.Name))
	if c.TypeParams != "" {
		head = append(head, pretty.Text("["), pretty.Text(fmtTypeParams(c.TypeParams)), pretty.Text("]"))
	}
	head = append(head, pretty.Text("("), pretty.Text(fmtParams(c.Params)), pretty.Text(") {"))

	body := pretty.Text("")
	if len(c.Body) > 0 {
		inner, _ := p.childrenInner(c.Body)
		if leadsWithSpace(c.Body[0]) || trailsWithSpace(c.Body[len(c.Body)-1]) {
			// edge-unsafe: keep inline so no newline absorbs the significant space
			return pretty.Concat(pretty.Concat(head...), inner, pretty.Text("}"), pretty.HardLine)
		}
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
	attrs := make([]pretty.Doc, 0, len(e.Attrs)*2)
	for _, a := range e.Attrs {
		if c, ok := a.(*ast.CommentAttr); ok {
			sep := pretty.Line
			if c.Trailing {
				sep = pretty.Text(" ") // glue to the previous attr's line
			}
			attrs = append(attrs, sep, p.attrDoc(a))
			if !c.Block {
				// A `//` line comment cannot share a flat line with what follows;
				// force the opening-tag group to break. Block comments may stay inline.
				attrs = append(attrs, pretty.BreakParent)
			}
			continue
		}
		attrs = append(attrs, pretty.Line, p.attrDoc(a))
	}
	tag := pretty.Text(e.Tag)
	if e.TypeArgs != "" {
		tag = pretty.Concat(tag, pretty.Text("["), pretty.Text(fmtTypeArgs(e.TypeArgs)), pretty.Text("]"))
	}
	// Opening tag group: flat → `<tag a b>`; broken → each attr on its own line
	// with `>` (or `/>`) alone. A forced break inside any attr (CondAttr) breaks
	// the group; otherwise it breaks only on width overflow.
	selfClose := e.Void && len(e.Children) == 0
	tail := pretty.Text(">")
	if selfClose {
		tail = pretty.Text("/>")
	}
	var openGroupBody pretty.Doc
	if len(e.Attrs) == 0 {
		openGroupBody = pretty.Concat(pretty.Text("<"), tag, tail)
	} else {
		openGroupBody = pretty.Concat(
			pretty.Text("<"), tag,
			pretty.Indent(pretty.Concat(attrs...)),
			pretty.SoftLine, tail)
	}
	openTag := pretty.Group(openGroupBody)

	if selfClose {
		return openTag
	}
	close := pretty.Concat(pretty.Text("</"), pretty.Text(e.Tag), pretty.Text(">"))

	if strings.EqualFold(e.Tag, "script") {
		if p.jsFmt != nil && isExecutableScript(e) {
			segments, holes := nodesToBody(e.Children)
			if doc, ok := rawfmt.Format(segments, holes, p.jsFmt); ok {
				return pretty.Concat(openTag, doc, close)
			}
		}
		return pretty.Concat(openTag, p.rawHoleChildren(e.Children), close)
	}
	if strings.EqualFold(e.Tag, "style") {
		if p.cssFmt != nil {
			segments, holes := nodesToBody(e.Children)
			if doc, ok := rawfmt.Format(segments, holes, p.cssFmt); ok {
				return pretty.Concat(openTag, doc, close)
			}
		}
		return pretty.Concat(openTag, p.rawHoleChildren(e.Children), close)
	}
	if isPreserveTag(e.Tag) {
		return pretty.Concat(openTag, p.childrenPreserve(e.Children), close)
	}

	if len(e.Children) == 0 {
		return pretty.Concat(openTag, close)
	}
	inner, edgeSafe := p.childrenInner(e.Children)
	if !edgeSafe {
		// Edge-unsafe children cannot host added breaks (a break would absorb a
		// significant leading/trailing space): keep them inline after `>`.
		return pretty.Concat(openTag, inner, close)
	}
	// One element group with the opening-tag group NESTED inside it. Structural
	// rule: a list containing a block-level child always breaks so the document
	// hierarchy is visible (the BreakParent forces it regardless of width); an
	// inline-only (text/interp) list stays on one line and breaks only if the
	// opening tag itself wraps. The nested opening-tag group re-decides
	// independently, so short attributes stay inline even when children break,
	// and a CondAttr's BreakParent inside the tag also forces the children open.
	force := pretty.Text("")
	if hasBlockChild(e.Children) || e.ChildrenMultiline {
		force = pretty.BreakParent
	}
	return pretty.Group(pretty.Concat(
		openTag, force,
		pretty.Indent(pretty.Concat(pretty.SoftLine, inner)),
		pretty.SoftLine, close))
}

// attrDoc renders one attribute as a Doc. Conditional attributes are rendered
// with their `{ if … { … } }` body broken across lines (templ-style), emitting
// a BreakParent so the enclosing opening-tag group breaks. ExprAttr and
// ClassAttr use fmtExprDoc so long or comment-bearing values can be multi-line.
func (p *printer) attrDoc(a ast.Attr) pretty.Doc {
	switch v := a.(type) {
	case *ast.CondAttr:
		return pretty.Concat(pretty.BreakParent, pretty.Text("{ "), p.condAttrChainDoc(v), pretty.Text(" }"))
	case *ast.ExprAttr:
		val := []pretty.Doc{fmtExprDoc(v.Expr)}
		for _, s := range v.Stages {
			val = append(val, pretty.Text(" |> "), multiline(pipeStageStr(s)))
		}
		return wrapAttrValue(v.Name, pretty.SoftLine, pretty.Concat(val...))
	case *ast.ClassAttr:
		parts := make([]pretty.Doc, 0, len(v.Parts)*2)
		for i, part := range v.Parts {
			if i > 0 {
				parts = append(parts, pretty.Text(","), pretty.Line)
			}
			parts = append(parts, p.classPartDoc(part))
		}
		return wrapAttrValue(v.Name, pretty.Line, pretty.Group(pretty.Concat(parts...)))
	default:
		return pretty.Text(attrInline(a))
	}
}

// classPartDoc renders one composed class/style contribution: `expr`,
// `expr |> stage`, `expr: cond`, or a value-form if/switch.
func (p *printer) classPartDoc(part ast.ClassPart) pretty.Doc {
	if part.CF != nil {
		return p.valueCFDoc(part.CF)
	}
	if part.CSSSegments != nil {
		seg := []pretty.Doc{pretty.Text(embeddedLiteralString(ast.EmbeddedCSS, part.CSSSegments, embeddedDelim(part.CSSDoubleQuoted)))}
		if part.Cond != "" {
			seg = append(seg, pretty.Text(": "), multiline(fmtExpr(part.Cond)))
		}
		return pretty.Concat(seg...)
	}
	seg := []pretty.Doc{fmtExprDoc(part.Expr)}
	for _, s := range part.Stages {
		seg = append(seg, pretty.Text(" |> "), multiline(pipeStageStr(s)))
	}
	if part.Cond != "" {
		seg = append(seg, pretty.Text(": "), multiline(fmtExpr(part.Cond)))
	}
	return pretty.Concat(seg...)
}

func (p *printer) valueCFDoc(cf *ast.ValueCF) pretty.Doc {
	if cf.If != nil {
		return pretty.Group(p.valueIfChain(cf.If))
	}
	return pretty.Group(p.valueSwitchDoc(cf.Switch))
}

func (p *printer) valueIfChain(i *ast.ValueIf) pretty.Doc {
	parts := []pretty.Doc{
		pretty.Text("if "), multiline(fmtExpr(i.Cond)),
		pretty.Text(" {"), p.valueArmBody(i.Then), pretty.Text("}"),
	}
	switch {
	case i.ElseIf != nil:
		parts = append(parts, pretty.Text(" else "), p.valueIfChain(i.ElseIf))
	case i.Else != nil:
		parts = append(parts, pretty.Text(" else {"), p.valueArmBody(i.Else), pretty.Text("}"))
	}
	return pretty.Concat(parts...)
}

// valueArmBody renders ` <expr> ` flat, or newline-indented when the enclosing
// Group breaks (Line = space when flat, newline+indent when broken).
func (p *printer) valueArmBody(a *ast.ValueArm) pretty.Doc {
	return pretty.Concat(pretty.Indent(pretty.Concat(pretty.Line, p.valueArmDoc(a))), pretty.Line)
}

func (p *printer) valueArmDoc(a *ast.ValueArm) pretty.Doc {
	seg := []pretty.Doc{fmtExprDoc(a.Expr)}
	for _, s := range a.Stages {
		seg = append(seg, pretty.Text(" |> "), multiline(pipeStageStr(s)))
	}
	return pretty.Concat(seg...)
}

func (p *printer) valueSwitchDoc(s *ast.ValueSwitch) pretty.Doc {
	head := []pretty.Doc{pretty.Text("switch")}
	if s.Tag != "" {
		head = append(head, pretty.Text(" "), multiline(fmtExpr(s.Tag)))
	}
	head = append(head, pretty.Text(" {"))
	cases := make([]pretty.Doc, 0, len(s.Cases))
	for _, c := range s.Cases {
		label := pretty.Text("default:")
		if !c.Default {
			label = pretty.Concat(pretty.Text("case "), pretty.Text(fmtCaseList(c.List)), pretty.Text(":"))
		}
		cases = append(cases,
			pretty.Line, label,
			pretty.Indent(pretty.Concat(pretty.Line, p.valueArmDoc(c.Value))))
	}
	return pretty.Group(pretty.Concat(pretty.Concat(head...), pretty.Concat(cases...), pretty.Line, pretty.Text("}")))
}

// wrapAttrValue renders `name={<sep>value<sep>}` where sep is the flat padding
// for this attribute kind: SoftLine for an expr attr (flat → `name={value}`)
// or Line for a class attr (flat → `name={ value }`). When the value is
// multi-line (carries a forced break) or overflows, the Group breaks and both
// seps become newline+indent: `name={` / indented value / `}`.
func wrapAttrValue(name string, sep pretty.Doc, value pretty.Doc) pretty.Doc {
	return pretty.Group(pretty.Concat(
		pretty.Text(name), pretty.Text("={"),
		pretty.Indent(pretty.Concat(sep, value)),
		sep, pretty.Text("}")))
}

func (p *printer) condAttrChainDoc(c *ast.CondAttr) pretty.Doc {
	parts := []pretty.Doc{pretty.Text("if "), multiline(fmtExpr(c.Cond)), pretty.Text(" {"),
		p.condAttrListDoc(c.Then), pretty.Text("}")}
	if len(c.Else) == 0 {
		return pretty.Concat(parts...)
	}
	if len(c.Else) == 1 {
		if elseIf, ok := c.Else[0].(*ast.CondAttr); ok {
			parts = append(parts, pretty.Text(" else "), p.condAttrChainDoc(elseIf))
			return pretty.Concat(parts...)
		}
	}
	parts = append(parts, pretty.Text(" else {"), p.condAttrListDoc(c.Else), pretty.Text("}"))
	return pretty.Concat(parts...)
}

// condAttrListDoc lays a conditional attribute's inner attrs one per line.
// The trailing HardLine ensures the closing `}` of the surrounding `if`/`else`
// block lands on its own line (same pattern as cfBody's trailing Line).
func (p *printer) condAttrListDoc(attrs []ast.Attr) pretty.Doc {
	inner := make([]pretty.Doc, 0, len(attrs)*2)
	for _, a := range attrs {
		sep := pretty.Doc(pretty.HardLine)
		if c, ok := a.(*ast.CommentAttr); ok && c.Trailing {
			sep = pretty.Text(" ") // glue a trailing comment to the previous attr's line
		}
		inner = append(inner, sep, p.attrDoc(a))
	}
	return pretty.Concat(pretty.Indent(pretty.Concat(inner...)), pretty.HardLine)
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
	case *ast.Comment:
		// Source-only content comment; canonical braced form. The `{// text }`
		// line form is safe on one line here — the printer controls layout, so
		// nothing after `}` is on the comment's line to be swallowed.
		if v.Block {
			return pretty.Concat(pretty.Text("{/* "), pretty.Text(v.Text), pretty.Text(" */}"))
		}
		return pretty.Concat(pretty.Text("{// "), pretty.Text(v.Text), pretty.HardLine, pretty.Text("}"))
	case *ast.Text:
		return pretty.Text(v.Value)
	case *ast.Interp:
		return p.interp(v)
	case *ast.EmbeddedInterp:
		return p.embeddedInterp(v)
	default:
		return p.fail("printer: unknown markup type %T", n)
	}
}

func (p *printer) fragment(f *ast.Fragment) pretty.Doc {
	inner, breakable := p.childrenInner(f.Children)
	if !breakable {
		return pretty.Concat(pretty.Text("<>"), inner, pretty.Text("</>"))
	}
	// An author line break after `<>` is preserved (force the group open);
	// otherwise the fragment breaks only on width overflow.
	force := pretty.Text("")
	if f.ChildrenMultiline {
		force = pretty.BreakParent
	}
	body := pretty.Concat(pretty.Indent(pretty.Concat(pretty.SoftLine, inner)), pretty.SoftLine)
	return pretty.Group(pretty.Concat(pretty.Text("<>"), force, body, pretty.Text("</>")))
}

func (p *printer) interp(i *ast.Interp) pretty.Doc {
	parts := []pretty.Doc{pretty.Text("{ "), fmtExprDoc(i.Expr)}
	for _, s := range i.Stages {
		parts = append(parts, pretty.Text(" |> "), multiline(pipeStageStr(s)))
	}
	parts = append(parts, pretty.Text(" }"))
	return pretty.Concat(parts...)
}

// embeddedInterp renders a body/child interpolating literal
// `{f`…@{expr}…` [|> stage…]}`. The form is preserved as-is (not canonicalized
// to interleaved Interp nodes): an f`…` literal wrapped in braces, with an
// optional whole-literal pipeline after the closing backtick.
func (p *printer) embeddedInterp(v *ast.EmbeddedInterp) pretty.Doc {
	var b strings.Builder
	b.WriteString("{")
	b.WriteString(embeddedLiteralString(ast.EmbeddedText, v.Segments, embeddedDelim(v.DoubleQuoted)))
	for _, s := range v.Stages {
		b.WriteString(" |> ")
		b.WriteString(pipeStageStr(s))
	}
	b.WriteString("}")
	return pretty.Text(b.String())
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
	parts := []pretty.Doc{pretty.Text("if "), multiline(fmtExpr(i.Cond)), pretty.Text(" {"), p.cfBody(i.Then, i.ThenMultiline), pretty.Text("}")}
	if len(i.Else) == 0 {
		return pretty.Concat(parts...)
	}
	if len(i.Else) == 1 {
		if elseIf, ok := i.Else[0].(*ast.IfMarkup); ok {
			parts = append(parts, pretty.Text(" else "), p.ifChain(elseIf))
			return pretty.Concat(parts...)
		}
	}
	parts = append(parts, pretty.Text(" else {"), p.cfBody(i.Else, i.ElseMultiline), pretty.Text("}"))
	return pretty.Concat(parts...)
}

func (p *printer) forMarkup(f *ast.ForMarkup) pretty.Doc {
	return pretty.Group(pretty.Concat(
		pretty.Text("{ for "), pretty.Text(fmtClause(f.Clause)), pretty.Text(" {"), p.cfBody(f.Body, f.BodyMultiline), pretty.Text("} }")))
}

// cfBody renders a control-flow body between an already-emitted `{` and a
// caller-emitted `}`. Always uses Line (not Text) so the enclosing Group can
// break even when children form a single non-breakable segment: flat mode →
// `{ … }` (Line renders as " "); break mode → newline-indented. This is
// correct for both short bodies (Group fits → collapses) and long bodies (Group
// doesn't fit → breaks). Indent is always applied so break-mode content is one
// tab deeper than the surrounding `{`/`}`. multiline is true when the source
// placed a line break after the body's opening `{` (ast.*Multiline), in which
// case the vertical layout is preserved even for an inline-only body that fits.
func (p *printer) cfBody(nodes []ast.Markup, multiline bool) pretty.Doc {
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
	body := pretty.Concat(pretty.Indent(pretty.Concat(pretty.Line, inner)), pretty.Line)
	// Structural rule: a control-flow body containing a block-level child always
	// breaks (the BreakParent forces the enclosing if/for group open), so e.g.
	// `{ for … { <Card/> } }` shows its hierarchy. An author line break after `{`
	// is likewise preserved. An inline-only body the author kept inline stays on
	// one line and breaks only if it overflows the width.
	if hasBlockChild(nodes) || multiline {
		return pretty.Concat(pretty.BreakParent, body)
	}
	return body
}

// switchMarkup always breaks (cases on their own lines) via HardLine, unless
// any arm body is edge-unsafe (leads/trails with a significant space), in which
// case the whole switch is rendered inline to avoid a HardLine absorbing the space.
func (p *printer) switchMarkup(s *ast.SwitchMarkup) pretty.Doc {
	head := []pretty.Doc{pretty.Text("{ switch")}
	if s.Tag != "" {
		head = append(head, pretty.Text(" "), multiline(fmtExpr(s.Tag)))
	}
	head = append(head, pretty.Text(" {"))

	if switchHasEdgeUnsafeArm(s) {
		// inline form: no HardLines so edge spaces in arm bodies aren't absorbed
		inlineCases := make([]pretty.Doc, 0, len(s.Cases))
		for _, c := range s.Cases {
			label := pretty.Text("default:")
			if !c.Default {
				label = pretty.Concat(pretty.Text("case "), pretty.Text(fmtCaseList(c.List)), pretty.Text(":"))
			}
			inlineCases = append(inlineCases, pretty.Concat(label, p.caseBody(c.Body)))
		}
		return pretty.Concat(
			pretty.Concat(head...),
			pretty.Concat(inlineCases...),
			pretty.Text("} }"))
	}

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

// switchHasEdgeUnsafeArm reports whether any arm body would lose a significant
// edge space if the switch were laid out multi-line (HardLine after each arm).
func switchHasEdgeUnsafeArm(s *ast.SwitchMarkup) bool {
	for _, c := range s.Cases {
		if len(c.Body) > 0 && (leadsWithSpace(c.Body[0]) || trailsWithSpace(c.Body[len(c.Body)-1])) {
			return true
		}
	}
	return false
}

// caseBody renders a switch arm. Block → each segment on its own line (one
// deeper than the `case`); inline → follows the colon.
func (p *printer) caseBody(nodes []ast.Markup) pretty.Doc {
	if len(nodes) == 0 {
		return pretty.Text("")
	}
	inner, edgeSafe := p.childrenInner(nodes)
	// A switch arm with a block-level child takes its own indented line(s); an
	// inline-only (or edge-unsafe) arm follows the colon on the same line.
	if !edgeSafe || !hasBlockChild(nodes) {
		return inner
	}
	return pretty.Indent(pretty.Concat(pretty.HardLine, inner))
}

// multiline turns a possibly multi-line Go fragment into a Doc: lines are
// joined with HardLine so the engine re-indents continuation lines to the
// current level (and any multi-line fragment forces its enclosing group to
// break). Newlines INSIDE raw string literals are the string's value, not
// layout — re-indenting them would change program behavior — so those stay
// verbatim inside a single Text (the engine's column tracking handles embedded
// newlines), with BreakParent still forcing the enclosing group to break. A
// single-line fragment is a plain Text.
func multiline(s string) pretty.Doc {
	if !strings.Contains(s, "\n") {
		return pretty.Text(s)
	}
	segs := splitOutsideRawStrings(s)
	parts := make([]pretty.Doc, 0, len(segs)*2+1)
	for i, seg := range segs {
		if i > 0 {
			parts = append(parts, pretty.HardLine)
		}
		parts = append(parts, pretty.Text(seg))
	}
	parts = append(parts, pretty.BreakParent)
	return pretty.Concat(parts...)
}

// splitOutsideRawStrings splits s at each newline that does NOT fall inside a
// raw string literal; raw-string interior newlines remain embedded in their
// segment. Raw string spans are found by lexing s with go/scanner — exact for
// any token stream, including the malformed-fragment fallbacks (an
// unterminated raw string scans to end-of-input, keeping the tail verbatim,
// the safe choice).
func splitOutsideRawStrings(s string) []string {
	fset := gotoken.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(s))
	var sc goscanner.Scanner
	sc.Init(file, []byte(s), nil, 0)
	type span struct{ start, end int }
	var raws []span
	for {
		pos, tok, lit := sc.Scan()
		if tok == gotoken.EOF {
			break
		}
		if tok == gotoken.STRING && strings.HasPrefix(lit, "`") {
			off := file.Offset(pos)
			raws = append(raws, span{off, off + len(lit)})
		}
	}
	var segs []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '\n' {
			continue
		}
		inside := false
		for _, r := range raws {
			if i > r.start && i < r.end {
				inside = true
				break
			}
		}
		if inside {
			continue
		}
		segs = append(segs, s[start:i])
		start = i + 1
	}
	return append(segs, s[start:])
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
	case *ast.CommentAttr:
		if v.Block {
			b.WriteString("/* ")
			b.WriteString(v.Text)
			b.WriteString(" */")
		} else {
			b.WriteString("// ")
			b.WriteString(v.Text)
		}
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
			if part.CSSSegments != nil {
				b.WriteString(embeddedLiteralString(ast.EmbeddedCSS, part.CSSSegments, embeddedDelim(part.CSSDoubleQuoted)))
			} else {
				b.WriteString(fmtExpr(part.Expr))
				for _, s := range part.Stages {
					b.WriteString(" |> ")
					b.WriteString(pipeStageStr(s))
				}
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
	case *ast.EmbeddedAttr:
		b.WriteString(v.Name)
		b.WriteString("=")
		// A whole-literal pipeline only parses in the braced form
		// (name={`…` |> f}) — parseEmbeddedAttrValue, the direct/unbraced
		// path, never sets Stages. Wrap in braces whenever Stages is
		// present so the printed output re-parses.
		braced := len(v.Stages) > 0
		if braced {
			b.WriteString("{")
		}
		delim := embeddedDelim(v.DoubleQuoted)
		b.WriteString(embeddedLangName(v.Lang))
		b.WriteByte(delim)
		writeEmbeddedAttrSegments(b, v.Segments, delim)
		b.WriteByte(delim)
		for _, s := range v.Stages {
			b.WriteString(" |> ")
			b.WriteString(pipeStageStr(s))
		}
		if braced {
			b.WriteString("}")
		}
	case *ast.OrderedAttrsAttr:
		b.WriteString(v.Name)
		if len(v.Pairs) == 0 {
			b.WriteString("={{ }}")
		} else {
			b.WriteString("={{ ")
			for i, pair := range v.Pairs {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(strconv.Quote(pair.Key))
				b.WriteString(": ")
				b.WriteString(fmtExpr(pair.Value))
			}
			b.WriteString(" }}")
		}
	default:
		// Attribute types are AST-defined and enumerable; an unrecognized type
		// here is a programming error, not user input — skip it explicitly.
	}
}

func embeddedLangName(lang ast.EmbeddedLang) string {
	switch lang {
	case ast.EmbeddedJS:
		return "js"
	case ast.EmbeddedCSS:
		return "css"
	default: // ast.EmbeddedText — interpolating plain-text literal, f` prefix
		return "f"
	}
}

// embeddedDelim returns the delimiter byte a literal round-trips to: '"' for the
// `"`-delimited escape-hatch form, '`' (the default) otherwise.
func embeddedDelim(dquoted bool) byte {
	if dquoted {
		return '"'
	}
	return '`'
}

func writeEmbeddedAttrSegments(b *strings.Builder, nodes []ast.Markup, delim byte) {
	for _, n := range nodes {
		switch v := n.(type) {
		case *ast.Text:
			writeEmbeddedLiteralText(b, v.Value, delim)
		case *ast.Interp:
			b.WriteString("@{")
			b.WriteString(fmtExpr(v.Expr))
			for _, s := range v.Stages {
				b.WriteString(" |> ")
				b.WriteString(pipeStageStr(s))
			}
			b.WriteString("}")
		default:
			b.WriteString(markupInlineString(n))
		}
	}
}

func embeddedLiteralString(lang ast.EmbeddedLang, nodes []ast.Markup, delim byte) string {
	var b strings.Builder
	b.WriteString(embeddedLangName(lang))
	b.WriteByte(delim)
	writeEmbeddedAttrSegments(&b, nodes, delim)
	b.WriteByte(delim)
	return b.String()
}

// writeEmbeddedLiteralText writes the literal text of an embedded (js/css/text)
// attribute segment, re-escaping the two sequences the parser treats specially
// inside such literals: a bare occurrence of the delimiter char (which would
// otherwise close the literal — a backtick for the f`/js`/css` forms, a `"` for
// the f"/js"/css" forms) and a literal `@{` (which would otherwise be re-parsed
// as a hole opener). Both escapes are only needed when the preceding run of
// backslashes in the (already-unescaped) source text is even — an odd run means
// the character is already escaped by a backslash written earlier in this loop.
func writeEmbeddedLiteralText(b *strings.Builder, s string, delim byte) {
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == delim:
			if !embeddedLiteralEscaped(s, i) {
				b.WriteByte('\\')
			}
		case s[i] == '@' && i+1 < len(s) && s[i+1] == '{':
			if !embeddedLiteralEscaped(s, i) {
				b.WriteByte('\\')
			}
		}
		b.WriteByte(s[i])
	}
}

// embeddedLiteralEscaped reports whether the character at s[i] is preceded by
// an odd number of backslashes in the unescaped source text s.
func embeddedLiteralEscaped(s string, i int) bool {
	backslashes := 0
	for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
		backslashes++
	}
	return backslashes%2 == 1
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
			b.WriteString(renderHole(v))
		default:
			b.WriteString(markupInlineString(n))
		}
	}
}

// nodesToBody splits a <style> body (only *ast.Text and @{ } *ast.Interp by the
// raw-text parser) into literal text segments and rendered holes for rawfmt.
// segments and holes interleave with len(segments) == len(holes)+1: an empty
// segment is inserted so a hole at the start/end or two adjacent holes still
// satisfy the invariant.
func nodesToBody(nodes []ast.Markup) (segments, holes []string) {
	var cur strings.Builder
	for _, n := range nodes {
		switch v := n.(type) {
		case *ast.Text:
			cur.WriteString(v.Value)
		case *ast.Interp:
			segments = append(segments, cur.String())
			cur.Reset()
			holes = append(holes, renderHole(v))
		default:
			// The raw-text parser never produces other node types here; render
			// defensively so the invariant still holds.
			cur.WriteString(markupInlineString(n))
		}
	}
	segments = append(segments, cur.String())
	return segments, holes
}

// renderHole renders one interpolation hole exactly as rawHoleChildren does.
func renderHole(v *ast.Interp) string {
	var b strings.Builder
	b.WriteString("@{ ")
	b.WriteString(fmtExpr(v.Expr))
	for _, s := range v.Stages {
		b.WriteString(" |> ")
		b.WriteString(pipeStageStr(s))
	}
	b.WriteString(" }")
	return b.String()
}

// isPreserveTag mirrors wsnorm: pre/textarea/script/style keep bodies verbatim.
func isPreserveTag(tag string) bool {
	switch strings.ToLower(tag) {
	case "pre", "textarea", "script", "style":
		return true
	}
	return false
}

// jsExecutableScriptTypes are <script type> values that run as JavaScript.
// Mirrors internal/jsx.jsExecutableTypes (kept local to avoid importing the
// codegen-time jsx package into the formatter path).
var jsExecutableScriptTypes = map[string]bool{
	"text/javascript": true, "module": true, "application/javascript": true,
	"text/ecmascript": true, "application/ecmascript": true,
}

// isExecutableScript reports whether a <script> runs as JavaScript: no static
// type attribute, or a static type in the executable set. A data island (e.g.
// type="application/json", type="text/template") is not executable and is left
// verbatim.
func isExecutableScript(e *ast.Element) bool {
	for _, a := range e.Attrs {
		if sa, ok := a.(*ast.StaticAttr); ok && strings.EqualFold(sa.Name, "type") {
			t := strings.ToLower(strings.TrimSpace(sa.Value))
			return t == "" || jsExecutableScriptTypes[t]
		}
	}
	return true
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
	// Format the chunk as a VALID FILE, not a fragment: go/format.Source's
	// fragment mode strips a fixed byte count off its output, which shears a
	// //go:build comment that go/printer hoisted above the injected clause.
	out, err := format.Source([]byte(blockFormBraces(goExprWrapper + src)))
	if err != nil {
		return strings.TrimSpace(src)
	}
	stripped, ok := StripSyntheticPackage(out)
	if !ok {
		return strings.TrimSpace(src)
	}
	return strings.TrimSpace(stripped)
}

// goExprWrapper is the synthetic package clause prepended to a GoWithElements'
// Go text to make it a compilation unit go/format will accept. The gap a
// GoWithElements spans is always a run of complete top-level declarations, so
// the clause is all that is missing.
const goExprWrapper = "package _gsxfmt\n"

// StripSyntheticPackage removes the synthetic package clause from formatted —
// the output of a Go formatter that was fed a synthetic clause + a GoChunk's
// text. It locates the clause by PARSING, never by assuming it is the first
// line: go/printer relocates build-constraint comments (//go:build) above the
// package clause, so a line- or byte-index strip would shear the constraint and
// leave the synthetic `package` declaration spliced into the user's source.
//
// The single blank line the formatter always places between the package clause
// and the following declaration is removed too — it is separation we introduced
// by adding the clause, not layout the author wrote.
//
// ok is false when formatted does not parse; the caller then leaves the text
// untouched.
func StripSyntheticPackage(formatted []byte) (string, bool) {
	fset := gotoken.NewFileSet()
	file, err := goparser.ParseFile(fset, "", formatted, goparser.PackageClauseOnly|goparser.ParseComments)
	if err != nil {
		return "", false
	}
	start := fset.Position(file.Package).Offset  // offset of the `package` keyword
	end := fset.Position(file.Name.End()).Offset // end of the package name
	for end < len(formatted) && formatted[end] != '\n' {
		end++
	}
	if end < len(formatted) {
		end++ // the newline terminating the clause
	}
	if end < len(formatted) && formatted[end] == '\n' {
		end++ // the single blank line the formatter puts after the clause
	}
	return string(formatted[:start]) + string(formatted[end:]), true
}

// goExprHoleRunes are candidate placeholder runes: Unicode modifier letters,
// which Go accepts as identifier letters (identifier = letter { letter | digit },
// letter = unicode_letter, which includes category Lm). Repeating one N times
// yields a valid Go identifier that is exactly N *runes* wide — and go/format's
// alignment runs through text/tabwriter, which measures cells in runes. That is
// what lets a placeholder stand in for a gsx value at its true rendered width
// (see fmtGoExprParts), down to widths no `_gsx`-prefixed name could reach.
// They are vanishingly unlikely to occur in real source; if one does, the next
// candidate is tried.
var goExprHoleRunes = []string{"ᴳ", "ᴴ", "ᴵ", "ᴶ"}

// goExprHoleRune picks a placeholder rune absent from src, so the formatted
// output can be re-split at the placeholders unambiguously (a rune occurring
// inside a string literal or comment would misdirect that split).
func goExprHoleRune(src string) (string, bool) {
	for _, r := range goExprHoleRunes {
		if !strings.Contains(src, r) {
			return r, true
		}
	}
	return "", false
}

// goExprFlatWidth reports the rune width of doc rendered on a single line, and
// whether it fits on one line at all. A forced break (a block element) makes a
// gsx value multi-line no matter the available width; such a value has no single
// width to hand to gofmt.
func goExprFlatWidth(doc pretty.Doc) (int, bool) {
	const wide = 1 << 20 // wider than any real line: nothing breaks unless forced
	flat := pretty.Print(doc, wide)
	if strings.Contains(flat, "\n") {
		return 0, false
	}
	return utf8.RuneCountInString(flat), true
}

// goExprValue builds the doc for one non-GoText part of a GoWithElements — a
// gsx value sitting in Go-expression position. Shared by fmtGoExprParts (which
// measures the value's rendered width) and goWithElements (which prints it), so
// the width gofmt is told and the text finally spliced in can never disagree.
func (p *printer) goExprValue(part ast.GoPart) (pretty.Doc, bool) {
	switch pt := part.(type) {
	case *ast.Element:
		return p.element(pt), true
	case *ast.Fragment:
		return p.fragment(pt), true
	case *ast.EmbeddedInterp:
		// A prefixed backtick literal in Go-expression value position: render the
		// raw f`…@{expr}…` literal (no braces, no whole-literal pipeline — value-
		// position literals carry no Stages), so it splices back into the
		// surrounding Go source exactly as authored.
		return pretty.Text(embeddedLiteralString(ast.EmbeddedText, pt.Segments, embeddedDelim(pt.DoubleQuoted))), true
	default:
		return pretty.Doc{}, false
	}
}

// fmtGoExprParts gofmt's the Go text of a GoWithElements decl.
//
// A GoText part on its own is an INCOMPLETE Go fragment ("var help = ") that
// go/format cannot parse. But the fragments are incomplete only because a gsx
// value — an element, a fragment, an f`…` literal — sits between them, and every
// position such a value can occupy is a Go *operand* position: a call argument, a
// composite-literal element, the right-hand side of an assignment. An identifier
// is a valid operand in all of them. So substituting one placeholder identifier
// per gsx value turns the whole run back into ordinary, complete Go, which
// go/format lays out exactly as it would anywhere else. This is the same
// claim-what-Go-leaves-free move the parser makes elsewhere: gsx never has to
// parse Go, it only has to hand Go something Go can parse.
//
// Each placeholder is made exactly as many runes wide as the value it stands for
// will render (goExprFlatWidth), because gofmt's column arithmetic — the spaces
// it lays down to align end-of-line comments — is computed from the value's
// width. A fixed-width placeholder would align those comments to the
// placeholder, not to the element, and the misalignment would survive the splice.
//
// A value that renders multi-line has no single width to report; it gets a
// one-rune placeholder. Layout is then still correct except for end-of-line
// comment columns in that value's alignment section, which gofmt (seeing a real
// multi-line value) would have split. Everything else in the region — indentation,
// `=` alignment, blank lines — is unaffected, since none of it depends on value
// width.
//
// The formatted text is re-split at the placeholders and returned as a fresh
// parts slice (the input, which aliases the AST, is never mutated): GoText parts
// carry the formatted Go, and the gsx values pass through untouched for the
// caller to print with the ordinary element/fragment printers.
//
// Returns ok=false — leaving the caller to relay the original text verbatim, as
// it always has — when go/format rejects the substituted source, when no
// placeholder rune is free, or when a placeholder cannot be located in the
// output. All are degrade-gracefully paths: gsx fmt must never fail on gsx it
// was able to parse.
func (p *printer) fmtGoExprParts(parts []ast.GoPart) ([]ast.GoPart, []goexprshape.Result, bool) {
	var text strings.Builder
	holeCount := 0
	for _, part := range parts {
		if gt, ok := part.(ast.GoText); ok {
			text.WriteString(gt.Src)
			continue
		}
		holeCount++
	}
	if holeCount == 0 {
		return nil, nil, false
	}
	hole, ok := goExprHoleRune(text.String())
	if !ok {
		return nil, nil, false
	}

	var src strings.Builder
	holes := make([]string, 0, holeCount)
	shapeHoles := make([]goexprshape.Hole, 0, holeCount)
	for _, part := range parts {
		if gt, ok := part.(ast.GoText); ok {
			src.WriteString(gt.Src)
			continue
		}
		doc, ok := p.goExprValue(part)
		if !ok {
			return nil, nil, false
		}
		width, flat := goExprFlatWidth(doc)
		if !flat {
			width = 1
		}
		h := strings.Repeat(hole, width)
		start := len(goExprWrapper) + src.Len()
		shapeHoles = append(shapeHoles, goexprshape.Hole{Start: start, End: start + len(h)})
		holes = append(holes, h)
		src.WriteString(h)
	}
	// go/format, like go/parser, chokes on a placeholder sitting alone on its
	// own line inside a bracket — the shape this printer's OWN decorative-paren
	// output takes. Sanitize collapses exactly those newlines, so re-formatting
	// an already-formatted file reaches gofmt instead of falling back to a
	// verbatim relay. Classify sees the same source, so the paren it reports as
	// Wrapped is the paren that survives into `formatted` for the caller to strip.
	sanitized, sanHoles := goexprshape.Sanitize(goExprWrapper+src.String(), shapeHoles)
	shapes := goexprshape.Classify(sanitized, sanHoles)

	// blockFormBraces only ever inserts text before a composite literal's `}`,
	// so it cannot move a hole across a token boundary, reorder holes, or change
	// any hole's classification — shapes stays valid, and the placeholders are
	// still found in output order by the re-split below.
	out, err := format.Source([]byte(blockFormBraces(sanitized)))
	if err != nil {
		return nil, shapes, false
	}
	// Drop the synthetic package clause. It is NOT reliably the first line:
	// go/printer hoists a //go:build comment above the package clause, so a
	// line-index strip would shear the constraint and splice `package
	// _gsxfmt` into the user's source. Locate it by parsing instead.
	formatted, ok := StripSyntheticPackage(out)
	if !ok {
		return nil, shapes, false
	}

	// Re-split at the placeholders, left to right. Placeholders of equal width are
	// identical strings, which is harmless: the cursor has already consumed every
	// earlier one, and only Go text (never the hole rune) lies between them.
	res := make([]ast.GoPart, len(parts))
	cursor, next := 0, 0
	for i, part := range parts {
		if _, ok := part.(ast.GoText); !ok {
			// A gsx value: the cursor must be sitting exactly on its placeholder.
			if next >= len(holes) || !strings.HasPrefix(formatted[cursor:], holes[next]) {
				return nil, shapes, false
			}
			cursor += len(holes[next])
			next++
			res[i] = part
			continue
		}
		// Go text runs up to the next placeholder, or to the end of the output.
		end := len(formatted)
		if next < len(holes) {
			j := strings.Index(formatted[cursor:], holes[next])
			if j < 0 {
				return nil, shapes, false
			}
			end = cursor + j
		}
		res[i] = ast.GoText{Src: formatted[cursor:end]}
		cursor = end
	}
	if next != len(holes) {
		return nil, shapes, false
	}
	return res, shapes, true
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

// fmtTypeParams formats a component type-parameter list via `func _m[T any]() {}`,
// extracting the gofmt-rendered field list (sans the outer brackets).
func fmtTypeParams(src string) string {
	trimmed := strings.TrimSpace(src)
	if trimmed == "" {
		return ""
	}
	f, fset, ok := parseWrapped("package p\nfunc _m[" + trimmed + "]() {}\n")
	if !ok {
		return trimmed
	}
	fd, ok := f.Decls[0].(*goast.FuncDecl)
	if !ok || fd.Type.TypeParams == nil {
		return trimmed
	}
	if out, ok := fmtFieldList(fset, fd.Type.TypeParams); ok {
		return out
	}
	return trimmed
}

// fmtTypeArgs formats a component tag type-argument list via `var _ = _m[ARGS]`,
// extracting gofmt's normalized bytes inside the brackets.
func fmtTypeArgs(src string) string {
	trimmed := strings.TrimSpace(src)
	if trimmed == "" {
		return ""
	}
	wrapped := "package p\nvar _ = _m[" + trimmed + "]\n"
	out, err := format.Source([]byte(wrapped))
	if err != nil {
		return trimmed
	}
	const prefix = "var _ = _m["
	s := string(out)
	_, rest, ok := strings.Cut(s, prefix)
	if !ok {
		return trimmed
	}
	j := strings.LastIndex(rest, "]")
	if j < 0 {
		return trimmed
	}
	return strings.TrimSpace(rest[:j])
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

// fmtExprPreserving formats a Go expression, PRESERVING comments (unlike
// fmtExpr's format.Node path). It wraps the expression as a package-level
// `var _ = <expr>` and runs format.Source, then extracts the value text. The
// result may be multi-line (gofmt's own wrapping of a long call); continuation
// lines retain gofmt's own indentation relative to the expression root. On any
// error it falls back to fmtExpr (single line, comment-free) so fmt never fails.
func fmtExprPreserving(src string) string {
	trimmed := strings.TrimSpace(src)
	if trimmed == "" {
		return ""
	}
	wrapped := "package p\nvar _ = " + trimmed + "\n"
	out, err := format.Source([]byte(wrapped))
	if err != nil {
		return fmtExpr(src)
	}
	s := string(out)
	const marker = "var _ = "
	_, body, ok := strings.Cut(s, marker)
	if !ok {
		return fmtExpr(src)
	}
	return strings.TrimRight(body, "\n")
}

// fmtExprDoc returns a Doc for a Go expression value, multi-line when gofmt
// wraps it (HardLine-joined; comments preserved).
func fmtExprDoc(src string) pretty.Doc {
	return multiline(fmtExprPreserving(src))
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
