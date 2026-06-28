package codegen

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestBundleModeMatchesGoList proves a Module driven by an injected Bundle
// (no packages.Load at analyze time) generates byte-identical .x.go to a Module
// using the default go-list importer. This is the end-to-end proof the WASM path
// can be the Module.
func TestBundleModeMatchesGoList(t *testing.T) {
	t.Parallel()
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxa\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package views\n\ncomponent Hi(name string, count int) {\n\t<div data-n={count}>{name}</div>\n}\n"
	gsxPath := filepath.Join(pkgDir, "views.gsx")
	writeFile(t, pkgDir, "views.gsx", src)

	// Default go-list Module.
	mGo, err := Open(Options{ModuleRoot: tmp, ModulePath: "gsxa", FilterPkgs: []string{StdImportPath}})
	if err != nil {
		t.Fatal(err)
	}
	goOut, goDiags, err := mGo.Generate(pkgDir)
	if err != nil {
		t.Fatalf("go-list generate: %v", err)
	}
	if len(goDiags) != 0 {
		t.Fatalf("go-list diags: %v", goDiags)
	}

	// Bundle Module: build the bundle once via packages.Load, then inject it.
	bundle, err := newCachedResolver(tmp, []string{StdImportPath}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	mB, err := Open(Options{ModuleRoot: tmp, ModulePath: "gsxa", FilterPkgs: []string{StdImportPath}, Bundle: bundle})
	if err != nil {
		t.Fatal(err)
	}
	bOut, bDiags, err := mB.Generate(pkgDir)
	if err != nil {
		t.Fatalf("bundle generate: %v", err)
	}
	if len(bDiags) != 0 {
		t.Fatalf("bundle diags: %v", bDiags)
	}
	// extLoads is incremented during Generate (in externalImporter), not at Open —
	// check here so this actually guards the zero-go-list invariant.
	if got := mB.externalLoads(); got != 0 {
		t.Fatalf("bundle Module did an external packages.Load (extLoads=%d), want 0", got)
	}

	if !bytes.Equal(goOut[gsxPath], bOut[gsxPath]) {
		t.Fatalf("bundle output differs from go-list output\n--- go-list ---\n%s\n--- bundle ---\n%s", goOut[gsxPath], bOut[gsxPath])
	}
	if len(bOut[gsxPath]) == 0 {
		t.Fatal("bundle produced empty output")
	}
}
