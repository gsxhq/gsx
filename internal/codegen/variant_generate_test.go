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

// TestMethodVariantSameSigGeneratesAllFiles is the method-component analogue of
// TestSameSigVariantGeneratesAllFiles. go/types reports a METHOD redeclaration
// with a different message ("method Form.Field already declared at FILE:L:C")
// than a func's ("redeclared in this block" + "other declaration" note). Before
// the fix, suppression did not recognize the method form, so a same-signature
// method variant under disjoint build tags blocked emission for the WHOLE
// package. All three files must now generate.
func TestMethodVariantSameSigGeneratesAllFiles(t *testing.T) {
	dir, m := openTestModule(t, map[string]string{
		"field_linux.gsx":   "//go:build linux\n\npackage views\n\ncomponent (f Form) Field(name string) { <span>linux:{ name }</span> }\n",
		"field_windows.gsx": "//go:build windows\n\npackage views\n\ncomponent (f Form) Field(name string) { <span>win:{ name }</span> }\n",
		"form.gsx":          "package views\n\ntype Form struct{}\n\ncomponent Page() { <div>page</div> }\n",
	})
	out, diags, err := m.Generate(dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if hasError(diags) {
		t.Fatalf("unexpected error diagnostics: %v", diags)
	}
	for _, want := range []string{"field_linux.gsx", "field_windows.gsx", "form.gsx"} {
		if _, ok := out[filepath.Join(dir, want)]; !ok {
			t.Fatalf("missing generated output for %s; got keys %v", want, keysOfGenerated(out))
		}
	}
	linuxOut := out[filepath.Join(dir, "field_linux.gsx")]
	if !strings.Contains(string(linuxOut), "//go:build linux") {
		t.Fatalf("linux variant lost its build constraint:\n%s", linuxOut)
	}
}

// TestMethodVariantDiffSigIsCleanError is the method-component analogue of
// TestDiffSigVariantIsCleanError: differing method signatures are a genuine
// ambiguity and must surface as a single clean duplicate-component diagnostic,
// never a raw go/types "already declared"/"redeclared" leak, with emission
// blocked entirely.
func TestMethodVariantDiffSigIsCleanError(t *testing.T) {
	dir, m := openTestModule(t, map[string]string{
		"field_linux.gsx":   "//go:build linux\n\npackage views\n\ncomponent (f Form) Field(name string) { <span>{ name }</span> }\n",
		"field_windows.gsx": "//go:build windows\n\npackage views\n\ncomponent (f Form) Field(name int) { <span>{ name }</span> }\n",
		"form.gsx":          "package views\n\ntype Form struct{}\n",
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
		if strings.Contains(d.Message, "already declared") || strings.Contains(d.Message, "redeclared") {
			t.Fatalf("raw go/types method-redeclaration error leaked: %q", d.Message)
		}
	}
	if !foundClean {
		t.Fatalf("no duplicate-component diagnostic in %v", diags)
	}
	if len(out) != 0 {
		t.Fatalf("diff-sig conflict must block emission, got %v", keysOfGenerated(out))
	}
}

// TestWithinFileRedeclarationKeptDespiteVariant pins finding #3: a name
// redeclared BOTH within file A (a real mistake) AND across files A/B (a
// same-signature variant) must NOT be silently generated — the within-file
// redeclaration stays a hard error. The over-reaching name+file-count
// suppression used to drop the within-file error too (its name spanned ≥2
// files). Detection now comes from the skeleton ASTs (collectRedeclFacts), so
// it is order-independent and exact.
func TestWithinFileRedeclarationKeptDespiteVariant(t *testing.T) {
	dir, m := openTestModule(t, map[string]string{
		"icon_a.gsx": "package views\n\ncomponent Icon(name string) { <a>{ name }</a> }\ncomponent Icon(name string) { <b>{ name }</b> }\n",
		"icon_b.gsx": "//go:build windows\n\npackage views\n\ncomponent Icon(name string) { <c>{ name }</c> }\n",
	})
	out, diags, err := m.Generate(dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !hasError(diags) {
		t.Fatalf("within-file redeclaration must stay a hard error, got diags=%v out=%v", diags, keysOfGenerated(out))
	}
	if len(out) != 0 {
		t.Fatalf("within-file redeclaration must block emission, got %v", keysOfGenerated(out))
	}
}
