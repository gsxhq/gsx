package codegen

import (
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
