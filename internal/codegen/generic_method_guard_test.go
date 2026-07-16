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

// TestGenericMethodGuardedCallSiteNoUndefinedSelector is the 1.26-reachable
// regression pin for a caller-side inference probe on a dotted generic method
// tag (`<p.Row v={1}/>`, with no explicit type arguments). On an unsupported
// toolchain the only diagnostic must be the positioned unsupported-toolchain
// error; analysis scaffolding must not leak an undefined receiver selector or
// an undefined synthesized type, and the package must not hard-abort.
func TestGenericMethodGuardedCallSiteNoUndefinedSelector(t *testing.T) {
	if toolchainHasGenericMethods() {
		t.Skip("toolchain parses generic methods; the guard path is inert (covered by TestGenericMethodComponentGo127)")
	}
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gm2\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	viewsDir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(viewsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, viewsDir, "views.gsx", `package views

type Page struct{}

component (p Page) Row[T string | int](v T) {
	<span>row</span>
}

component (p Page) Render() {
	<p.Row v={1} />
}
`)
	out, err := GenerateDirs(tmp, []string{viewsDir}, Options{}, nil)
	if err != nil {
		t.Fatalf("hard error (whole-run abort — not the standard broken-component experience): %v", err)
	}
	diags := out[viewsDir].Diags
	var toolchainDiags int
	for _, d := range diags {
		if strings.Contains(d.Message, "GsxInfer") || strings.Contains(strings.ToLower(d.Message), "undefined") {
			t.Errorf("call-site probe produced an undefined-selector/undefined-type diagnostic (the finding-8 bug shape): %+v", d)
		}
		if d.Code == "unsupported-toolchain" {
			toolchainDiags++
			if !strings.HasSuffix(d.Start.Filename, "views.gsx") || d.Start.Line != 5 {
				t.Errorf("unsupported-toolchain diagnostic not anchored at views.gsx:5: %+v", d.Start)
			}
		}
	}
	if toolchainDiags != 1 {
		t.Fatalf("want exactly 1 unsupported-toolchain diagnostic (masked or missing = regression); got %d; diags=%+v", toolchainDiags, diags)
	}
}
