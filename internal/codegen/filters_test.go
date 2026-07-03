package codegen

import (
	"os"
	"path/filepath"
	"testing"
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
	repoRoot := repoRoot(t)
	tmp := t.TempDir()

	// Write go.mod with a replace directive pointing to the real gsx repo
	modContent := "module testfilters\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => " + repoRoot + "\n"
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
	table, err := loadFilterTableMulti(tmp, []string{"testfilters/filters"}, nil)
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
