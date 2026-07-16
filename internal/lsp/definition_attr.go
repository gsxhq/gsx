package lsp

import (
	"go/token"
	"go/types"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
)

type componentTargetCursor struct {
	element *gsxast.Element
	fact    ComponentCallFact
	start   int
	length  int
}

// componentTargetAtOffset returns the exact callable selected by codegen for a
// cursor on either spelling of a successfully planned component tag.
func componentTargetAtOffset(pkg *Package, path string, off int) (componentTargetCursor, bool) {
	if pkg == nil || pkg.GSXFset == nil || pkg.Files[path] == nil {
		return componentTargetCursor{}, false
	}
	var found componentTargetCursor
	inspectWithEmbedded(pkg.Files[path], func(n gsxast.Node) bool {
		if found.element != nil {
			return false
		}
		el, ok := n.(*gsxast.Element)
		if !ok {
			return true
		}
		fact, ok := pkg.ComponentCalls[el]
		if !ok || fact.Target == nil {
			return true
		}
		start := pkg.GSXFset.Position(el.TagPos).Offset
		onOpen := off >= start && off < start+len(el.Tag)
		onClose := false
		if el.CloseNamePos.IsValid() {
			closeStart := pkg.GSXFset.Position(el.CloseNamePos).Offset
			onClose = off >= closeStart && off < closeStart+len(el.Tag)
			if onClose {
				start = closeStart
			}
		}
		if onOpen || onClose {
			found = componentTargetCursor{element: el, fact: fact, start: start, length: len(el.Tag)}
			return false
		}
		return true
	})
	return found, found.element != nil
}

func componentTargetObject(fact ComponentCallFact) types.Object {
	if fact.TargetOrigin != nil {
		return fact.TargetOrigin
	}
	return fact.Target
}

func componentTargetDeclAt(pkg *Package, path string, off int) (token.Position, bool) {
	cursor, ok := componentTargetAtOffset(pkg, path, off)
	if !ok || pkg.Fset == nil {
		return token.Position{}, false
	}
	obj := componentTargetObject(cursor.fact)
	if obj == nil || !obj.Pos().IsValid() {
		return token.Position{}, false
	}
	pos := pkg.Fset.Position(obj.Pos())
	if pos.Filename == "" || strings.HasSuffix(pos.Filename, ".x.go") {
		return token.Position{}, false
	}
	return pos, true
}

// componentDeclForTarget finds a GSX declaration by the retained target
// identity's resolved declaration position. It is used only to preserve the
// component-specific hover presentation; target resolution itself is exact.
func componentDeclForTarget(pkg *Package, fact ComponentCallFact) *gsxast.Component {
	if pkg == nil || pkg.Fset == nil || pkg.GSXFset == nil {
		return nil
	}
	obj := componentTargetObject(fact)
	if obj == nil || !obj.Pos().IsValid() {
		return nil
	}
	want := pkg.Fset.Position(obj.Pos())
	for _, file := range pkg.Files {
		for _, decl := range file.Decls {
			comp, ok := decl.(*gsxast.Component)
			if !ok || !comp.NamePos.IsValid() {
				continue
			}
			got := pkg.GSXFset.Position(comp.NamePos)
			if got.Filename == want.Filename && got.Line == want.Line && got.Column == want.Column {
				return comp
			}
		}
	}
	return nil
}

type componentAttrCursor struct {
	element *gsxast.Element
	attr    gsxast.Attr
	name    string
	start   int
	fact    ComponentCallFact
	param   ComponentParamFact
}

// componentAttrAtOffset returns codegen's exact binding for a cursor on a
// component attribute name. Only attributes present in the retained semantic
// fact map are navigable: unmatched HTML-attribute fallthrough deliberately is
// not a reference to the attrs parameter.
func componentAttrAtOffset(pkg *Package, path string, off int) (componentAttrCursor, bool) {
	if pkg == nil || pkg.GSXFset == nil || pkg.Files[path] == nil {
		return componentAttrCursor{}, false
	}
	var found componentAttrCursor
	inspectWithEmbedded(pkg.Files[path], func(n gsxast.Node) bool {
		if found.element != nil {
			return false
		}
		el, ok := n.(*gsxast.Element)
		if !ok {
			return true
		}
		fact, ok := pkg.ComponentCalls[el]
		if !ok {
			return true
		}
		for attr, param := range fact.Params {
			name, named := attrName(attr)
			if !named || name == "" || !attr.Pos().IsValid() {
				continue
			}
			start := pkg.GSXFset.Position(attr.Pos()).Offset
			if off >= start && off < start+len(name) {
				found = componentAttrCursor{
					element: el,
					attr:    attr,
					name:    name,
					start:   start,
					fact:    fact,
					param:   param,
				}
				return false
			}
		}
		return true
	})
	return found, found.element != nil
}

func componentAttrParamAt(pkg *Package, path string, off int) (token.Position, bool) {
	cursor, ok := componentAttrAtOffset(pkg, path, off)
	if !ok || pkg.Fset == nil {
		return token.Position{}, false
	}
	param := cursor.param.Origin
	if param == nil {
		param = cursor.param.Var
	}
	if param == nil || !param.Pos().IsValid() {
		return token.Position{}, false
	}
	pos := pkg.Fset.Position(param.Pos())
	if pos.Filename == "" || strings.HasSuffix(pos.Filename, ".x.go") {
		return token.Position{}, false
	}
	return pos, true
}

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
	case *gsxast.EmbeddedAttr:
		return t.Name, true
	case *gsxast.ClassAttr:
		return t.Name, true
	case *gsxast.OrderedAttrsAttr:
		return t.Name, true
	default:
		return "", false
	}
}

// findComponentDecl supports the retained cross-package source resolver. Exact
// same-package call navigation uses ComponentCalls and does not call this.
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
