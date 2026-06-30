package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratedExplicitAttrsHasNoUnusedGuard(t *testing.T) {
	t.Parallel()

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod",
		"module explicitattrs\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")

	viewsDir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(viewsDir, 0o755); err != nil {
		t.Fatalf("mkdir views: %v", err)
	}
	writeFile(t, viewsDir, "card.gsx",
		"package views\n\ncomponent Card() {\n\t<div { attrs... }/>\n}\n")

	res, err := GenerateDirs(tmp, []string{viewsDir}, Options{
		FilterPkgs: []string{StdImportPath},
	}, nil)
	if err != nil {
		t.Fatalf("GenerateDirs: %v", err)
	}
	dr := res[viewsDir]
	if hasDiagErrors(dr.Diags) {
		t.Fatalf("GenerateDirs: unexpected errors: %v", dr.Diags)
	}

	var got string
	for _, src := range dr.Files {
		got = string(src)
	}
	if !strings.Contains(got, "attrs := _gsxp.Attrs") {
		t.Fatalf("missing explicit attrs binding:\n%s", got)
	}
	if strings.Contains(got, "_ = attrs") {
		t.Fatalf("generated explicit attrs binding has an unused guard:\n%s", got)
	}
}
