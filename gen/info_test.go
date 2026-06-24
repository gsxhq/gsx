package gen

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/attrclass"
)

// TestRunInfoStd drives runInfo against the repo root (a module that resolves
// std) and checks the output lists std filters and the filter-packages header.
func TestRunInfoStd(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	code := runInfo(&out, &bytes.Buffer{}, repoRoot, []string{stdImportPath}, attrclass.Builtin(), "", nil)
	if code != 0 {
		t.Fatalf("runInfo exit = %d, want 0", code)
	}
	got := out.String()
	for _, want := range []string{"upper", "truncate", "Filter packages"} {
		if !strings.Contains(got, want) {
			t.Fatalf("runInfo output missing %q:\n%s", want, got)
		}
	}
}

// TestRunInfoVersionSingleLine proves the info banner reports the version with a
// single "gsx " prefix (regression: version() began returning a banner that
// itself starts with "gsx ", which doubled the prefix to "gsx gsx ...").
func TestRunInfoVersionSingleLine(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	code := runInfo(&out, &bytes.Buffer{}, repoRoot, []string{stdImportPath}, attrclass.Builtin(), "", nil)
	if code != 0 {
		t.Fatalf("runInfo exit = %d, want 0", code)
	}
	if strings.Contains(out.String(), "gsx gsx") {
		t.Fatalf("info version line has doubled prefix:\n%s", out.String())
	}
}

// TestRunInfoShadow proves a shadowed filter is marked with "(shadows ".
func TestRunInfoShadow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-load shadow test in -short mode")
	}
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module gsxmf\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mfDir := filepath.Join(tmp, "myfilters")
	if err := os.MkdirAll(mfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mfDir, "myfilters.go"), []byte("package myfilters\n\nfunc Upper(s string) string { return \"USER:\" + s }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	code := runInfo(&out, &bytes.Buffer{}, tmp, []string{stdImportPath, "gsxmf/myfilters"}, attrclass.Builtin(), "", nil)
	if code != 0 {
		t.Fatalf("runInfo exit = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "(shadows ") {
		t.Fatalf("expected a shadow marker in output:\n%s", out.String())
	}
}

// TestRunInfoBadPkg proves a non-existent filter package makes runInfo exit 1.
func TestRunInfoBadPkg(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	var out, errBuf bytes.Buffer
	code := runInfo(&out, &errBuf, repoRoot, []string{"github.com/gsxhq/gsx/does-not-exist"}, attrclass.Builtin(), "", nil)
	if code != 1 {
		t.Fatalf("runInfo exit = %d, want 1", code)
	}
}
