package codegen

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/gsxhq/gsx/internal/diag"
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

// diagFingerprints builds a sorted slice of "severity:message" strings from a
// diagnostic slice so two slices can be compared order-independently.
func diagFingerprints(ds []diag.Diagnostic) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = fmt.Sprintf("%v:%s", d.Severity, d.Message)
	}
	sort.Strings(out)
	return out
}

// TestSnapshotCacheHitResolvesAndDiagsParity proves three properties of a cache-hit
// PackageResult:
//
//	(a) The second Package call returns the SAME pointer (it is genuinely a hit —
//	    the test is non-tautological only when r2==r1).
//	(b) The hit result resolves util.Y's declaration to util.gsx (not .x.go) at the
//	    same line as the fresh analysis (positions are not orphaned).
//	(c) The hit result's diagnostics match a fresh analysis on an identical module
//	    (same count and severity:message pairs, order-independent). Using a SECOND
//	    Module on identical sources gives a deterministic fresh baseline without
//	    bypassing the cache — we compare Message+Severity only, never filenames,
//	    because the two Modules use different tempdirs.
func TestSnapshotCacheHitResolvesAndDiagsParity(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	comp := filepath.Join(root, "components")

	// Warm: first Package call analyzes and populates pkgResults[comp].
	r1, err := m.Package(comp)
	if err != nil {
		t.Fatal(err)
	}
	file1, line1 := utilSymbolDeclPos(t, r1, "Y", "util")
	if !strings.HasSuffix(file1, "util.gsx") {
		t.Fatalf("warm: filename must end in util.gsx (not .x.go), got %q", file1)
	}

	// Hit: second Package call (no edit) must return the SAME pointer.
	// Non-tautology: we assert r2==r1 FIRST, so the subsequent position and diags
	// checks are verified against the cached result, not a fresh one.
	r2, err := m.Package(comp)
	if err != nil {
		t.Fatal(err)
	}
	if r2 != r1 {
		t.Fatalf("second Package(comp) must be a cache hit (same pointer); got distinct results — cache miss means the following position/diags checks are vacuous")
	}

	// (b) Cached result still resolves to the correct position.
	file2, line2 := utilSymbolDeclPos(t, r2, "Y", "util")
	if !strings.HasSuffix(file2, "util.gsx") {
		t.Errorf("hit: filename must end in util.gsx (not .x.go), got %q", file2)
	}
	if line2 != line1 {
		t.Errorf("hit: util.Y decl line = %d, want %d (same as warm result)", line2, line1)
	}

	// (c) Diags parity: compare against a SECOND fresh Module on identical sources.
	// Two Modules use different tempdirs, so diag.Start.Filename differs — compare
	// only Message + Severity (order-independent via sorted fingerprints).
	m2, root2 := setupChainModule(t)
	rFresh, err := m2.Package(filepath.Join(root2, "components"))
	if err != nil {
		t.Fatalf("fresh module Package: %v", err)
	}
	cachedFP := diagFingerprints(r2.Diags)
	freshFP := diagFingerprints(rFresh.Diags)
	if len(cachedFP) != len(freshFP) {
		t.Errorf("diags parity: cached hit has %d diags, fresh analysis has %d\ncached=%v\nfresh=%v",
			len(cachedFP), len(freshFP), cachedFP, freshFP)
	} else {
		for i := range cachedFP {
			if cachedFP[i] != freshFP[i] {
				t.Errorf("diags parity mismatch at [%d]: cached=%q fresh=%q", i, cachedFP[i], freshFP[i])
			}
		}
	}
}

// TestSnapshotCacheClearedByRebuildStillResolves is the key adversarial test for the
// fset-rebuild path. It proves that after rebuildFset clears pkgResults, a subsequent
// Package call re-analyzes and the new result resolves util.Y correctly.
//
// Non-tautology:
//
//	(a) We assert r2 != r1 — the rebuild actually cleared the cache (if rebuildFset
//	    failed to clear pkgResults, it would return the stale r1 with positions into
//	    the discarded FileSet).
//	(b) We assert m.rebuilds() > 0 — a rebuild actually fired (the threshold was low
//	    enough).
//	(c) We then verify the new result's position — if either (a) or (b) fails, the
//	    test is already flagged, making (c) a genuine new-result check.
func TestSnapshotCacheClearedByRebuildStillResolves(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	comp := filepath.Join(root, "components")

	// Phase 1: warm — populates pkgResults[comp] with r1.
	r1, err := m.Package(comp)
	if err != nil {
		t.Fatal(err)
	}
	_, line1 := utilSymbolDeclPos(t, r1, "Y", "util")

	// Force a rebuild: set threshold below the FileSet growth already accumulated
	// by the warm-up. The next Package call fires maybeRebuildFset → rebuildFset →
	// clears pkgResults → re-analyzes → returns a new pointer.
	m.fsetRebuildBytes = 1

	// Phase 2: re-analyze — must get a NEW pointer (cache was cleared).
	r2, err := m.Package(comp)
	if err != nil {
		t.Fatal(err)
	}
	// (a) Cache was cleared: r2 must be a new allocation.
	if r2 == r1 {
		t.Errorf("post-rebuild Package(comp) must return a NEW pointer (pkgResults must have been cleared by rebuildFset); got the pre-rebuild cached result — if rebuildFset does not clear pkgResults, orphaned positions would be served")
	}
	// (b) A rebuild actually fired.
	if m.rebuilds() == 0 {
		t.Errorf("expected ≥1 rebuild with fsetRebuildBytes=1; got 0 — threshold may not be wired")
	}
	// (c) The new result resolves util.Y correctly (positions are not orphaned).
	file2, line2 := utilSymbolDeclPos(t, r2, "Y", "util")
	if !strings.HasSuffix(file2, "util.gsx") {
		t.Errorf("post-rebuild: filename must end in util.gsx (not .x.go), got %q", file2)
	}
	if line2 != line1 {
		t.Errorf("post-rebuild: util.Y decl line = %d, want %d (same source, same line)", line2, line1)
	}
}

// TestConcurrentPackageResultCache runs 8 goroutines that mix SetOverride+Package
// with plain Package calls on the same Module. The -race detector is the effective
// assertion: any data race on pkgResults (or the fields it is guarded by) surfaces
// as a race report and fails the test.
func TestConcurrentPackageResultCache(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	comp := filepath.Join(root, "components")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%3 == 0 {
				m.SetOverride(filepath.Join(comp, "card.gsx"),
					fmt.Appendf(nil, "package components\n\nimport \"example.com/x/util\"\n\ncomponent X(title string) {\n\t<div>%d<util.Y label={ title }/></div>\n}\n", i))
			}
			_, _ = m.Package(comp)
		}(i)
	}
	wg.Wait()
}
