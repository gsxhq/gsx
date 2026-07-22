package lsp

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/sourceintel"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// methodComponentFixture assembles a Package with the two-fset geometry the
// real analyzer produces: an authored gsx AST (GSXFset) that carries the
// *gsxast.Component spans a tag cursor is located against, and an independently
// type-checked skeleton (Fset/Info) whose per-component generated func binds the
// receiver var. SigTypes bridges a component to a skeleton signature-type
// position inside that func's scope — exactly the retained fact
// receiverVarComponentItems consumes. ComponentDecls carries a plain component
// (".Card"), a value-receiver method component ("UsersPage.Row"), and a
// pointer-receiver method component ("Form.Submit") on a second type.
//
// gsxSrc is returned so callers compute cursor offsets by substring into the
// Page component body, where `p` is the UsersPage receiver and `f` a *Form param.
func methodComponentFixture(t *testing.T) (pkg *Package, gsxSrc string) {
	t.Helper()

	// Skeleton: one generated func per component. `_page`'s receiver is the
	// UsersPage receiver var `p`; it also takes a `*Form`-typed param `f`, so a
	// param-typed value binding is exercised alongside the receiver.
	skel := `package page

type UsersPage struct{ Title string }
type Form struct{ Name string }

func (p UsersPage) _row(x string) { _ = p; _ = x }

func (p UsersPage) _page(f *Form) { _ = p; _ = f }

func (f *Form) _submit() { _ = f }
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "page.x.go", skel, 0)
	if err != nil {
		t.Fatalf("parse skeleton: %v", err)
	}
	info := &types.Info{
		Types:  map[ast.Expr]types.TypeAndValue{},
		Defs:   map[*ast.Ident]types.Object{},
		Uses:   map[*ast.Ident]types.Object{},
		Scopes: map[ast.Node]*types.Scope{},
	}
	conf := types.Config{Importer: importer.Default(), Error: func(error) {}}
	tpkg, _ := conf.Check("example.com/app/page", fset, []*ast.File{file}, info)

	// A signature-type expr inside `_page`'s FuncType scope (the receiver type),
	// the seed receiverVarComponentItems uses to reach that scope.
	var pageRecvType ast.Expr
	for _, d := range file.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
			continue
		}
		if fn.Name.Name == "_page" {
			pageRecvType = fn.Recv.List[0].Type
		}
	}
	if pageRecvType == nil {
		t.Fatal("skeleton missing _page receiver type")
	}

	// Authored gsx: the enclosing method component body holds the `<p.` cursor.
	gsxSrc = "package page\n\n" +
		"component (p UsersPage) Row(x string) {\n\t<span>{x}</span>\n}\n\n" +
		"component (p UsersPage) Page(f *Form) {\n\t<div><p.Row x=\"a\"/></div>\n}\n"
	gsxFset := token.NewFileSet()
	gsxFile, err := gsxparser.ParseFile(gsxFset, "page.gsx", []byte(gsxSrc), 0)
	if err != nil {
		t.Fatalf("parse gsx: %v", err)
	}
	var pageComp *gsxast.Component
	for _, d := range gsxFile.Decls {
		if c, ok := d.(*gsxast.Component); ok && c.Name == "Page" {
			pageComp = c
		}
	}
	if pageComp == nil {
		t.Fatal("gsx source missing Page component")
	}

	pkg = &Package{
		Types:   tpkg,
		Info:    info,
		Fset:    fset,
		GSXFset: gsxFset,
		Files:   map[string]*gsxast.File{"page.gsx": gsxFile},
		SigTypes: map[*gsxast.Component][]SigTypeRef{
			pageComp: {{GSXPos: pageComp.RecvPos, SkelTyp: pageRecvType}},
		},
		ComponentDecls: map[ComponentDeclKey][]sourceintel.VersionedSpan{
			{PackagePath: "example.com/app/page", ComponentKey: ".Card"}:          nil,
			{PackagePath: "example.com/app/page", ComponentKey: "UsersPage.Row"}:  nil,
			{PackagePath: "example.com/app/page", ComponentKey: "UsersPage.Grid"}: nil,
			{PackagePath: "example.com/app/page", ComponentKey: "Form.Submit"}:    nil,
		},
	}
	return pkg, gsxSrc
}

// TestReceiverVarTagResolvesMethodComponents pins the primary case: inside a
// method component `Page`, a `<p.▮` cursor where `p` is the receiver var offers
// the method components declared on p's type (UsersPage), as ciKindClass tag
// items at tierContext — never the plain component (".Card") or the other type's
// method (Form.Submit).
func TestReceiverVarTagResolvesMethodComponents(t *testing.T) {
	pkg, src := methodComponentFixture(t)
	off := strings.Index(src, "<p.Row") + len("<p.")
	items := componentTagItems(pkg, "p", false, "page.gsx", off, "", 0, 0, encUTF8)
	got := map[string]int{}
	for _, it := range items {
		got[it.Label] = it.Kind
	}
	if len(got) != 2 {
		t.Fatalf("labels = %v, want exactly [Grid Row]", got)
	}
	for _, name := range []string{"Row", "Grid"} {
		kind, ok := got[name]
		if !ok {
			t.Errorf("item %q missing from `<p.` completion", name)
		} else if kind != ciKindClass {
			t.Errorf("item %q kind = %d, want ciKindClass", name, kind)
		}
	}
	if _, ok := got["Card"]; ok {
		t.Errorf("plain component Card leaked into `<p.` completion")
	}
	if _, ok := got["Submit"]; ok {
		t.Errorf("other type's method Submit leaked into `<p.` completion")
	}
	for _, it := range items {
		if !strings.HasPrefix(it.SortText, "05") {
			t.Errorf("item %q SortText = %q, want tierContext (05) prefix", it.Label, it.SortText)
		}
	}
}

// TestParamVarTagResolvesMethodComponents pins that a PARAMETER binding (the
// `*Form`-typed param `f` of Page) resolves too, dereferencing the pointer to
// key method components on the bare type name (codegen's parseRecv strips `*`).
func TestParamVarTagResolvesMethodComponents(t *testing.T) {
	pkg, src := methodComponentFixture(t)
	off := strings.Index(src, "<p.Row") + len("<p.")
	items := componentTagItems(pkg, "f", false, "page.gsx", off, "", 0, 0, encUTF8)
	if len(items) != 1 || items[0].Label != "Submit" {
		t.Fatalf("items = %+v, want exactly [Submit] for the *Form param `f`", items)
	}
	if items[0].Kind != ciKindClass {
		t.Errorf("Submit kind = %d, want ciKindClass", items[0].Kind)
	}
}

// TestReceiverVarUnknownQualifierEmpty pins that a qualifier resolving to no
// in-scope value binding and no import yields an empty list (not a panic).
func TestReceiverVarUnknownQualifierEmpty(t *testing.T) {
	pkg, src := methodComponentFixture(t)
	off := strings.Index(src, "<p.Row") + len("<p.")
	if items := componentTagItems(pkg, "zzz", false, "page.gsx", off, "", 0, 0, encUTF8); len(items) != 0 {
		t.Fatalf("items = %+v, want empty for an unresolvable qualifier", items)
	}
}

// TestReceiverVarShadowsImport pins the Go-scoping precedence: when a qualifier
// names BOTH an in-scope value binding and an import, the binding wins — the
// import's components are never offered. Here `p` (the receiver var) shadows a
// same-named imported package that declares a `.Button` component; `<p.▮` must
// offer UsersPage's methods, not Button.
func TestReceiverVarShadowsImport(t *testing.T) {
	pkg, src := methodComponentFixture(t)
	// Attach an import named "p" that declares a component, as importQualifierCandidates would surface it.
	imp := types.NewPackage("example.com/app/pkgp", "p")
	imp.MarkComplete()
	pkg.Types.SetImports([]*types.Package{imp})
	pkg.ComponentDecls[ComponentDeclKey{PackagePath: "example.com/app/pkgp", ComponentKey: ".Button"}] = nil

	off := strings.Index(src, "<p.Row") + len("<p.")
	items := componentTagItems(pkg, "p", false, "page.gsx", off, "", 0, 0, encUTF8)
	for _, it := range items {
		if it.Label == "Button" {
			t.Fatalf("import component Button leaked past the shadowing receiver var: %+v", items)
		}
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v, want the receiver var's methods [Grid Row]", items)
	}
}

// TestReceiverVarBindingWithoutMethodsStopsFallback pins that once a qualifier
// resolves to a value binding whose type declares NO method components, the
// import fallback still does NOT run (the binding shadows the import): an empty
// list, not the import's components. `p`'s type here is stripped of method
// components, yet the same-named import's Button must stay hidden.
func TestReceiverVarBindingWithoutMethodsStopsFallback(t *testing.T) {
	pkg, src := methodComponentFixture(t)
	delete(pkg.ComponentDecls, ComponentDeclKey{PackagePath: "example.com/app/page", ComponentKey: "UsersPage.Row"})
	delete(pkg.ComponentDecls, ComponentDeclKey{PackagePath: "example.com/app/page", ComponentKey: "UsersPage.Grid"})
	imp := types.NewPackage("example.com/app/pkgp", "p")
	imp.MarkComplete()
	pkg.Types.SetImports([]*types.Package{imp})
	pkg.ComponentDecls[ComponentDeclKey{PackagePath: "example.com/app/pkgp", ComponentKey: ".Button"}] = nil

	off := strings.Index(src, "<p.Row") + len("<p.")
	if items := componentTagItems(pkg, "p", false, "page.gsx", off, "", 0, 0, encUTF8); len(items) != 0 {
		t.Fatalf("items = %+v, want empty (receiver var shadows import even with no methods)", items)
	}
}

// TestImportQualifierStillWinsWhenNotShadowed pins the fallback: a qualifier
// that is NOT an in-scope value binding still resolves through the import path.
func TestImportQualifierStillWinsWhenNotShadowed(t *testing.T) {
	src := `package p

import myui "strings"

var _ = myui.ToUpper
`
	pkg, _ := buildSyntheticPackage(t, src)
	pkg.ComponentDecls = map[ComponentDeclKey][]sourceintel.VersionedSpan{
		{PackagePath: "strings", ComponentKey: ".Button"}: nil,
	}
	// No Files/GSXFset/SigTypes: enclosingComponentAt finds nothing, the package
	// scope has no `myui` var, so resolution declines and the import path runs.
	items := componentTagItems(pkg, "myui", false, "page.gsx", 10, "", 0, 0, encUTF8)
	if len(items) != 1 || items[0].Label != "Button" {
		t.Fatalf("items = %+v, want [Button] via import fallback", items)
	}
}
