package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModulePackageSurfacesTypeErrors(t *testing.T) {
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(root, "page")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// `nope` is undefined → a type error inside the interpolation.
	writeFile(t, pkgDir, "page.gsx", "package page\n\ncomponent Home() {\n\t<div>{ nope() }</div>\n}\n")

	m, _ := Open(Options{ModuleRoot: root, ModulePath: "example.com/app", FilterPkgs: []string{StdImportPath}})
	pr, err := m.Package(pkgDir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range pr.Diags {
		if strings.Contains(d.Message, "nope") && strings.HasSuffix(d.Start.Filename, ".gsx") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a .gsx-positioned type-error diagnostic mentioning 'nope'; got %+v", pr.Diags)
	}
}

func TestModuleResetPackageCacheKeepsExternalWarm(t *testing.T) {
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(root, "comp")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, pkgDir, "comp.gsx", "package comp\n\ncomponent Button(label string) {\n\t<button>{label}</button>\n}\n")

	m, _ := Open(Options{ModuleRoot: root, ModulePath: "example.com/app", FilterPkgs: []string{StdImportPath}})
	if _, err := m.typesPackage(pkgDir); err != nil {
		t.Fatal(err)
	}
	// edit comp.gsx in-memory: Button now takes an int
	m.SetOverride(filepath.Join(pkgDir, "comp.gsx"), []byte("package comp\n\ncomponent Button(n int) {\n\t<button>{ n }</button>\n}\n"))
	m.ResetPackageCache()
	pkg, err := m.typesPackage(pkgDir)
	if err != nil {
		t.Fatal(err)
	}
	// The freshly-rebuilt Button props struct must reflect the int param.
	if obj := pkg.Scope().Lookup("ButtonProps"); obj != nil {
		if !strings.Contains(obj.Type().Underlying().String(), "int") {
			t.Fatalf("ResetPackageCache did not refresh comp types: %s", obj.Type().Underlying())
		}
	}
	// ext importer must still be non-nil (warm, not cleared)
	if m.ext == nil {
		t.Fatalf("ResetPackageCache wrongly cleared the external importer")
	}
}
