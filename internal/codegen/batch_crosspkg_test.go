package codegen

import (
	"os"
	"path/filepath"
	"testing"
)

// writeCrossPkgModule writes a 2-package module: root package x imports
// example.com/x/components and references Input from a .gsx tag and a .go call
// site; subdir components declares Input. Returns (root, componentsDir).
func writeCrossPkgModule(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	repoRoot = filepath.Dir(repoRoot) // internal/codegen -> repo root
	must := func(p, c string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("components/input.gsx", "package components\n\ncomponent Input(name string) {\n\t<input name={ name }/>\n}\n")
	must("post.gsx", "package x\n\nimport \"example.com/x/components\"\n\ncomponent Post() {\n\t<main><components.Input name=\"a\"/></main>\n}\n")
	must("use.go", "package x\n\nimport \"example.com/x/components\"\n\nfunc use() { _ = components.Input }\n")
	return root, filepath.Join(root, "components")
}

// TestCrossPkgReferencesRouted verified that cross-package CrossRef entries are
// populated when the declaring and referencing packages are both analyzed
// together. This property is now exercised by TestAnalyzeModuleCrossPkg in
// gen/references_crosspkg_test.go, which covers the same scenario via the LSP
// AnalyzeModule path. The test is removed here to avoid redundancy.

// TestSingleDirReferencesNoRegression verifies that per-package analysis (via
// Module.Package on the components dir alone) does NOT produce any cross-package
// refs from the root package — because root is never analyzed here.
func TestSingleDirReferencesNoRegression(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip()
	}
	root, componentsDir := writeCrossPkgModule(t)
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/x"})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := m.Package(componentsDir)
	if err != nil {
		t.Fatal(err)
	}
	// Single-package analysis over components: root package was never analyzed,
	// so Input has no refs from root (.gsx tag or .go call site).
	var refs []string
	for _, cr := range pr.CrossIndex {
		if cr.Name != "Input" {
			continue
		}
		for _, r := range cr.Refs {
			refs = append(refs, filepath.Base(r.Filename))
		}
	}
	if len(refs) != 0 {
		t.Errorf("single-dir analysis over components alone should have no Input refs; got %v", refs)
	}
}
