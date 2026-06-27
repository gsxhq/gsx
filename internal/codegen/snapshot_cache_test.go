package codegen

import (
	"path/filepath"
	"testing"
)

func TestInvalidateDropsPkgResults(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	util := filepath.Join(root, "util")
	comp := filepath.Join(root, "components")
	pages := filepath.Join(root, "pages")
	solo := filepath.Join(root, "solo")
	// Warm the import graph (analyze records util←components←pages edges). In Task 1
	// Package does NOT yet populate pkgResults, and no SetOverride was called, so
	// applyDirty is a no-op — we seed pkgResults by hand AFTER warming.
	if _, err := m.Package(pages); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Package(solo); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	for _, d := range []string{util, comp, pages, solo} {
		m.pkgResults[d] = &PackageResult{}
	}
	m.mu.Unlock()
	// Invalidate util's reverse closure {util, components, pages}; solo is unrelated.
	m.Invalidate(util)
	got := m.cachedResultDirs()
	if len(got) != 1 || got[0] != solo {
		t.Errorf("Invalidate(util) must drop the util-importer closure from pkgResults and keep solo; remaining=%v", got)
	}
}

func TestRebuildClearsPkgResults(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	comp := filepath.Join(root, "components")
	m.mu.Lock()
	m.pkgResults[comp] = &PackageResult{}
	m.mu.Unlock()
	m.rebuildFset()
	if got := m.cachedResultDirs(); len(got) != 0 {
		t.Errorf("rebuildFset must clear pkgResults; remaining=%v", got)
	}
}
