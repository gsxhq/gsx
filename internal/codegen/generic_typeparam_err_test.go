package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A malformed type-param list must produce a positioned diagnostic and must
// NOT take down generation for healthy siblings in the same package.
func TestBadTypeParamListDiagnosticAndSiblingSurvival(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module badtp\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Missing constraint: go/parser rejects `[T]` in a func type-param list.
	writeFile(t, pkgDir, "bad.gsx", "package views\n\ncomponent Box[T](value T) {\n\t<span>x</span>\n}\n")
	writeFile(t, pkgDir, "good.gsx", "package views\n\ncomponent Ok() {\n\t<p>ok</p>\n}\n")

	out, err := GenerateDirs(tmp, []string{pkgDir}, Options{}, nil)
	if err != nil {
		t.Fatalf("hard error: %v", err)
	}
	dr := out[pkgDir]
	var found bool
	for _, d := range dr.Diags {
		if d.Code == "invalid-syntax" && strings.Contains(d.Message, "type params") {
			found = true
			if !strings.HasSuffix(d.Start.Filename, "bad.gsx") || d.Start.Line != 3 {
				t.Errorf("diagnostic not anchored at bad.gsx:3: %+v", d.Start)
			}
		}
	}
	if !found {
		t.Fatalf("no invalid-syntax diagnostic for the bad type-param list; diags=%+v", dr.Diags)
	}
	var goodGenerated bool
	for path := range dr.Files {
		if strings.HasSuffix(path, "good.gsx") {
			goodGenerated = true
		}
	}
	if !goodGenerated {
		t.Errorf("sibling good.gsx lost its generated output; files=%v", dr.Files)
	}
}
