package codegen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestOpenReadsFsetRebuildThreshold(t *testing.T) {
	// Default when env unset.
	t.Setenv("GSX_FSET_REBUILD_BYTES", "")
	os.Unsetenv("GSX_FSET_REBUILD_BYTES")
	m, err := Open(Options{ModuleRoot: t.TempDir(), ModulePath: "example.com/x"})
	if err != nil {
		t.Fatal(err)
	}
	if m.fsetRebuildBytes != defaultFsetRebuildBytes {
		t.Errorf("default threshold = %d, want %d", m.fsetRebuildBytes, defaultFsetRebuildBytes)
	}
	// Env override (including 0 = disabled).
	for _, tc := range []struct {
		env  string
		want int
	}{{"4096", 4096}, {"0", 0}, {"bogus", defaultFsetRebuildBytes}, {"-5", defaultFsetRebuildBytes}} {
		t.Setenv("GSX_FSET_REBUILD_BYTES", tc.env)
		m, err := Open(Options{ModuleRoot: t.TempDir(), ModulePath: "example.com/x"})
		if err != nil {
			t.Fatal(err)
		}
		if m.fsetRebuildBytes != tc.want {
			t.Errorf("env %q: threshold = %d, want %d", tc.env, m.fsetRebuildBytes, tc.want)
		}
	}
}

func TestFsetGrowthIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	util := filepath.Join(root, "util")
	utilFile := filepath.Join(util, "util.gsx")
	// Force frequent rebuilds: tiny threshold.
	m.fsetRebuildBytes = 4096
	var maxBase int
	for i := 0; i < 12; i++ {
		// Each edit changes content (distinct label text) so the dir is marked dirty
		// and re-parsed, growing the fset.
		src := fmt.Appendf(nil, "package util\n\ncomponent Y(label string) {\n\t<span>%d:{label}</span>\n}\n", i)
		m.SetOverride(utilFile, src)
		if _, err := m.Package(util); err != nil {
			t.Fatalf("edit %d: %v", i, err)
		}
		if b := m.fset.Base(); b > maxBase {
			maxBase = b
		}
	}
	if m.rebuilds() == 0 {
		t.Fatalf("expected ≥1 rebuild under a 4 KiB threshold over 12 edits; got 0 (maxBase=%d)", maxBase)
	}
	// Bounded: the final fset.Base() reflects at most a post-rebuild baseline + a bit,
	// far below the unbounded 12×-growth it would reach without rebuilds.
	t.Logf("rebuilds=%d finalBase=%d maxBase=%d", m.rebuilds(), m.fset.Base(), maxBase)
}

func TestFsetRebuildDisabledAtZero(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	m.fsetRebuildBytes = 0 // disabled
	util := filepath.Join(root, "util")
	utilFile := filepath.Join(util, "util.gsx")
	for i := 0; i < 5; i++ {
		m.SetOverride(utilFile, fmt.Appendf(nil, "package util\n\ncomponent Y(label string) {\n\t<b>%d:{label}</b>\n}\n", i))
		if _, err := m.Package(util); err != nil {
			t.Fatal(err)
		}
	}
	if m.rebuilds() != 0 {
		t.Errorf("threshold 0 must disable rebuilding; got %d rebuilds", m.rebuilds())
	}
}

// utilSymbolDeclPos finds the declaration position of the symbol named symName
// from a package whose import path ends in pkgSuffix, as recorded in pr.Info.Uses.
// It uses pr.Fset (the Module-wide shared FileSet) to resolve the declaration position.
// Returns the absolute filename and 1-indexed line, or calls t.Fatal if not found.
func utilSymbolDeclPos(t *testing.T, pr *PackageResult, symName, pkgSuffix string) (filename string, line int) {
	t.Helper()
	seen := map[string]bool{}
	for _, obj := range pr.Info.Uses {
		if obj == nil || obj.Name() != symName {
			continue
		}
		p := obj.Pkg()
		if p == nil || !strings.HasSuffix(p.Path(), pkgSuffix) {
			continue
		}
		pos := pr.Fset.Position(obj.Pos())
		if pos.Line <= 0 || seen[pos.Filename] {
			continue
		}
		seen[pos.Filename] = true
		return pos.Filename, pos.Line
	}
	t.Fatalf("no Info.Uses entry for %q from package ending in %q", symName, pkgSuffix)
	return "", -1
}

// TestCrossPkgResolutionSurvivesRebuild proves that after a FileSet rebuild,
// cross-package symbol declaration positions remain correct. It warms components
// (which imports util and uses util.Y), forces a rebuild by lowering fsetRebuildBytes
// below current FileSet growth, re-analyzes, and asserts that util.Y still resolves
// to util.gsx at the same line — proving the new FileSet has consistent positions
// and ext was reloaded (no orphaned positions from the old fset).
func TestCrossPkgResolutionSurvivesRebuild(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	comp := filepath.Join(root, "components")

	// Phase 1: warm components, which imports util and calls util.Y.
	pr1, err := m.Package(comp)
	if err != nil {
		t.Fatal(err)
	}
	file1, line1 := utilSymbolDeclPos(t, pr1, "Y", "util")
	if !strings.HasSuffix(file1, "util.gsx") {
		t.Fatalf("pre-rebuild: filename must end in util.gsx (not .x.go), got %q", file1)
	}

	// Phase 2: force a rebuild by setting fsetRebuildBytes=1. The first Package call
	// already grew fset.Base() past fsetBaseline+1 (ext load + parse), so the next
	// Package call triggers rebuildFset before analyzing.
	m.fsetRebuildBytes = 1
	pr2, err := m.Package(comp)
	if err != nil {
		t.Fatal(err)
	}
	if m.rebuilds() == 0 {
		t.Fatal("expected a rebuild (fsetRebuildBytes=1 must fire on second Package)")
	}

	// Assert: util.Y still resolves to util.gsx at the same line in the new fset.
	// A stale/orphaned position would resolve to a wrong filename or line 0.
	file2, line2 := utilSymbolDeclPos(t, pr2, "Y", "util")
	if !strings.HasSuffix(file2, "util.gsx") {
		t.Errorf("post-rebuild: filename must end in util.gsx (not .x.go), got %q", file2)
	}
	if line2 != line1 {
		t.Errorf("post-rebuild: util.Y decl line = %d, want %d", line2, line1)
	}
}

// TestGraphSurvivesRebuild proves that the import graph (imports/importedBy) is not
// cleared by rebuildFset, so reverse-dependency invalidation keeps working after a
// FileSet rebuild. If rebuildFset were to clear the graph, editing util post-rebuild
// would fail to evict components and pages from the cache.
func TestGraphSurvivesRebuild(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	util := filepath.Join(root, "util")
	comp := filepath.Join(root, "components")
	pages := filepath.Join(root, "pages")
	solo := filepath.Join(root, "solo")

	// Warm the full chain and record the import graph.
	if _, err := m.Package(pages); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Package(solo); err != nil {
		t.Fatal(err)
	}

	// Force a rebuild by lowering the threshold below current growth.
	m.fsetRebuildBytes = 1
	if _, err := m.Package(pages); err != nil {
		t.Fatal(err)
	}
	if m.rebuilds() == 0 {
		t.Fatal("expected a rebuild to have occurred")
	}

	// Graph edges must survive the rebuild: rebuildFset must NOT clear imports/importedBy.
	_, rev := m.importGraphSnapshot()
	assertEdge(t, rev, util, comp)
	assertEdge(t, rev, comp, pages)

	// Disable further rebuilds so the post-rebuild re-warm is stable.
	m.fsetRebuildBytes = 0

	// Re-warm all dirs (rebuild cleared pkgTypes; repopulate for invalidation test).
	if _, err := m.Package(pages); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Package(solo); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{util, comp, pages, solo} {
		if !contains(m.cachedDirs(), d) {
			t.Fatalf("expected %s cached after post-rebuild re-warm; got %v", d, m.cachedDirs())
		}
	}

	// Edit util → reverse closure must evict util+comp+pages; solo must stay warm.
	m.SetOverride(filepath.Join(util, "util.gsx"),
		[]byte("package util\n\ncomponent Y(label string) {\n\t<em>{label}</em>\n}\n"))
	m.applyDirty()
	cached := m.cachedDirs()
	if contains(cached, comp) || contains(cached, pages) {
		t.Errorf("post-rebuild: editing util must evict comp+pages via importedBy graph; cached=%v", cached)
	}
	if !contains(cached, solo) {
		t.Errorf("post-rebuild: solo must remain warm (not in util's closure); cached=%v", cached)
	}
}

// TestRebuildFsetPreservesGraph is a focused, load-bearing test that proves
// rebuildFset() itself does NOT clear imports/importedBy. It calls rebuildFset
// directly, then immediately snapshots the graph — no re-analysis can re-establish
// edges in between. If someone made rebuildFset also clear the import graph, this
// test fails (whereas TestGraphSurvivesRebuild would not, because it re-analyzes
// the whole chain after the rebuild, which re-records every edge).
func TestRebuildFsetPreservesGraph(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	util := filepath.Join(root, "util")
	comp := filepath.Join(root, "components")
	pages := filepath.Join(root, "pages")

	// Warm the graph at the default threshold (no rebuild will trigger).
	if _, err := m.Package(pages); err != nil {
		t.Fatal(err)
	}
	if m.rebuilds() != 0 {
		t.Fatalf("unexpected rebuild during warm-up: got %d", m.rebuilds())
	}

	// Confirm pre-state: reverse edges exist.
	_, rev := m.importGraphSnapshot()
	assertEdge(t, rev, util, comp)
	assertEdge(t, rev, comp, pages)

	// Call rebuildFset DIRECTLY — no Package/Generate call follows.
	m.rebuildFset()

	// Assert counter bumped — confirms the rebuild actually ran.
	if m.rebuilds() != 1 {
		t.Errorf("rebuilds() = %d, want 1", m.rebuilds())
	}

	// Assert pkgTypes cleared — the type cache was reset.
	if cached := m.cachedDirs(); len(cached) != 0 {
		t.Errorf("cachedDirs after rebuildFset = %v, want empty", cached)
	}

	// Assert graph SURVIVED — no re-analysis ran since the rebuild.
	// If rebuildFset cleared imports/importedBy these assertions will fail.
	_, rev2 := m.importGraphSnapshot()
	assertEdge(t, rev2, util, comp)
	assertEdge(t, rev2, comp, pages)
}

// TestGenerateOutputIdenticalAcrossRebuild proves that Module.Generate produces
// byte-identical output before and after a forced FileSet rebuild.
func TestGenerateOutputIdenticalAcrossRebuild(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	comp := filepath.Join(root, "components")
	before, _, err := m.Generate(comp)
	if err != nil {
		t.Fatal(err)
	}
	// Force a rebuild, then regenerate the SAME (unedited) package.
	m.fsetRebuildBytes = 1 // any growth triggers a rebuild on the next call
	after, _, err := m.Generate(comp)
	if err != nil {
		t.Fatal(err)
	}
	if m.rebuilds() == 0 {
		t.Fatalf("expected a rebuild")
	}
	for path, b := range before {
		if string(after[path]) != string(b) {
			t.Errorf("file %s changed across rebuild:\n--- before ---\n%s\n--- after ---\n%s", path, b, after[path])
		}
	}
	if len(after) != len(before) {
		t.Errorf("file count changed across rebuild: before=%d after=%d", len(before), len(after))
	}
}

// TestConcurrentRebuildAndPackage verifies that concurrent SetOverride+Package
// calls on the same Module under a small fsetRebuildBytes threshold produce no
// data races. The -race detector is the effective assertion: any race on
// fset/ext/pkgTypes/fsetBaseline/rebuildCount surfaces immediately.
// fsetRebuildBytes must be assigned BEFORE goroutines start because the field is
// not guarded by mu (reads/writes are fine once goroutines are serialized by
// analysisMu, but the initial assignment races with concurrent reads otherwise).
func TestConcurrentRebuildAndPackage(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	m.fsetRebuildBytes = 2048 // assign before goroutines start
	comp := filepath.Join(root, "components")
	card := filepath.Join(comp, "card.gsx")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			m.SetOverride(card, fmt.Appendf(nil, "package components\n\nimport \"example.com/x/util\"\n\ncomponent X(title string) {\n\t<div>%d<util.Y label={ title }/></div>\n}\n", i))
			_, _ = m.Package(comp)
		}(i)
	}
	wg.Wait()
}
