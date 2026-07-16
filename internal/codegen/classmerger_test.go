package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTempMergerPkg writes a minimal Go module at t.TempDir() containing
// mrg/<src> and returns the module root dir. The module path is "mrgmod" and
// the package lives at mrg/mrg.go. Does NOT include a replace directive for
// github.com/gsxhq/gsx because the merger package itself has no gsx import.
func writeTempMergerPkg(t *testing.T, src string) string {
	t.Helper()
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module mrgmod\n\ngo 1.26.1\n")
	mrgDir := filepath.Join(tmp, "mrg")
	if err := os.MkdirAll(mrgDir, 0o755); err != nil {
		t.Fatalf("mkdir mrg: %v", err)
	}
	writeFile(t, mrgDir, "mrg.go", src)
	return tmp
}

func TestValidateClassMergerSignature(t *testing.T) {
	t.Parallel()
	dir := writeTempMergerPkg(t, `package mrg
func Good(t []string) string { return "" }
func BadVariadic(t ...any) string { return "" }
func BadVariadicString(t ...string) string { return "" }
func BadReturn(t []string) int { return 0 }
`)
	if err := ValidateClassMerger(dir, &ClassMergerRef{PkgPath: "mrgmod/mrg", FuncName: "Good"}); err != nil {
		t.Fatalf("Good: unexpected error: %v", err)
	}
	err := ValidateClassMerger(dir, &ClassMergerRef{PkgPath: "mrgmod/mrg", FuncName: "BadVariadic"})
	if err == nil || !strings.Contains(err.Error(), "func([]string) string") {
		t.Fatalf("BadVariadic: want signature error, got %v", err)
	}
	err = ValidateClassMerger(dir, &ClassMergerRef{PkgPath: "mrgmod/mrg", FuncName: "BadVariadicString"})
	if err == nil || !strings.Contains(err.Error(), "func([]string) string") {
		t.Fatalf("BadVariadicString (variadic-string): want signature error, got %v", err)
	}
	err = ValidateClassMerger(dir, &ClassMergerRef{PkgPath: "mrgmod/mrg", FuncName: "BadReturn"})
	if err == nil || !strings.Contains(err.Error(), "func([]string) string") {
		t.Fatalf("BadReturn: want signature error, got %v", err)
	}
	if err := ValidateClassMerger(dir, &ClassMergerRef{PkgPath: "mrgmod/mrg", FuncName: "Missing"}); err == nil {
		t.Fatalf("Missing: want error, got nil")
	}
}

// generateClassFixture writes a minimal gsx component with a static class and
// a merger package into a temp module, runs GenerateDirs with ClassMerger set,
// and returns the generated source for the views package.
func generateClassFixture(t *testing.T, ref *ClassMergerRef) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping go-build test in -short mode")
	}
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod",
		"module mrgmod\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")

	// Write the merger package (mrgmod/mrg).
	mrgDir := filepath.Join(tmp, "mrg")
	if err := os.MkdirAll(mrgDir, 0o755); err != nil {
		t.Fatalf("mkdir mrg: %v", err)
	}
	writeFile(t, mrgDir, "mrg.go", "package mrg\n\nfunc Merge(t []string) string { return \"\" }\n")

	// Write a gsx component that explicitly merges forwarded classes.
	viewsDir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(viewsDir, 0o755); err != nil {
		t.Fatalf("mkdir views: %v", err)
	}
	writeFile(t, viewsDir, "card.gsx", "package views\n\nimport \"github.com/gsxhq/gsx\"\n\ncomponent Card(attrs gsx.Attrs, children gsx.Node) {\n\t<section class=\"card\" { attrs... }>{children}</section>\n}\n")

	res, err := GenerateDirs(tmp, []string{viewsDir}, Options{
		FilterPkgs:  []string{StdImportPath},
		ClassMerger: ref,
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
	if got == "" {
		t.Fatal("generateClassFixture: no generated output")
	}
	return got
}

func TestGeneratedClassUsesConfiguredMerger(t *testing.T) {
	t.Parallel()
	// generate a component with an explicit attrs spread, ClassMerger set, assert the
	// emitted .x.go references _gsxcm.Merge and imports the merger pkg under _gsxcm.
	got := generateClassFixture(t, &ClassMergerRef{PkgPath: "mrgmod/mrg", FuncName: "Merge"})
	if !strings.Contains(got, `_gsxcm "mrgmod/mrg"`) {
		t.Fatalf("missing aliased import:\n%s", got)
	}
	if !strings.Contains(got, "_gsxgw.Class(_gsxcm.Merge,") {
		t.Fatalf("missing direct merger reference:\n%s", got)
	}
}
