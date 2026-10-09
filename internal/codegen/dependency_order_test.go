package codegen

import (
	"go/types"
	"path/filepath"
	"slices"
	"testing"
)

// TestCachedPackageKeptAcrossEntryPoints pins #242 through the Module entry
// points in the order DependencyOrder avoids: holder imports decl (caching it)
// before decl is analyzed itself. The cached decl must be kept, so holder stays
// cached and user sees one decl.T through holder and directly.
func TestCachedPackageKeptAcrossEntryPoints(t *testing.T) {
	t.Parallel()
	tmp := tempModule(t, "gsxkeep")
	dirHolder := makeSubPkg(t, tmp, "holder",
		"package holder\n\nimport \"gsxkeep/decl\"\n\ntype Props struct{ T decl.T }\n\ncomponent Card(p Props) {\n\t<div>{ p.T.N }</div>\n}\n",
	)
	dirDecl := makeSubPkg(t, tmp, "decl",
		"package decl\n\ntype T struct{ N int }\n\ncomponent Show(t T) {\n\t<span>{ t.N }</span>\n}\n",
	)
	dirUser := makeSubPkg(t, tmp, "user",
		"package user\n\nimport (\n\t\"gsxkeep/decl\"\n\t\"gsxkeep/holder\"\n)\n\ncomponent Page() {\n\t<holder.Card p={ holder.Props{T: decl.T{N: 1}} } />\n}\n",
	)
	m, err := Open(Options{ModuleRoot: tmp, ModulePath: "gsxkeep"})
	if err != nil {
		t.Fatal(err)
	}
	cached := func() (decl, holder *types.Package) {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.pkgTypes[dirDecl], m.pkgTypes[dirHolder]
	}

	if _, err := m.Package(dirHolder); err != nil {
		t.Fatal(err)
	}
	decl, holder := cached()
	if decl == nil || holder == nil {
		t.Fatal("Package(holder) did not cache holder and decl; fixture broken")
	}
	if _, err := m.Package(dirDecl); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Generate(dirDecl); err != nil {
		t.Fatal(err)
	}
	if gotDecl, gotHolder := cached(); gotDecl != decl || gotHolder != holder {
		t.Fatal("analyzing decl replaced its cached package or dropped holder; importers must stay bound to the cached decl")
	}
	res, err := m.Package(dirUser)
	if err != nil {
		t.Fatal(err)
	}
	if hasDiagErrors(res.Diags) {
		t.Fatalf("Package(user): %v", res.Diags)
	}
	if _, diags, err := m.Generate(dirUser); err != nil || hasDiagErrors(diags) {
		t.Fatalf("Generate(user): err=%v diags=%v", err, diags)
	}
}

// TestDependencyOrder pins that every dir follows the dirs it imports — through
// .gsx imports, companion .go imports, and Go-only intermediaries outside the
// input — that ties keep input order, that input spellings are returned as
// given, and that an import cycle terminates.
func TestDependencyOrder(t *testing.T) {
	t.Parallel()
	tmp := tempModule(t, "gsxorder")
	comp := func(pkg, imports, body string) string {
		return "package " + pkg + "\n\n" + imports + "component C() {\n\t" + body + "\n}\n"
	}
	// Each edge kind has its own dependency, listed after its importer:
	// a -> za via a .gsx import, b -> zb via the Go-only shim, c -> zc via its
	// companion .go file. d imports a and b; x and y import each other.
	dirZA := makeSubPkg(t, tmp, "za", comp("za", "", "<p>za</p>"))
	dirZB := makeSubPkg(t, tmp, "zb", comp("zb", "", "<p>zb</p>"))
	dirZC := makeSubPkg(t, tmp, "zc", comp("zc", "", "<p>zc</p>"))
	dirA := makeSubPkg(t, tmp, "a", comp("a", "import \"gsxorder/za\"\n\n", "<za.C />"))
	writeFile(t, filepath.Join(tmp, "shim"), "shim.go", "package shim\n\nimport \"gsxorder/zb\"\n\nvar C = zb.C\n")
	dirB := makeSubPkg(t, tmp, "b", comp("b", "import \"gsxorder/shim\"\n\n", "<shim.C />"))
	dirC := makeSubPkg(t, tmp, "c", comp("c", "", "<p>c</p>"))
	writeFile(t, dirC, "dep.go", "package c\n\nimport \"gsxorder/zc\"\n\nvar _ = zc.C\n")
	dirD := makeSubPkg(t, tmp, "d", comp("d", "import (\n\t\"gsxorder/a\"\n\t\"gsxorder/b\"\n)\n\n", "<a.C /><b.C />"))
	dirX := makeSubPkg(t, tmp, "x", comp("x", "import \"gsxorder/y\"\n\n", "<y.C />"))
	dirY := makeSubPkg(t, tmp, "y", comp("y", "import \"gsxorder/x\"\n\n", "<x.C />"))

	m, err := Open(Options{ModuleRoot: tmp, ModulePath: "gsxorder"})
	if err != nil {
		t.Fatal(err)
	}
	spelledD := dirA + string(filepath.Separator) + ".." + string(filepath.Separator) + "d"
	if spelledD == dirD {
		t.Fatal("fixture: spelledD must differ from the clean dir")
	}
	got := m.DependencyOrder([]string{spelledD, dirC, dirX, dirY, dirZA, dirZB, dirZC, dirA, dirB})
	want := []string{dirZA, dirA, dirZB, dirB, spelledD, dirZC, dirC, dirY, dirX}
	if !slices.Equal(got, want) {
		t.Fatalf("DependencyOrder:\n got %v\nwant %v", got, want)
	}
}
