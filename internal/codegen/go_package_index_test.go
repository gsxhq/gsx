package codegen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/sourceintel"
)

const reverseDepMainSource = "package main\n\nimport (\n\t\"example.com/rd/app\"\n\t\"example.com/rd/components\"\n)\n\nfunc main() {\n\tvar h app.Home\n\t_ = h.Page\n\t_ = components.Input\n\tvar s components.Size\n\t_ = s\n}\n"

const reverseDepInputSource = "package components\n\ntype Size int\n\ncomponent Input(name string, size Size) {\n\t<input name={ name }/>\n}\n"

// writeReverseDepModule: components/ (gsx) ← app/ (gsx, imports components)
// ← cmd/ (Go-only main, imports both) ; util/ (Go-only, imports nothing gsx).
func writeReverseDepModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/rd\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, root, "components/input.gsx", reverseDepInputSource)
	writeFile(t, root, "app/page.gsx", "package app\n\nimport \"example.com/rd/components\"\n\ntype Home struct{}\n\ncomponent (h Home) Page() {\n\t<main><components.Input name=\"a\" size={ components.Size(1) }/></main>\n}\n")
	writeFile(t, root, "cmd/main.go", reverseDepMainSource)
	writeFile(t, root, "util/util.go", "package util\n\nfunc U() {}\n")
	return root
}

func TestReverseDependencyGoPackageIndex(t *testing.T) {
	root := writeReverseDepModule(t)
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/rd", FilterPkgs: []string{StdImportPath}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Package(filepath.Join(root, "app")); err != nil { // warms the inventory
		t.Fatal(err)
	}
	dirs, err := m.reverseDependencyGoDirs()
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 1 || filepath.Base(dirs[0]) != "cmd" {
		t.Fatalf("reverseDependencyGoDirs = %v, want [cmd]", dirs)
	}
	index, pkg, err := m.GoPackageIndex(dirs[0])
	if err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(root, "cmd", "main.go")
	src := reverseDepMainSource
	occ, ok := index.At(mainPath, strings.Index(src, "app.Home")+4)
	if !ok || occ.Object == nil || occ.Object.Name() != "Home" {
		t.Fatalf("main.go Home occurrence: %+v %v", occ, ok)
	}
	k := sourceintel.NewKeyer(pkg)
	key, _ := k.Key(occ.Object)
	if string(key) != "example.com/rd/app Home" {
		t.Fatalf("Home key from main.go = %q", key)
	}
	// second call is cached (no re-check)
	before := m.goPackageAnalysisCount()
	if _, _, err := m.GoPackageIndex(dirs[0]); err != nil {
		t.Fatal(err)
	}
	if m.goPackageAnalysisCount() != before {
		t.Fatal("GoPackageIndex must reuse the cached analysis")
	}
	// editing app/page.gsx invalidates cmd's analysis (reverse closure)
	m.Invalidate(filepath.Join(root, "app"))
	if _, _, err := m.GoPackageIndex(dirs[0]); err != nil {
		t.Fatal(err)
	}
	if m.goPackageAnalysisCount() != before+1 {
		t.Fatalf("expected re-analysis after dependency invalidation, count %d → %d", before, m.goPackageAnalysisCount())
	}
}
