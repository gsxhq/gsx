package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLineDirectives(t *testing.T) {
	t.Parallel()
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxl\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "genpkg")
	os.MkdirAll(pkgDir, 0o755)
	writeFile(t, pkgDir, "views.gsx", "package views\n\ncomponent Greeting(name string) {\n\t<p>{name}</p>\n}\n")
	res, err := GenerateDirs(tmp, []string{pkgDir}, Options{FilterPkgs: []string{stdImportPath}, CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	gen := res[pkgDir].Files
	var got string
	for _, src := range gen {
		got = string(src)
	}
	if !strings.Contains(got, "//line views.gsx:") {
		t.Fatalf("expected //line directives in generated source:\n%s", got)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
