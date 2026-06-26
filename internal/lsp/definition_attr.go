package lsp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"unicode"
)

// firstUpper returns s with its first rune upper-cased (the gsx exported-field
// rule: attr name `title` ↔ field/param `Title`). "" stays "".
func firstUpper(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// paramOffsetIn parses a gsx component's raw parameter-list source (e.g.
// "comments []store.Comment" or grouped "a, b string") with go/parser and
// returns the byte offset, WITHIN params, of the name of the parameter matching
// attr under the default exported-field rule firstUpper(name)==firstUpper(attr).
// ok is false when params is empty, unparseable, or has no matching parameter —
// the caller falls through to a null definition. It never panics.
func paramOffsetIn(params, attr string) (int, bool) {
	if strings.TrimSpace(params) == "" {
		return 0, false
	}
	const prefix = "package p\nfunc _("
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", prefix+params+"){}", 0)
	if err != nil {
		return 0, false
	}
	var fn *ast.FuncDecl
	for _, d := range file.Decls {
		if f, ok := d.(*ast.FuncDecl); ok {
			fn = f
			break
		}
	}
	if fn == nil || fn.Type.Params == nil {
		return 0, false
	}
	// params starts immediately after the prefix, so a name's offset within
	// params is its offset in the synthetic source minus len(prefix).
	want := firstUpper(attr)
	for _, field := range fn.Type.Params.List {
		for _, name := range field.Names {
			if firstUpper(name.Name) == want {
				return fset.Position(name.Pos()).Offset - len(prefix), true
			}
		}
	}
	return 0, false
}
