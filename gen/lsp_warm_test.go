package gen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLSPAnalyzeUsesWarmModule(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(root, "page")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, pkgDir, "page.gsx", "package page\n\ncomponent Home(name string) {\n\t<h1>Hi {name}</h1>\n}\n")

	a := newLSPAnalyzer(config{}, nil) // constructor introduced in Step 3
	pkg, err := a.Analyze(pkgDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Info == nil || pkg.ExprMap == nil || pkg.GSXFset == nil {
		t.Fatalf("warm-module Analyze returned empty analysis: %+v", pkg)
	}
	if _, ok := pkg.CrossIndex[".Home"]; !ok {
		t.Fatalf("CrossIndex missing .Home: %v", pkg.CrossIndex)
	}
	// No .x.go was written to disk by analysis.
	if _, err := os.Stat(filepath.Join(pkgDir, "page.x.go")); err == nil {
		t.Fatalf("Analyze must not write .x.go to disk")
	}
}
