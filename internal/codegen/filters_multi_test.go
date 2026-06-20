package codegen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeMultiFile writes content to dir/name, failing the test on error. It is a
// local helper for the multi-package filter tests so this file is self-contained
// (the parallel test-reorg may move the shared writeFile).
func writeMultiFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// renderWithFilters mirrors renderPackage but drives the multi-package filter
// backend: it lays out a temp module containing a `myfilters` package and a
// views package, runs GeneratePackageWithFilters(viewsDir, filterPkgs), writes
// the generated .x.go, compiles a harness rendering `invocation` (package alias
// `p`), and returns the rendered HTML. The module path is fixed to `gsxmf` so
// filterPkgs can reference `gsxmf/myfilters`.
func renderWithFilters(t *testing.T, myfilters string, views map[string]string, filterPkgs []string, invocation string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping go-run render test in -short mode")
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
	writeMultiFile(t, mfDir, "myfilters.go", myfilters)

	viewsDir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(viewsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range views {
		writeMultiFile(t, viewsDir, name, content)
	}

	gen, err := GeneratePackageWithFilters(viewsDir, filterPkgs)
	if err != nil {
		t.Fatalf("GeneratePackageWithFilters: %v", err)
	}
	for gsxPath, src := range gen {
		base := strings.TrimSuffix(filepath.Base(gsxPath), ".gsx")
		writeMultiFile(t, viewsDir, base+".x.go", string(src))
	}

	writeMultiFile(t, tmp, "main.go", `package main

import (
	"context"
	"os"

	"github.com/gsxhq/gsx"
	p "gsxmf/views"
)

var _ = gsx.Raw

func main() {
	_ = `+invocation+`.Render(context.Background(), os.Stdout)
}
`)
	cmd := exec.Command("go", "run", ".")
	cmd.Dir = tmp
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run failed: %v\n%s", err, out)
	}
	return string(out)
}

// TestMultiFilterUserAndStd proves a user filter package resolves under the
// reserved _gsxf0 alias (shout → myfilters.Shout) WHILE a std filter in the SAME
// interpolation file still resolves under _gsxstd (upper → std.Upper).
func TestMultiFilterUserAndStd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go-build filter test in -short mode")
	}
	myfilters := `package myfilters

func Shout(s string) string { return s + "!" }
`
	views := map[string]string{
		"views.gsx": `package views

component C(n string) {
	<p>{ n |> shout } <span>{ n |> upper }</span></p>
}
`,
	}
	got := renderWithFilters(t, myfilters, views,
		[]string{stdImportPath, "gsxmf/myfilters"},
		`p.C(p.CProps{N: "hi"})`)
	if !strings.Contains(got, "hi!") {
		t.Fatalf("expected user filter shout to render \"hi!\"; got:\n%s", got)
	}
	if !strings.Contains(got, "HI") {
		t.Fatalf("expected std filter upper to render \"HI\"; got:\n%s", got)
	}
}

// TestMultiFilterLastWins proves last-wins precedence: a user Upper listed AFTER
// std shadows std's built-in upper.
func TestMultiFilterLastWins(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go-build filter test in -short mode")
	}
	myfilters := `package myfilters

func Upper(s string) string { return "USER:" + s }
`
	views := map[string]string{
		"views.gsx": `package views

component C(n string) {
	<p>{ n |> upper }</p>
}
`,
	}
	got := renderWithFilters(t, myfilters, views,
		[]string{stdImportPath, "gsxmf/myfilters"},
		`p.C(p.CProps{N: "hi"})`)
	if !strings.Contains(got, "USER:hi") {
		t.Fatalf("expected user Upper to shadow std (last-wins): want \"USER:hi\"; got:\n%s", got)
	}
}

// TestMultiFilterStdWinsWhenListedLast proves order matters: with std listed
// AFTER the user package, std's upper wins for the same name.
func TestMultiFilterStdWinsWhenListedLast(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go-build filter test in -short mode")
	}
	myfilters := `package myfilters

func Upper(s string) string { return "USER:" + s }
`
	views := map[string]string{
		"views.gsx": `package views

component C(n string) {
	<p>{ n |> upper }</p>
}
`,
	}
	got := renderWithFilters(t, myfilters, views,
		[]string{"gsxmf/myfilters", stdImportPath},
		`p.C(p.CProps{N: "hi"})`)
	if !strings.Contains(got, "HI") || strings.Contains(got, "USER:") {
		t.Fatalf("expected std Upper to win (listed last): want \"HI\"; got:\n%s", got)
	}
}

// TestMultiFilterUnknownErrors proves an unknown filter (present in neither
// package) is a clean codegen error.
func TestMultiFilterUnknownErrors(t *testing.T) {
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
	writeMultiFile(t, mfDir, "myfilters.go", "package myfilters\n\nfunc Shout(s string) string { return s + \"!\" }\n")
	viewsDir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(viewsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMultiFile(t, viewsDir, "views.gsx", "package views\n\ncomponent C(n string) {\n\t<p>{ n |> nope }</p>\n}\n")

	_, err = GeneratePackageWithFilters(viewsDir, []string{stdImportPath, "gsxmf/myfilters"})
	if err == nil {
		t.Fatal("expected error for unknown filter \"nope\"")
	}
	if !strings.Contains(err.Error(), "unknown filter") {
		t.Fatalf("expected unknown-filter error; got: %v", err)
	}
}

// TestFilterAliasAssignment checks the deterministic alias scheme directly:
// stdImportPath keeps _gsxstd; each non-std package gets a stable _gsxf<i> by
// position among non-std packages, independent of where std sits in the list.
func TestFilterAliasAssignment(t *testing.T) {
	got := filterAliases([]string{stdImportPath, "example.com/a", "example.com/b"})
	want := map[string]string{
		stdImportPath:   "_gsxstd",
		"example.com/a": "_gsxf0",
		"example.com/b": "_gsxf1",
	}
	for path, wantAlias := range want {
		if got[path] != wantAlias {
			t.Fatalf("alias[%q] = %q, want %q", path, got[path], wantAlias)
		}
	}
	// std anywhere in the list still maps to _gsxstd and does not consume an
	// _gsxf index.
	got2 := filterAliases([]string{"example.com/a", stdImportPath, "example.com/b"})
	if got2["example.com/a"] != "_gsxf0" || got2["example.com/b"] != "_gsxf1" || got2[stdImportPath] != "_gsxstd" {
		t.Fatalf("alias scheme not stable across std position: %v", got2)
	}
}
