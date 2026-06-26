package lsp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
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

// paramDeclIn parses a gsx component's raw parameter-list source and returns the
// matched parameter's declaration as "name type" (e.g. "comments []store.Comment",
// grouped "b string"), matched to attr by firstUpper(name)==firstUpper(attr). ok
// is false when params is empty, unparseable, or has no matching parameter. Never
// panics.
func paramDeclIn(params, attr string) (string, bool) {
	if strings.TrimSpace(params) == "" {
		return "", false
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", "package p\nfunc _("+params+"){}", 0)
	if err != nil {
		return "", false
	}
	var fn *ast.FuncDecl
	for _, d := range file.Decls {
		if f, ok := d.(*ast.FuncDecl); ok {
			fn = f
			break
		}
	}
	if fn == nil || fn.Type.Params == nil {
		return "", false
	}
	want := firstUpper(attr)
	for _, field := range fn.Type.Params.List {
		for _, name := range field.Names {
			if firstUpper(name.Name) == want {
				return name.Name + " " + types.ExprString(field.Type), true
			}
		}
	}
	return "", false
}

// isComponentTag reports whether tag names a component invocation — a simple
// upper-initial tag (Card) or a dotted qualifier.Name (components.Input).
func isComponentTag(tag string) bool {
	if isSimpleComponentTag(tag) {
		return true
	}
	_, _, ok := splitDottedTag(tag)
	return ok
}

// componentAttrAtOffset finds a cursor on a component-invocation attribute NAME.
// It walks the in-memory gsx AST for an element whose tag is a component (simple
// or dotted) and whose named attr's name span [attr.Pos(), +len(name)) covers off.
// Returns the tag, the attr name, and the attr-name byte start (edited-file offset).
func componentAttrAtOffset(pkg *Package, path string, off int) (tag, attr string, attrStart int, ok bool) {
	f := pkg.Files[path]
	if f == nil || pkg.GSXFset == nil {
		return "", "", 0, false
	}
	gsxast.Inspect(f, func(n gsxast.Node) bool {
		if tag != "" {
			return false
		}
		el, isEl := n.(*gsxast.Element)
		if !isEl || !isComponentTag(el.Tag) {
			return true
		}
		for _, a := range el.Attrs {
			name, named := attrName(a)
			if !named || name == "" {
				continue
			}
			start := pkg.GSXFset.Position(a.Pos()).Offset
			if off >= start && off < start+len(name) {
				tag, attr, attrStart = el.Tag, name, start
				return false
			}
		}
		return true
	})
	return tag, attr, attrStart, tag != ""
}

// componentAttrParamAt resolves a cursor on a component-invocation attribute name
// to that component's matching parameter position (same-package and cross-package).
func componentAttrParamAt(pkg *Package, path string, off int) (token.Position, bool) {
	tag, attr, _, ok := componentAttrAtOffset(pkg, path, off)
	if !ok {
		return token.Position{}, false
	}
	comp, fset, ok := resolveTagComponent(pkg, tag)
	if !ok || !comp.ParamsPos.IsValid() {
		return token.Position{}, false
	}
	rel, ok := paramOffsetIn(comp.Params, attr)
	if !ok {
		return token.Position{}, false
	}
	return fset.Position(comp.ParamsPos + token.Pos(rel)), true
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
// any .gsx file in the package, or nil. A package cannot declare two
// function-components with the same name (a Go redeclaration error), so the
// first match is unambiguous despite pkg.Files being a map; the c.Recv == ""
// filter excludes a same-named method-component.
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
