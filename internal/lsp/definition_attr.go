package lsp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"unicode"

	gsxast "github.com/gsxhq/gsx/ast"
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

// componentAttrParamAt resolves a cursor on a same-package function-component
// tag's attribute NAME to that component's matching parameter position. It walks
// the in-memory gsx AST (pkg.Files) — never the generated .x.go. Returns false
// when the cursor is not on such an attribute name, the component or a matching
// param can't be found, or the param list is unparseable.
func componentAttrParamAt(pkg *Package, path string, off int) (token.Position, bool) {
	f := pkg.Files[path]
	if f == nil || pkg.GSXFset == nil {
		return token.Position{}, false
	}
	var tag, attr string
	gsxast.Inspect(f, func(n gsxast.Node) bool {
		if tag != "" {
			return false // already found
		}
		el, ok := n.(*gsxast.Element)
		if !ok || !isSimpleComponentTag(el.Tag) {
			return true
		}
		for _, a := range el.Attrs {
			name, ok := attrName(a)
			if !ok || name == "" {
				continue
			}
			start := pkg.GSXFset.Position(a.Pos()).Offset
			if off >= start && off < start+len(name) {
				tag, attr = el.Tag, name
				return false
			}
		}
		return true
	})
	if tag == "" {
		return token.Position{}, false
	}
	comp := findComponentDecl(pkg, tag)
	if comp == nil || !comp.ParamsPos.IsValid() {
		return token.Position{}, false
	}
	rel, ok := paramOffsetIn(comp.Params, attr)
	if !ok {
		return token.Position{}, false
	}
	return pkg.GSXFset.Position(comp.ParamsPos + token.Pos(rel)), true
}

// isSimpleComponentTag reports whether tag is a same-package function-component
// tag (non-empty, undotted, upper-initial) — the inverse of the dotted/lowercase
// exclusion in componentTagDeclAt. Dotted (cross-package) tags are Phase 2.
func isSimpleComponentTag(tag string) bool {
	return tag != "" && !strings.Contains(tag, ".") && tag[0] >= 'A' && tag[0] <= 'Z'
}

// attrName returns the attribute's name and true for the named attr kinds; a
// SpreadAttr (no name) returns ("", false).
func attrName(a gsxast.Attr) (string, bool) {
	switch t := a.(type) {
	case *gsxast.ExprAttr:
		return t.Name, true
	case *gsxast.StaticAttr:
		return t.Name, true
	case *gsxast.BoolAttr:
		return t.Name, true
	case *gsxast.MarkupAttr:
		return t.Name, true
	case *gsxast.JSAttr:
		return t.Name, true
	default:
		return "", false
	}
}

// findComponentDecl returns the function-component (no receiver) named name from
// any .gsx file in the package, or nil.
func findComponentDecl(pkg *Package, name string) *gsxast.Component {
	for _, f := range pkg.Files {
		for _, d := range f.Decls {
			if c, ok := d.(*gsxast.Component); ok && c.Recv == "" && c.Name == name {
				return c
			}
		}
	}
	return nil
}
