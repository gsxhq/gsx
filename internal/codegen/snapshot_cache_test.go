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

func TestPackageResultCacheHitAndMiss(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	comp := filepath.Join(root, "components")
	r1, err := m.Package(comp)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := m.Package(comp)
	if err != nil {
		t.Fatal(err)
	}
	if r1 != r2 {
		t.Errorf("repeat Package(comp) with no edit must hit the cache (same pointer); got distinct results")
	}
	// Edit components → its dirty closure drops the cached result → re-analysis → new pointer.
	m.SetOverride(filepath.Join(comp, "card.gsx"), componentsEdited)
	r3, err := m.Package(comp)
	if err != nil {
		t.Fatal(err)
	}
	if r3 == r1 {
		t.Errorf("Package(comp) after an edit must re-analyze (different pointer); got the stale cached result")
	}
}

func TestPackageResultCacheDependencyInvalidation(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	util := filepath.Join(root, "util")
	comp := filepath.Join(root, "components")
	solo := filepath.Join(root, "solo")
	rc1, err := m.Package(comp)
	if err != nil {
		t.Fatal(err)
	}
	rs1, err := m.Package(solo)
	if err != nil {
		t.Fatal(err)
	}
	// Edit util (a dep of components): components' cached result must drop; solo (unrelated) stays.
	m.SetOverride(filepath.Join(util, "util.gsx"),
		[]byte("package util\n\ncomponent Y(label string) {\n\t<em>{label}</em>\n}\n"))
	rc2, err := m.Package(comp)
	if err != nil {
		t.Fatal(err)
	}
	if rc2 == rc1 {
		t.Errorf("editing dep util must invalidate components' cached result (different pointer)")
	}
	rs2, err := m.Package(solo)
	if err != nil {
		t.Fatal(err)
	}
	if rs2 != rs1 {
		t.Errorf("editing util must NOT drop unrelated solo's cached result (same pointer expected)")
	}
}
