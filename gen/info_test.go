package gen

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/codegen"
)

// TestRunInfoStd drives runInfo against the repo root (a module that resolves
// std) and checks the output lists std filters and the filter-packages header.
func TestRunInfoStd(t *testing.T) {
	t.Parallel()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	code := runInfo(&out, &bytes.Buffer{}, repoRoot, "", []string{stdImportPath}, nil, attrclass.Builtin(), "", nil, nil, MinifyNone, MinifyNone)
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
	t.Parallel()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	code := runInfo(&out, &bytes.Buffer{}, repoRoot, "", []string{stdImportPath}, nil, attrclass.Builtin(), "", nil, nil, MinifyNone, MinifyNone)
	if code != 0 {
		t.Fatalf("runInfo exit = %d, want 0", code)
	}
	if strings.Contains(out.String(), "gsx gsx") {
		t.Fatalf("info version line has doubled prefix:\n%s", out.String())
	}
}

// TestRunInfoShadow proves a shadowed filter is marked with "(shadows ".
func TestRunInfoShadow(t *testing.T) {
	t.Parallel()
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
	code := runInfo(&out, &bytes.Buffer{}, tmp, "", []string{stdImportPath, "gsxmf/myfilters"}, nil, attrclass.Builtin(), "", nil, nil, MinifyNone, MinifyNone)
	if code != 0 {
		t.Fatalf("runInfo exit = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "(shadows ") {
		t.Fatalf("expected a shadow marker in output:\n%s", out.String())
	}
}

// TestRunInfoBadPkg proves a non-existent filter package makes runInfo exit 1.
func TestRunInfoBadPkg(t *testing.T) {
	t.Parallel()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	var out, errBuf bytes.Buffer
	code := runInfo(&out, &errBuf, repoRoot, "", []string{"github.com/gsxhq/gsx/does-not-exist"}, nil, attrclass.Builtin(), "", nil, nil, MinifyNone, MinifyNone)
	if code != 1 {
		t.Fatalf("runInfo exit = %d, want 1", code)
	}
}

// TestRunInfoPrintsConfigBeforeFilterError proves the I3 fix: info prints the
// banner + `config: <path>` line BEFORE resolving filters, so an unresolvable
// alias still shows which config is in effect (spec §6 debugging scenario). The
// exit stays nonzero, but the config line must already be on stdout.
func TestRunInfoPrintsConfigBeforeFilterError(t *testing.T) {
	t.Parallel()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(repoRoot, "gsx.toml")
	aliases := []codegen.FilterAlias{{Name: "x", PkgPath: "github.com/gsxhq/gsx/does-not-exist", FuncName: "F"}}
	var out, errBuf bytes.Buffer
	code := runInfo(&out, &errBuf, repoRoot, cfgPath, nil, aliases, attrclass.Builtin(), "", nil, nil, MinifyNone, MinifyNone)
	if code != 1 {
		t.Fatalf("runInfo exit = %d, want 1 (alias targets a non-resolvable package)", code)
	}
	if !strings.Contains(out.String(), "config: "+cfgPath) {
		t.Fatalf("info must print the config line on stdout even when alias resolution fails; got stdout:\n%s", out.String())
	}
}

// TestRunInfo_MinifyAndEnv proves gsx info reports the resolved minify levels
// and an Environment section listing each registered env var.
func TestRunInfo_MinifyAndEnv(t *testing.T) {
	t.Setenv("GSX_MINIFY", "full")
	var out, errb bytes.Buffer
	// css=none/js=none reflect the explicitly-passed MinifyNone levels; GSX_MINIFY
	// appears in the Environment section because it is set.
	code := runInfo(&out, &errb, ".", "", nil, nil, nil, "", nil,
		[]string{}, MinifyNone, MinifyNone)
	if code != 0 {
		t.Fatalf("runInfo exit %d, stderr=%s", code, errb.String())
	}
	s := out.String()
	if !strings.Contains(s, "minify: css=none js=none") {
		t.Fatalf("missing minify line:\n%s", s)
	}
	if !strings.Contains(s, "GSX_MINIFY") || !strings.Contains(s, "full") {
		t.Fatalf("missing env section:\n%s", s)
	}
}
