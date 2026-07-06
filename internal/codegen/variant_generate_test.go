package codegen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/diag"
)

// hasError reports whether diags contains an Error-severity diagnostic.
func hasError(diags []diag.Diagnostic) bool {
	for _, d := range diags {
		if d.Severity == diag.Error {
			return true
		}
	}
	return false
}

// keysOfGenerated returns the sorted-order-agnostic key list of a Generate
// output map, for readable failure messages.
func keysOfGenerated(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestSameSigVariantGeneratesAllFiles is the regression for the bug this
// subsystem fixes: two .gsx files under disjoint //go:build tags declaring a
// same-name/same-signature component (a legitimate build-tag variant) used to
// produce a cross-file "redeclared in this block" go/types error, which
// blocked emission for the WHOLE package — not just the redeclared component.
// suppressCrossFileRedeclarations must tolerate this so all three files in
// the package still generate.
func TestSameSigVariantGeneratesAllFiles(t *testing.T) {
	dir, m := openTestModule(t, map[string]string{
		"icon_linux.gsx":   "//go:build linux\n\npackage views\n\ncomponent Icon(name string) { <span>linux:{ name }</span> }\n",
		"icon_windows.gsx": "//go:build windows\n\npackage views\n\ncomponent Icon(name string) { <span>win:{ name }</span> }\n",
		"page.gsx":         "package views\n\ncomponent Page() { <Icon name=\"x\"/> }\n",
	})
	out, diags, err := m.Generate(dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if hasError(diags) {
		t.Fatalf("unexpected error diagnostics: %v", diags)
	}
	for _, want := range []string{"icon_linux.gsx", "icon_windows.gsx", "page.gsx"} {
		if _, ok := out[filepath.Join(dir, want)]; !ok {
			t.Fatalf("missing generated output for %s; got keys %v", want, keysOfGenerated(out))
		}
	}
	linuxOut := out[filepath.Join(dir, "icon_linux.gsx")]
	if !strings.Contains(string(linuxOut), "//go:build linux") {
		t.Fatalf("linux variant lost its build constraint:\n%s", linuxOut)
	}
}

// TestDiffSigVariantIsCleanError covers the genuine-conflict side: a same-name
// component declared with DIFFERENT signatures across build-tagged files is a
// real ambiguity (gsx does not parse build tags, so it cannot know which
// signature wins). This must surface as a single clean duplicate-component
// diagnostic — never a raw go/types "redeclared in this block" — and must
// block emission entirely (not just for the conflicting component).
func TestDiffSigVariantIsCleanError(t *testing.T) {
	dir, m := openTestModule(t, map[string]string{
		"icon_linux.gsx":   "//go:build linux\n\npackage views\n\ncomponent Icon(name string) { <span>{ name }</span> }\n",
		"icon_windows.gsx": "//go:build windows\n\npackage views\n\ncomponent Icon(name int) { <span>{ name }</span> }\n",
	})
	out, diags, err := m.Generate(dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !hasError(diags) {
		t.Fatalf("expected a duplicate-component error, got none: %v", diags)
	}
	foundClean := false
	for _, d := range diags {
		if d.Code == "duplicate-component" {
			foundClean = true
		}
		if strings.Contains(d.Message, "redeclared in this block") {
			t.Fatalf("raw go/types redeclared error leaked: %q", d.Message)
		}
	}
	if !foundClean {
		t.Fatalf("no duplicate-component diagnostic in %v", diags)
	}
	if len(out) != 0 {
		t.Fatalf("diff-sig conflict must block emission, got %v", keysOfGenerated(out))
	}
}
