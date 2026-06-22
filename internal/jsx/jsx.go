// Package jsx is gsx's codegen-time JavaScript context engine for <script>
// interpolation (Slice C1). ResolveScripts walks the AST and, for every @{ … }
// hole inside a <script> element, classifies the JavaScript context it sits in
// (value / string / template / regex / comment) so codegen can later escape each
// hole correctly. Misclassification is an XSS, so it fails closed: any
// identifier/binding position, unclassifiable hole, lex error, or sentinel
// collision returns an error rather than a guess.
//
// It is a codegen-time package and MAY import tdewolff; it MUST NOT be imported
// by the root gsx runtime package.
package jsx

import (
	"fmt"
	"strings"

	"github.com/gsxhq/gsx/ast"
	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/js"
)

// holePrefix is the sentinel identifier stem substituted for each @{ … } hole in
// the lexed skeleton. It is a valid JS identifier, so in code position it lexes
// as a single IdentifierToken and inside a string/template/regex/comment it
// becomes a substring of that literal's bytes — that distinction is how a hole's
// context is classified.
const holePrefix = "_GSXJSHOLE_"

// ResolveScripts walks f and classifies every @{ … } hole inside each <script>
// element, setting interp.JSCtx, un-splitting comment holes back to literal
// Text, or returning a positioned fail-closed error. <style> and non-script
// interps are left untouched (JSCtxNone).
func ResolveScripts(f *ast.File) error {
	for _, d := range f.Decls {
		if comp, ok := d.(*ast.Component); ok {
			if err := resolveMarkup(comp.Body); err != nil {
				return err
			}
		}
	}
	return nil
}

// resolveMarkup mirrors internal/jsmin/file.go's minifyMarkup walk: recurse
// through Element/Fragment/IfMarkup/ForMarkup/SwitchMarkup, but stop at <script>
// elements (resolve them, don't recurse into their raw-text children).
func resolveMarkup(nodes []ast.Markup) error {
	for _, n := range nodes {
		switch v := n.(type) {
		case *ast.Element:
			// Resolve JS-context attributes (e.g. x-data, onclick) on EVERY
			// element, including <script> (it can carry <script onload="@{…}">).
			// This must run before the holes are type-probed in codegen.
			for _, a := range v.Attrs {
				if ja, ok := a.(*ast.JSAttr); ok {
					if err := ResolveJSAttr(ja.Name, ja.Segments); err != nil {
						return err
					}
				}
			}
			if strings.EqualFold(v.Tag, "script") {
				if err := resolveScript(v); err != nil {
					return err
				}
				continue
			}
			if err := resolveMarkup(v.Children); err != nil {
				return err
			}
		case *ast.Fragment:
			if err := resolveMarkup(v.Children); err != nil {
				return err
			}
		case *ast.IfMarkup:
			if err := resolveMarkup(v.Then); err != nil {
				return err
			}
			if err := resolveMarkup(v.Else); err != nil {
				return err
			}
		case *ast.ForMarkup:
			if err := resolveMarkup(v.Body); err != nil {
				return err
			}
		case *ast.SwitchMarkup:
			for i := range v.Cases {
				if err := resolveMarkup(v.Cases[i].Body); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// hole records one @{ … } interpolation in the lexed skeleton.
type hole struct {
	interp   *ast.Interp
	childIdx int    // index of the Interp within el.Children
	start    int    // byte offset of the placeholder in the skeleton
	end      int    // start + len(placeholder)
	ctx      ast.JSCtx
	comment  bool   // hole fell inside a JS comment → un-split to literal
	resolved bool   // a token covered this hole
}

// jsExecutableTypes are the <script type> values that run as JavaScript. Any
// other (non-empty) type marks a data block (e.g. application/json) — not JS.
var jsExecutableTypes = map[string]bool{
	"text/javascript": true, "module": true, "application/javascript": true,
	"text/ecmascript": true, "application/ecmascript": true,
}

// isDataIslandScript reports whether el is a <script> whose type marks it a data
// block (not executable JS), e.g. <script type="application/json">.
func isDataIslandScript(el *ast.Element) bool {
	for _, a := range el.Attrs {
		if sa, ok := a.(*ast.StaticAttr); ok && strings.EqualFold(sa.Name, "type") {
			t := strings.ToLower(strings.TrimSpace(sa.Value))
			return t != "" && !jsExecutableTypes[t]
		}
	}
	return false
}

// scriptType returns the static `type` attribute value of el (for diagnostics),
// or "" if absent / non-static.
func scriptType(el *ast.Element) string {
	for _, a := range el.Attrs {
		if sa, ok := a.(*ast.StaticAttr); ok && strings.EqualFold(sa.Name, "type") {
			return sa.Value
		}
	}
	return ""
}

// resolveDataIsland classifies a data-block <script> (e.g. application/json):
// the whole body must be exactly one @{ } hole (modulo whitespace), emitted as a
// JSON value. Anything else fails closed.
func resolveDataIsland(el *ast.Element) error {
	var theInterp *ast.Interp
	for _, c := range el.Children {
		switch v := c.(type) {
		case *ast.Text:
			if strings.TrimSpace(v.Value) != "" {
				return fmt.Errorf("jsx: a data <script> (type=%q) must contain exactly one @{ } value; found literal text %q",
					scriptType(el), strings.TrimSpace(v.Value))
			}
		case *ast.Interp:
			if theInterp != nil {
				return fmt.Errorf("jsx: a data <script> must contain exactly one @{ } value; found more than one")
			}
			theInterp = v
		default:
			return fmt.Errorf("jsx: unexpected %T in data <script> body", c)
		}
	}
	if theInterp == nil {
		return nil // holeless data block (static JSON) — nothing to interpolate.
	}
	theInterp.JSCtx = ast.JSCtxValue
	return nil
}

// resolveScript classifies every Interp child of a <script> element.
func resolveScript(el *ast.Element) error {
	// A data-block <script> (e.g. type="application/json") is not JavaScript:
	// its body must be exactly one @{ } value emitted as JSON. This branch runs
	// FIRST so a holeless data block also takes the data path (and is never
	// JS-classified); resolveDataIsland returns nil for a holeless body.
	if isDataIslandScript(el) {
		return resolveDataIsland(el)
	}

	// Collect holes and build the skeleton. Bail early if there are no holes.
	hasInterp := false
	for _, c := range el.Children {
		if _, ok := c.(*ast.Interp); ok {
			hasInterp = true
			break
		}
	}
	if !hasInterp {
		return nil
	}

	// Placeholder-collision guard (CVE-grade, fail-closed): if any literal Text
	// already contains the sentinel stem, we cannot safely place sentinels.
	for _, c := range el.Children {
		if t, ok := c.(*ast.Text); ok && strings.Contains(t.Value, holePrefix) {
			return fmt.Errorf("jsx: <script> at %d: source contains the reserved sentinel %q; cannot classify @{ } holes safely",
				el.Pos(), holePrefix)
		}
	}

	var sb strings.Builder
	var holes []*hole
	for idx, c := range el.Children {
		switch v := c.(type) {
		case *ast.Text:
			sb.WriteString(v.Value)
		case *ast.Interp:
			ph := fmt.Sprintf("%s%d", holePrefix, len(holes))
			start := sb.Len()
			sb.WriteString(ph)
			holes = append(holes, &hole{
				interp:   v,
				childIdx: idx,
				start:    start,
				end:      start + len(ph),
			})
		default:
			// Should not occur inside a raw-text <script>; fail closed.
			return fmt.Errorf("jsx: <script> at %d: unexpected non-text/interp node %T in script body",
				el.Pos(), c)
		}
	}

	if err := classify(sb.String(), holes); err != nil {
		return err
	}

	// Apply: set JSCtx, or rewrite comment holes to literal Text in place.
	for _, h := range holes {
		if !h.resolved {
			return fmt.Errorf("jsx: @{ } at %d in <script> could not be classified (lex error or unreachable position); fails closed",
				h.interp.Pos())
		}
		if h.comment {
			lit := interpLiteral(h.interp)
			if strings.Contains(strings.ToLower(lit), "</script") {
				return fmt.Errorf("jsx: @{ } at %d inside a <script> comment contains \"</script\", which would close the script element; remove it or move the value out of the comment",
					h.interp.Pos())
			}
			el.Children[h.childIdx] = &ast.Text{Value: lit}
			continue
		}
		h.interp.JSCtx = h.ctx
	}
	return nil
}

// ResolveJSAttr classifies every @{ … } hole in a JS-context attribute value
// (e.g. x-data="{ tab: @{ tab } }"). It builds the same _GSXJSHOLE_ skeleton as
// resolveScript, runs the same classify, and sets each Interp.JSCtx — so codegen
// can later escape each hole by its JS context. An attribute value is a single JS
// expression (not a program), so a hole that lands inside a JS comment is
// degenerate and FAILS CLOSED here (unlike <script>, where comment holes
// un-split): we never mutate the segments before the type-probe. Identifier /
// binding / unclassifiable positions fail closed exactly as in <script>.
func ResolveJSAttr(name string, segments []ast.Markup) error {
	// Bail early if there are no holes (a hole-free JS attr stays StaticAttr and
	// never reaches here, but be defensive).
	hasInterp := false
	for _, c := range segments {
		if _, ok := c.(*ast.Interp); ok {
			hasInterp = true
			break
		}
	}
	if !hasInterp {
		return nil
	}

	// Placeholder-collision guard (fail-closed): if any literal Text already
	// contains the sentinel stem, we cannot safely place sentinels.
	for _, c := range segments {
		if t, ok := c.(*ast.Text); ok && strings.Contains(t.Value, holePrefix) {
			return fmt.Errorf("jsx: attribute %q: value contains the reserved sentinel %q; cannot classify @{ } holes safely",
				name, holePrefix)
		}
	}

	var sb strings.Builder
	var holes []*hole
	for idx, c := range segments {
		switch v := c.(type) {
		case *ast.Text:
			sb.WriteString(v.Value)
		case *ast.Interp:
			ph := fmt.Sprintf("%s%d", holePrefix, len(holes))
			start := sb.Len()
			sb.WriteString(ph)
			holes = append(holes, &hole{
				interp:   v,
				childIdx: idx,
				start:    start,
				end:      start + len(ph),
			})
		default:
			return fmt.Errorf("jsx: attribute %q: value may contain only text and @{ } interpolations, got %T", name, c)
		}
	}

	if err := classify(sb.String(), holes); err != nil {
		return err
	}

	for _, h := range holes {
		if !h.resolved {
			return fmt.Errorf("jsx: @{ } at %d in attribute %q could not be classified (lex error or unreachable position); fails closed",
				h.interp.Pos(), name)
		}
		if h.comment {
			return fmt.Errorf("jsx: @{ } at %d inside a JS comment in attribute %q is not supported; move it out of the comment",
				h.interp.Pos(), name)
		}
		h.interp.JSCtx = h.ctx
	}
	return nil
}

// classify lexes the skeleton and assigns each hole a context (setting h.ctx /
// h.comment / h.resolved), or returns a positioned fail-closed error for a hole
// in an unsafe code position.
func classify(skeleton string, holes []*hole) error {
	l := js.NewLexer(parse.NewInputString(skeleton))
	pos := 0                 // running byte offset = start of the token about to be processed
	prevSig := js.ErrorToken // previous SIGNIFICANT token (start-of-input)

	for {
		tt, data := l.Next()
		if tt == js.ErrorToken {
			break // EOF or lex error; unresolved holes are caught by the caller
		}

		// Regex-vs-division: when the lexer hands back a bare DivToken in a
		// regex-start position, re-lex the full regex literal so a hole inside
		// it is covered by one RegExpToken (mirrors internal/jsmin).
		if tt == js.DivToken && regexPosition(prevSig) {
			rtt, rdata := l.RegExp()
			if rtt == js.RegExpToken {
				tt, data = rtt, rdata
			}
		}

		start, end := pos, pos+len(data)
		pos = end

		for _, h := range holes {
			if h.resolved {
				continue
			}
			if h.start < start || h.end > end {
				continue // hole not (fully) within this token
			}
			if err := classifyHole(h, tt, start, end, prevSig); err != nil {
				return err
			}
		}

		if isSignificant(tt) {
			prevSig = tt
		}
	}
	return nil
}

// classifyHole assigns a context to one hole that falls within token (tt) spanning
// [tokStart, tokEnd), given the previous significant token prevSig.
func classifyHole(h *hole, tt js.TokenType, tokStart, tokEnd int, prevSig js.TokenType) error {
	ownToken := h.start == tokStart && h.end == tokEnd

	if !ownToken {
		// The placeholder is a strict substring of a literal token's bytes.
		switch tt {
		case js.StringToken:
			h.ctx = ast.JSCtxString
		case js.TemplateToken, js.TemplateStartToken, js.TemplateMiddleToken, js.TemplateEndToken:
			h.ctx = ast.JSCtxTemplate
		case js.RegExpToken:
			h.ctx = ast.JSCtxRegexp
		case js.CommentToken, js.CommentLineTerminatorToken:
			h.comment = true
		default:
			return fmt.Errorf("jsx: @{ } at %d in <script> lands inside a %v token; fails closed",
				h.interp.Pos(), tt)
		}
		h.resolved = true
		return nil
	}

	// The placeholder IS its own IdentifierToken: classify by prevSig.
	if isValuePosition(prevSig) {
		h.ctx = ast.JSCtxValue
		h.resolved = true
		return nil
	}
	return fmt.Errorf("jsx: @{ } at %d here is not a safe JavaScript value position "+
		"(it looks like an identifier/binding); wrap the value where it is used as a value, or use a data attribute",
		h.interp.Pos())
}

// interpLiteral reconstructs the @{ … } source of an Interp, mirroring the
// printer's styleChildren interp branch (internal/printer/printer.go:244-254).
// Used to un-split a comment-context hole back to inert literal Text. The
// reconstructed whitespace need not match the original — it is inert inside a JS
// comment.
func interpLiteral(in *ast.Interp) string {
	var b strings.Builder
	b.WriteString("@{ ")
	b.WriteString(in.Expr)
	if in.Try {
		b.WriteByte('?')
	}
	for _, s := range in.Stages {
		b.WriteString(" |> ")
		b.WriteString(s.Name)
		if s.HasArgs {
			b.WriteByte('(')
			b.WriteString(s.Args)
			b.WriteByte(')')
		}
		if s.Try {
			b.WriteByte('?')
		}
	}
	b.WriteString(" }")
	return b.String()
}
