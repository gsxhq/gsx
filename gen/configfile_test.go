package gen

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/gsxfmt"
)

// mkfile writes content to path, creating parent dirs.
func mkfile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLoadConfigAllKeys decodes each schema key and asserts the resolved config.
func TestLoadConfigAllKeys(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "gsx.toml")
	mkfile(t, path, `
filterPackages = ["github.com/gsxhq/gsx/std", "example.com/myfilters"]

[filters]
url    = "github.com/jackielii/structpages.URLFor"
id     = "github.com/jackielii/structpages.ID"

[[urlAttrs]]
name = "data-href"
`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(cfg.filterPkgs) != 2 || cfg.filterPkgs[0] != "github.com/gsxhq/gsx/std" || cfg.filterPkgs[1] != "example.com/myfilters" {
		t.Fatalf("filterPkgs = %v", cfg.filterPkgs)
	}
	// Aliases are emitted sorted by name (id before url) for determinism.
	if len(cfg.aliases) != 2 {
		t.Fatalf("aliases = %+v", cfg.aliases)
	}
	if cfg.aliases[0].Name != "id" || cfg.aliases[0].PkgPath != "github.com/jackielii/structpages" || cfg.aliases[0].FuncName != "ID" {
		t.Fatalf("alias[0] = %+v", cfg.aliases[0])
	}
	if cfg.aliases[1].Name != "url" || cfg.aliases[1].FuncName != "URLFor" {
		t.Fatalf("alias[1] = %+v", cfg.aliases[1])
	}
	if len(cfg.urlRules) != 1 || cfg.urlRules[0].Name != "data-href" {
		t.Fatalf("urlRules = %+v", cfg.urlRules)
	}
}

// TestSplitPkgFunc covers the shared alias-string parser, including a dotted
// final path segment (gopkg.in/x.F) and non-exported rejection.
func TestSplitPkgFunc(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		pkg     string
		fn      string
		wantErr bool
	}{
		{"github.com/jackielii/structpages.URLFor", "github.com/jackielii/structpages", "URLFor", false},
		{"gopkg.in/x.F", "gopkg.in/x", "F", false},
		{"example.com/p.helper", "", "", true},   // non-exported
		{"github.com/foo/bar.T.M", "", "", true}, // method value (dotted final seg)
		{"noPackageQualified", "", "", true},
	}
	for _, c := range cases {
		pkg, fn, err := splitPkgFunc(c.in)
		if c.wantErr {
			if err == nil {
				t.Fatalf("splitPkgFunc(%q): expected error, got (%q,%q)", c.in, pkg, fn)
			}
			continue
		}
		if err != nil {
			t.Fatalf("splitPkgFunc(%q): %v", c.in, err)
		}
		if pkg != c.pkg || fn != c.fn {
			t.Fatalf("splitPkgFunc(%q) = (%q,%q), want (%q,%q)", c.in, pkg, fn, c.pkg, c.fn)
		}
	}
}

// TestLoadConfigBadAlias proves a non-exported alias target errors clearly,
// naming the path and the alias key.
func TestLoadConfigBadAlias(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "gsx.toml")
	mkfile(t, path, "[filters]\nbad = \"example.com/p.helper\"\n")
	_, err := loadConfig(path)
	if err == nil {
		t.Fatal("expected error for non-exported alias target")
	}
	if !strings.Contains(err.Error(), "bad") || !strings.Contains(err.Error(), path) {
		t.Fatalf("error should name the path + alias key; got: %v", err)
	}
}

// TestLoadConfigUnknownKey proves strict decoding rejects an unknown key.
func TestLoadConfigUnknownKey(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "gsx.toml")
	mkfile(t, path, "filteres = [\"x\"]\n") // typo
	_, err := loadConfig(path)
	if err == nil {
		t.Fatal("expected error for unknown key")
	}
	if !strings.Contains(err.Error(), "filteres") || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("error should name the unknown key; got: %v", err)
	}
}

func TestLoadConfigRejectsJSAttrs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gsx.toml")
	mkfile(t, path, "[[jsAttrs]]\nname = \"wire:click\"\n")
	_, err := loadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "unknown key") || !strings.Contains(err.Error(), "jsAttrs") {
		t.Fatalf("loadConfig err = %v, want unknown jsAttrs", err)
	}
}

func TestLoadConfigRejectsCSSAttrs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gsx.toml")
	mkfile(t, path, "[[cssAttrs]]\nname = \"data-style\"\n")
	_, err := loadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "unknown key") || !strings.Contains(err.Error(), "cssAttrs") {
		t.Fatalf("loadConfig err = %v, want unknown cssAttrs", err)
	}
}

// TestLoadConfigBothNamePrefix proves a rule with both name+prefix is rejected.
func TestLoadConfigBothNamePrefix(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "gsx.toml")
	mkfile(t, path, "[[urlAttrs]]\nname = \"a\"\nprefix = \"b\"\n")
	_, err := loadConfig(path)
	if err == nil {
		t.Fatal("expected error for both name+prefix")
	}
	if !strings.Contains(err.Error(), "urlAttrs") {
		t.Fatalf("error should name the rule table; got: %v", err)
	}
}

// TestDiscoverConfigMissing proves a tree with no gsx.toml yields (",false).
func TestDiscoverConfigMissing(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	sub := filepath.Join(tmp, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if path, ok := discoverConfig(sub); ok {
		t.Fatalf("expected no config, got %q", path)
	}
}

// TestDiscoverConfigAcrossModuleBoundary proves the walk crosses a go.mod
// boundary: a repo-root gsx.toml (bounded by .git) is found from a nested
// sub-module dir.
func TestDiscoverConfigAcrossModuleBoundary(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	rootCfg := filepath.Join(tmp, "gsx.toml")
	mkfile(t, rootCfg, "filters = []\n")
	mkfile(t, filepath.Join(tmp, "sub", "go.mod"), "module sub\n\ngo 1.26.1\n")
	views := filepath.Join(tmp, "sub", "views")
	if err := os.MkdirAll(views, 0o755); err != nil {
		t.Fatal(err)
	}
	got, ok := discoverConfig(views)
	if !ok || got != rootCfg {
		t.Fatalf("discoverConfig = (%q,%v), want (%q,true)", got, ok, rootCfg)
	}
}

// TestDiscoverConfigNearerWins proves a nearer gsx.toml overrides an ancestor.
func TestDiscoverConfigNearerWins(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	mkfile(t, filepath.Join(tmp, "gsx.toml"), "filters = []\n")
	nearer := filepath.Join(tmp, "sub", "gsx.toml")
	mkfile(t, nearer, "filters = []\n")
	views := filepath.Join(tmp, "sub", "views")
	if err := os.MkdirAll(views, 0o755); err != nil {
		t.Fatal(err)
	}
	got, ok := discoverConfig(views)
	if !ok || got != nearer {
		t.Fatalf("discoverConfig = (%q,%v), want (%q,true)", got, ok, nearer)
	}
}

// TestDiscoverConfigStopsAtGitRoot proves a gsx.toml ABOVE the .git repo root
// is NOT used (the walk is bounded at the repo root, inclusive).
func TestDiscoverConfigStopsAtGitRoot(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	// gsx.toml above the repo root — must NOT be used.
	mkfile(t, filepath.Join(tmp, "gsx.toml"), "filters = []\n")
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	views := filepath.Join(repo, "views")
	if err := os.MkdirAll(views, 0o755); err != nil {
		t.Fatal(err)
	}
	if path, ok := discoverConfig(views); ok {
		t.Fatalf("expected no config (above-repo gsx.toml must be ignored), got %q", path)
	}
}

// TestDiscoverConfigNonRepoFallback proves that without a .git anywhere, the
// walk falls back to the module root (go.mod) as the stop and finds a gsx.toml
// beside go.mod.
func TestDiscoverConfigNonRepoFallback(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	modCfg := filepath.Join(tmp, "gsx.toml")
	mkfile(t, modCfg, "filters = []\n")
	mkfile(t, filepath.Join(tmp, "go.mod"), "module ex\n\ngo 1.26.1\n")
	views := filepath.Join(tmp, "views")
	if err := os.MkdirAll(views, 0o755); err != nil {
		t.Fatal(err)
	}
	got, ok := discoverConfig(views)
	if !ok || got != modCfg {
		t.Fatalf("discoverConfig = (%q,%v), want (%q,true)", got, ok, modCfg)
	}
}

// TestConfigAgnosticCommandsSurviveMalformedConfig proves a malformed gsx.toml in
// the tree does NOT break config-agnostic commands (version, fmt) — only generate
// and info load config, so they alone fail (exit 2) on the bad config. This is
// the I1+I2 regression: config used to load unconditionally before dispatch, so a
// typo'd gsx.toml broke `gsx version` etc.
func TestConfigAgnosticCommandsSurviveMalformedConfig(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Malformed: an unknown key strict-decode rejects (the same typo as the
	// unknown-key unit test). loadConfig fails before any module work.
	mkfile(t, filepath.Join(tmp, "gsx.toml"), "filteres = [\"x\"]\n")

	// version + fmt must succeed (exit 0) despite the bad config in the tree.
	for _, args := range [][]string{
		{"-C", tmp, "version"},
		{"-C", tmp, "fmt"},
	} {
		var out, errb bytes.Buffer
		if code := run(args, &out, &errb); code != 0 {
			t.Fatalf("run %v exit=%d, want 0 (malformed gsx.toml must not break a config-agnostic command); stderr=%q", args, code, errb.String())
		}
	}

	// generate + info must still fail (exit 2) on the malformed config, naming it.
	for _, args := range [][]string{
		{"-C", tmp, "generate"},
		{"-C", tmp, "info"},
	} {
		var out, errb bytes.Buffer
		code := run(args, &out, &errb)
		if code != 2 {
			t.Fatalf("run %v exit=%d, want 2 on malformed config; stderr=%q", args, code, errb.String())
		}
		if !strings.Contains(errb.String(), "gsx.toml") {
			t.Fatalf("run %v stderr should name the config path; got: %q", args, errb.String())
		}
	}
}

// TestMergeConfigOptsOverride proves opts append after base (opts win last) and
// func-valued opts override base.
func TestMergeConfigOptsOverride(t *testing.T) {
	t.Parallel()
	base := applyOpts()
	base.filterPkgs = []string{"a"}
	base.aliases = []codegen.FilterAlias{{Name: "base", PkgPath: "p", FuncName: "Base"}}
	opts := applyOpts()
	opts.filterPkgs = []string{"b"}
	opts.aliases = []codegen.FilterAlias{{Name: "base", PkgPath: "p", FuncName: "Override"}}

	merged := mergeConfig(base, opts)
	if len(merged.filterPkgs) != 2 || merged.filterPkgs[0] != "a" || merged.filterPkgs[1] != "b" {
		t.Fatalf("filterPkgs = %v", merged.filterPkgs)
	}
	// base alias first, opt alias appended after (last-wins → opt overrides).
	if len(merged.aliases) != 2 || merged.aliases[0].FuncName != "Base" || merged.aliases[1].FuncName != "Override" {
		t.Fatalf("aliases = %+v", merged.aliases)
	}
}

func TestConfigPrintWidth(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "gsx.toml")
	if err := os.WriteFile(path, []byte("[formatter]\nprint_width = 100\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if got := cfg.effectivePrintWidth(); got != 100 {
		t.Fatalf("print_width = %d, want 100", got)
	}
}

// TestConfigPrintWidthOldKeyRejected pins the rename: the pre-[formatter]
// top-level printWidth key is an unknown key, surfaced by strict decoding.
func TestConfigPrintWidthOldKeyRejected(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "gsx.toml")
	if err := os.WriteFile(path, []byte("printWidth = 100\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "printWidth") {
		t.Fatalf("loadConfig err = %v, want unknown-key error naming printWidth", err)
	}
}

func TestConfigPrintWidthDefault(t *testing.T) {
	t.Parallel()
	var c config
	if got := c.effectivePrintWidth(); got != 80 {
		t.Fatalf("default printWidth = %d, want 80", got)
	}
}

// TestLoadConfigClassMerger proves class_merger = "pkg.Func" is accepted and
// parsed into cfg.classMerger with the correct PkgPath and FuncName.
func TestLoadConfigClassMerger(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "gsx.toml")
	mkfile(t, path, `class_merger = "example.com/twcfg.Merge"`)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.classMerger == nil || cfg.classMerger.PkgPath != "example.com/twcfg" || cfg.classMerger.FuncName != "Merge" {
		t.Fatalf("got %+v", cfg.classMerger)
	}
}

// TestLoadConfigClassMergerBadValue proves a non-qualified ref (no dot, no
// package path) is rejected with a clear error.
func TestLoadConfigClassMergerBadValue(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "gsx.toml")
	mkfile(t, path, `class_merger = "noDotHere"`)
	if _, err := loadConfig(path); err == nil {
		t.Fatalf("want error for unqualified ref")
	}
}

// TestConfigImportsMode: [formatter] imports selects the mode.
func TestConfigImportsMode(t *testing.T) {
	for _, tc := range []struct {
		toml string
		want gsxfmt.ImportsMode
	}{
		{"[formatter]\nimports = \"gofmt\"\n", gsxfmt.ImportsGofmt},
		{"[formatter]\nimports = \"goimports\"\n", gsxfmt.ImportsGoimports},
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, "gsx.toml")
		if err := os.WriteFile(path, []byte(tc.toml), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadConfig(path)
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if got := cfg.effectiveImportsMode(); got != tc.want {
			t.Fatalf("imports = %v, want %v", got, tc.want)
		}
	}
}

// TestConfigImportsModeDefault: absent key ⇒ goimports.
func TestConfigImportsModeDefault(t *testing.T) {
	var c config
	if got := c.effectiveImportsMode(); got != gsxfmt.ImportsGoimports {
		t.Fatalf("default imports mode = %v, want goimports", got)
	}
}

// TestConfigImportsModeInvalid: an unknown spelling errors, naming the key and
// both valid values.
func TestConfigImportsModeInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gsx.toml")
	if err := os.WriteFile(path, []byte("[formatter]\nimports = \"gofumpt\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadConfig(path)
	if err == nil {
		t.Fatal("want error for invalid imports mode")
	}
	for _, want := range []string{"formatter.imports", "gofmt", "goimports"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

// TestConfigImportsModeUnknownKeyRejected: strict decoding still rejects typos
// inside [formatter].
func TestConfigImportsModeUnknownKeyRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gsx.toml")
	if err := os.WriteFile(path, []byte("[formatter]\nimport = \"gofmt\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), "import") {
		t.Fatalf("loadConfig err = %v, want unknown-key error naming import", err)
	}
}
