package codegen

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveFiltersStd resolves the std filter package and checks the table is
// sorted by Name and carries the expected entries with empty Shadows.
func TestResolveFiltersStd(t *testing.T) {
	t.Parallel()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	infos, _, err := ResolveFilters(repoRoot, []string{stdImportPath}, nil, nil)
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
	if upper.Ctx {
		t.Fatalf("upper should be ctx-less, got Ctx=true")
	}
	if len(upper.Shadows) != 0 {
		t.Fatalf("upper.Shadows = %v, want empty", upper.Shadows)
	}
	trunc, ok := byName["truncate"]
	if !ok {
		t.Fatal("expected a \"truncate\" filter")
	}
	if trunc.Ctx {
		t.Fatalf("truncate should be ctx-less, got Ctx=true")
	}
}

// TestResolveFiltersEmptyDefaultsToStd proves an empty filterPkgs defaults to
// the std package (matching the std-filter default).
func TestResolveFiltersEmptyDefaultsToStd(t *testing.T) {
	t.Parallel()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	infos, _, err := ResolveFilters(repoRoot, nil, nil, nil)
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
	t.Parallel()
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

	infos, _, err := ResolveFilters(tmp, []string{stdImportPath, "gsxmf/myfilters"}, nil, nil)
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

// TestDedupFilterPkgsAlwaysIncludesStd proves dedupFilterPkgs always prepends
// stdImportPath as the FIRST (lowest-precedence) entry, whether the input is
// empty, a non-std user list, or already contains std explicitly.
func TestDedupFilterPkgsAlwaysIncludesStd(t *testing.T) {
	// Empty -> just std.
	if got := dedupFilterPkgs(nil); len(got) != 1 || got[0] != stdImportPath {
		t.Fatalf("dedupFilterPkgs(nil) = %v, want [%s]", got, stdImportPath)
	}
	// Non-empty without std -> std is prepended as lowest precedence.
	got := dedupFilterPkgs([]string{"example.com/userfilters"})
	if len(got) != 2 || got[0] != stdImportPath || got[1] != "example.com/userfilters" {
		t.Fatalf("dedupFilterPkgs(user) = %v, want [std, user]", got)
	}
	// std listed explicitly (anywhere) -> not duplicated, still first.
	got = dedupFilterPkgs([]string{"example.com/userfilters", stdImportPath})
	if len(got) != 2 || got[0] != stdImportPath || got[1] != "example.com/userfilters" {
		t.Fatalf("dedupFilterPkgs(user,std) = %v, want [std, user]", got)
	}
}

// TestResolveFiltersBadPkg proves a non-existent filter package is a clean error.
func TestResolveFiltersBadPkg(t *testing.T) {
	t.Parallel()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = ResolveFilters(repoRoot, []string{"github.com/gsxhq/gsx/does-not-exist"}, nil, nil)
	if err == nil {
		t.Fatal("expected error for non-existent filter package")
	}
}

// TestResolveFiltersRenderers proves ResolveFilters also resolves the
// registered [renderers] (sharing the same packages.Load as the filter
// packages), sorted by TypeKey, with no Shadows tracked (harvestRenderers'
// table is last-wins only — see RendererInfo's doc comment).
func TestResolveFiltersRenderers(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-load renderer test in -short mode")
	}
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	writeMultiFile(t, tmp, "go.mod", "module gsxmf\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pgDir := filepath.Join(tmp, "pg")
	if err := os.MkdirAll(pgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMultiFile(t, pgDir, "pg.go", "package pg\n\ntype Text struct {\n\tString string\n\tValid  bool\n}\n")
	rendDir := filepath.Join(tmp, "rend")
	if err := os.MkdirAll(rendDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMultiFile(t, rendDir, "rend.go", "package rend\n\nimport \"gsxmf/pg\"\n\nfunc PgText(t pg.Text) string { return t.String }\n")

	renderers := []RendererAlias{{TypeKey: "gsxmf/pg.Text", PkgPath: "gsxmf/rend", FuncName: "PgText"}}
	_, rinfos, err := ResolveFilters(tmp, []string{stdImportPath}, nil, renderers)
	if err != nil {
		t.Fatalf("ResolveFilters: %v", err)
	}
	if len(rinfos) != 1 {
		t.Fatalf("rinfos = %+v, want exactly one entry", rinfos)
	}
	ri := rinfos[0]
	if ri.TypeKey != "gsxmf/pg.Text" || ri.Pkg != "gsxmf/rend" || ri.Func != "PgText" || ri.HasErr {
		t.Fatalf("unexpected RendererInfo: %+v", ri)
	}
}

// TestResolveFunctionsLocalRenderer proves the module-aware info resolver uses
// the Module's one external importer plus local declaration resolver. Repeated
// resolution on the same Module must reuse both and never fall back to the
// standalone filter packages.Load path.
func TestResolveFunctionsLocalRenderer(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-load renderer test in -short mode")
	}
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeMultiFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pgDir := filepath.Join(root, "pg")
	if err := os.MkdirAll(pgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMultiFile(t, pgDir, "pg.go", "package pg\n\ntype Text struct { Value string }\n")
	rendererDir := filepath.Join(root, "renderers")
	if err := os.MkdirAll(rendererDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMultiFile(t, rendererDir, "package.go", "package renderers\n")
	writeMultiFile(t, rendererDir, "renderers.gsx", `package renderers

import "example.com/app/pg"

func RenderText(v pg.Text) (string, error) {
	return v.Value, nil
}
`)
	renderers := []RendererAlias{{TypeKey: "example.com/app/pg.Text", PkgPath: "example.com/app/renderers", FuncName: "RenderText"}}
	m, err := Open(Options{
		ModuleRoot: root,
		ModulePath: "example.com/app",
		FilterPkgs: []string{stdImportPath},
		Renderers:  renderers,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		infos, rinfos, err := m.resolveFunctions()
		if err != nil {
			t.Fatalf("resolveFunctions call %d: %v", i+1, err)
		}
		if len(infos) == 0 {
			t.Fatal("expected std filter info")
		}
		if len(rinfos) != 1 {
			t.Fatalf("rinfos = %+v, want one renderer", rinfos)
		}
		want := RendererInfo{TypeKey: "example.com/app/pg.Text", Pkg: "example.com/app/renderers", Func: "RenderText", HasErr: true}
		if rinfos[0] != want {
			t.Fatalf("rinfos[0] = %+v, want %+v", rinfos[0], want)
		}
	}
	if got := m.externalLoads(); got != 1 {
		t.Fatalf("externalLoads = %d, want 1 after repeated resolution", got)
	}
	if got := m.filterTableLoads(); got != 0 {
		t.Fatalf("filterTableLoads = %d, want 0 (reuse external importer types)", got)
	}
}
