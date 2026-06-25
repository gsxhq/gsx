package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/codegen"
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
	tmp := t.TempDir()
	path := filepath.Join(tmp, "gsx.toml")
	mkfile(t, path, `
filters = ["github.com/gsxhq/gsx/std", "example.com/myfilters"]

[aliases]
url    = "github.com/jackielii/structpages.URLFor"
id     = "github.com/jackielii/structpages.ID"

[[urlAttrs]]
name = "data-href"
[[jsAttrs]]
prefix = "data-on-"
[[cssAttrs]]
name = "data-style"
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
	if len(cfg.jsRules) != 1 || cfg.jsRules[0].Prefix != "data-on-" {
		t.Fatalf("jsRules = %+v", cfg.jsRules)
	}
	if len(cfg.cssRules) != 1 || cfg.cssRules[0].Name != "data-style" {
		t.Fatalf("cssRules = %+v", cfg.cssRules)
	}
}

// TestSplitPkgFunc covers the shared alias-string parser, including a dotted
// final path segment (gopkg.in/x.F) and non-exported rejection.
func TestSplitPkgFunc(t *testing.T) {
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
	tmp := t.TempDir()
	path := filepath.Join(tmp, "gsx.toml")
	mkfile(t, path, "[aliases]\nbad = \"example.com/p.helper\"\n")
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

// TestLoadConfigBothNamePrefix proves a rule with both name+prefix is rejected.
func TestLoadConfigBothNamePrefix(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "gsx.toml")
	mkfile(t, path, "[[jsAttrs]]\nname = \"a\"\nprefix = \"b\"\n")
	_, err := loadConfig(path)
	if err == nil {
		t.Fatal("expected error for both name+prefix")
	}
	if !strings.Contains(err.Error(), "jsAttrs") {
		t.Fatalf("error should name the rule table; got: %v", err)
	}
}

// TestDiscoverConfigMissing proves a tree with no gsx.toml yields (",false).
func TestDiscoverConfigMissing(t *testing.T) {
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

// TestMergeConfigOptsOverride proves opts append after base (opts win last) and
// func-valued opts override base.
func TestMergeConfigOptsOverride(t *testing.T) {
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
