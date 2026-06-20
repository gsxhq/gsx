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
	table, err := loadFilterTable(repoRoot(t))
	if err != nil {
		t.Fatalf("loadFilterTable: %v", err)
	}

	cases := []struct {
		name     string
		funcName string
		kind     filterKind
	}{
		{"upper", "Upper", filterBare},
		{"lower", "Lower", filterBare},
		{"trim", "Trim", filterBare},
		{"truncate", "Truncate", filterParam},
		{"join", "Join", filterParam},
		{"default", "Default", filterParam},
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
		if e.kind != c.kind {
			t.Errorf("filter %q: kind = %v, want %v", c.name, e.kind, c.kind)
		}
	}

	if _, ok := table.lookup("bogus"); ok {
		t.Errorf("filter %q: unexpectedly present", "bogus")
	}
}
