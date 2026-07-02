package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// On a toolchain WITHOUT generic methods, a generic method component must be
// skipped with a positioned unsupported-toolchain diagnostic — never a hard
// abort — and other packages in the same run must still generate.
func TestGenericMethodUnsupportedToolchain(t *testing.T) {
	if toolchainHasGenericMethods() {
		t.Skip("toolchain parses generic methods; the guard path is inert (covered by TestGenericMethodComponentGo127)")
	}
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gm\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	viewsDir := filepath.Join(tmp, "views")
	otherDir := filepath.Join(tmp, "other")
	for _, d := range []string{viewsDir, otherDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, viewsDir, "views.gsx", "package views\n\ntype Page struct{}\n\ncomponent (p Page) Box[T string | int](value T) {\n\t<span>box</span>\n}\n")
	writeFile(t, otherDir, "other.gsx", "package other\n\ncomponent Ok() {\n\t<p>ok</p>\n}\n")

	out, err := GenerateDirs(tmp, []string{viewsDir, otherDir}, Options{}, nil)
	if err != nil {
		t.Fatalf("hard error (whole-run abort — the bug this task fixes): %v", err)
	}
	var found bool
	for _, d := range out[viewsDir].Diags {
		if d.Code == "unsupported-toolchain" {
			found = true
			if !strings.HasSuffix(d.Start.Filename, "views.gsx") || d.Start.Line != 5 {
				t.Errorf("diagnostic not anchored at views.gsx:5: %+v", d.Start)
			}
		}
	}
	if !found {
		t.Fatalf("no unsupported-toolchain diagnostic; diags=%+v", out[viewsDir].Diags)
	}
	if len(out[otherDir].Files) != 1 {
		t.Errorf("sibling package must still generate; got files=%v", out[otherDir].Files)
	}
}
