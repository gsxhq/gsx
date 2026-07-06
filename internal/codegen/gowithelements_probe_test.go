package codegen

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestGoWithElementsChildPropTypeMismatchDiagnosed is Task 5's Step 1 RED
// test: a gsx element embedded directly in top-level Go-expression position
// (a *ast.GoWithElements, Task 2/3) that passes a mistyped value to a child
// component's prop (`<Child x={badInt}/>` where Child's declared param is
// `x string` and badInt is an int) must produce a positioned type-mismatch
// diagnostic — the SAME way it already does when the identical tag sits
// inside a component body (see the sibling case this mirrors: a component
// body's child-tag prop mismatch is caught via the real Go struct-literal
// assignability check `_ = Child(ChildProps{X: badInt})`, which go/types
// rejects on its own, no special-cased "int is invalid for X" logic
// required).
//
// Before Task 5, buildSkeleton did not walk *ast.GoWithElements at all: the
// embedded element (and the surrounding `var help = ...` construct) was
// silently dropped from the skeleton, so this mismatch went completely
// unchecked and the diagnostic list was empty.
func TestGoWithElementsChildPropTypeMismatchDiagnosed(t *testing.T) {
	t.Parallel()
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxgwprobe1\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "genpkg")
	writeFile(t, pkgDir, "views.gsx", "package views\n\ncomponent Child(x string) {\n\t{ x }\n}\n\nvar badInt = 5\nvar help = <Child x={badInt}/>\n")

	res, err := GenerateDirs(tmp, []string{pkgDir}, Options{FilterPkgs: []string{stdImportPath}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pr, ok := res[pkgDir]
	if !ok {
		t.Fatalf("no result for %s", pkgDir)
	}
	if len(pr.Diags) == 0 {
		t.Fatalf("expected a type-mismatch diagnostic for the embedded element's mistyped child prop; got none. Files: %v", pr.Files)
	}
	found := false
	for _, d := range pr.Diags {
		if strings.Contains(d.Message, "cannot use") && strings.Contains(d.Message, "string") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a 'cannot use ... as string' diagnostic, got: %v", pr.Diags)
	}
}

// TestGoWithElementsValidInterpGeneratesCleanly is Task 5's boundary case
// (flagged by the Task-4 reviewer): an embedded element containing
// interpolations that reference outer-scope package-level vars — `var help =
// <a href={u}>{ label }</a>` — must generate cleanly end to end, with no
// "unresolved-interp" diagnostic, and the interpolations must be emitted
// verbatim (ordinary Go closure capture against the outer package scope).
func TestGoWithElementsValidInterpGeneratesCleanly(t *testing.T) {
	t.Parallel()
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxgwprobe2\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "genpkg")
	writeFile(t, pkgDir, "views.gsx", "package views\n\nvar u = \"/\"\nvar label = \"Home\"\nvar help = <a href={u}>{ label }</a>\n")

	res, err := GenerateDirs(tmp, []string{pkgDir}, Options{FilterPkgs: []string{stdImportPath}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pr, ok := res[pkgDir]
	if !ok {
		t.Fatalf("no result for %s", pkgDir)
	}
	for _, d := range pr.Diags {
		if strings.Contains(d.Message, "unresolved-interp") || strings.Contains(d.Code, "unresolved-interp") {
			t.Fatalf("unexpected unresolved-interp diagnostic: %v", pr.Diags)
		}
	}
	if len(pr.Diags) != 0 {
		t.Fatalf("expected no diagnostics for a valid embedded element, got: %v", pr.Diags)
	}
	var got string
	for _, src := range pr.Files {
		got = string(src)
	}
	if got == "" {
		t.Fatalf("no generated output; diags: %v", pr.Diags)
	}
	if !strings.Contains(got, "var help = gsx.Func(") {
		t.Fatalf("expected `var help = gsx.Func(` in generated source, got:\n%s", got)
	}
	if !strings.Contains(got, "label") {
		t.Fatalf("expected the outer-scope identifier `label` referenced in the generated source, got:\n%s", got)
	}
}
