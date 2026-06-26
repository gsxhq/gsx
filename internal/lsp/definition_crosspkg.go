package lsp

import (
	"go/token"
	"go/types"
	"path/filepath"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// splitDottedTag splits a dotted component tag "qualifier.Name" into its parts,
// requiring a single dot and an upper-initial Name (a component, not a field
// access). "components.Input" → ("components","Input",true);
// "p.Content" → ("p","Content",true) — the qualifier just won't match an import.
func splitDottedTag(tag string) (qualifier, name string, ok bool) {
	i := strings.LastIndex(tag, ".")
	if i <= 0 || i == len(tag)-1 {
		return "", "", false
	}
	qualifier, name = tag[:i], tag[i+1:]
	if strings.Contains(qualifier, ".") || name[0] < 'A' || name[0] > 'Z' {
		return "", "", false
	}
	return qualifier, name, true
}

// resolveCrossPkgComponent resolves a dotted tag's (qualifier, name) to the
// function-component declaration in the imported package's .gsx. It finds the
// imported types.Package by name, locates the dependency DIRECTORY from the
// component object's position filename (the only use of the dep's compiled
// form), then parses the dependency's .gsx files IN MEMORY to return the decl
// node and the FileSet its positions belong to. Returns false on any miss.
//
// Ambiguous-qualifier safety: if more than one imported package has the same
// declared name as qualifier (e.g. two distinct imports both named "components"),
// the function returns (nil, nil, false) rather than picking the wrong one —
// preserving the "never a wrong jump" invariant.
func resolveCrossPkgComponent(pkg *Package, qualifier, name string) (*gsxast.Component, *token.FileSet, bool) {
	if pkg == nil || pkg.Types == nil || pkg.Fset == nil {
		return nil, nil, false
	}
	var imp *types.Package
	for _, p := range pkg.Types.Imports() {
		if p.Name() == qualifier {
			if imp != nil {
				// Ambiguous: two imports share the same declared name; bail rather
				// than risk a wrong jump.
				return nil, nil, false
			}
			imp = p
		}
	}
	if imp == nil {
		return nil, nil, false
	}
	obj := imp.Scope().Lookup(name)
	if obj == nil || !obj.Pos().IsValid() {
		return nil, nil, false
	}
	depFile := pkg.Fset.Position(obj.Pos()).Filename
	if depFile == "" {
		return nil, nil, false
	}
	dir := filepath.Dir(depFile)
	matches, err := filepath.Glob(filepath.Join(dir, "*.gsx"))
	if err != nil {
		return nil, nil, false
	}
	fset := token.NewFileSet()
	for _, m := range matches {
		f, err := gsxparser.ParseFile(fset, m, nil, 0)
		if err != nil {
			continue
		}
		for _, d := range f.Decls {
			if c, ok := d.(*gsxast.Component); ok && c.Recv == "" && c.Name == name {
				return c, fset, true
			}
		}
	}
	return nil, nil, false
}

// crossPkgTagDeclAt resolves a cursor on a dotted component tag NAME to that
// component's .gsx declaration in the imported package. Returns false when the
// cursor is not on such a tag or the component can't be resolved.
func crossPkgTagDeclAt(pkg *Package, path string, off int) (token.Position, bool) {
	if pkg == nil || pkg.GSXFset == nil || pkg.Files == nil {
		return token.Position{}, false
	}
	f := pkg.Files[path]
	if f == nil {
		return token.Position{}, false
	}
	var result token.Position
	found := false
	gsxast.Inspect(f, func(n gsxast.Node) bool {
		if found {
			return false
		}
		el, ok := n.(*gsxast.Element)
		if !ok || !strings.Contains(el.Tag, ".") {
			return true
		}
		nameStart := pkg.GSXFset.Position(el.Pos()).Offset + 1 // skip '<'
		if off < nameStart || off >= nameStart+len(el.Tag) {
			return true
		}
		qualifier, name, ok := splitDottedTag(el.Tag)
		if !ok {
			return true
		}
		comp, fset, ok := resolveCrossPkgComponent(pkg, qualifier, name)
		if !ok || !comp.NamePos.IsValid() {
			return true
		}
		result = fset.Position(comp.NamePos)
		found = true
		return false
	})
	return result, found
}
