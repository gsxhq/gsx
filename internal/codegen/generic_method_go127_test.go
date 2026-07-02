package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenericMethodComponentGo127(t *testing.T) {
	if !toolchainHasGenericMethods() {
		if os.Getenv("GSX_REQUIRE_GENERIC_METHODS") == "1" {
			t.Fatal("GSX_REQUIRE_GENERIC_METHODS=1 but the active toolchain does not parse generic methods")
		}
		t.Skip("active Go toolchain does not parse generic methods yet")
	}
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module genericmethod\n\ngo 1.27\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, pkgDir, "views.gsx", `package views

type Page struct{}

component (p Page) Box[T string | int](value T) {
	<span>box</span>
}

component (p Page) Render() {
	<p.Box[int] value={7} />
}
`)
	res, err := GenerateDirs(tmp, []string{pkgDir}, Options{FilterPkgs: []string{stdImportPath}, CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for _, src := range res[pkgDir].Files {
		got = string(src)
	}
	for _, want := range []string{
		"type PageBoxProps[T string | int] struct",
		"func (p Page) Box[T string | int](_gsxp PageBoxProps[T]) gsx.Node",
		"p.Box[int](PageBoxProps[int]{Value: 7})",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("generated source missing %q:\n%s", want, got)
		}
	}
}
