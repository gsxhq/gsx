package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Pins the dotted cross-package generic-tag path <components.Button[int]>:
// typeArgUse appended to a package-qualified callTarget + propsType. The txtar
// corpus (internal/corpus) is single-package — every case is one input.gsx in
// one package — so this cross-package context cannot be expressed there and
// lives as a GenerateDirs unit test instead, mirroring writeCrossPkgModule in
// batch_crosspkg_test.go and the Options used by
// TestGenericMethodComponentGo127 in generic_method_go127_test.go. See
// CLAUDE.md's per-context corpus coverage rule.
func TestGenericCrossPackageTag(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module example.com/xg\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	compDir := filepath.Join(tmp, "components")
	if err := os.MkdirAll(compDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, compDir, "button.gsx", "package components\n\ncomponent Button[T string | int](label T) {\n\t<button>{label}</button>\n}\n")
	writeFile(t, tmp, "post.gsx", "package xg\n\nimport \"example.com/xg/components\"\n\ncomponent Post() {\n\t<components.Button[int] label={7} />\n}\n")

	res, err := GenerateDirs(tmp, []string{tmp, compDir}, Options{FilterPkgs: []string{stdImportPath}, CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if diags := res[tmp].Diags; len(diags) != 0 {
		t.Fatalf("unexpected diagnostics generating root package: %+v", diags)
	}
	if diags := res[compDir].Diags; len(diags) != 0 {
		t.Fatalf("unexpected diagnostics generating components package: %+v", diags)
	}
	var root string
	for _, src := range res[tmp].Files {
		root = string(src)
	}
	for _, want := range []string{
		"components.Button[int](components.ButtonProps[int]{Label: 7})",
	} {
		if !strings.Contains(root, want) {
			t.Fatalf("generated root source missing %q:\n%s", want, root)
		}
	}
}
