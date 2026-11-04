package printer

import "github.com/gsxhq/gsx/ast"

// Embedded JS/CSS literals have separate formatting contracts. Only markup
// operands can contain literal HTML data newlines.
func (p *printer) anchoredGoMarkup(part ast.GoPart) bool {
	switch part.(type) {
	case *ast.Element, *ast.Fragment:
		return p.anchoredNode(part)
	case ast.GoText, *ast.EmbeddedInterp:
		return false
	}
	return false
}
