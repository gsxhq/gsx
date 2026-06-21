package codegen

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveFiltersStd resolves the std filter package and checks the table is
// sorted by Name and carries the expected entries with empty Shadows.
func TestResolveFiltersStd(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	infos, err := ResolveFilters(repoRoot, []string{stdImportPath})
	if err != nil {
		t.Fatalf("ResolveFilters: %v", err)
	}
	if len(infos) == 0 {
		t.Fatal("expected at least one std filter")
	}
	// Sorted by Name.
	for i := 1; i < len(infos); i++ {
		if infos[i-1].Name > infos[i].Name {
			t.Fatalf("not sorted by Name: %q before %q", infos[i-1].Name, infos[i].Name)
		}
	}
	byName := map[string]FilterInfo{}
	for _, fi := range infos {
		byName[fi.Name] = fi
	}
	upper, ok := byName["upper"]
	if !ok {
		t.Fatal("expected an \"upper\" filter")
	}
	if upper.Func != "Upper" {
		t.Fatalf("upper.Func = %q, want \"Upper\"", upper.Func)
	}
	if upper.Pkg != stdImportPath {
		t.Fatalf("upper.Pkg = %q, want %q", upper.Pkg, stdImportPath)
	}
	if upper.Param {
		t.Fatalf("upper should be bare, got Param=true")
	}
	if len(upper.Shadows) != 0 {
		t.Fatalf("upper.Shadows = %v, want empty", upper.Shadows)
	}
	trunc, ok := byName["truncate"]
	if !ok {
		t.Fatal("expected a \"truncate\" filter")
	}
	if !trunc.Param {
		t.Fatalf("truncate should be param, got Param=false")
	}
}

// TestResolveFiltersEmptyDefaultsToStd proves an empty filterPkgs defaults to
// the std package (matching GeneratePackageWithFilters).
func TestResolveFiltersEmptyDefaultsToStd(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	infos, err := ResolveFilters(repoRoot, nil)
	if err != nil {
		t.Fatalf("ResolveFilters: %v", err)
	}
	found := false
	for _, fi := range infos {
		if fi.Name == "upper" {
			found = true
			if fi.Pkg != stdImportPath {
				t.Fatalf("upper.Pkg = %q, want %q", fi.Pkg, stdImportPath)
			}
		}
	}
	if !found {
		t.Fatal("expected std \"upper\" filter with empty filterPkgs")
	}
}

// TestResolveFiltersShadowing proves a user Upper listed AFTER std wins (Pkg ==
// user pkg) and records the shadowed std import path in Shadows.
func TestResolveFiltersShadowing(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-load shadowing test in -short mode")
	}
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	writeMultiFile(t, tmp, "go.mod", "module gsxmf\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	mfDir := filepath.Join(tmp, "myfilters")
	if err := os.MkdirAll(mfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMultiFile(t, mfDir, "myfilters.go", "package myfilters\n\nfunc Upper(s string) string { return \"USER:\" + s }\n")

	infos, err := ResolveFilters(tmp, []string{stdImportPath, "gsxmf/myfilters"})
	if err != nil {
		t.Fatalf("ResolveFilters: %v", err)
	}
	var upper *FilterInfo
	for i := range infos {
		if infos[i].Name == "upper" {
			upper = &infos[i]
		}
	}
	if upper == nil {
		t.Fatal("expected an \"upper\" filter")
	}
	if upper.Pkg != "gsxmf/myfilters" {
		t.Fatalf("upper.Pkg = %q, want %q (user pkg wins, last-wins)", upper.Pkg, "gsxmf/myfilters")
	}
	if len(upper.Shadows) != 1 || upper.Shadows[0] != stdImportPath {
		t.Fatalf("upper.Shadows = %v, want [%q]", upper.Shadows, stdImportPath)
	}
}

// TestResolveFiltersBadPkg proves a non-existent filter package is a clean error.
func TestResolveFiltersBadPkg(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	_, err = ResolveFilters(repoRoot, []string{"github.com/gsxhq/gsx/does-not-exist"})
	if err == nil {
		t.Fatal("expected error for non-existent filter package")
	}
}
