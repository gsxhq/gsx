package printer

import (
	"fmt"
	gotoken "go/token"
	"strings"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/pretty"
	"github.com/gsxhq/gsx/internal/wsnorm"
	"github.com/gsxhq/gsx/parser"
)

// goFieldForm is the Go statement a markup Go field is formatted inside, so
// gofmt sees complete Go: an expression, an if header, a switch tag, a case
// list or a for clause. open/close surround the field inside a func body.
type goFieldForm struct{ open, close string }

var (
	exprField   = goFieldForm{"_ = ", ""}
	ifField     = goFieldForm{"if ", " {\n}"}
	switchField = goFieldForm{"switch ", " {\n}"}
	caseField   = goFieldForm{"switch {\ncase ", ":\n}"}
	forField    = goFieldForm{"for ", " {\n}"}
)

// goValue is a marked value in formatted Go text: a multi-line js`/css`
// literal whose body the printer re-indents (lit), or an element or fragment
// literal printed with the ordinary markup printer (doc; flat is its one-line
// rendering when it has one).
type goValue struct {
	lit    *ast.EmbeddedInterp
	doc    pretty.Doc
	flat   string
	isFlat bool
}

// multiline reports whether v renders across lines.
func (v goValue) multiline() bool { return v.lit != nil || !v.isFlat }

// goFieldDoc lays out a markup Go field (an attribute value, an interpolation,
// a control-flow header). A field holding gsx values — element, fragment or
// prefixed literals — is not Go on its own, so plain (the field's go/format
// path) would relay it verbatim, and a verbatim multi-line field re-indents one
// level deeper on every pass. Such a field goes through the same placeholder
// round-trip as a {{ }} block instead: gofmt lays out the Go, and each element
// is printed from its AST at the Go line's depth, so the layout is a function
// of the AST alone and preserved text (a <pre> body) is never re-indented.
func (p *printer) goFieldDoc(src string, form goFieldForm, plain func(string) string) pretty.Doc {
	if text, values, ok := p.fmtGoField(src, form); ok {
		return p.goTextDoc(text, values, false)
	}
	return multiline(plain(src))
}

// fmtGoField formats src, a Go field holding at least one gsx value, inside
// form. ok is false when src holds no value or cannot be formatted.
func (p *printer) fmtGoField(src string, form goFieldForm) (string, map[string]goValue, bool) {
	if !mayHoldGoValue(src) {
		return "", nil, false
	}
	parts, ok := splitGoParts(strings.TrimSpace(src))
	if !ok || len(parts) == 0 {
		return "", nil, false
	}
	strip := func(out []byte) (string, bool) {
		body, ok := extractFuncBody(string(out))
		if !ok {
			return "", false
		}
		body, ok = strings.CutPrefix(body, form.open)
		if !ok {
			return "", false
		}
		return strings.CutSuffix(body, form.close)
	}
	return p.fmtGoPartsText(parts, goBlockWrapperPrefix+form.open, form.close+goBlockWrapperSuffix, strip)
}

// mayHoldGoValue is the cheap precheck before a split (the one codegen's
// materializeEmbeddedMarkup applies): a gsx value needs a `<`, or a prefixed
// literal, which needs a quote or backtick.
func mayHoldGoValue(src string) bool {
	if strings.ContainsRune(src, '<') {
		return true
	}
	if !strings.ContainsAny(src, "`\"") {
		return false
	}
	_, ok := parser.ContainsEmbeddedLiteral(src)
	return ok
}

// splitGoParts splits Go source into interleaved GoText and gsx value parts,
// using a fresh FileSet (the printer holds no shared one, and the split only
// needs source-relative positions — the reformatted holes read from the value
// nodes, not absolute source positions). The values' markup is
// whitespace-normalized as codegen normalizes it, so an element prints the
// same whatever indentation its text carried. It returns (nil, true) when src
// holds no value (the plain-Go fast path) and (nil, false) when the split
// reports a parse error, so the caller can fall back to the verbatim relay.
func splitGoParts(src string) ([]ast.GoPart, bool) {
	fset := gotoken.NewFileSet()
	f := fset.AddFile("", fset.Base(), len(src))
	parts, errs := parser.SplitGoExprElements(fset, src, f.Pos(0), nil)
	if len(errs) > 0 {
		return nil, false
	}
	wsnorm.NormalizeGoParts(parts)
	return parts, true
}

// fmtGoPartsText runs the shared placeholder round-trip (formatGoParts) and
// reassembles one canonical string: the formatted Go, each flat prefixed
// literal re-rendered in place, and a marker for every value the line layout
// must print as a doc — each element and fragment, and each multi-line
// js`/css` literal. The marker prefix is grown until absent from the Go text
// (never an assumed-unique sentinel), so Go that happens to spell it cannot be
// mistaken for a value.
func (p *printer) fmtGoPartsText(parts []ast.GoPart, wrapperPrefix, wrapperSuffix string, strip func([]byte) (string, bool)) (string, map[string]goValue, bool) {
	formatted, _, ok := p.formatGoParts(parts, wrapperPrefix, wrapperSuffix, strip)
	if !ok {
		return "", nil, false
	}
	prefix := "gsxǁvalueǁ"
	var scan strings.Builder
	for _, part := range formatted {
		if gt, ok := part.(ast.GoText); ok {
			scan.WriteString(gt.Src)
		}
	}
	for strings.Contains(scan.String(), prefix) {
		prefix += "q"
	}

	var b strings.Builder
	var values map[string]goValue
	mark := func(v goValue) {
		if values == nil {
			values = map[string]goValue{}
		}
		marker := fmt.Sprintf("%s%dz", prefix, len(values))
		values[marker] = v
		b.WriteString(marker)
	}
	for _, part := range formatted {
		switch v := part.(type) {
		case ast.GoText:
			b.WriteString(v.Src)
			continue
		case *ast.Element, *ast.Fragment:
			doc, _ := p.goExprValue(part)
			flat, isFlat := goExprFlatText(doc)
			mark(goValue{doc: doc, flat: flat, isFlat: isFlat})
			continue
		case *ast.EmbeddedInterp:
			if (v.Lang == ast.EmbeddedJS || v.Lang == ast.EmbeddedCSS) && embeddedSegmentsMultiline(v.Segments) {
				mark(goValue{lit: v})
				continue
			}
		}
		doc, ok := p.goExprValue(part)
		if !ok {
			return "", nil, false
		}
		b.WriteString(pretty.Print(doc, 1<<30, p.tabWidth))
	}
	return b.String(), values, true
}

// goTextDoc lays out formatted Go text carrying value markers: each line after
// the first on its own managed line (HardLine), a blank line as a bare newline
// (no trailing tabs), raw-string interiors verbatim. block reports that the
// first line, too, starts on a fresh line.
func (p *printer) goTextDoc(text string, values map[string]goValue, block bool) pretty.Doc {
	segs := splitOutsideRawStrings(text)
	docs := make([]pretty.Doc, 0, len(segs)*2)
	for i, seg := range segs {
		if i > 0 && strings.TrimSpace(seg) == "" {
			docs = append(docs, pretty.Text("\n"))
			continue
		}
		line := p.goLineDocs(seg, values)
		if i == 0 && !block {
			line = line[1:]
		}
		docs = append(docs, line...)
	}
	multi := len(segs) > 1
	for _, v := range values {
		multi = multi || v.multiline()
	}
	if multi {
		docs = append(docs, pretty.BreakParent)
	}
	return pretty.Concat(docs...)
}

// goLineDocs renders one line of formatted Go text as [HardLine, …]. The
// line's leading tabs are its Go-structural depth: a value marker prints its
// doc indented that deep (so an element's children and closing tag land under
// the Go line, whatever the enclosing markup depth), and a multi-line js`/css`
// literal re-indents its body one level under it.
func (p *printer) goLineDocs(seg string, values map[string]goValue) []pretty.Doc {
	tabs := 0
	for tabs < len(seg) && seg[tabs] == '\t' {
		tabs++
	}
	docs := []pretty.Doc{pretty.HardLine}
	rest := seg
	for {
		at, marker := -1, ""
		for m := range values {
			if i := strings.Index(rest, m); i >= 0 && (at < 0 || i < at) {
				at, marker = i, m
			}
		}
		if at < 0 {
			return append(docs, pretty.Text(strings.TrimRight(rest, " \t")))
		}
		before := rest[:at]
		rest = rest[at+len(marker):]
		v := values[marker]
		if v.lit == nil {
			if v.isFlat {
				docs = append(docs, pretty.Text(before+v.flat))
			} else {
				docs = append(docs, pretty.Text(before), indentN(tabs, v.doc))
			}
			continue
		}
		lit := p.goBlockLiteralDocs(before, v.lit, tabs)
		// Drop the literal layout's own leading HardLine: it continues this line.
		docs = append(docs, lit[1:]...)
	}
}
