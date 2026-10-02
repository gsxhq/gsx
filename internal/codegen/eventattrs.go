package codegen

import (
	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/htmlattr"
)

// rejectTextLiteralHandlers reports an error for each f`…` or css`…` literal
// on an event-handler attribute (onclick, …) of the native element el, in any
// if/switch branch. Its value is JavaScript, but those literals escape their
// holes for HTML text or CSS, so a hole could run as code; a js`…` literal
// escapes each hole for its JavaScript position. Emission continues so every
// offending attribute is reported; the error fails the generation.
func rejectTextLiteralHandlers(bag *diag.Bag, el *ast.Element) {
	var walk func(attrs []ast.Attr)
	walk = func(attrs []ast.Attr) {
		for _, a := range attrs {
			if branches := attrGroupBranches(a); branches != nil {
				for _, branch := range branches {
					walk(branch)
				}
				continue
			}
			lit, isLit := a.(*ast.EmbeddedAttr)
			if !isLit || lit.Lang == ast.EmbeddedJS || !htmlattr.IsEventHandler(lit.Name) {
				continue
			}
			bag.Errorf(lit.Pos(), lit.End(), "text-literal-event-handler",
				"%s is a JavaScript event handler; use js`…` instead of %s`…`, which would not escape its holes for JavaScript", lit.Name, embeddedLangName(lit.Lang))
		}
	}
	walk(el.Attrs)
}
