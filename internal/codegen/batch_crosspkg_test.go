package codegen

import (
	"os"
	"path/filepath"
	"strings"
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

func inputRefs(t *testing.T, out map[string]*PackageResult, componentsDir string) []string {
	t.Helper()
	pr := out[componentsDir]
	if pr == nil {
		t.Fatalf("no result for components dir %s; keys=%v", componentsDir, resultKeysOf(out))
	}
	var files []string
	for _, cr := range pr.CrossIndex {
		if cr.Name != "Input" {
			continue
		}
		for _, r := range cr.Refs {
			files = append(files, filepath.Base(r.Filename))
		}
	}
	return files
}

func resultKeysOf(m map[string]*PackageResult) []string {
	var k []string
	for s := range m {
		k = append(k, s)
	}
	return k
}

func TestCrossPkgReferencesRouted(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	root, componentsDir := writeCrossPkgModule(t)
	out, err := GeneratePackages(root, []string{root, componentsDir})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(inputRefs(t, out, componentsDir), ",")
	if !strings.Contains(got, "post.gsx") {
		t.Errorf("Input refs missing post.gsx (cross-pkg .gsx tag); got %q", got)
	}
	if !strings.Contains(got, "use.go") {
		t.Errorf("Input refs missing use.go (cross-pkg .go site); got %q", got)
	}
}

func TestSingleDirReferencesNoRegression(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	root, componentsDir := writeCrossPkgModule(t)
	out, err := GeneratePackages(root, []string{componentsDir})
	if err != nil {
		t.Fatal(err)
	}
	if got := inputRefs(t, out, componentsDir); len(got) != 0 {
		t.Errorf("single-dir batch over components alone should have no Input refs; got %v", got)
	}
}
