package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratedComposedStyleUsesStyleConstructors(t *testing.T) {
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/styleparts\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")

	viewsDir := filepath.Join(root, "views")
	if err := os.MkdirAll(viewsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gsxPath := filepath.Join(viewsDir, "badge.gsx")
	writeFile(t, viewsDir, "badge.gsx", `package views

component Badge(hidden bool) {
	<span class={ "badge", "hidden": hidden } style={ "color:red", "display:none": hidden }>badge</span>
}
`)

	module, err := Open(Options{
		ModuleRoot: root,
		ModulePath: "example.com/styleparts",
		FilterPkgs: []string{StdImportPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	output, diagnostics, err := module.Generate(viewsDir)
	if err != nil {
		t.Fatalf("Generate: %v (diagnostics=%v)", err, diagnostics)
	}
	generated := string(output[gsxPath])

	for _, want := range []string{
		`_gsxgw.Class(_gsxrt.DefaultClassMerge, _gsxrt.Class("badge"), _gsxrt.ClassIf("hidden", hidden))`,
		`_gsxgw.Style(_gsxrt.Style("color:red"), _gsxrt.StyleIf("display:none", hidden))`,
	} {
		if !strings.Contains(generated, want) {
			t.Fatalf("generated output does not contain %q:\n%s", want, generated)
		}
	}
	if strings.Contains(generated, `_gsxgw.Style(_gsxrt.Class`) {
		t.Fatalf("generated style still uses class constructors:\n%s", generated)
	}
}
