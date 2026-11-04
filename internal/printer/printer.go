// Package printer renders GSX ASTs as canonical source. Literal markup text
// and child adjacency are preserved exactly. Width-aware layout applies to Go,
// tag syntax, and parser-trimmed syntactic body edges; JS/CSS bodies retain
// their separate token-preserving formatter contracts.
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

	"github.com/gsxhq/gsx/internal/htmlattr"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/cssfmt"
	"github.com/gsxhq/gsx/internal/goexprshape"
	"github.com/gsxhq/gsx/internal/jsfmt"
	"github.com/gsxhq/gsx/internal/pretty"
	"github.com/gsxhq/gsx/internal/rawfmt"
)

// Fprint writes the canonical gsx rendering of f to w, wrapping lists that
// exceed width columns. width <= 0 uses pretty.DefaultPrintWidth; tabWidth <= 0
// uses pretty.DefaultTabWidth.
func Fprint(w io.Writer, f *ast.File, width, tabWidth int) error {
	if tabWidth <= 0 {
		tabWidth = pretty.DefaultTabWidth
	}
	// The built-in path uses LINE formatters (jsfmt/cssfmt FormatLines), which
	// carry logical-line structure so a multi-line Opaque token (template literal
	// / block comment) interior survives re-indentation verbatim.
	return fprint(w, f, width, tabWidth, printer{
		cssLineFmt: defaultCSSLineFormatter(width, tabWidth),
		jsLineFmt:  defaultJSLineFormatter(width, tabWidth),
	})
}

// FprintWith is Fprint with explicit external CSS and JS formatters for
// <style>/<script> bodies. A nil formatter leaves that body verbatim. External
// flat formatters carry no token structure, so a multi-line-token interior in
// their output is re-indented (the same limitation as before line formatters).
func FprintWith(w io.Writer, f *ast.File, width, tabWidth int, cssFmt, jsFmt rawfmt.Formatter) error {
	return fprint(w, f, width, tabWidth, printer{cssFmt: cssFmt, jsFmt: jsFmt})
}

func fprint(w io.Writer, f *ast.File, width, tabWidth int, p printer) error {
	if width <= 0 {
		width = pretty.DefaultPrintWidth
	}
	if tabWidth <= 0 {
		tabWidth = pretty.DefaultTabWidth
	}
	p.source = f.Layout
	p.width = width
	p.tabWidth = tabWidth
	doc := p.file(f)
	if p.err != nil {
		return p.err
	}
	_, err := io.WriteString(w, pretty.Print(doc, width, tabWidth))
	return err
}

// defaultCSSLineFormatter / defaultJSLineFormatter bind the built-in
// line-returning re-indenters to the print width.
func defaultCSSLineFormatter(width, tabWidth int) rawfmt.LineFormatter {
	return func(src []byte) ([]string, bool) { return cssfmt.FormatLines(src, width, tabWidth) }
}

func defaultJSLineFormatter(width, tabWidth int) rawfmt.LineFormatter {
	return func(src []byte) ([]string, bool) { return jsfmt.FormatLines(src, width, tabWidth) }
}

// printer accumulates the first I/O-independent error encountered while
// building the document.
type printer struct {
	source           ast.SourceLayout
	firstLineAnchors map[string]bool
	err              error
	anchored         map[ast.Node]bool
	anchoredFields   map[string]bool
	originIndent     *string
	// External flat formatters (via FprintWith). A nil formatter leaves the body
	// verbatim; they cannot preserve multi-line-token interiors.
	cssFmt rawfmt.Formatter
	jsFmt  rawfmt.Formatter
	// Built-in line formatters (via Fprint), preferred when set: they carry
	// logical-line structure so template-literal / block-comment interiors survive.
	cssLineFmt rawfmt.LineFormatter
	jsLineFmt  rawfmt.LineFormatter
	width      int // print width fmtGoChunk/fmtGoExprParts measure Go fragments against
	tabWidth   int
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
		pkgClause = fmtGoChunk(f.Doc+"\n"+pkgClause, p.width, p.tabWidth)
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
		return pretty.Concat(multiline(fmtGoChunk(v.Src, p.width, p.tabWidth)), pretty.HardLine)
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
	source, _ := p.source.Slice(v.Pos(), v.End())
	if text, values, ok, err := p.fmtSourceGoParts(parts, source, goExprWrapper, "", StripSyntheticPackage); err != nil {
		return p.fail("%s", err)
	} else if ok {
		return p.goTextDoc(strings.TrimSpace(text), values, false)
	}
	formatted, shapes, ok := p.fmtGoExprParts(parts)
	if ok {
		parts = formatted
	}
	eligible, wrapped := decorativeParens(parts, shapes)
	last := len(parts) - 1
	docs := make([]pretty.Doc, 0, len(parts))
	precedingGoText := ""
	for i, part := range parts {
		if gt, ok := part.(ast.GoText); ok {
			src := goTextSrc(gt, i, last, wrapped)
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
		// Whether a gsx value in Go-expression position breaks is the AUTHOR's
		// call, never the width's. gsx already works this way for markup: a
		// newline after `{` or `>` keeps a body multi-line. Here the decorative
		// paren is the same signal — write `(` around the value and it breaks;
		// leave it bare and it stays on one line, however long that line ends up.
		//
		// The formatter therefore never guesses, and there is nothing for a
		// second pass to guess differently. Guessing is what made every earlier
		// version of this code non-idempotent: a value broken on width re-parses
		// as an author-broken value, which then breaks for a different reason.
		//
		// Three cases, and the width is in none of them:
		//   - the value cannot be printed flat (literal newlines or broken tag
		//     syntax) — it breaks, and takes parens;
		//   - the author wrapped it in parens — it breaks, and keeps them;
		//   - otherwise it is emitted as fixed text, which no Group can reflow.
		//
		// A wide line is still fixed, just not here: breakWideLiterals breaks the
		// composite literal's FIELDS, which is what actually made the line wide.
		flatText, flat := goExprFlatText(doc)
		switch {
		case eligible[i] && (!flat || wrapped[i]):
			doc = parenWrapDoc(doc)
		case flat:
			doc = pretty.Text(flatText)
		}
		docs = append(docs, indentN(depth, doc))
	}
	return pretty.Concat(docs...)
}

// decorativeParens maps shapes (indexed by value order) onto parts. eligible[i]
// reports whether parts[i] is an *Element/*Fragment in a position safe to
// visually wrap in (...) — the only case that ever carries a decorative paren.
// Never an EmbeddedInterp (f`...` literal), which codegen doesn't lower into a
// closure and so has no matching strip step (see internal/codegen's emit.go
// GoWithElements case). wrapped[i] reports whether that eligible value ALSO
// sits inside a real paren in this source — e.g. a `var (…)` group's own
// closing paren can immediately follow an unwrapped, eligible value with no
// relation to it, and must be left alone.
func decorativeParens(parts []ast.GoPart, shapes []goexprshape.Result) (eligible, wrapped []bool) {
	eligible = make([]bool, len(parts))
	wrapped = make([]bool, len(parts))
	shapeIdx := 0
	for i, part := range parts {
		switch part.(type) {
		case ast.GoText:
			continue
		case *ast.Element, *ast.Fragment:
			if shapeIdx < len(shapes) && shapes[shapeIdx].Shape == goexprshape.ParenWrap {
				eligible[i] = true
				wrapped[i] = shapes[shapeIdx].Wrapped
			}
		}
		shapeIdx++
	}
	return eligible, wrapped
}

// goTextSrc is GoText part i of last as the printer emits it: its outer edges
// trimmed, and the decorative paren of a wrapped neighbor (see
// decorativeParens) removed, since parenWrapDoc re-derives it.
func goTextSrc(gt ast.GoText, i, last int, wrapped []bool) string {
	src := trimGoTextEdges(gt.Src, i == 0, i == last)
	if i > 0 && wrapped[i-1] {
		src = goexprshape.StripLeadingParen(src)
	}
	if i < last && wrapped[i+1] {
		src = goexprshape.StripTrailingParen(src)
	}
	return src
}

// parenWrapDoc wraps doc in "(" ")" and ALWAYS breaks. It is only ever called
// for a value that is already committed to breaking: one that cannot be printed
// flat, or one the author parenthesized. The paren is the author's break
// request, the same way a newline after `>` is — so it must not silently vanish
// when the value happens to fit, or `gsx fmt` would delete the request and then
// re-derive a different answer on the next pass.
//
// BreakParent is what forces it: pretty.Group computes its `forced` flag from
// any hard break among its children, and a value that is already multi-line
// carries one of its own. IfBreak's branches stay Text-only so the parens
// themselves never add a break — they render because the group broke, not the
// other way around.
//
// The parens this emits are purely cosmetic for the .gsx source: codegen
// strips the matching literal "(" / ")" out of the surrounding GoText before
// splicing in the element's lowered closure (see emit.go), so they never
// reach the generated .x.go and can't trip Go's automatic semicolon insertion
// on the closure's own trailing "}" / ")".
func parenWrapDoc(doc pretty.Doc) pretty.Doc {
	return pretty.Group(pretty.Concat(
		pretty.BreakParent,
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

// component formats the syntactic body edges, preserving its literal interior.
func (p *printer) component(c *ast.Component) pretty.Doc {
	if p.anchoredBody(c.Body) && !strings.ContainsAny(c.Layout.Leading, "\r\n") {
		return p.sourceComponent(c)
	}
	head := []pretty.Doc{pretty.Text("component ")}
	if c.Recv != "" {
		head = append(head, pretty.Text(fmtRecv(c.Recv)), pretty.Text(" "))
	}
	head = append(head, pretty.Text(c.Name))
	if c.TypeParams != "" {
		head = append(head, pretty.Text("["), pretty.Text(fmtTypeParams(c.TypeParams)), pretty.Text("]"))
	}
	head = append(head, pretty.Text("("), pretty.Text(fmtParams(c.Params)), pretty.Text(") {"))

	if p.anchoredBody(c.Body) {
		return pretty.Concat(pretty.Concat(head...), p.sourceBody(c.Body, c.Layout), pretty.Text(c.Layout.Trailing), pretty.Text("}"), pretty.HardLine)
	}
	body := pretty.Text("")
	if len(c.Body) > 0 {
		body = pretty.Indent(pretty.Concat(pretty.HardLine, p.children(c.Body)))
	}
	return pretty.Concat(pretty.Concat(head...), body, pretty.HardLine, pretty.Text("}"), pretty.HardLine)
}

// element renders <tag attrs>children</tag>.
func (p *printer) element(e *ast.Element) pretty.Doc {
	attrs := make([]pretty.Doc, 0, len(e.Attrs)*2)
	fixedOpening := p.anchoredAttrOpening(e)
	previous := e.TagPos + gotoken.Pos(len(e.Tag))
	if end, ok := p.source.FieldEnd(e.TagPos); ok {
		previous = end
	}
	if e.TypeArgsClosePos.IsValid() {
		previous = e.TypeArgsClosePos + 1
	}
	for _, a := range e.Attrs {
		if fixedOpening {
			gap, ok := p.source.Slice(previous, a.Pos())
			if !ok || strings.TrimSpace(gap) != "" {
				return p.fail("printer: missing opening-tag source layout")
			}
			attrs = append(attrs, pretty.Text(gap), p.sourceAttrChild(a))
			previous = a.End()
			continue
		}
		if c, ok := a.(*ast.CommentAttr); ok {
			sep := pretty.Line
			if c.Trailing {
				sep = pretty.Text(" ") // glue to the previous attr's line
			}
			// A `//` comment forces its tag group to break; block comments
			// may stay inline.
			attrs = append(attrs, sep, p.attrDoc(a))
			continue
		}
		attrs = append(attrs, pretty.Line, p.attrDoc(a))
	}
	if e.AttrsMultiline && !fixedOpening {
		// The author broke the opening tag across lines; keep the attribute list
		// block-formatted (one per line, `>` alone) instead of collapsing it onto
		// a single line. Same mechanism as the `//` line-comment attr above. With
		// no attributes there is no list to break (the parser never sets
		// AttrsMultiline then), so this is a no-op for the len(e.Attrs)==0 branch.
		attrs = append(attrs, pretty.BreakParent)
	}
	tag := pretty.Text(e.Tag)
	if e.TypeArgs != "" {
		if fixedOpening {
			end, valid := p.source.FieldEnd(e.TypeArgsPos)
			before, ok := p.source.Slice(e.TypeArgsOpenPos+1, e.TypeArgsPos)
			after, okAfter := p.source.Slice(end, e.TypeArgsClosePos)
			nameEnd, _ := p.source.FieldEnd(e.TagPos)
			gap, okGap := p.source.Slice(nameEnd, e.TypeArgsOpenPos)
			if !valid || !ok || !okAfter || !okGap {
				return p.fail("printer: missing type-argument source layout")
			}
			tag = pretty.Concat(tag, pretty.Text(gap+"["+before+e.TypeArgs+after+"]"))
		} else {
			tag = pretty.Concat(tag, pretty.Text("["+fmtTypeArgs(e.TypeArgs)+"]"))
		}
	}
	// Opening tag group: flat → `<tag a b>`; broken → each attr on its own line
	// with `>` (or `/>`) alone. A forced break inside any attr (CondAttr) breaks
	// the group; otherwise it breaks only on width overflow.
	selfClose := e.Void && len(e.Children) == 0
	tail := pretty.Text(">")
	if selfClose {
		tail = pretty.Text("/>")
	}
	tagTail := pretty.SoftLine
	if fixedOpening && len(e.Attrs) > 0 {
		rest, ok := p.source.Slice(previous, e.End())
		if !ok {
			return p.fail("printer: missing opening-tag tail source layout")
		}
		tagTail = pretty.Text(sourceLeading(rest))
	}
	var openGroupBody pretty.Doc
	if len(e.Attrs) == 0 {
		openGroupBody = pretty.Concat(pretty.Text("<"), tag, tail)
	} else {
		openGroupBody = pretty.Concat(
			pretty.Text("<"), tag,
			pretty.Indent(pretty.Concat(attrs...)),
			tagTail, tail)
	}
	openTag := pretty.LocalGroup(openGroupBody)

	if selfClose {
		return openTag
	}
	close := pretty.Concat(pretty.Text("</"), pretty.Text(e.Tag), pretty.Text(">"))

	if strings.EqualFold(e.Tag, "script") {
		if isExecutableScript(e) {
			segments, holes := nodesToBody(e.Children)
			if doc, ok := p.jsBodyDoc(segments, holes); ok {
				return pretty.Concat(openTag, doc, close)
			}
		}
		return pretty.Concat(openTag, p.rawHoleChildren(e.Children), close)
	}
	if strings.EqualFold(e.Tag, "style") {
		segments, holes := nodesToBody(e.Children)
		if doc, ok := p.cssBodyDoc(segments, holes); ok {
			return pretty.Concat(openTag, doc, close)
		}
		return pretty.Concat(openTag, p.rawHoleChildren(e.Children), close)
	}
	return pretty.Concat(openTag, pretty.Indent(p.children(e.Children)), close)
}

// attrDoc renders one attribute as a Doc. Conditional attributes are rendered
// with their `{ if … { … } }` body broken across lines (templ-style), emitting
// a BreakParent so the enclosing opening-tag group breaks. ExprAttr and
// ComposedAttr use goFieldDoc so long or comment-bearing values can be multi-line.
func (p *printer) attrDoc(a ast.Attr) pretty.Doc {
	if p.anchoredNode(a) {
		if doc, ok := p.sourceAttr(a); ok {
			return doc
		}
	}
	switch v := a.(type) {
	case *ast.CondAttr:
		return pretty.Concat(pretty.BreakParent, pretty.Text("{ "), p.condAttrChainDoc(v), pretty.Text(" }"))
	case *ast.SwitchAttr:
		return pretty.Concat(pretty.BreakParent, pretty.Text("{ "), p.switchAttrDoc(v), pretty.Text(" }"))
	case *ast.CommentAttr:
		if v.Block {
			if v.Text == "" {
				// Empty comment group ({} in source): /**/ is the only bare
				// spelling with nothing in it. Inline-safe, so no forced break.
				return pretty.Text("/**/")
			}
			return pretty.Text("/* " + v.Text + " */")
		}
		// Force tag layout to put subsequent attributes on a fresh line.
		if v.Text == "" {
			return pretty.Concat(pretty.Text("//"), pretty.BreakParent)
		}
		return pretty.Concat(pretty.Text("// "), pretty.Text(v.Text), pretty.BreakParent)
	case *ast.ExprAttr:
		val := []pretty.Doc{p.goFieldDoc(v.Expr, exprField, fmtExprPreserving, v.ExprPos)}
		for _, s := range v.Stages {
			val = append(val, pretty.Text(" |> "), multiline(pipeStageStr(s)))
		}
		if p.inlineGoAttr(v) {
			before, ok := p.source.Slice(v.Pos(), v.ExprPos)
			if !ok {
				return p.fail("printer: missing attribute source layout")
			}
			gap := before[len(strings.TrimRight(before, " \t\r\n")):]
			return pretty.Concat(pretty.Text(v.Name+"={"+gap), pretty.Concat(val...), pretty.Text(" }"))
		}
		return wrapAttrValue(v.Name, pretty.SoftLine, pretty.Concat(val...))
	case *ast.ComposedAttr:
		parts := make([]pretty.Doc, 0, len(v.Parts)*2)
		for i, part := range v.Parts {
			if i > 0 {
				parts = append(parts, pretty.Text(","), pretty.Line)
			}
			parts = append(parts, p.composedPartDoc(part))
		}
		return wrapAttrValue(v.Name, pretty.Line, pretty.Group(pretty.Concat(parts...)))
	case *ast.EmbeddedAttr:
		if doc, ok := p.embeddedAttrDoc(v); ok {
			return doc
		}
		return pretty.Text(attrInline(a))
	case *ast.MarkupAttr:
		if p.anchoredBody(v.Value) {
			return pretty.Group(pretty.Concat(pretty.Text(v.Name+"={"), p.sourceBody(v.Value, v.Layout), pretty.Text(v.Layout.Trailing), pretty.Text("}")))
		}
		return wrapAttrValue(v.Name, pretty.Line, p.children(v.Value))
	default:
		return pretty.Text(attrInline(a))
	}
}

// composedPartDoc renders one composed class/style contribution: `expr`,
// `expr |> stage`, `expr: cond`, or a value-form if/switch.
func (p *printer) composedPartDoc(part ast.ComposedPart) pretty.Doc {
	if part.CF != nil {
		return p.valueCFDoc(part.CF)
	}
	if part.LiteralSegments != nil {
		seg := []pretty.Doc{pretty.Text(embeddedLiteralString(part.LiteralLang, part.LiteralSegments, embeddedDelim(part.LiteralDoubleQuoted)))}
		if part.Cond != "" {
			seg = append(seg, pretty.Text(": "), p.goFieldDoc(part.Cond, exprField, fmtExpr, part.CondPos))
		}
		return pretty.Concat(seg...)
	}
	seg := []pretty.Doc{p.goFieldDoc(part.Expr, exprField, fmtExprPreserving, part.ExprPos)}
	for _, s := range part.Stages {
		seg = append(seg, pretty.Text(" |> "), multiline(pipeStageStr(s)))
	}
	if part.Cond != "" {
		seg = append(seg, pretty.Text(": "), p.goFieldDoc(part.Cond, exprField, fmtExpr, part.CondPos))
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
		pretty.Text("if "), p.goFieldDoc(i.Cond, ifField, fmtExpr, i.CondPos),
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
	if a.Segments != nil {
		// Same rendering a non-arm literal part gets (composedPartDoc), so an
		// arm and a plain part print identically.
		return pretty.Text(embeddedLiteralString(a.Lang, a.Segments, embeddedDelim(a.DoubleQuoted)))
	}
	seg := []pretty.Doc{p.goFieldDoc(a.Expr, exprField, fmtExprPreserving, a.ExprPos)}
	for _, s := range a.Stages {
		seg = append(seg, pretty.Text(" |> "), multiline(pipeStageStr(s)))
	}
	return pretty.Concat(seg...)
}

func (p *printer) valueSwitchDoc(s *ast.ValueSwitch) pretty.Doc {
	head := []pretty.Doc{pretty.Text("switch")}
	if s.Tag != "" {
		head = append(head, pretty.Text(" "), p.goFieldDoc(s.Tag, switchField, fmtExpr, s.TagPos))
	}
	head = append(head, pretty.Text(" {"))
	cases := make([]pretty.Doc, 0, len(s.Cases))
	for _, c := range s.Cases {
		label := pretty.Text("default:")
		if !c.Default {
			label = pretty.Concat(pretty.Text("case "), p.goFieldDoc(c.List, caseField, fmtCaseList, c.ListPos), pretty.Text(":"))
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
	parts := []pretty.Doc{pretty.Text("if "), p.goFieldDoc(c.Cond, ifField, fmtExpr, c.CondPos), pretty.Text(" {"),
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

// switchAttrDoc renders `switch [Tag] { case List: … default: … }` in
// attribute position. Case labels sit at the switch's own indent (Go's layout
// for switch bodies) and each arm's attributes are indented one level under
// their label, mirroring condAttrChainDoc's one-attr-per-line body.
func (p *printer) switchAttrDoc(s *ast.SwitchAttr) pretty.Doc {
	parts := []pretty.Doc{pretty.Text("switch")}
	if strings.TrimSpace(s.Tag) != "" {
		parts = append(parts, pretty.Text(" "), p.goFieldDoc(s.Tag, switchField, fmtExpr, s.TagPos))
	}
	parts = append(parts, pretty.Text(" {"))
	for _, cc := range s.Cases {
		label := pretty.Text("default:")
		if !cc.Default {
			label = pretty.Concat(pretty.Text("case "), p.goFieldDoc(cc.List, caseField, fmtExpr, cc.ListPos), pretty.Text(":"))
		}
		parts = append(parts, pretty.HardLine, label, p.attrCaseBodyDoc(cc.Body))
	}
	parts = append(parts, pretty.HardLine, pretty.Text("}"))
	return pretty.Concat(parts...)
}

// attrCaseBodyDoc lays one case arm's attributes one per line, indented under
// their label. An empty arm contributes nothing, so `case 1:` with no
// attributes stays on its own line rather than emitting a blank one.
func (p *printer) attrCaseBodyDoc(attrs []ast.Attr) pretty.Doc {
	if len(attrs) == 0 {
		return pretty.Text("")
	}
	inner := make([]pretty.Doc, 0, len(attrs)*2)
	for _, a := range attrs {
		sep := pretty.Doc(pretty.HardLine)
		if c, ok := a.(*ast.CommentAttr); ok && c.Trailing {
			sep = pretty.Text(" ") // glue a trailing comment to the previous attr's line
		}
		inner = append(inner, sep, p.attrDoc(a))
	}
	return pretty.Indent(pretty.Concat(inner...))
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

// children concatenates literal content without adding separators.
func (p *printer) children(nodes []ast.Markup) pretty.Doc {
	parts := make([]pretty.Doc, 0, len(nodes))
	constrained := p.anchoredBody(nodes)
	for _, n := range nodes {
		if comment, ok := n.(*ast.Comment); ok && constrained {
			parts = append(parts, pretty.Text(comment.Layout.Leading+comment.Text+comment.Layout.Trailing))
			continue
		}
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
	case *ast.Marker:
		return pretty.Concat(pretty.Text("<?marker "), p.attrDoc(v.Name), pretty.Text(">"))
	case *ast.MarkerRegion:
		return p.markerRegion(v)
	case *ast.HTMLComment:
		return pretty.Concat(pretty.Text("<!--"), pretty.Text(v.Text), pretty.Text("-->"))
	case *ast.Comment:
		if v.Bare {
			// Interior line boundaries live in adjacent literal Text. At a
			// trimmed syntactic edge, force the enclosing body onto fresh lines.
			if v.Text == "" {
				return pretty.Concat(pretty.Text("//"), pretty.BreakParent)
			}
			return pretty.Concat(pretty.Text("// "), pretty.Text(v.Text), pretty.BreakParent)
		}
		// Source-only content comment; canonical braced form. The `{// text }`
		// line form is safe on one line here — the printer controls layout, so
		// nothing after `}` is on the comment's line to be swallowed.
		if v.Block {
			if v.Text == "" {
				// Empty comment group: `{}` is the minimal braced spelling and
				// reparses to exactly this node.
				return pretty.Text("{}")
			}
			return pretty.Concat(pretty.Text("{/* "), pretty.Text(v.Text), pretty.Text(" */}"))
		}
		if v.Text == "" {
			return pretty.Concat(pretty.Text("{//"), pretty.HardLine, pretty.Text("}"))
		}
		return pretty.Concat(pretty.Text("{// "), pretty.Text(v.Text), pretty.HardLine, pretty.Text("}"))
	case *ast.Text:
		return pretty.Text(v.Value)
	case *ast.Interp:
		return p.interp(v)
	case *ast.EmbeddedInterp:
		if p.anchoredNode(v) {
			return p.sourceEmbeddedInterp(v)
		}
		return p.embeddedInterp(v)
	default:
		return p.fail("printer: unknown markup type %T", n)
	}
}

func (p *printer) fragment(f *ast.Fragment) pretty.Doc {
	return pretty.Concat(pretty.Text("<>"), pretty.Indent(p.children(f.Children)), pretty.Text("</>"))
}

func (p *printer) markerRegion(r *ast.MarkerRegion) pretty.Doc {
	return pretty.Concat(pretty.Text("<?start "), p.attrDoc(r.Name), pretty.Text(">"), pretty.Indent(p.children(r.Children)), pretty.Text("<?end>"))
}

func (p *printer) interp(i *ast.Interp) pretty.Doc {
	if p.anchoredNode(i) {
		return p.sourceInterp(i)
	}
	parts := []pretty.Doc{pretty.Text("{ "), p.goFieldDoc(i.Expr, exprField, fmtExprPreserving, i.ExprPos)}
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
	b.WriteString(embeddedLiteralString(v.Lang, v.Segments, embeddedDelim(v.DoubleQuoted)))
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
	if p.anchoredGoField(b.Code) {
		return p.sourceGoBlock(b)
	}
	s, values := p.goBlockCode(b.Code, b.CodePos)
	if p.firstLineGoAnchor(b.Code) {
		return pretty.Concat(pretty.Text("{{"+sourceLeading(b.Code)), pretty.Indent(p.goTextDoc(s, values, false)), pretty.HardLine, pretty.Text("}}"))
	}

	// A single-statement block with no multi-line value stays inline:
	// `{{ stmt }}`. A block carrying a multi-line js`/css` literal or element
	// always breaks (its body is multi-line even when the statement is not).
	if !strings.Contains(s, "\n") && goValuesFlat(values) {
		return pretty.Concat(pretty.Text("{{ "), p.goTextDoc(s, values, false), pretty.Text(" }}"))
	}
	// A multi-statement block breaks like a Go block body: `{{` alone on its line,
	// the statements indented one level deeper, `}}` alone on its own line at the
	// block's column. Raw-string interior newlines stay embedded in their segment;
	// a value carried as a marker in s is laid out under the line it opens on.
	return pretty.Concat(
		pretty.Text("{{"),
		pretty.Indent(p.goTextDoc(s, values, true)),
		pretty.BreakParent,
		pretty.HardLine, pretty.Text("}}"),
	)
}

// goValuesFlat reports whether every marked value renders on one line.
func goValuesFlat(values map[string]goValue) bool {
	for _, v := range values {
		if v.multiline() {
			return false
		}
	}
	return true
}

// goBlockLiteralDocs renders the line that opens a multi-line js`/css`
// literal: pre (the Go text up to the literal, e.g. "\t\tValue: ") on the
// opening line, the literal's re-indented body one level under that line's
// Go-structural indent (litTabs), and the closing delimiter. The result starts
// with a HardLine. A literal the formatter cannot re-indent is relayed
// verbatim.
func (p *printer) goBlockLiteralDocs(pre string, lit *ast.EmbeddedInterp, litTabs int) []pretty.Doc {
	lines, ok := p.embeddedInterpLines(lit)
	if !ok {
		return []pretty.Doc{pretty.HardLine, pretty.Text(pre + embeddedLiteralString(lit.Lang, lit.Segments, embeddedDelim(lit.DoubleQuoted)))}
	}
	delim := embeddedDelim(lit.DoubleQuoted)
	opener := embeddedLangName(lit.Lang) + string(delim)
	var cb strings.Builder
	cb.WriteByte(delim)
	for _, st := range lit.Stages {
		cb.WriteString(" |> ")
		cb.WriteString(pipeStageStr(st))
	}
	return literalLineDocs(pre+opener, lines, cb.String(), litTabs)
}

// literalLineDocs lays out a multi-line js`/css` literal body inside a Go
// region. opener is the pre-text plus the opening delimiter (attached to the
// literal's line); lines are the re-indented body logical lines; closer is the
// closing delimiter plus trailing Go text. litTabs is the literal line's
// Go-structural indent; the body baked at litTabs+1 tabs and the closer at
// litTabs, both riding on the block's managed base (supplied by HardLine).
//
// Layout mirrors embeddedAttrValueDoc: a leading blank logical line means the
// body opened on its own line → block layout (delimiters alone, body one level
// under); otherwise inline (opener hugs the first body line, closer the last).
func literalLineDocs(opener string, lines []string, closer string, litTabs int) []pretty.Doc {
	block := len(lines) > 0 && lines[0] == ""
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	bodyTab := strings.Repeat("\t", litTabs+1)
	closeTab := strings.Repeat("\t", litTabs)
	if len(lines) == 0 {
		return []pretty.Doc{pretty.HardLine, pretty.Text(opener + closer)}
	}
	// bodyLine emits one continuation line at tab (its Go-structural indent); the
	// block's managed base is supplied by the HardLine. A blank line emits a bare
	// newline (no trailing tabs → idempotence).
	bodyLine := func(tab, ln string) (pretty.Doc, pretty.Doc, bool) {
		if ln = strings.TrimRight(ln, " \t"); ln == "" {
			return pretty.Text("\n"), pretty.Doc{}, false
		}
		return pretty.HardLine, pretty.Text(tab + ln), true
	}
	if block {
		// Block: delimiters alone, body one level under (bodyTab), closer at litTabs.
		docs := []pretty.Doc{pretty.HardLine, pretty.Text(opener)}
		for _, ln := range lines {
			a, b, two := bodyLine(bodyTab, ln)
			docs = append(docs, a)
			if two {
				docs = append(docs, b)
			}
		}
		return append(docs, pretty.HardLine, pretty.Text(closeTab+closer))
	}
	// Inline: the opener hugs the first body line (an object's `{`); each further
	// line is re-based at the literal's own column (closeTab) PLUS its own brace
	// depth, which the re-indenter baked into the line's leading whitespace. So an
	// object's `{` hugs the js` line, its members sit one brace-level under, and
	// the closing `}` (brace depth 0) returns to the js` column.
	docs := []pretty.Doc{pretty.HardLine, pretty.Text(opener + strings.TrimRight(lines[0], " \t"))}
	for _, ln := range lines[1:] {
		a, b, two := bodyLine(closeTab, ln)
		docs = append(docs, a)
		if two {
			docs = append(docs, b)
		}
	}
	return append(docs, pretty.Text(closer))
}

// embeddedInterpLines re-indents a Go-expression js`/css` literal's body into
// logical lines with the configured JS/CSS formatter, mirroring embeddedAttrDoc's
// body build. ok=false for a non-JS/CSS literal or any formatter failure.
func (p *printer) embeddedInterpLines(v *ast.EmbeddedInterp) ([]string, bool) {
	if v.Lang != ast.EmbeddedJS && v.Lang != ast.EmbeddedCSS {
		return nil, false
	}
	segments, holes := embeddedAttrBody(v.Segments)
	delim := embeddedDelim(v.DoubleQuoted)
	escape := func(s string) string {
		var b strings.Builder
		writeEmbeddedLiteralText(&b, s, delim)
		return b.String()
	}
	return p.embeddedAttrLines(v.Lang, segments, holes, escape)
}

// goBlockCode returns the canonical statement text of a `{{ }}` block. A block
// carrying an embedded gsx value is not, on its own, parseable Go — fmtStmts'
// go/format call rejects it and relays the raw text verbatim, so the block's
// indentation is never normalized. goBlockCode restores parseability with the
// same placeholder round-trip fmtGoExprParts uses (formatGoParts), so gofmt
// lays the statements out and the values splice back in. A block with no
// embedded value (or whose split fails) falls back to the plain fmtStmts path.
// It also returns the values carried in the string as markers (see
// fmtGoPartsText), so goBlock lays each out under the statement it opens on;
// nil on the plain path.
func (p *printer) goBlockCode(code string, origin ...gotoken.Pos) (string, map[string]goValue) {
	if s, values, ok := p.fmtGoBlockCode(code, origin...); ok {
		return s, values
	}
	return fmtStmts(code), nil
}

// goBlockWrapperPrefix/Suffix wrap a GoBlock's statements in a synthetic
// package + func so go/format accepts them — the statement-context analog of
// goExprWrapper. extractFuncBody peels both back off after formatting.
const (
	goBlockWrapperPrefix = goExprWrapper + "func _m() {\n"
	goBlockWrapperSuffix = "\n}\n"
)

// fmtGoBlockCode formats independent statements while keeping the smallest
// regions containing fixed literal lines aligned to their source. Current Code
// supplies GoText; parser-owned source origins supply only enclosing geometry.
func (p *printer) fmtGoBlockCode(code string, origin ...gotoken.Pos) (string, map[string]goValue, bool) {
	parts, ok := splitGoParts(code)
	if !ok || len(parts) == 0 {
		return "", nil, false
	}
	sub := *p
	outerPrefix := ""
	if len(origin) > 0 {
		outerPrefix = p.sourceIndent(origin[0])
	}
	sub.source = ast.NewSourceLayout(code, parts[0].Pos()).WithFieldOrigins(&ast.File{Decls: []ast.Decl{&ast.GoWithElements{Parts: parts}}})
	sub.originIndent = &outerPrefix
	defer func() {
		if sub.err != nil {
			p.err = sub.err
		}
	}()
	strip := func(out []byte) (string, bool) { return extractFuncBody(string(out)) }
	if text, values, ok, err := sub.fmtSourceGoParts(parts, code, goBlockWrapperPrefix, goBlockWrapperSuffix, strip, origin...); err != nil {
		p.fail("%s", err)
		return "", nil, false
	} else if ok {
		return text, values, true
	}
	return sub.fmtGoPartsText(parts, goBlockWrapperPrefix, goBlockWrapperSuffix, strip)
}

// ifMarkup renders `{ if cond { … }[ else …] }` as a group: short → one line,
// long → block body.
func (p *printer) ifMarkup(i *ast.IfMarkup) pretty.Doc {
	if p.anchoredNode(i) {
		return p.sourceIf(i)
	}
	return pretty.Group(pretty.Concat(pretty.Text("{ "), p.ifChain(i), pretty.Text(" }")))
}

func (p *printer) ifChain(i *ast.IfMarkup) pretty.Doc {
	parts := []pretty.Doc{pretty.Text("if "), p.goFieldDoc(i.Cond, ifField, fmtExpr, i.CondPos), pretty.Text(" {"), p.cfBody(i.Then, i.ThenMultiline, i.ThenLayout), pretty.Text("}")}
	if len(i.Else) == 0 {
		return pretty.Concat(parts...)
	}
	if len(i.Else) == 1 {
		if elseIf, ok := i.Else[0].(*ast.IfMarkup); ok {
			parts = append(parts, pretty.Text(" else "), p.ifChain(elseIf))
			return pretty.Concat(parts...)
		}
	}
	parts = append(parts, pretty.Text(" else {"), p.cfBody(i.Else, i.ElseMultiline, i.ElseLayout), pretty.Text("}"))
	return pretty.Concat(parts...)
}

func (p *printer) forMarkup(f *ast.ForMarkup) pretty.Doc {
	if p.anchoredNode(f) {
		return p.sourceFor(f)
	}
	return pretty.Group(pretty.Concat(
		pretty.Text("{ for "), p.goFieldDoc(f.Clause, forField, fmtClause, f.ClausePos), pretty.Text(" {"), p.cfBody(f.Body, f.BodyMultiline, f.Layout), pretty.Text("} }")))
}

// anchoredNode memoizes literal multiline content. Syntactic layout fields are
// deliberately excluded: they describe placement, not emitted content.
func (p *printer) anchoredNode(n ast.Node) bool {
	if n == nil {
		return false
	}
	if p.anchored == nil {
		p.anchored = make(map[ast.Node]bool)
	}
	if value, ok := p.anchored[n]; ok {
		return value
	}
	value := false
	var rawSegments []ast.Markup
	raw := false
	switch v := n.(type) {
	case *ast.EmbeddedAttr:
		if v.Lang != ast.EmbeddedText {
			raw = true
			rawSegments = v.Segments
		}
	case *ast.EmbeddedInterp:
		if v.Lang != ast.EmbeddedText {
			raw = true
			rawSegments = v.Segments
		}
	case *ast.ComposedPart:
		if v.LiteralSegments != nil && v.LiteralLang != ast.EmbeddedText {
			raw = true
			rawSegments = v.LiteralSegments
		}
	case *ast.ValueArm:
		if v.Segments != nil && v.Lang != ast.EmbeddedText {
			raw = true
			rawSegments = v.Segments
		}
	}
	ast.GoFields(n, func(field ast.GoField) {
		if p.anchoredGoField(field.Src) {
			value = true
		}
	})
	for _, stage := range nodeStages(n) {
		if p.anchoredGoField(stage.Args) {
			value = true
		}
	}
	if raw {
		for _, segment := range rawSegments {
			if hole, ok := segment.(*ast.Interp); ok && p.anchoredGoField(hole.Expr) {
				value = true
			}
		}
		p.anchored[n] = value
		return value
	}
	switch v := n.(type) {
	case *ast.Interp:
		value = value || p.anchoredGoField(v.Expr)
	case *ast.GoBlock:
		value = value || p.anchoredGoField(v.Code)
	}
	if text, ok := n.(*ast.Text); ok {
		value = strings.ContainsAny(text.Value, "\r\n")
	}
	root := true
	ast.Inspect(n, func(child ast.Node) bool {
		if root {
			root = false
			return true
		}
		if p.anchoredNode(child) {
			value = true
		}
		return false
	})
	p.anchored[n] = value
	return value
}

func (p *printer) anchoredBody(nodes []ast.Markup) bool {
	for _, n := range nodes {
		if p.anchoredNode(n) {
			return true
		}
	}
	return false
}

// sourceBody keeps the first node in the same authored coordinate system as
// literal interior lines, which cannot be reindented. Closing syntax is separate.
func (p *printer) sourceBody(nodes []ast.Markup, layout ast.BodyLayout) pretty.Doc {
	return pretty.Concat(pretty.Text(layout.Leading), pretty.Indent(p.children(nodes)))
}

// cfBody formats only syntactic body edges; its interior is literal content.
func (p *printer) cfBody(nodes []ast.Markup, multiline bool, layout ast.BodyLayout) pretty.Doc {
	if p.anchoredBody(nodes) {
		return pretty.Concat(p.sourceBody(nodes, layout), pretty.Text(layout.Trailing))
	}
	if len(nodes) == 0 {
		return pretty.Text("")
	}
	body := pretty.Concat(pretty.Indent(pretty.Concat(pretty.Line, p.children(nodes))), pretty.Line)
	if multiline {
		return pretty.Concat(pretty.BreakParent, body)
	}
	return body
}

// switchMarkup aligns case labels with the switch and formats syntactic edges.
func (p *printer) switchMarkup(s *ast.SwitchMarkup) pretty.Doc {
	if p.anchoredNode(s) {
		return p.sourceSwitch(s)
	}
	head := []pretty.Doc{pretty.Text("{ switch")}
	if s.Tag != "" {
		head = append(head, pretty.Text(" "), p.goFieldDoc(s.Tag, switchField, fmtExpr, s.TagPos))
	}
	head = append(head, pretty.Text(" {"))

	caseParts := make([]pretty.Doc, 0, len(s.Cases))
	for _, c := range s.Cases {
		label := pretty.Text("default:")
		if !c.Default {
			label = pretty.Concat(pretty.Text("case "), p.goFieldDoc(c.List, caseField, fmtCaseList, c.ListPos), pretty.Text(":"))
		}
		caseParts = append(caseParts, pretty.Concat(pretty.HardLine, label, p.caseBody(c.Body, c.BodyMultiline, c.Layout)))
	}
	// Labels align with switch; caseBody owns each arm's syntactic edge.
	return pretty.Group(pretty.Concat(
		pretty.Concat(head...),
		pretty.Concat(caseParts...),
		pretty.HardLine, pretty.Text("} }")))
}

// caseBody formats the syntactic edge after a case label.
func (p *printer) caseBody(nodes []ast.Markup, multiline bool, layout ast.BodyLayout) pretty.Doc {
	if p.anchoredBody(nodes) {
		return pretty.Concat(pretty.Text(layout.Leading), pretty.Indent(p.children(nodes)))
	}
	if len(nodes) == 0 {
		return pretty.Text("")
	}
	inner := p.children(nodes)
	if multiline {
		return pretty.Indent(pretty.Concat(pretty.HardLine, inner))
	}
	return pretty.Concat(pretty.Text(" "), inner)
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
		b.WriteString(v.Raw)
		b.WriteString(`"`)
	case *ast.CommentAttr:
		switch {
		case v.Block && v.Text == "":
			b.WriteString("/**/")
		case v.Block:
			b.WriteString("/* ")
			b.WriteString(v.Text)
			b.WriteString(" */")
		case v.Text == "":
			b.WriteString("//")
		default:
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
	case *ast.ComposedAttr:
		b.WriteString(v.Name)
		b.WriteString("={ ")
		for i, part := range v.Parts {
			if i > 0 {
				b.WriteString(", ")
			}
			if part.LiteralSegments != nil {
				b.WriteString(embeddedLiteralString(part.LiteralLang, part.LiteralSegments, embeddedDelim(part.LiteralDoubleQuoted)))
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
	case *ast.SwitchAttr:
		b.WriteString("{ ")
		writeSwitchAttr(b, v)
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
		// Preserve the braced form (name={js`…`}). It must round-trip because a
		// braced literal binds a declared component prop while the bare form
		// falls through to the Attrs bag — stripping the braces would silently
		// change the meaning. A whole-literal pipeline only parses braced
		// (name={`…` |> f}), so Stages forces braces too even on the rare node
		// where Braced was not recorded.
		braced := v.Braced || len(v.Stages) > 0
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

// embeddedSegmentsMultiline reports whether an embedded literal's body spans
// more than one source line (only then does fmt re-indent it; single-line values
// stay inline, unchanged).
func embeddedSegmentsMultiline(segs []ast.Markup) bool {
	for _, n := range segs {
		if t, ok := n.(*ast.Text); ok && strings.Contains(t.Value, "\n") {
			return true
		}
	}
	return false
}

// embeddedAttrBody splits an embedded-attribute literal's Segments into raw text
// segments and rendered holes for rawfmt, mirroring nodesToBody but emitting the
// TIGHT `@{expr}` hole form used inside attribute literals (not the spaced body
// form). segments and holes interleave with len(segments) == len(holes)+1.
func embeddedAttrBody(segs []ast.Markup) (segments, holes []string) {
	var cur strings.Builder
	for _, n := range segs {
		switch v := n.(type) {
		case *ast.Text:
			cur.WriteString(v.Value)
		case *ast.Interp:
			segments = append(segments, cur.String())
			cur.Reset()
			holes = append(holes, embeddedHoleString(v))
		default:
			cur.WriteString(markupInlineString(n))
		}
	}
	segments = append(segments, cur.String())
	return segments, holes
}

// embeddedHoleString renders one `@{ expr |> stage }` hole tight (`@{expr}`),
// matching writeEmbeddedAttrSegments.
func embeddedHoleString(v *ast.Interp) string {
	var b strings.Builder
	b.WriteString("@{")
	b.WriteString(fmtExpr(v.Expr))
	for _, s := range v.Stages {
		b.WriteString(" |> ")
		b.WriteString(pipeStageStr(s))
	}
	b.WriteString("}")
	return b.String()
}

// embeddedAttrValueDoc builds the Doc for a multi-line embedded-literal attribute
// value. opener is the text up to and including the opening delimiter; formatted
// is the re-indented body from the formatter; closer is the closing delimiter
// plus any `|> stage` / `}`.
//
// Two layouts, chosen by the body's own structure (which the formatter preserves):
//   - Inline (content-adjacent, e.g. js"{ … }" whose `{`/`}` hug the delimiters):
//     the opening delimiter attaches to the first body line and the closer to the
//     last; body nesting comes from the formatter's own brace-depth tabs.
//   - Block (body on its own lines, signalled by a leading newline, e.g.
//     css`\n…\n`): the delimiters stand alone and the body is indented one level
//     under the attribute — the same shape as a <script>/<style> element body.
//
// Blank lines emit as bare "\n" so they never carry trailing tabs (idempotence).
func embeddedAttrValueDoc(opener string, lines []string, closer string) pretty.Doc {
	// A leading blank logical line means the body opened on its own line → block
	// layout (delimiters alone, body one level under). Then drop leading/trailing
	// blank logical lines. Each logical line may carry internal newlines (an
	// Opaque token — template literal / block comment); emitted as one Text they
	// are verbatim (the pretty engine adds no managed indent inside a Text).
	block := len(lines) > 0 && lines[0] == ""
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return pretty.Text(opener + closer)
	}
	if block {
		inner := make([]pretty.Doc, 0, len(lines)*2)
		for _, ln := range lines {
			if ln = strings.TrimRight(ln, " \t"); ln == "" {
				inner = append(inner, pretty.Text("\n"))
				continue
			}
			inner = append(inner, pretty.HardLine, pretty.Text(ln))
		}
		return pretty.Concat(
			pretty.Text(opener),
			pretty.Indent(pretty.Concat(inner...)),
			pretty.HardLine, pretty.Text(closer),
		)
	}
	parts := make([]pretty.Doc, 0, len(lines)*2+2)
	parts = append(parts, pretty.Text(opener+lines[0]))
	for _, ln := range lines[1:] {
		if ln = strings.TrimRight(ln, " \t"); ln == "" {
			parts = append(parts, pretty.Text("\n"))
			continue
		}
		parts = append(parts, pretty.HardLine, pretty.Text(ln))
	}
	parts = append(parts, pretty.Text(closer))
	return pretty.Concat(parts...)
}

// embeddedAttrDoc re-indents a multi-line js`/css` attribute value's body with
// the configured JS/CSS formatter. ok=false (caller falls back to verbatim
// inline) for a non-JS/CSS literal, a nil formatter, a single-line body, or any
// formatter failure.
func (p *printer) embeddedAttrDoc(v *ast.EmbeddedAttr) (pretty.Doc, bool) {
	if v.Lang != ast.EmbeddedJS && v.Lang != ast.EmbeddedCSS {
		return pretty.Doc{}, false // ast.EmbeddedText — f`…`: no sublanguage.
	}
	if !embeddedSegmentsMultiline(v.Segments) {
		return pretty.Doc{}, false
	}
	segments, holes := embeddedAttrBody(v.Segments)
	delim := embeddedDelim(v.DoubleQuoted)
	escape := func(s string) string {
		var b strings.Builder
		writeEmbeddedLiteralText(&b, s, delim)
		return b.String()
	}
	lines, ok := p.embeddedAttrLines(v.Lang, segments, holes, escape)
	if !ok {
		return pretty.Doc{}, false
	}
	// Keep the inline form if the formatter collapsed the body to a single
	// physical line (one logical line with no internal newline). A leading blank
	// logical line (block signal) still counts as multi-line.
	if len(lines) <= 1 && (len(lines) == 0 || !strings.Contains(lines[0], "\n")) {
		return pretty.Doc{}, false
	}
	braced := v.Braced || len(v.Stages) > 0
	var opener strings.Builder
	opener.WriteString(v.Name)
	opener.WriteString("=")
	if braced {
		opener.WriteString("{")
	}
	opener.WriteString(embeddedLangName(v.Lang))
	opener.WriteByte(delim)
	var closer strings.Builder
	closer.WriteByte(delim)
	for _, s := range v.Stages {
		closer.WriteString(" |> ")
		closer.WriteString(pipeStageStr(s))
	}
	if braced {
		closer.WriteString("}")
	}
	return embeddedAttrValueDoc(opener.String(), lines, closer.String()), true
}

// embeddedAttrLines formats an embedded-attribute body into re-indented LOGICAL
// lines (a multi-line Opaque token stays within one line). It prefers the
// built-in LineFormatter (set by Fprint); an external flat Formatter (via
// FprintWith) falls back to the naive physical-line split, which cannot preserve
// multi-line-token interiors — external custom formatters carry no token info.
func (p *printer) embeddedAttrLines(lang ast.EmbeddedLang, segments, holes []string, escape func(string) string) ([]string, bool) {
	var lf rawfmt.LineFormatter
	var f rawfmt.Formatter
	switch lang {
	case ast.EmbeddedJS:
		lf, f = p.jsLineFmt, p.jsFmt
	case ast.EmbeddedCSS:
		lf, f = p.cssLineFmt, p.cssFmt
	}
	if lf != nil {
		return rawfmt.FormatStringLines(segments, holes, lf, escape)
	}
	if f != nil {
		s, ok := rawfmt.FormatString(segments, holes, f, escape)
		if !ok {
			return nil, false
		}
		return strings.Split(s, "\n"), true
	}
	return nil, false
}

// jsBodyDoc / cssBodyDoc format a <script>/<style> body, preferring the built-in
// LineFormatter (preserves multi-line-token interiors) over an external flat
// Formatter. ok=false (no formatter or a formatter failure) → caller renders the
// body verbatim.
func (p *printer) jsBodyDoc(segments, holes []string) (pretty.Doc, bool) {
	if p.jsLineFmt != nil {
		return rawfmt.FormatLines(segments, holes, p.jsLineFmt)
	}
	if p.jsFmt != nil {
		return rawfmt.Format(segments, holes, p.jsFmt)
	}
	return pretty.Doc{}, false
}

func (p *printer) cssBodyDoc(segments, holes []string) (pretty.Doc, bool) {
	if p.cssLineFmt != nil {
		return rawfmt.FormatLines(segments, holes, p.cssLineFmt)
	}
	if p.cssFmt != nil {
		return rawfmt.Format(segments, holes, p.cssFmt)
	}
	return pretty.Doc{}, false
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

// writeSwitchAttr is switchAttrDoc's flat counterpart, for the inline
// attribute slots that render without line breaks.
func writeSwitchAttr(b *strings.Builder, s *ast.SwitchAttr) {
	b.WriteString("switch")
	if tag := fmtExpr(s.Tag); strings.TrimSpace(tag) != "" {
		b.WriteString(" ")
		b.WriteString(tag)
	}
	b.WriteString(" {")
	for _, cc := range s.Cases {
		if cc.Default {
			b.WriteString(" default:")
		} else {
			b.WriteString(" case ")
			b.WriteString(fmtExpr(cc.List))
			b.WriteString(":")
		}
		writeCondAttrList(b, cc.Body)
	}
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
	return pretty.Print(p.markup(n), 1<<30, pretty.DefaultTabWidth)
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

// isExecutableScript reports whether a <script> runs as JavaScript: no static
// type attribute, or one htmlattr.ScriptTypeIsJS accepts. A data island (e.g.
// type="application/json", type="text/template") is not executable and is left
// verbatim.
func isExecutableScript(e *ast.Element) bool {
	for _, a := range e.Attrs {
		if sa, ok := a.(*ast.StaticAttr); ok && strings.EqualFold(sa.Name, "type") {
			return htmlattr.ScriptTypeIsJS(sa.Value)
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
//
// width and tabWidth are the measure breakWideLiterals breaks over-long
// composite literals against — the same values the caller prints the rest of
// the file at. This stays a package-level function, not a method: the
// faithfulness normalizer (corpus_test.go's canonGo) has no *printer to call
// it on, and must still apply exactly the same pass the printer does, or the
// two Src strings it compares diverge on every over-long literal.
func fmtGoChunk(src string, width, tabWidth int) string {
	// Format the chunk as a VALID FILE, not a fragment: go/format.Source's
	// fragment mode strips a fixed byte count off its output, which shears a
	// //go:build comment that go/printer hoisted above the injected clause.
	// forceMarker is "" here: a GoChunk is ordinary top-level Go with no gsx
	// values, so it holds no placeholder standing in for an unmeasurable value.
	prepared := breakWideLiterals(goExprWrapper+src, width, tabWidth, "")
	out, err := format.Source([]byte(blockFormBraces(prepared)))
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

// goExprHoleRuneCandidates are candidate placeholder runes: Unicode modifier
// letters, which Go accepts as identifier letters (identifier = letter { letter |
// digit }, letter = unicode_letter, which includes category Lm). Repeating one N
// times yields a valid Go identifier that is exactly N *runes* wide — and
// go/format's alignment runs through text/tabwriter, which measures cells in
// runes. That is what lets a placeholder stand in for a gsx value at its true
// rendered width (see fmtGoExprParts), down to widths no `_gsx`-prefixed name
// could reach. They are vanishingly unlikely to occur in real source; if one
// does, the next candidate is tried.
var goExprHoleRuneCandidates = []string{"ᴳ", "ᴴ", "ᴵ", "ᴶ"}

// goExprHoleRunes picks TWO distinct placeholder runes absent from src: flat
// stands in for a value with a known one-line width (repeated to that width);
// multi stands in for a multi-line value, always a SINGLE rune (its true width
// is unknowable). The two runes must differ so the re-split below can never
// confuse a one-rune flat placeholder (flat value of width 1) with the one-rune
// multi-line marker, and so breakWideLiterals can recognise the marker as a
// forced break. Both must be absent from src, so the formatted output can be
// re-split at the placeholders unambiguously (a rune occurring inside a string
// literal or comment would misdirect that split). If two free runes cannot be
// found, ok is false and the caller relays the region verbatim, exactly as when
// one free rune could not be found before.
func goExprHoleRunes(src string) (flat, multi string, ok bool) {
	var free []string
	for _, r := range goExprHoleRuneCandidates {
		if !strings.Contains(src, r) {
			free = append(free, r)
			if len(free) == 2 {
				return free[0], free[1], true
			}
		}
	}
	return "", "", false
}

// goExprFlatText renders doc as it would appear on a single line, and reports
// whether it fits on one line at all. A forced break (a block element, or an
// author line break) makes a gsx value multi-line no matter the available
// width; such a value has no single flat rendering to hand to gofmt.
func goExprFlatText(doc pretty.Doc) (string, bool) {
	const wide = 1 << 20 // wider than any real line: nothing breaks unless forced
	flat := pretty.Print(doc, wide, pretty.DefaultTabWidth)
	if strings.Contains(flat, "\n") {
		return "", false
	}
	return flat, true
}

// goExprFlatWidth reports the rune width of doc rendered on a single line, and
// whether it fits on one line at all. See goExprFlatText.
func goExprFlatWidth(doc pretty.Doc) (int, bool) {
	flat, ok := goExprFlatText(doc)
	if !ok {
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
		return pretty.Text(embeddedLiteralString(pt.Lang, pt.Segments, embeddedDelim(pt.DoubleQuoted))), true
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
// one-rune placeholder drawn from a rune DISTINCT from the flat one, so it can
// double as breakWideLiterals' forceMarker (a line holding it is over budget
// however short it measures — the value forces a break and can never be a
// one-liner, so the enclosing literal breaks its fields). Layout is then still
// correct except for end-of-line comment columns in that value's alignment
// section, which gofmt (seeing a real multi-line value) would have split.
// Everything else in the region — indentation, `=` alignment, blank lines — is
// unaffected, since none of it depends on value width.
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
	return p.formatGoParts(parts, goExprWrapper, "", StripSyntheticPackage)
}

// formatGoParts is the placeholder round-trip shared by every Go region that
// carries gsx values: it substitutes one width-matched placeholder identifier
// per value, wraps the run in a synthetic compilation unit (wrapperPrefix …
// wrapperSuffix), lays it out with gofmt (breakWideLiterals + blockFormBraces),
// strips the wrapper via strip, and re-splits the formatted Go at the
// placeholders. The wrapper is the only difference between the call sites: a
// GoWithElements decl wraps in a bare package clause (top-level declarations,
// wrapperSuffix ""), a `{{ }}` GoBlock in a package clause plus a func body
// (statements, wrapperSuffix "\n}\n"). See fmtGoExprParts's own doc for why
// placeholders make an incomplete Go fragment whole again.
func (p *printer) formatGoParts(parts []ast.GoPart, wrapperPrefix, wrapperSuffix string, strip func([]byte) (string, bool)) ([]ast.GoPart, []goexprshape.Result, bool) {
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
	flatHole, multiHole, ok := goExprHoleRunes(text.String())
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
		// A flat value's placeholder is the flat rune repeated to its rendered
		// width, so gofmt aligns end-of-line comments to the value's true column.
		// A multi-line value's real width is unknowable, so it gets a SINGLE
		// multi-line rune — distinct from the flat rune. That distinct rune is
		// handed to breakWideLiterals as forceMarker: a multi-line value forces a
		// break and can never be a one-liner, so the literal holding it must break
		// its fields even though this 1-rune placeholder measures short. The
		// element printer lays the value out regardless of the placeholder's width;
		// only gofmt's alignment of the surrounding Go ever reads it.
		var h string
		if width, flat := goExprFlatWidth(doc); flat {
			h = strings.Repeat(flatHole, width)
		} else {
			h = multiHole
		}
		start := len(wrapperPrefix) + src.Len()
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
	sanitized, sanHoles := goexprshape.Sanitize(wrapperPrefix+src.String()+wrapperSuffix, shapeHoles)
	shapes := goexprshape.Classify(sanitized, sanHoles)

	// blockFormBraces only ever inserts text before a composite literal's `}`,
	// so it cannot move a hole across a token boundary, reorder holes, or change
	// any hole's classification — shapes stays valid, and the placeholders are
	// still found in output order by the re-split below.
	prepared := breakWideLiterals(sanitized, p.width, p.tabWidth, multiHole)
	out, err := format.Source([]byte(blockFormBraces(prepared)))
	if err != nil {
		return nil, shapes, false
	}
	// Drop the synthetic wrapper. For the package-clause wrapper this is NOT
	// reliably the first line: go/printer hoists a //go:build comment above the
	// package clause, so a line-index strip would shear the constraint and splice
	// `package _gsxfmt` into the user's source. StripSyntheticPackage locates it
	// by parsing instead; the func-body wrapper (GoBlock) strips via
	// extractFuncBody.
	formatted, ok := strip(out)
	if !ok {
		return nil, shapes, false
	}

	// Re-split at the placeholders, left to right. Two values can share an
	// identical placeholder string — two flat values of equal width, or two
	// multi-line values (both the single multi rune) — which is harmless: the
	// cursor advances strictly left to right, consuming holes[next] in value
	// order, and neither hole rune ever appears in the surrounding Go text (both
	// were chosen absent from src). So the next placeholder the cursor meets is
	// always this value's own. The flat and multi runes being DISTINCT is what
	// keeps a width-1 flat placeholder from being mistaken for the multi marker
	// when firstWideLine scans for the latter — a re-split search never even
	// compares across the two, since it only ever looks for the exact holes[next].
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
