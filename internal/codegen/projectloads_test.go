package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProjectLoadsHasOneLoadSite is the drift guard for the go-list load
// budget. ProjectLoadCalls is what TestWatchSession_EditLoadBudget measures,
// and it can only be trusted while every packages.Load in this package is
// counted. An uninstrumented ninth call site would silently void that gate
// instead of failing it, so the counter lives in exactly one wrapper —
// loadPackages — and this test pins that the wrapper is the package's only
// direct caller of packages.Load.
func TestProjectLoadsHasOneLoadSite(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	sites := map[string]int{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(string(source), "packages.Load("); n != 0 {
			sites[name] = n
		}
	}
	if len(sites) != 1 || sites["loadpackages.go"] != 1 {
		t.Fatalf("packages.Load( call sites = %v, want exactly one in loadpackages.go; every load must go through loadPackages so projectLoads counts it", sites)
	}
}
