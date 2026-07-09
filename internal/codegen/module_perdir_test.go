package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupPerDirModule builds a module with two gsx packages, each with its OWN
// filter package, plus a class-merger package. Both filter packages export a
// filter named "shout" with different bodies, so a table leak is observable in
// the generated output rather than merely in a count.
func setupPerDirModule(t *testing.T, perDir map[string]DirOptions, loadPkgs []string) (*Module, string) {
	t.Helper()
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	repoRoot = filepath.Dir(repoRoot)
	must := func(p, c string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("afilters/f.go", "package afilters\n\nfunc Shout(s string) string { return s + \"!A\" }\n")
	must("bfilters/f.go", "package bfilters\n\nfunc Shout(s string) string { return s + \"!B\" }\n")
	must("mrg/mrg.go", "package mrg\n\nfunc Keep(cs []string) string { return cs[0] }\n")
	must("a/a.gsx", "package a\n\ncomponent A(s string) {\n\t<p>{ s |> shout }</p>\n}\n")
	must("b/b.gsx", "package b\n\ncomponent B(s string) {\n\t<p>{ s |> shout }</p>\n}\n")

	abs := map[string]DirOptions{}
	for d, o := range perDir {
		abs[filepath.Join(root, d)] = o
	}
	m, err := Open(Options{
		ModuleRoot: root, ModulePath: "example.com/x",
		FilterPkgs: []string{StdImportPath},
		LoadPkgs:   loadPkgs,
		PerDir:     abs,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return m, root
}

// TestPerDirFilterTablesIsolated is the load-bearing test for the union/per-dir
// split: two dirs, two different filter packages, ONE Module. Each dir must see
// only its own filter, and the whole thing must cost one packages.Load.
func TestPerDirFilterTablesIsolated(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	loadPkgs := []string{"example.com/x/afilters", "example.com/x/bfilters"}
	m, root := setupPerDirModule(t, map[string]DirOptions{
		"a": {FilterPkgs: []string{StdImportPath, "example.com/x/afilters"}},
		"b": {FilterPkgs: []string{StdImportPath, "example.com/x/bfilters"}},
	}, loadPkgs)

	genOf := func(dir string) string {
		out, diags, err := m.Generate(filepath.Join(root, dir))
		if err != nil {
			t.Fatalf("generate %s: %v", dir, err)
		}
		for _, d := range diags {
			t.Fatalf("generate %s: unexpected diagnostic: %s", dir, d.Message)
		}
		var sb strings.Builder
		for _, b := range out {
			sb.Write(b)
		}
		return sb.String()
	}

	genA, genB := genOf("a"), genOf("b")

	// Dir a resolved shout -> afilters.Shout, and must not even mention bfilters.
	if !strings.Contains(genA, "afilters") || strings.Contains(genA, "bfilters") {
		t.Errorf("dir a resolved the wrong filter package\n%s", genA)
	}
	if !strings.Contains(genB, "bfilters") || strings.Contains(genB, "afilters") {
		t.Errorf("dir b resolved the wrong filter package\n%s", genB)
	}

	// The whole point: one go-list for the importer, and ZERO filter-table loads,
	// because both tables were harvested from the types that load already produced.
	if got := m.externalLoads(); got != 1 {
		t.Errorf("externalLoads = %d; want 1", got)
	}
	if got := m.filterTableLoads(); got != 0 {
		t.Errorf("filterTableLoads = %d; want 0 (per-dir tables must harvest from loaded types, not packages.Load)", got)
	}
}

// TestPerDirFilterTableRejectsForeignFilter is the anti-vacuity guard. `shout`
// exists in bfilters and bfilters IS loaded — but dir a does not whitelist it,
// so the pipe must be rejected. If per-dir narrowing ever degrades to "use the
// union", this test is the only thing that notices.
func TestPerDirFilterTableRejectsForeignFilter(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupPerDirModule(t, map[string]DirOptions{
		"a": {FilterPkgs: []string{StdImportPath}}, // std only: no shout
	}, []string{"example.com/x/afilters", "example.com/x/bfilters"})

	_, diags, err := m.Generate(filepath.Join(root, "a"))
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	var found bool
	for _, d := range diags {
		if strings.Contains(d.Message, "shout") {
			found = true
		}
	}
	if !found {
		t.Fatalf("dir a whitelists only std, yet `shout` resolved: the union leaked into the per-dir table. diags=%v", diags)
	}
}

// TestPerDirUnloadedFilterPkgIsHardError: naming a filter package that was never
// loaded must fail loudly. An empty table here would silently disable every
// filter for that dir.
//
// The package must live OUTSIDE the module: the importer's load ends with
// "./...", so any in-module package is loaded whether or not LoadPkgs names it.
// A mistyped or non-vendored filter package is the case that actually reaches
// this guard.
func TestPerDirUnloadedFilterPkgIsHardError(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupPerDirModule(t, map[string]DirOptions{
		"a": {FilterPkgs: []string{StdImportPath, "example.com/x/missing"}},
	}, nil)

	_, _, err := m.Generate(filepath.Join(root, "a"))
	if err == nil {
		t.Fatal("expected a hard error for an unloaded filter package; got nil")
	}
	if !strings.Contains(err.Error(), "was not loaded") {
		t.Fatalf("wrong error: %v", err)
	}
}

// TestPerDirClassMergerApplies pins the merger half of DirOptions.
func TestPerDirClassMergerApplies(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupPerDirModule(t, map[string]DirOptions{
		"a": {
			FilterPkgs:  []string{StdImportPath, "example.com/x/afilters"},
			ClassMerger: &ClassMergerRef{PkgPath: "example.com/x/mrg", FuncName: "Keep"},
		},
		"b": {FilterPkgs: []string{StdImportPath, "example.com/x/bfilters"}},
	}, []string{"example.com/x/afilters", "example.com/x/bfilters", "example.com/x/mrg"})

	if err := m.validatePerDirMergers(); err != nil {
		t.Fatalf("validatePerDirMergers: %v", err)
	}
	if got := m.classMergerFor(filepath.Join(root, "a")); got == nil || got.FuncName != "Keep" {
		t.Errorf("dir a merger = %v; want mrg.Keep", got)
	}
	if got := m.classMergerFor(filepath.Join(root, "b")); got != nil {
		t.Errorf("dir b merger = %v; want nil (inherit)", got)
	}
	// Validation must not cost a second load beyond the importer's.
	if got := m.externalLoads(); got != 1 {
		t.Errorf("externalLoads = %d; want 1 (merger validated from loaded types)", got)
	}
}

// TestPerDirUnloadedMergerIsHardError guards the merger half the same way.
func TestPerDirUnloadedMergerIsHardError(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, _ := setupPerDirModule(t, map[string]DirOptions{
		"a": {ClassMerger: &ClassMergerRef{PkgPath: "example.com/x/nope", FuncName: "Keep"}},
	}, nil)
	err := m.validatePerDirMergers()
	if err == nil || !strings.Contains(err.Error(), "was not loaded") {
		t.Fatalf("expected hard error for unloaded merger package; got %v", err)
	}
}
