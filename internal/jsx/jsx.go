// Package jsx is gsx's codegen-time JavaScript context engine for <script>
// interpolation (Slice C1). ResolveScripts walks the AST and, for every @{ … }
// hole inside a <script> element, classifies the JavaScript context it sits in
// (value / string / template / regex / comment / binding) so codegen can later
// escape each hole correctly. Misclassification is an XSS, so it fails closed:
// an unclassifiable hole, lex error, or sentinel collision returns an error
// rather than a guess. A binding/lvalue position is classified JSCtxBinding and
// deferred to emit, which admits it only for a gsx.RawJS-typed hole (the type
// isn't known here); everything else there errors at emit.
//
// It is a codegen-time package and MAY import tdewolff; it MUST NOT be imported
// by the root gsx runtime package.
package jsx

import (
	"fmt"
	"strings"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
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
// Text, or recording a positioned fail-closed diagnostic in bag. Returns true if
// clean (no errors recorded), false if any diagnostic was added. <style> and
// non-script interps are left untouched (JSCtxNone).
func ResolveScripts(f *ast.File, bag *diag.Bag) bool {
	ok := true
	for _, d := range f.Decls {
		switch v := d.(type) {
		case *ast.Component:
			if !resolveMarkup(v.Body, bag) {
				ok = false
			}
		case *ast.GoWithElements:
			// A top-level Go region carrying embedded elements/literals in
			// expression position (e.g. `var h = js`save(@{id})``). Classify the
			// @{ } holes of every js`…` literal part so codegen can escape each by
			// its JS context, exactly as an explicit JS attribute literal does.
			if !resolveGoParts(v.Parts, bag) {
				ok = false
			}
		}
	}
	return ok
}

// resolveGoParts classifies the @{ } holes of every js`…` EmbeddedInterp part in
// a Go-expression region (a GoWithElements). Element/Fragment parts recurse into
// their markup so a js`…` literal embedded deeper (e.g. inside an element in the
// region) is also reached. css`…` needs no classification (single context).
// Returns true if clean, false if any diagnostic was added to bag.
func resolveGoParts(parts []ast.GoPart, bag *diag.Bag) bool {
	ok := true
	for _, p := range parts {
		switch v := p.(type) {
		case *ast.EmbeddedInterp:
			if v.Lang == ast.EmbeddedJS {
				if !ResolveEmbedded(v.Segments, bag) {
					ok = false
				}
			}
			if !resolveMarkup(v.Segments, bag) {
				ok = false
			}
		case *ast.Element:
			if !resolveMarkup([]ast.Markup{v}, bag) {
				ok = false
			}
		case *ast.Fragment:
			if !resolveMarkup([]ast.Markup{v}, bag) {
				ok = false
			}
		}
	}
	return ok
}

// ResolveScriptsErr is the backward-compatible error-returning wrapper for tests
// and callers that do not yet hold a *diag.Bag.
func ResolveScriptsErr(f *ast.File) error {
	bag := diag.NewBag(nil)
	if ResolveScripts(f, bag) {
		return nil
	}
	if diags := bag.Sorted(); len(diags) > 0 {
		return fmt.Errorf("%s", diags[0].Message)
	}
	return fmt.Errorf("jsx: ResolveScripts: unclassifiable error")
}

// resolveMarkup mirrors internal/jsmin/file.go's minifyMarkup walk: recurse
// through Element/Fragment/IfMarkup/ForMarkup/SwitchMarkup, but stop at <script>
// elements (resolve them, don't recurse into their raw-text children).
// Returns true if clean, false if any diagnostic was added to bag.
func resolveMarkup(nodes []ast.Markup, bag *diag.Bag) bool {
	ok := true
	for _, n := range nodes {
		switch v := n.(type) {
		case *ast.Element:
			// Resolve explicit JS attribute literals on EVERY element, including
			// conditional attr branches and <script> tags (they can carry
			// <script onload={ if ok { js`...` } }>). This must run before the
			// holes are type-probed in codegen.
			if !resolveAttrList(v.Attrs, bag) {
				ok = false
			}
			if strings.EqualFold(v.Tag, "script") {
				if !resolveScript(v, bag) {
					ok = false
					continue
				}
				// resolveScript classifies the script's own holes. A hole's Go
				// expression can itself contain a materialized js literal; recurse
				// through those surviving Interp.Embedded payloads without
				// reclassifying the outer script context.
				if !resolveMarkup(v.Children, bag) {
					ok = false
				}
				continue
			}
			if !resolveMarkup(v.Children, bag) {
				ok = false
			}
		case *ast.Fragment:
			if !resolveMarkup(v.Children, bag) {
				ok = false
			}
		case *ast.IfMarkup:
			if !resolveMarkup(v.Then, bag) {
				ok = false
			}
			if !resolveMarkup(v.Else, bag) {
				ok = false
			}
		case *ast.ForMarkup:
			if !resolveMarkup(v.Body, bag) {
				ok = false
			}
		case *ast.SwitchMarkup:
			for i := range v.Cases {
				if !resolveMarkup(v.Cases[i].Body, bag) {
					ok = false
				}
			}
		case *ast.Interp:
			if !resolveGoParts(v.Embedded, bag) {
				ok = false
			}
		case *ast.EmbeddedInterp:
			if v.Lang == ast.EmbeddedJS {
				if !ResolveEmbedded(v.Segments, bag) {
					ok = false
				}
			}
			if !resolveMarkup(v.Segments, bag) {
				ok = false
			}
		case *ast.GoBlock:
			if v.UnsupportedMarkup == nil && !resolveGoParts(v.Embedded, bag) {
				ok = false
			}
		}
	}
	return ok
}

func resolveAttrList(attrs []ast.Attr, bag *diag.Bag) bool {
	ok := true
	for _, a := range attrs {
		switch at := a.(type) {
		case *ast.MarkupAttr:
			if !resolveMarkup(at.Value, bag) {
				ok = false
			}
		case *ast.EmbeddedAttr:
			if at.Lang == ast.EmbeddedJS {
				if !resolveJSAttr(at.Name, at.Segments, bag) {
					ok = false
				}
			}
			if !resolveMarkup(at.Segments, bag) {
				ok = false
			}
		case *ast.ClassAttr:
			for i := range at.Parts {
				if at.Parts[i].CSSSegments != nil && !resolveMarkup(at.Parts[i].CSSSegments, bag) {
					ok = false
				}
			}
		case *ast.CondAttr:
			if !resolveAttrList(at.Then, bag) {
				ok = false
			}
			if !resolveAttrList(at.Else, bag) {
				ok = false
			}
		}
	}
	return ok
}

// hole records one @{ … } interpolation in the lexed skeleton.
type hole struct {
	interp   *ast.Interp
	childIdx int // index of the Interp within el.Children
	start    int // byte offset of the placeholder in the skeleton
	end      int // start + len(placeholder)
	ctx      ast.JSCtx
	comment  bool // hole fell inside a JS comment → un-split to literal
	resolved bool // a token covered this hole
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
// Returns true if clean, false if any diagnostic was added to bag.
func resolveDataIsland(el *ast.Element, bag *diag.Bag) bool {
	var theInterp *ast.Interp
	for _, c := range el.Children {
		switch v := c.(type) {
		case *ast.Text:
			if strings.TrimSpace(v.Value) != "" {
				bag.Report(el.Pos(), el.End(), diag.Error, "jsx-data-island", "jsx",
					"jsx: a data <script> (type=%q) must contain exactly one @{ } value; found literal text %q",
					scriptType(el), strings.TrimSpace(v.Value))
				return false
			}
		case *ast.Interp:
			if theInterp != nil {
				bag.Report(el.Pos(), el.End(), diag.Error, "jsx-data-island", "jsx",
					"jsx: a data <script> must contain exactly one @{ } value; found more than one")
				return false
			}
			theInterp = v
		default:
			bag.Report(el.Pos(), el.End(), diag.Error, "jsx-data-island", "jsx",
				"jsx: unexpected %T in data <script> body", c)
			return false
		}
	}
	if theInterp == nil {
		return true // holeless data block (static JSON) — nothing to interpolate.
	}
	theInterp.JSCtx = ast.JSCtxValue
	return true
}

// resolveScript classifies every Interp child of a <script> element.
// Returns true if clean, false if any diagnostic was added to bag.
func resolveScript(el *ast.Element, bag *diag.Bag) bool {
	// A data-block <script> (e.g. type="application/json") is not JavaScript:
	// its body must be exactly one @{ } value emitted as JSON. This branch runs
	// FIRST so a holeless data block also takes the data path (and is never
	// JS-classified); resolveDataIsland returns true for a holeless body.
	if isDataIslandScript(el) {
		return resolveDataIsland(el, bag)
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
		return true
	}

	// Placeholder-collision guard (CVE-grade, fail-closed): if any literal Text
	// already contains the sentinel stem, we cannot safely place sentinels.
	for _, c := range el.Children {
		if t, ok := c.(*ast.Text); ok && strings.Contains(t.Value, holePrefix) {
			bag.Report(el.Pos(), el.End(), diag.Error, "jsx-sentinel-collision", "jsx",
				"jsx: <script> source contains the reserved sentinel %q; cannot classify @{ } holes safely",
				holePrefix)
			return false
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
			bag.Report(el.Pos(), el.End(), diag.Error, "jsx-unexpected-node", "jsx",
				"jsx: unexpected non-text/interp node %T in script body", c)
			return false
		}
	}

	if !classify(sb.String(), holes, bag) {
		return false
	}

	// Apply: set JSCtx, or rewrite comment holes to literal Text in place.
	ok := true
	for _, h := range holes {
		if !h.resolved {
			bag.Report(h.interp.Pos(), h.interp.End(), diag.Error, "jsx-unresolved", "jsx",
				"jsx: @{ } in <script> could not be classified (lex error or unreachable position); fails closed")
			ok = false
			continue
		}
		if h.comment {
			lit := interpLiteral(h.interp)
			if strings.Contains(strings.ToLower(lit), "</script") {
				bag.Report(h.interp.Pos(), h.interp.End(), diag.Error, "jsx-script-close", "jsx",
					"jsx: @{ } inside a <script> comment contains \"</script\", which would close the script element; remove it or move the value out of the comment")
				ok = false
				continue
			}
			el.Children[h.childIdx] = &ast.Text{Value: lit}
			continue
		}
		h.interp.JSCtx = h.ctx
	}
	return ok
}

// ResolveJSAttr is the public entry point for callers outside the JSX engine
// (e.g. unit tests) that do not yet hold a *diag.Bag. It creates a temporary
// bag, delegates to resolveJSAttr, and converts any recorded diagnostic back
// into an error for backward compatibility.
func ResolveJSAttr(name string, segments []ast.Markup) error {
	bag := diag.NewBag(nil)
	if resolveJSAttr(name, segments, bag) {
		return nil
	}
	if diags := bag.Sorted(); len(diags) > 0 {
		return fmt.Errorf("%s", diags[0].Message)
	}
	return fmt.Errorf("jsx: attribute %q: unclassifiable @{ } hole", name)
}

// resolveJSAttr classifies every @{ … } hole in an explicit JS attribute literal
// (e.g. x-data=js`{tab:@{tab}}`). It builds the same _GSXJSHOLE_ skeleton as
// resolveScript, runs the same classify, and sets each Interp.JSCtx — so codegen
// can later escape each hole by its JS context. An attribute value is a single JS
// expression (not a program), so a hole that lands inside a JS comment is
// degenerate and FAILS CLOSED here (unlike <script>, where comment holes
// un-split): we never mutate the segments before the type-probe. Identifier /
// binding / unclassifiable positions fail closed exactly as in <script>.
// Returns true if clean, false if any diagnostic was added to bag.
func resolveJSAttr(name string, segments []ast.Markup, bag *diag.Bag) bool {
	return resolveEmbeddedJS(fmt.Sprintf("attribute %q", name), segments, bag)
}

// ResolveEmbedded classifies every @{ … } hole in a bare segment list for a
// js`...` literal found in Go-expression position (no attribute name to hang
// diagnostics on — e.g. a future `f(js\`...\`)` call). It shares the exact
// skeleton-builder + classify/classifyHole machinery resolveJSAttr uses,
// including its fail-closed behavior for JS-comment holes (this is a single JS
// expression, not a program, so comment holes are never un-split) and its
// deferral of binding/lvalue positions to JSCtxBinding. Diagnostic wording uses
// a neutral "js literal" label instead of an attribute name.
// Sets Interp.JSCtx on every *ast.Interp segment; safe to call again on an
// already-classified segment list (re-classification just overwrites the same
// values). Returns true if clean, false if any diagnostic was added to bag.
func ResolveEmbedded(segments []ast.Markup, bag *diag.Bag) bool {
	return resolveEmbeddedJS("js literal", segments, bag)
}

// resolveEmbeddedJS is the shared segment-walking core behind resolveJSAttr and
// ResolveEmbedded: it builds the _GSXJSHOLE_ skeleton, runs classify, and
// applies the result (setting Interp.JSCtx, or failing closed on comment /
// unresolved holes). descriptor names the value being classified for
// diagnostic wording only (e.g. `attribute "x-data"` or the neutral
// "js literal") and never affects classification logic.
// Returns true if clean, false if any diagnostic was added to bag.
func resolveEmbeddedJS(descriptor string, segments []ast.Markup, bag *diag.Bag) bool {
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
		return true
	}

	// Placeholder-collision guard (fail-closed): if any literal Text already
	// contains the sentinel stem, we cannot safely place sentinels.
	for _, c := range segments {
		if t, ok := c.(*ast.Text); ok && strings.Contains(t.Value, holePrefix) {
			// Use the first interp's position as a best-effort location.
			var firstInterp *ast.Interp
			for _, seg := range segments {
				if interp, ok2 := seg.(*ast.Interp); ok2 {
					firstInterp = interp
					break
				}
			}
			if firstInterp != nil {
				bag.Report(firstInterp.Pos(), firstInterp.End(), diag.Error, "jsx-sentinel-collision", "jsx",
					"jsx: %s: value contains the reserved sentinel %q; cannot classify @{ } holes safely",
					descriptor, holePrefix)
			}
			return false
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
			// Use first interp's position as best effort.
			var firstInterp *ast.Interp
			for _, seg := range segments {
				if interp, ok2 := seg.(*ast.Interp); ok2 {
					firstInterp = interp
					break
				}
			}
			if firstInterp != nil {
				bag.Report(firstInterp.Pos(), firstInterp.End(), diag.Error, "jsx-unexpected-node", "jsx",
					"jsx: %s: value may contain only text and @{ } interpolations, got %T", descriptor, c)
			}
			return false
		}
	}

	if !classify(sb.String(), holes, bag) {
		return false
	}

	ok := true
	for _, h := range holes {
		if !h.resolved {
			bag.Report(h.interp.Pos(), h.interp.End(), diag.Error, "jsx-unresolved", "jsx",
				"jsx: @{ } in %s could not be classified (lex error or unreachable position); fails closed", descriptor)
			ok = false
			continue
		}
		if h.comment {
			bag.Report(h.interp.Pos(), h.interp.End(), diag.Error, "jsx-attr-comment", "jsx",
				"jsx: @{ } inside a JS comment in %s is not supported; move it out of the comment", descriptor)
			ok = false
			continue
		}
		h.interp.JSCtx = h.ctx
	}
	return ok
}

// classify lexes the skeleton and assigns each hole a context (setting h.ctx /
// h.comment / h.resolved), recording positioned fail-closed diagnostics in bag
// for any hole in an unsafe code position. Returns true if clean.
func classify(skeleton string, holes []*hole, bag *diag.Bag) bool {
	l := js.NewLexer(parse.NewInputString(skeleton))
	pos := 0                 // running byte offset = start of the token about to be processed
	prevSig := js.ErrorToken // previous SIGNIFICANT token (start-of-input)
	ok := true

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
			if !classifyHole(h, tt, start, end, prevSig, bag) {
				ok = false
			}
		}

		if isSignificant(tt) {
			prevSig = tt
		}
	}
	return ok
}

// classifyHole assigns a context to one hole that falls within token (tt) spanning
// [tokStart, tokEnd), given the previous significant token prevSig.
// Returns true if clean, false if a diagnostic was added to bag.
func classifyHole(h *hole, tt js.TokenType, tokStart, tokEnd int, prevSig js.TokenType, bag *diag.Bag) bool {
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
			bag.Report(h.interp.Pos(), h.interp.End(), diag.Error, "jsx-bad-token", "jsx",
				"jsx: @{ } in <script> lands inside a %v token; fails closed", tt)
			return false
		}
		h.resolved = true
		return true
	}

	// The placeholder IS its own IdentifierToken: classify by prevSig.
	if isValuePosition(prevSig) {
		h.ctx = ast.JSCtxValue
		h.resolved = true
		return true
	}
	// Non-value (binding/lvalue) position: whether this is legal depends on the
	// hole's Go type (only gsx.RawJS may splice here), which is unknown at
	// classify time. Defer to emit; mark the context so codegen can adjudicate.
	h.ctx = ast.JSCtxBinding
	h.resolved = true
	return true
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
	for _, s := range in.Stages {
		b.WriteString(" |> ")
		b.WriteString(s.Name)
		if s.HasArgs {
			b.WriteByte('(')
			b.WriteString(s.Args)
			b.WriteByte(')')
		}
	}
	b.WriteString(" }")
	return b.String()
}
