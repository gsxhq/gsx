package ast_test

import (
	goast "go/ast"
	goparser "go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

// This file is CloneFile's exhaustiveness check (P1 review suggestion): every
// concrete type ast.go stamps into one of the four sealed families
// (Decl/Markup/Attr/GoPart — see ast.go's interface docs) must have a working
// clone.go case, or CloneFile silently shares a mutable node across trees
// instead of copying it (clone.go's own doc: an unhandled type panics rather
// than sharing).
//
// The family MEMBERSHIP side is fully automatic and cannot silently drift: it
// is discovered by parsing ast.go's own source for the marker methods
// (markupNode/declNode/attrNode/goPartNode) that make a type a sealed-family
// member in the first place — the exact mechanism ast.go itself uses. A type
// added to ast.go with a new marker method is picked up here with no list to
// update.
//
// The one place this test DOES require upkeep is astZeroValueTypes below: a
// reflect.Type has no runtime "look up by name string" operation, so mapping
// a discovered type name to a constructible reflect.Type needs one explicit
// entry per type (this is the "maintained explicit list, tied to ast.go"
// fallback the P1 review sanctioned). Forgetting an entry fails loudly, naming
// the type — it can drift out of sync with "missing", never silently pass.

// markerFamilies maps each sealed-family marker method (see ast.go's Markup/
// Decl/Attr/GoPart interface definitions) to the family label used in this
// file's failure messages and in astZeroValueTypes lookups.
var markerFamilies = map[string]string{
	"declNode":   "Decl",
	"markupNode": "Markup",
	"attrNode":   "Attr",
	"goPartNode": "GoPart",
}

// familyMember records one concrete type ast.go declares as belonging to a
// sealed family, discovered by scanning for its marker-method receiver.
type familyMember struct {
	typeName string
	pointer  bool // marker method has a pointer receiver (false only for GoText)
}

// discoverFamilyMembers parses ast.go's source and returns, per family label,
// every concrete type ast.go stamps into that family — the live,
// self-updating half of this exhaustiveness check. It intentionally does NOT
// use go/types: a type belongs to a sealed family, in this codebase, exactly
// when it declares the corresponding zero-method marker (see ast.go's
// comment above the interfaces), so a syntactic scan for that marker is both
// sufficient and precise, and needs no `go build`/packages.Load.
func discoverFamilyMembers(t *testing.T) map[string][]familyMember {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed; cannot locate ast.go relative to this test file")
	}
	astGoPath := filepath.Join(filepath.Dir(thisFile), "ast.go")
	fset := token.NewFileSet()
	f, err := goparser.ParseFile(fset, astGoPath, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", astGoPath, err)
	}
	out := map[string][]familyMember{}
	for _, decl := range f.Decls {
		fd, ok := decl.(*goast.FuncDecl)
		if !ok || fd.Recv == nil || len(fd.Recv.List) != 1 {
			continue
		}
		family, isMarker := markerFamilies[fd.Name.Name]
		if !isMarker {
			continue
		}
		recvType := fd.Recv.List[0].Type
		pointer := false
		if star, ok := recvType.(*goast.StarExpr); ok {
			pointer = true
			recvType = star.X
		}
		ident, ok := recvType.(*goast.Ident)
		if !ok {
			t.Fatalf("marker method %s has unexpected receiver type %T; discovery scan needs updating", fd.Name.Name, recvType)
		}
		out[family] = append(out[family], familyMember{typeName: ident.Name, pointer: pointer})
	}
	return out
}

// astZeroValueTypes maps every concrete sealed-family type name ast.go can
// declare to its reflect.Type, so TestCloneHandlesEverySealedType can
// construct a zero value generically instead of one hand-written CloneFile
// call per type. See this file's top-of-file doc for why this map, unlike
// family membership above, must be maintained by hand.
var astZeroValueTypes = map[string]reflect.Type{
	"GoChunk":          reflect.TypeFor[ast.GoChunk](),
	"GoWithElements":   reflect.TypeFor[ast.GoWithElements](),
	"Component":        reflect.TypeFor[ast.Component](),
	"Element":          reflect.TypeFor[ast.Element](),
	"Fragment":         reflect.TypeFor[ast.Fragment](),
	"Text":             reflect.TypeFor[ast.Text](),
	"Doctype":          reflect.TypeFor[ast.Doctype](),
	"HTMLComment":      reflect.TypeFor[ast.HTMLComment](),
	"Comment":          reflect.TypeFor[ast.Comment](),
	"Interp":           reflect.TypeFor[ast.Interp](),
	"EmbeddedInterp":   reflect.TypeFor[ast.EmbeddedInterp](),
	"GoBlock":          reflect.TypeFor[ast.GoBlock](),
	"IfMarkup":         reflect.TypeFor[ast.IfMarkup](),
	"ForMarkup":        reflect.TypeFor[ast.ForMarkup](),
	"SwitchMarkup":     reflect.TypeFor[ast.SwitchMarkup](),
	"StaticAttr":       reflect.TypeFor[ast.StaticAttr](),
	"ExprAttr":         reflect.TypeFor[ast.ExprAttr](),
	"BoolAttr":         reflect.TypeFor[ast.BoolAttr](),
	"SpreadAttr":       reflect.TypeFor[ast.SpreadAttr](),
	"MarkupAttr":       reflect.TypeFor[ast.MarkupAttr](),
	"EmbeddedAttr":     reflect.TypeFor[ast.EmbeddedAttr](),
	"CondAttr":         reflect.TypeFor[ast.CondAttr](),
	"ClassAttr":        reflect.TypeFor[ast.ClassAttr](),
	"OrderedAttrsAttr": reflect.TypeFor[ast.OrderedAttrsAttr](),
	"CommentAttr":      reflect.TypeFor[ast.CommentAttr](),
	"GoText":           reflect.TypeFor[ast.GoText](),
}

// TestCloneHandlesEverySealedType discovers every concrete type ast.go stamps
// into the Decl/Markup/Attr/GoPart sealed families, constructs a zero value of
// each, and drives it through CloneFile in the syntactic position its family
// occupies. clone.go's switches panic on an unhandled concrete type; a family
// member missing a clone.go case therefore fails this test with a panic
// naming the type, instead of surfacing only via a corpus golden or (worse) a
// silent shared-mutable-node bug in production.
func TestCloneHandlesEverySealedType(t *testing.T) {
	families := discoverFamilyMembers(t)
	for _, label := range []string{"Decl", "Markup", "Attr", "GoPart"} {
		members := families[label]
		if len(members) == 0 {
			t.Fatalf("discovered no %s-family members in ast.go; the marker-method scan is broken", label)
		}
		for _, m := range members {
			t.Run(label+"/"+m.typeName, func(t *testing.T) {
				zt, ok := astZeroValueTypes[m.typeName]
				if !ok {
					t.Fatalf("ast.go declares %s as a %s-family member (via its marker method) with no entry in astZeroValueTypes in this file; add one", m.typeName, label)
				}
				var node any
				if m.pointer {
					node = reflect.New(zt).Interface()
				} else {
					node = reflect.New(zt).Elem().Interface()
				}
				file := wrapForFamily(t, label, node)
				clone := mustCloneOK(t, file)
				assertClonedDistinct(t, label, file, clone, m.pointer)
			})
		}
	}
}

// wrapForFamily builds the minimal *ast.File that exercises node in the
// syntactic position its family occupies: a Decl at the top level, a Markup
// inside a Component's body, an Attr inside an Element's attribute list
// (itself inside a Component's body), and a GoPart inside a GoWithElements'
// part list.
func wrapForFamily(t *testing.T, label string, node any) *ast.File {
	t.Helper()
	switch label {
	case "Decl":
		d, ok := node.(ast.Decl)
		if !ok {
			t.Fatalf("%T does not implement ast.Decl despite declaring declNode()", node)
		}
		return &ast.File{Decls: []ast.Decl{d}}
	case "Markup":
		m, ok := node.(ast.Markup)
		if !ok {
			t.Fatalf("%T does not implement ast.Markup despite declaring markupNode()", node)
		}
		return &ast.File{Decls: []ast.Decl{&ast.Component{Body: []ast.Markup{m}}}}
	case "Attr":
		a, ok := node.(ast.Attr)
		if !ok {
			t.Fatalf("%T does not implement ast.Attr despite declaring attrNode()", node)
		}
		return &ast.File{Decls: []ast.Decl{&ast.Component{Body: []ast.Markup{&ast.Element{Attrs: []ast.Attr{a}}}}}}
	case "GoPart":
		p, ok := node.(ast.GoPart)
		if !ok {
			t.Fatalf("%T does not implement ast.GoPart despite declaring goPartNode()", node)
		}
		return &ast.File{Decls: []ast.Decl{&ast.GoWithElements{Parts: []ast.GoPart{p}}}}
	default:
		t.Fatalf("unknown family label %q", label)
		return nil
	}
}

// mustCloneOK runs ast.CloneFile(f), converting a panic (an unhandled
// concrete type in one of clone.go's switches) into a t.Fatalf naming it,
// instead of aborting the whole test binary.
func mustCloneOK(t *testing.T, f *ast.File) *ast.File {
	t.Helper()
	var clone *ast.File
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("CloneFile panicked (a sealed-family type in ast.go has no clone.go case): %v", r)
			}
		}()
		clone = ast.CloneFile(f)
	}()
	return clone
}

// assertClonedDistinct extracts the cloned node back out of clone at the same
// family position and, for pointer-kind nodes, confirms it is a different
// pointer than the original — catching a clone.go case that returns v as-is
// instead of a fresh copy (a shared-mutable-node bug, distinct from the
// missing-case panic mustCloneOK catches). Value-kind nodes (GoText) are
// legitimately shared by value; see clone.go's doc on that case.
func assertClonedDistinct(t *testing.T, label string, orig, clone *ast.File, pointer bool) {
	t.Helper()
	if !pointer {
		return
	}
	var origNode, cloneNode any
	switch label {
	case "Decl":
		origNode, cloneNode = orig.Decls[0], clone.Decls[0]
	case "Markup":
		origNode = orig.Decls[0].(*ast.Component).Body[0]
		cloneNode = clone.Decls[0].(*ast.Component).Body[0]
	case "Attr":
		origNode = orig.Decls[0].(*ast.Component).Body[0].(*ast.Element).Attrs[0]
		cloneNode = clone.Decls[0].(*ast.Component).Body[0].(*ast.Element).Attrs[0]
	case "GoPart":
		origNode = orig.Decls[0].(*ast.GoWithElements).Parts[0]
		cloneNode = clone.Decls[0].(*ast.GoWithElements).Parts[0]
	}
	ov := reflect.ValueOf(origNode)
	cv := reflect.ValueOf(cloneNode)
	if ov.Kind() != reflect.Ptr || cv.Kind() != reflect.Ptr {
		t.Fatalf("expected pointer-kind original/clone for %s, got %s/%s", label, ov.Kind(), cv.Kind())
	}
	if ov.Pointer() == cv.Pointer() {
		t.Fatalf("clone shares the original %s pointer; clone.go's case must allocate a fresh copy", label)
	}
}
