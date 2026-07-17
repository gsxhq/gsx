package jsmin

import (
	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/fullmin"
	"strings"
)

func jsminFileMinify(f *ast.File, ext func(string) (string, error)) error {
	return MinifyFile(f, Minifiers{JS: ext, JSON: fullmin.JSON})
}
func fullminJS(s string) (string, error) { return fullmin.JS(s) }
func containsNL(s string) bool           { return strings.Contains(s, "\n") }
func has(s, sub string) bool             { return strings.Contains(s, sub) }
