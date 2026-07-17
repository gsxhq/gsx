package codegen

import (
	"encoding/base64"
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
// views package, runs GenerateDirs(tmp, []string{viewsDir}, ...) with filterPkgs,
// writes the generated .x.go, compiles a harness rendering `invocation` (package
// alias `p`), and returns the rendered HTML. The module path is fixed to `gsxmf`
// so filterPkgs can reference `gsxmf/myfilters`.
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

	genRes, err := GenerateDirs(tmp, []string{viewsDir}, Options{FilterPkgs: filterPkgs, CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatalf("GenerateDirs: %v", err)
	}
	if hasDiagErrors(genRes[viewsDir].Diags) {
		t.Fatalf("GenerateDirs: unexpected errors: %v", genRes[viewsDir].Diags)
	}
	for gsxPath, src := range genRes[viewsDir].Files {
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
	t.Parallel()
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
		`p.C("hi")`)
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
	t.Parallel()
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
		`p.C("hi")`)
	if !strings.Contains(got, "USER:hi") {
		t.Fatalf("expected user Upper to shadow std (last-wins): want \"USER:hi\"; got:\n%s", got)
	}
}

// TestMultiFilterUserWinsEvenWhenStdListedLast proves std is unconditionally the
// lowest-precedence filter base (dedupFilterPkgs always forces it first): listing
// std AFTER the user package in filter_packages no longer makes std win. This
// behavior intentionally replaced the older "list std last to win" convention so a
// user can override an individual built-in (e.g. dataURL) without dropping the
// rest of std by omission; see docs/superpowers/specs/2026-07-06-data-image-resource-url-design.md
// ("std as the lowest-precedence filter base").
func TestMultiFilterUserWinsEvenWhenStdListedLast(t *testing.T) {
	t.Parallel()
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
		`p.C("hi")`)
	if !strings.Contains(got, "USER:hi") {
		t.Fatalf("expected user Upper to win regardless of list order (std is always lowest-precedence): want \"USER:hi\"; got:\n%s", got)
	}
}

// TestUserDataURLOverridesStd proves the user-wins-over-std mechanism (std as
// the lowest-precedence filter base, see TestMultiFilterUserWinsEvenWhenStdListedLast)
// applies specifically to the built-in dataURL filter: a user-registered DataURL
// shadows std.DataURL for `|> dataURL(...)`, while an unrelated std filter
// (upper) in the SAME component still resolves to std, proving the override is
// surgical rather than dropping the rest of std by omission. The user's DataURL
// prepends a "USER:" marker into the encoded payload so its result remains a
// VALID data:image/...;base64,... URL — passing the runtime's image-resource
// sanitizer (writer.go's URLImage / escape.go's isImageDataURL, which checks
// only the data: scheme + MIME allow-list + ";base64" marker, not payload
// content) — yet is byte-for-byte distinguishable from std.DataURL's plain
// encoding of the same input.
func TestUserDataURLOverridesStd(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping go-build filter test in -short mode")
	}
	myfilters := `package myfilters

import "encoding/base64"

func DataURL(subject []byte, mime string) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(append([]byte("USER:"), subject...))
}
`
	views := map[string]string{
		"views.gsx": `package views

component C(png []byte, s string) {
	<p><img src={ png |> dataURL("image/png") } alt="a" /><span>{ s |> upper }</span></p>
}
`,
	}
	got := renderWithFilters(t, myfilters, views,
		[]string{stdImportPath, "gsxmf/myfilters"},
		`p.C([]byte("hi"), "hi")`)

	wantUserDataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("USER:hi"))
	if !strings.Contains(got, wantUserDataURL) {
		t.Fatalf("expected user DataURL to shadow std.DataURL: want %q in:\n%s", wantUserDataURL, got)
	}
	stdDataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("hi"))
	if strings.Contains(got, stdDataURL) {
		t.Fatalf("std.DataURL leaked through despite user override: got:\n%s", got)
	}
	if !strings.Contains(got, "HI") {
		t.Fatalf("expected unrelated std filter upper to still resolve to std: got:\n%s", got)
	}
}

// TestMultiFilterUnknownErrors proves an unknown filter (present in neither
// package) is a clean codegen error.
func TestMultiFilterUnknownErrors(t *testing.T) {
	t.Parallel()
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

	unkRes, unkErr := GenerateDirs(tmp, []string{viewsDir}, Options{FilterPkgs: []string{stdImportPath, "gsxmf/myfilters"}, CSSMinify: true, JSMinify: true}, nil)
	if unkErr != nil {
		t.Fatalf("GenerateDirs returned unexpected hard error: %v", unkErr)
	}
	unkDr := unkRes[viewsDir]
	if !hasDiagErrors(unkDr.Diags) {
		t.Fatal("expected error diagnostics for unknown filter \"nope\"")
	}
	var foundUnknown bool
	for _, d := range unkDr.Diags {
		if strings.Contains(d.Message, "unknown filter") {
			foundUnknown = true
			break
		}
	}
	if !foundUnknown {
		t.Fatalf("expected unknown-filter diagnostic; got: %v", unkDr.Diags)
	}
}

// TestFilterAliasAssignment checks the deterministic alias scheme directly:
// stdImportPath keeps _gsxstd; each non-std package gets a stable _gsxf<i> by
// position among non-std packages, independent of where std sits in the list.
func TestFilterAliasAssignment(t *testing.T) {
	t.Parallel()
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
