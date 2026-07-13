package codegen

import (
	"go/token"
	"strings"

	"github.com/gsxhq/gsx/ast"
)

// reserved_bindings.go — the body-scope reservation check for the three
// component-body identifiers (`ctx`, `children`, `attrs`). It is the "best-effort
// wording" half of the reserved-identifiers design: it upgrades the raw Go
// collision error a body-scope binding of a reserved name would otherwise draw
// into a positioned, worded `reserved-identifier` diagnostic. It NEVER gates a
// correct program — a shape it cannot see (or a nested-scope shadow, which is
// legal Go) simply falls through to the Go compiler's own error, the backstop.
//
// Scope is the whole game. A reserved name declared in the render closure's TOP
// scope collides with what the generator binds there (`ctx` closure param,
// `children := _gsxp.Children`, `attrs := _gsxp.Attrs`); a reserved name bound in
// a NESTED scope (a `for`/`if`/`switch` body, a func literal, an inner block) is
// an ordinary Go shadow and must NOT be flagged — flagging it would reject
// correct code, the exact bug class this feature eliminates.
//
// The emitter is the scope oracle (verified against emit.go's genNode):
//   - GoBlock            → emits `t.Code` verbatim (emit.go:1975-1977), no block.
//   - Fragment (`<>…</>`) → emits its children inline (emit.go:1925-1930), no block.
//   - Element (`<div>…`)  → writes the open tag, then emits its children inline
//     (emit.go:1911-1917), no block — an element does NOT open a Go scope.
//   - ForMarkup           → emits `for … {` … `}` (emit.go:1931-1939): a real block.
//   - IfMarkup            → emits `if … {` … `}` (+ `else {`) (emit.go:1940-1958): real blocks.
//   - SwitchMarkup        → emits `switch … { case …: … }` (emit.go:1959-1974): case
//     clauses are their own implicit Go blocks.
//
// Consequence: a GoBlock reached WITHOUT crossing a for/if/switch boundary emits
// into the closure's top scope (body-scope — flag it); a GoBlock nested under one
// of those emits inside that block (nested-scope — legal shadow, do not flag).
// `<script>`/`<style>` children route through genScriptChild/genStyleChild, not
// genNode, so they cannot carry a body GoBlock and are not descended.  Attribute
// markup (MarkupAttr child props, CondAttr branches) and embedded-interp segments
// lower through nested closures/expressions, never the top scope; they are not
// descended either (a false negative there is a nested shadow we would not flag
// anyway — sound).

// checkReservedBodyBindings reports every body-scope binding of a reserved
// component-body identifier (`ctx`/`children`/`attrs`) in c, positioned at the
// binding ident. It walks c.Body, tracking whether the current markup position
// still emits into the render closure's top scope, and reads each top-scope
// GoBlock's top-level bindings via fragmentBindings (which already excludes
// nested-block and func-literal bindings and filters to the three reserved
// names). Clause bindings (`for _, attrs := range …`) are nested by construction
// and are intentionally not reported.
func checkReservedBodyBindings(c *ast.Component) []reservedDecl {
	if c == nil {
		return nil
	}
	var out []reservedDecl
	var walk func(nodes []ast.Markup, topScope bool)
	walk = func(nodes []ast.Markup, topScope bool) {
		for _, n := range nodes {
			switch t := n.(type) {
			case *ast.GoBlock:
				if !topScope || !t.CodePos.IsValid() {
					continue
				}
				for _, b := range fragmentBindings(t.Code, fragStmts) {
					out = append(out, reservedDecl{name: b.name, pos: t.CodePos + token.Pos(b.off)})
				}
			case *ast.Fragment:
				walk(t.Children, topScope)
			case *ast.Element:
				// An element opens no Go scope; its children emit inline at the same
				// scope — EXCEPT <script>/<style>, whose children do not route through
				// genNode and cannot carry a body GoBlock.
				if !strings.EqualFold(t.Tag, "script") && !strings.EqualFold(t.Tag, "style") {
					walk(t.Children, topScope)
				}
			case *ast.ForMarkup:
				walk(t.Body, false)
			case *ast.IfMarkup:
				walk(t.Then, false)
				walk(t.Else, false)
			case *ast.SwitchMarkup:
				for _, cc := range t.Cases {
					walk(cc.Body, false)
				}
			}
		}
	}
	walk(c.Body, true)
	return out
}

// reservedBodyMeaning is the human-readable meaning of a reserved component-body
// identifier, for the `reserved-identifier` diagnostic. The wording is the
// design's canonical body-scope phrasing (spec "The model" / "Reservation
// check"); it differs deliberately from checkReservedParams/checkReservedRecvVar,
// whose legacy param/receiver wording ("explicit attribute forwarding") is pinned
// by existing goldens.
func reservedBodyMeaning(name string) string {
	switch name {
	case "ctx":
		return "the ambient context"
	case "children":
		return "the implicit children slot"
	case "attrs":
		return "the implicit fallthrough bag"
	}
	return "a reserved identifier"
}
