package codegen

import (
	goast "go/ast"
	"go/importer"
	goparser "go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportPathDirRoundTrip(t *testing.T) {
	root := "/m"
	mod := "example.com/app"
	// dir under root → import path
	if got, ok := importPathForDir(root, mod, "/m/ui/admin"); !ok || got != "example.com/app/ui/admin" {
		t.Fatalf("importPathForDir = %q,%v; want example.com/app/ui/admin,true", got, ok)
	}
	// module root dir → bare module path
	if got, ok := importPathForDir(root, mod, "/m"); !ok || got != "example.com/app" {
		t.Fatalf("root dir: got %q,%v", got, ok)
	}
	// dir outside the module → not ok
	if _, ok := importPathForDir(root, mod, "/other/x"); ok {
		t.Fatalf("outside dir should be !ok")
	}
	// inverse
	if got, ok := dirForImportPath(root, mod, "example.com/app/ui/admin"); !ok || got != "/m/ui/admin" {
		t.Fatalf("dirForImportPath = %q,%v; want /m/ui/admin,true", got, ok)
	}
	if _, ok := dirForImportPath(root, mod, "fmt"); ok {
		t.Fatalf("stdlib path should be !ok")
	}
}

func TestCheckSkeletonPackageReturnsPkg(t *testing.T) {
	src := "package p\n\nfunc F() int { return 1 }\n"
	fset := token.NewFileSet()
	f, err := goparser.ParseFile(fset, "/m/p/p.go", src, goparser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	pkg, info, errs := checkSkeletonPackage("/m/p", "p", []*goast.File{f}, fset, importer.Default())
	if len(errs) != 0 {
		t.Fatalf("unexpected type errors: %v", errs)
	}
	if pkg == nil || pkg.Scope().Lookup("F") == nil {
		t.Fatalf("pkg missing F")
	}
	if info == nil || info.Defs == nil {
		t.Fatalf("info not populated")
	}
}

func TestModuleSourceOverrideThenDisk(t *testing.T) {
	dir := t.TempDir()
	onDisk := filepath.Join(dir, "a.gsx")
	if err := os.WriteFile(onDisk, []byte("DISK"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Open(Options{ModuleRoot: dir, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	if b, ok := m.source(onDisk); !ok || string(b) != "DISK" {
		t.Fatalf("disk read: %q,%v", b, ok)
	}
	m.SetOverride(onDisk, []byte("BUF"))
	if b, ok := m.source(onDisk); !ok || string(b) != "BUF" {
		t.Fatalf("override read: %q,%v", b, ok)
	}
	// in-memory-only path (no disk file) resolves from override
	mem := filepath.Join(dir, "mem.gsx")
	m.SetOverride(mem, []byte("MEM"))
	if b, ok := m.source(mem); !ok || string(b) != "MEM" {
		t.Fatalf("in-memory read: %q,%v", b, ok)
	}
}

func TestModuleImporterCrossPackageNoXGo(t *testing.T) {
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	compDir := filepath.Join(root, "comp")
	if err := os.MkdirAll(compDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, compDir, "comp.gsx", "package comp\n\ncomponent Button(label string) {\n\t<button>{label}</button>\n}\n")
	pageDir := filepath.Join(root, "page")
	if err := os.MkdirAll(pageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, pageDir, "page.gsx",
		"package page\n\nimport \"example.com/app/comp\"\n\ncomponent Home() {\n\t<div>{ comp.Button(\"hi\") }</div>\n}\n")

	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app", FilterPkgs: []string{StdImportPath}})
	if err != nil {
		t.Fatal(err)
	}
	// NOTE: no .x.go exists anywhere on disk.
	pkg, err := m.typesPackage(filepath.Join(root, "comp"))
	if err != nil {
		t.Fatalf("typesPackage(comp): %v", err)
	}
	if pkg.Scope().Lookup("Button") == nil {
		t.Fatalf("comp package missing Button (skeleton import failed)")
	}
	// page must type-check against comp's in-memory skeleton (the importer payoff)
	pagePkg, err := m.typesPackage(filepath.Join(root, "page"))
	if err != nil {
		t.Fatalf("typesPackage(page): %v", err)
	}
	// Verify the importer actually ran by checking that pagePkg imported comp and
	// that comp's skeleton exposed Button as a function.
	var compFromPage *types.Package
	for _, imp := range pagePkg.Imports() {
		if strings.HasSuffix(imp.Path(), "/comp") || imp.Path() == "example.com/app/comp" {
			compFromPage = imp
			break
		}
	}
	if compFromPage == nil {
		t.Fatalf("page did not import comp: pagePkg.Imports() = %v", pagePkg.Imports())
	}
	buttonObj := compFromPage.Scope().Lookup("Button")
	if _, ok := buttonObj.(*types.Func); !ok {
		t.Fatalf("comp.Button is %T, want *types.Func", buttonObj)
	}
}

func TestModulePackageRetainsAnalysis(t *testing.T) {
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pageDir := filepath.Join(root, "page")
	if err := os.MkdirAll(pageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, pageDir, "page.gsx",
		"package page\n\ncomponent Home(name string) {\n\t<h1>Hi {name}</h1>\n}\n")

	m, _ := Open(Options{ModuleRoot: root, ModulePath: "example.com/app", FilterPkgs: []string{StdImportPath}})
	pr, err := m.Package(pageDir)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Info == nil || pr.Types == nil || pr.ExprMap == nil || pr.GSXFset == nil || pr.Fset == nil {
		t.Fatalf("retained analysis not populated: %+v", pr)
	}
	if _, ok := pr.CrossIndex[".Home"]; !ok {
		t.Fatalf("CrossIndex missing .Home: %v", pr.CrossIndex)
	}
}

// TestModuleImporterRejectsImportCycle proves that the cycle guard in
// moduleImporter.Import returns an error (not an infinite recursion/hang) when
// two gsx packages mutually import each other.
func TestModuleImporterRejectsImportCycle(t *testing.T) {
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, root, "go.mod", "module example.com/cycle\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	aDir := filepath.Join(root, "a")
	if err := os.MkdirAll(aDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bDir := filepath.Join(root, "b")
	if err := os.MkdirAll(bDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, aDir, "a.gsx", "package a\n\nimport \"example.com/cycle/b\"\n\ncomponent A() {\n\t<div>{ b.B() }</div>\n}\n")
	writeFile(t, bDir, "b.gsx", "package b\n\nimport \"example.com/cycle/a\"\n\ncomponent B() {\n\t<div>{ a.A() }</div>\n}\n")

	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/cycle", FilterPkgs: []string{StdImportPath}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.typesPackage(aDir)
	if err == nil {
		t.Fatal("typesPackage(a) expected import cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "import cycle") {
		t.Fatalf("expected 'import cycle' in error, got: %v", err)
	}
}
