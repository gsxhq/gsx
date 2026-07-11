package codegen

import (
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/gsxhq/gsx/ast"
)

// repoRoot resolves the module root from the test's working dir. Tests run in
// internal/codegen, so the root is two levels up.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return root
}

// harvestFixture creates a temp module with a filter package containing the
// given source, loads it, and returns the harvested filterTable. This helper
// supports writing tests that verify filter entry properties (like hasErr).
func harvestFixture(t *testing.T, source string) filterTable {
	t.Helper()
	root := repoRoot(t)
	tmp := t.TempDir()

	// Write go.mod with a replace directive pointing to the real gsx repo
	modContent := "module testfilters\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => " + root + "\n"
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte(modContent), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	// Create a filter package directory with the source
	filterDir := filepath.Join(tmp, "filters")
	if err := os.Mkdir(filterDir, 0o755); err != nil {
		t.Fatalf("mkdir filters: %v", err)
	}
	if err := os.WriteFile(filepath.Join(filterDir, "filters.go"), []byte(source), 0o644); err != nil {
		t.Fatalf("write filters.go: %v", err)
	}

	// Load and harvest the filter package
	table, _, err := loadFilterTableMulti(tmp, []string{"testfilters/filters"}, nil, nil)
	if err != nil {
		t.Fatalf("loadFilterTableMulti: %v", err)
	}
	return table
}

func TestFilterHarvest(t *testing.T) {
	t.Parallel()
	table, err := loadFilterTable(repoRoot(t))
	if err != nil {
		t.Fatalf("loadFilterTable: %v", err)
	}

	// All std filters are seed-first and ctx-less under the new contract.
	cases := []struct {
		name     string
		funcName string
		wantsCtx bool
	}{
		{"upper", "Upper", false},
		{"lower", "Lower", false},
		{"trim", "Trim", false},
		{"truncate", "Truncate", false},
		{"join", "Join", false},
		{"default", "Default", false},
	}
	for _, c := range cases {
		e, ok := table.lookup(c.name)
		if !ok {
			t.Errorf("filter %q: missing from table", c.name)
			continue
		}
		if e.funcName != c.funcName {
			t.Errorf("filter %q: funcName = %q, want %q", c.name, e.funcName, c.funcName)
		}
		if e.wantsCtx != c.wantsCtx {
			t.Errorf("filter %q: wantsCtx = %v, want %v", c.name, e.wantsCtx, c.wantsCtx)
		}
	}

	if _, ok := table.lookup("bogus"); ok {
		t.Errorf("filter %q: unexpectedly present", "bogus")
	}
}

func TestHarvestHasErr(t *testing.T) {
	t.Parallel()
	// Fixture package source:
	//   func Plain(s string) string                  → hasErr=false
	//   func Fallible(s string) (string, error)      → hasErr=true
	//   func CtxFallible(ctx context.Context, s string) (int, error) → hasErr=true, wantsCtx=true
	//   func Generic[T any](s []T) (T, error)        → hasErr=true
	table := harvestFixture(t, `package filters

import "context"

func Plain(s string) string { return s }
func Fallible(s string) (string, error) { return s, nil }
func CtxFallible(ctx context.Context, s string) (int, error) { return 0, nil }
func Generic[T any](s []T) (T, error) { var z T; return z, nil }
`)
	for name, want := range map[string]bool{
		"plain": false, "fallible": true, "ctxFallible": true, "generic": true,
	} {
		e, ok := table.lookup(name)
		if !ok {
			t.Fatalf("filter %q not harvested", name)
		}
		if e.hasErr != want {
			t.Errorf("%s: hasErr = %v, want %v", name, e.hasErr, want)
		}
	}
}

// harvestFixtureFromTypes mirrors harvestFixture but exercises the
// harvestFromTypes/loadFilterTableFromTypes path (bundle.go) instead of the
// packages.Load-based harvestFilters. That path is what the WASM playground
// uses once packages are already type-checked (e.g. reconstructed from an
// embedded typebundle): it takes a pre-built map[string]*types.Package rather
// than doing its own packages.Load. Here packages.Load is used ONLY to obtain
// real *types.Package values for the fixture (standing in for a typebundle
// reconstruction); the harvest itself goes through harvestFromTypes, not
// harvestFilters, so it exercises the second, parallel construction site.
func harvestFixtureFromTypes(t *testing.T, source string) filterTable {
	t.Helper()
	root := repoRoot(t)
	tmp := t.TempDir()

	modContent := "module testfilters\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => " + root + "\n"
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte(modContent), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	filterDir := filepath.Join(tmp, "filters")
	if err := os.Mkdir(filterDir, 0o755); err != nil {
		t.Fatalf("mkdir filters: %v", err)
	}
	if err := os.WriteFile(filepath.Join(filterDir, "filters.go"), []byte(source), 0o644); err != nil {
		t.Fatalf("write filters.go: %v", err)
	}

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedImports | packages.NeedDeps,
		Dir:  tmp,
	}
	pkgPath := "testfilters/filters"
	pkgs, err := packages.Load(cfg, pkgPath)
	if err != nil {
		t.Fatalf("packages.Load: %v", err)
	}
	byPath := map[string]*types.Package{}
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.Types != nil {
			byPath[p.PkgPath] = p.Types
		}
	})

	table, _, err := loadFilterTableFromTypes(byPath, []string{pkgPath}, nil, nil)
	if err != nil {
		t.Fatalf("loadFilterTableFromTypes: %v", err)
	}
	return table
}

// TestHarvestFromTypesHasErr is the bundle-path counterpart to
// TestHarvestHasErr: same fixture, but harvested through harvestFromTypes
// (bundle.go) instead of harvestFilters (filters.go). Regression coverage for
// the bug where bundle.go's filterEntry literals omitted hasErr entirely, so
// error-returning filters harvested via the WASM/typebundle path silently got
// hasErr == false.
func TestHarvestFromTypesHasErr(t *testing.T) {
	t.Parallel()
	table := harvestFixtureFromTypes(t, `package filters

import "context"

func Plain(s string) string { return s }
func Fallible(s string) (string, error) { return s, nil }
func CtxFallible(ctx context.Context, s string) (int, error) { return 0, nil }
`)
	for name, want := range map[string]bool{
		"plain": false, "fallible": true, "ctxFallible": true,
	} {
		e, ok := table.lookup(name)
		if !ok {
			t.Fatalf("filter %q not harvested", name)
		}
		if e.hasErr != want {
			t.Errorf("%s: hasErr = %v, want %v", name, e.hasErr, want)
		}
	}
}

func TestLowerPipeMidStageErr(t *testing.T) {
	table := funcTables{filters: filterTable{
		"parse": {funcName: "Parse", alias: "_gsxf0", pkgPath: "m/f", hasErr: true},
		"join":  {funcName: "Join", alias: "_gsxstd", pkgPath: "github.com/gsxhq/gsx/std"},
	}}
	stages := []ast.PipeStage{{Name: "parse"}, {Name: "join", HasArgs: true, Args: `" "`}}

	// probe-style wrap
	got, _, err := lowerPipe("csv", stages, table, func(c string) string { return "_gsxunwrap(" + c + ")" })
	if err != nil {
		t.Fatal(err)
	}
	want := `_gsxstd.Join(_gsxunwrap(_gsxf0.Parse((csv))), " ")`
	if got != want {
		t.Errorf("probe form:\n got %s\nwant %s", got, want)
	}

	// final-stage hasErr is NOT wrapped
	got2, _, _ := lowerPipe("csv", []ast.PipeStage{{Name: "parse"}}, table, func(c string) string { return "WRAPPED" })
	if got2 != "_gsxf0.Parse((csv))" {
		t.Errorf("final stage must stay unwrapped, got %s", got2)
	}

	// nil wrap + mid-stage hasErr → friendly error
	_, _, err = lowerPipe("csv", stages, table, nil)
	if err == nil || !strings.Contains(err.Error(), `filter "parse" returns (R, error)`) {
		t.Errorf("nil-wrap error = %v", err)
	}
}
