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
	if !strings.Contains(got, "var help = _gsxrt.Func(") {
		t.Fatalf("expected `var help = _gsxrt.Func(` in generated source, got:\n%s", got)
	}
	if !strings.Contains(got, "label") {
		t.Fatalf("expected the outer-scope identifier `label` referenced in the generated source, got:\n%s", got)
	}
}

// TestGoWithElementsFuncLocalScopeCapture is the final-review CRITICAL repro:
// an embedded element inside a top-level func body must resolve its
// interpolations against the SURROUNDING lexical scope — a func parameter
// (`label`) and a local (`greeting`) — not a separate top-level func scope
// (which would false-`undefined`). The probe lowers the element to an inline
// scope-capturing IIFE (mirroring emit's inline gsx.Func closure), so
// generation is clean and the generated code references both names.
func TestGoWithElementsFuncLocalScopeCapture(t *testing.T) {
	t.Parallel()
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxgwprobe3\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "genpkg")
	// Returns string (no user gsx import needed); the generated gsx.Func
	// import is auto-injected. Element interpolates a param AND a local.
	writeFile(t, pkgDir, "views.gsx", "package views\n\nfunc Help(label string) string {\n\tgreeting := label + \"!\"\n\t_ = <div>{ greeting }{ label }</div>\n\treturn greeting\n}\n")

	res, err := GenerateDirs(tmp, []string{pkgDir}, Options{FilterPkgs: []string{stdImportPath}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pr := res[pkgDir]
	if len(pr.Diags) != 0 {
		t.Fatalf("expected clean generation (no false undefined), got: %v", pr.Diags)
	}
	var got string
	for _, src := range pr.Files {
		got = string(src)
	}
	for _, want := range []string{"greeting", "label", "_gsxrt.Func("} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in generated source, got:\n%s", want, got)
		}
	}
}

// TestGoWithElementsFuncLocalTypeErrorCaught confirms the scope fix did not
// lose type-checking: a mistyped interpolation inside an embedded element in
// func-local context (an int on a string-typed child prop) still produces a
// diagnostic.
func TestGoWithElementsFuncLocalTypeErrorCaught(t *testing.T) {
	t.Parallel()
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxgwprobe4\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "genpkg")
	writeFile(t, pkgDir, "views.gsx", "package views\n\ncomponent Child(x string) {\n\t{ x }\n}\n\nfunc Help() string {\n\tn := 5\n\t_ = <Child x={n}/>\n\treturn \"ok\"\n}\n")

	res, err := GenerateDirs(tmp, []string{pkgDir}, Options{FilterPkgs: []string{stdImportPath}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pr := res[pkgDir]
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

// TestGoWithElementsTwoFilesNoRedeclare guards IMPORTANT #2: two .gsx files
// in ONE package, each with an element literal, must not collide on any
// generated skeleton identifier (the inline-IIFE approach declares no named
// per-file probe func — the only marker, `_gsxelem`, is a shared helper), AND
// a real type error in one file must still be diagnosed (no cross-file
// suppression).
func TestGoWithElementsTwoFilesNoRedeclare(t *testing.T) {
	t.Parallel()
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxgwprobe5\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "genpkg")
	// a.gsx: valid embedded element. b.gsx: embedded element with a type error.
	writeFile(t, pkgDir, "a.gsx", "package views\n\nvar aLabel = \"A\"\nvar a = <div>{ aLabel }</div>\n")
	writeFile(t, pkgDir, "b.gsx", "package views\n\ncomponent Child(x string) {\n\t{ x }\n}\n\nvar bN = 7\nvar b = <Child x={bN}/>\n")

	res, err := GenerateDirs(tmp, []string{pkgDir}, Options{FilterPkgs: []string{stdImportPath}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pr := res[pkgDir]
	for _, d := range pr.Diags {
		if strings.Contains(d.Message, "redeclared") {
			t.Fatalf("cross-file redeclare error (IMPORTANT #2 regression): %v", pr.Diags)
		}
	}
	found := false
	for _, d := range pr.Diags {
		if strings.Contains(d.Message, "cannot use") && strings.Contains(d.Message, "string") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected b.gsx's type error still diagnosed, got: %v", pr.Diags)
	}
}
