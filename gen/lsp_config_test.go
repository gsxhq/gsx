package gen

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/lsp"
)

// lspFilterModule writes a temp module (replace-directive'd at the repo root)
// with a local filter sub-package myf exposing Shout (ctx-less) and URL
// (ctx + variadic + (string,error)). Each test writes its own card.gsx and,
// optionally, a gsx.toml. Returns the module dir and a writer for extra files.
func lspFilterModule(t *testing.T) (dir string, must func(p, c string)) {
	t.Helper()
	dir = t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must = func(p, c string) {
		t.Helper()
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("myf/myf.go", "package myf\n\nimport \"context\"\n\nfunc Shout(s string) string { return s + \"!\" }\n\nfunc URL(ctx context.Context, page any, args ...any) (string, error) { return \"/\", nil }\n")
	return dir, must
}

// hasUnknownFilter reports whether pkg carries a codegen "unknown filter %q"
// diagnostic for name — the signal that a pipeline filter did NOT resolve.
func hasUnknownFilter(pkg *lsp.Package, name string) bool {
	for _, d := range pkg.Diags {
		if strings.Contains(d.Message, `unknown filter "`+name+`"`) {
			return true
		}
	}
	return false
}

// A gsx.toml alias resolves a project filter in the LSP.
func TestLSPAnalyzeResolvesTomlAlias(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir, must := lspFilterModule(t)
	must("gsx.toml", "[filters]\nshout = \"example.com/x/myf.Shout\"\n")
	must("card.gsx", "package x\n\ncomponent Card(name string) {\n\t<p>{ name |> shout }</p>\n}\n")
	pkg, err := lspAnalyzer{}.Analyze(dir, nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if hasUnknownFilter(pkg, "shout") {
		t.Fatalf("gsx.toml alias shout not resolved; diags: %v", pkg.Diags)
	}
}

// A ctx-injected, variadic, (R,error) alias resolves — proving the analyzer
// re-derives the seed-first contract from the alias's live signature.
func TestLSPAnalyzeResolvesCtxAlias(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir, must := lspFilterModule(t)
	must("gsx.toml", "[filters]\nurl = \"example.com/x/myf.URL\"\n")
	must("card.gsx", "package x\n\ncomponent Card(name string) {\n\t<a href={ name |> url(\"id\", name) }>x</a>\n}\n")
	pkg, err := lspAnalyzer{}.Analyze(dir, nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if hasUnknownFilter(pkg, "url") {
		t.Fatalf("ctx alias url not resolved; diags: %v", pkg.Diags)
	}
}

// A malformed gsx.toml is ignored: Analyze never errors, the std baseline still
// resolves (upper), the alias does NOT (shout), and a warning is logged.
func TestLSPAnalyzeMalformedConfigFallsBack(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir, must := lspFilterModule(t)
	// Leading unknown TOP-LEVEL key → loadConfig's strict Undecoded check errors.
	must("gsx.toml", "bogusKey = 123\n[filters]\nshout = \"example.com/x/myf.Shout\"\n")
	must("card.gsx", "package x\n\ncomponent Card(name string) {\n\t<p>{ name |> upper }{ name |> shout }</p>\n}\n")
	var warn bytes.Buffer
	pkg, err := lspAnalyzer{warnw: &warn}.Analyze(dir, nil)
	if err != nil {
		t.Fatalf("Analyze must not error on a malformed gsx.toml: %v", err)
	}
	if hasUnknownFilter(pkg, "upper") {
		t.Fatalf("std upper must still resolve under fallback; diags: %v", pkg.Diags)
	}
	if !hasUnknownFilter(pkg, "shout") {
		t.Fatalf("alias shout must NOT resolve when gsx.toml is ignored; diags: %v", pkg.Diags)
	}
	if !strings.Contains(warn.String(), "gsx.toml") {
		t.Fatalf("expected a warning naming gsx.toml, got: %q", warn.String())
	}
}

// No gsx.toml → std baseline: upper resolves, an undeclared filter is unknown.
func TestLSPAnalyzeNoConfigStdBaseline(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir, must := lspFilterModule(t)
	must("card.gsx", "package x\n\ncomponent Card(name string) {\n\t<p>{ name |> upper }{ name |> shout }</p>\n}\n")
	pkg, err := lspAnalyzer{}.Analyze(dir, nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if hasUnknownFilter(pkg, "upper") {
		t.Fatalf("std upper should resolve; diags: %v", pkg.Diags)
	}
	if !hasUnknownFilter(pkg, "shout") {
		t.Fatalf("shout has no alias and must be unknown; diags: %v", pkg.Diags)
	}
}

// In-process opts (as a custom binary built with WithFilter would supply) feed
// the analyzer even with no gsx.toml present.
func TestLSPAnalyzeHonorsInProcessOpts(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir, must := lspFilterModule(t)
	must("card.gsx", "package x\n\ncomponent Card(name string) {\n\t<p>{ name |> shout }</p>\n}\n")
	opt := config{aliases: []codegen.FilterAlias{{Name: "shout", PkgPath: "example.com/x/myf", FuncName: "Shout"}}}
	pkg, err := lspAnalyzer{optCfg: opt}.Analyze(dir, nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if hasUnknownFilter(pkg, "shout") {
		t.Fatalf("in-process opt alias not honored; diags: %v", pkg.Diags)
	}
}

// resolveConfigBestEffort: no file → optCfg unchanged; valid file → merged;
// malformed file → optCfg + a warning, no panic.
func TestResolveConfigBestEffort(t *testing.T) {
	dir, must := lspFilterModule(t)
	opt := config{aliases: []codegen.FilterAlias{{Name: "x", PkgPath: "p", FuncName: "F"}}}

	got := resolveConfigBestEffort(dir, opt, io.Discard)
	if len(got.aliases) != 1 || got.aliases[0].Name != "x" {
		t.Fatalf("no-config: want optCfg unchanged, got %+v", got.aliases)
	}

	must("gsx.toml", "[filters]\nshout = \"example.com/x/myf.Shout\"\n")
	got = resolveConfigBestEffort(dir, opt, io.Discard)
	names := map[string]bool{}
	for _, a := range got.aliases {
		names[a.Name] = true
	}
	if !names["shout"] || !names["x"] {
		t.Fatalf("merged: want both shout (file) and x (opt), got %+v", got.aliases)
	}

	must("gsx.toml", "bogusKey = 1\n")
	var warn bytes.Buffer
	got = resolveConfigBestEffort(dir, opt, &warn)
	if len(got.aliases) != 1 || got.aliases[0].Name != "x" {
		t.Fatalf("malformed: want optCfg fallback, got %+v", got.aliases)
	}
	if !strings.Contains(warn.String(), "gsx.toml") {
		t.Fatalf("malformed: want warning, got %q", warn.String())
	}
}
