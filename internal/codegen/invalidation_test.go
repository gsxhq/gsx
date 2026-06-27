package codegen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gsxhq/gsx/internal/diag"
)

// setupChainModule creates a temporary module with a 3-package chain:
//
//	util  ← components  ← pages
//
// plus an unrelated solo package with no project imports. Returns the Module
// and the module root directory.
func setupChainModule(t *testing.T) (*Module, string) {
	t.Helper()
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	repoRoot = filepath.Dir(repoRoot) // internal/codegen -> repo root
	must := func(p, c string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("util/util.gsx", "package util\n\ncomponent Y(label string) {\n\t<span>{label}</span>\n}\n")
	must("components/card.gsx", "package components\n\nimport \"example.com/x/util\"\n\ncomponent X(title string) {\n\t<div><util.Y label={ title }/></div>\n}\n")
	must("pages/page.gsx", "package pages\n\nimport \"example.com/x/components\"\n\ncomponent Page() {\n\t<main><components.X title=\"hello\"/></main>\n}\n")
	must("solo/solo.gsx", "package solo\n\ncomponent Alone() {\n\t<p>alone</p>\n}\n")
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/x", FilterPkgs: []string{StdImportPath}})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return m, root
}

func assertEdge(t *testing.T, g map[string][]string, from, to string) {
	t.Helper()
	for _, s := range g[from] {
		if s == to {
			return
		}
	}
	t.Errorf("expected edge %s -> %s; from-neighbors: %v", from, to, g[from])
}

func TestImportGraphRecorded(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t) // helper: util<-components<-pages + solo; returns Module + module root
	pagesDir := filepath.Join(root, "pages")
	if _, err := m.Package(pagesDir); err != nil {
		t.Fatalf("analyze pages: %v", err)
	}
	if _, err := m.Package(filepath.Join(root, "solo")); err != nil {
		t.Fatalf("analyze solo: %v", err)
	}
	fwd, rev := m.importGraphSnapshot()
	// pages -> components, components -> util.
	assertEdge(t, fwd, filepath.Join(root, "pages"), filepath.Join(root, "components"))
	assertEdge(t, fwd, filepath.Join(root, "components"), filepath.Join(root, "util"))
	// reverse: util importedBy components; components importedBy pages.
	assertEdge(t, rev, filepath.Join(root, "util"), filepath.Join(root, "components"))
	assertEdge(t, rev, filepath.Join(root, "components"), filepath.Join(root, "pages"))
	// solo has no project edges.
	if len(fwd[filepath.Join(root, "solo")]) != 0 {
		t.Errorf("solo should have no deps, got %v", fwd[filepath.Join(root, "solo")])
	}
}

// componentsWithoutUtil is a valid card.gsx that no longer imports util, used
// to verify that recordImports replaces (not appends) forward edges.
var componentsWithoutUtil = []byte("package components\n\ncomponent X(title string) {\n\t<div>{title}</div>\n}\n")

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func TestImportGraphEdgeReplacedOnImportRemoval(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	compDir := filepath.Join(root, "components")
	utilDir := filepath.Join(root, "util")
	if _, err := m.Package(compDir); err != nil {
		t.Fatal(err)
	}
	_, rev := m.importGraphSnapshot()
	assertEdge(t, rev, utilDir, compDir) // components importedBy util initially
	// Edit components to drop its import of util (override with a version that
	// no longer references util).
	m.SetOverride(filepath.Join(compDir, "card.gsx"), componentsWithoutUtil)
	if _, err := m.Package(compDir); err != nil {
		t.Fatal(err)
	}
	_, rev = m.importGraphSnapshot()
	if contains(rev[utilDir], compDir) {
		t.Errorf("after removing import, util.importedBy should not contain components")
	}
}

// utilWithNewExport is a helper.gsx added to the util package, introducing a
// new exported component Z. Used by TestPackageCachesEditedPackageForImporters
// to verify that Package(util) updates pkgTypes[util] so importers see Z.
var utilWithNewExport = []byte("package util\n\ncomponent Z(text string) {\n\t<p>{text}</p>\n}\n")

// componentsUsingZ is a version of card.gsx that imports util and uses util.Z
// (the new export from utilWithNewExport). If pkgTypes[util] is stale (no Z),
// type-checking this file produces an "undefined" diagnostic.
var componentsUsingZ = []byte("package components\n\nimport \"example.com/x/util\"\n\ncomponent X(title string) {\n\t<div><util.Z text={ title }/></div>\n}\n")

// assertResolvesUtilSymbol verifies that the PackageResult for components has
// no error-severity diagnostics, proving that util.Z (the new export) resolved
// correctly — i.e. the fresh pkgTypes[util] (with Z) was used, not a stale one.
// We use the Diags approach rather than Info.Uses because it is robust even when
// card.gsx's skeleton collapses the selector into a synthetic call site.
func assertResolvesUtilSymbol(t *testing.T, pr *PackageResult) {
	t.Helper()
	for _, d := range pr.Diags {
		if d.Severity == diag.Error {
			t.Errorf("unexpected error diagnostic in components after util edit: %s", d.Message)
		}
	}
}

func TestSetOverrideDirtinessDetection(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}

	// Sub-case: new file not on disk, empty content (src==nil, no base) → not dirty.
	// This is the degenerate path: helper.gsx does not exist on disk so
	// os.ReadFile returns nil, and SetOverride sees (haveBase=false, len(src)==0).
	t.Run("new_file_nil_content_not_dirty", func(t *testing.T) {
		m, root := setupChainModule(t)
		utilDir := filepath.Join(root, "util")
		helper := filepath.Join(utilDir, "helper.gsx")
		disk, _ := os.ReadFile(helper) // helper.gsx does not exist → disk == nil
		m.SetOverride(helper, disk)
		if got := m.dirtyDirs(); len(got) != 0 {
			t.Errorf("new-file nil-content override must not mark dirty; got %v", got)
		}
	})

	// Sub-case: buffer bytes == real on-disk file (canonical didOpen/navigation
	// path) → not dirty. Exercises the haveBase && bytes.Equal branch via an
	// actual disk file (util/util.gsx, created by setupChainModule).
	t.Run("buffer_equals_disk_not_dirty", func(t *testing.T) {
		m, root := setupChainModule(t)
		utilDir := filepath.Join(root, "util")
		utilFile := filepath.Join(utilDir, "util.gsx")
		diskBytes, err := os.ReadFile(utilFile)
		if err != nil {
			t.Fatalf("reading util.gsx: %v", err)
		}
		m.SetOverride(utilFile, diskBytes)
		if got := m.dirtyDirs(); len(got) != 0 {
			t.Errorf("override==disk must not mark dirty; got %v", got)
		}
	})

	// The remaining assertions use a fresh module so earlier dirty-marks don't
	// pollute them.
	m, root := setupChainModule(t)
	utilDir := filepath.Join(root, "util")
	helper := filepath.Join(utilDir, "helper.gsx")
	// A real change marks util dirty.
	m.SetOverride(helper, utilWithNewExport)
	if !contains(m.dirtyDirs(), utilDir) {
		t.Errorf("changed override must mark util dirty; got %v", m.dirtyDirs())
	}
	// Re-setting the same changed bytes does not un-mark, but a no-op set of the
	// now-current override bytes adds nothing new.
	m.SetOverride(helper, utilWithNewExport)
	if got := m.dirtyDirs(); !contains(got, utilDir) {
		t.Errorf("dirty must persist until consumed; got %v", got)
	}
}

// componentsEdited is a modified card.gsx that still imports util and uses util.Y,
// but adds a class attribute to the div — a cosmetic change that still compiles.
var componentsEdited = []byte("package components\n\nimport \"example.com/x/util\"\n\ncomponent X(title string) {\n\t<div class=\"card\"><util.Y label={ title }/></div>\n}\n")

// pagesEdited is a new index.gsx file for the pages package — it introduces a
// second component (Index) alongside the existing Page component in page.gsx.
// It still imports and uses components.X so the file type-checks correctly.
var pagesEdited = []byte("package pages\n\nimport \"example.com/x/components\"\n\ncomponent Index() {\n\t<main><components.X title=\"world\"/></main>\n}\n")

// sameSet reports whether two string slices contain exactly the same elements
// (order-independent).
func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]int, len(a))
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		m[s]--
		if m[s] < 0 {
			return false
		}
	}
	return true
}

func TestEditInvalidatesReverseClosureOnly(t *testing.T) {
	m, root := setupChainModule(t)
	util := filepath.Join(root, "util")
	comp := filepath.Join(root, "components")
	pages := filepath.Join(root, "pages")
	solo := filepath.Join(root, "solo")
	// Warm everything.
	for _, d := range []string{pages, solo} {
		if _, err := m.Package(d); err != nil {
			t.Fatal(err)
		}
	}
	// All four cached (pages pulls util+components transitively; solo standalone).
	for _, d := range []string{util, comp, pages, solo} {
		if !contains(m.cachedDirs(), d) {
			t.Fatalf("expected %s cached; got %v", d, m.cachedDirs())
		}
	}
	// Edit components: closure {components, pages} drops; util + solo stay.
	m.SetOverride(filepath.Join(comp, "card.gsx"), componentsEdited)
	m.applyDirty() // simulate the start of the next analysis
	cached := m.cachedDirs()
	if contains(cached, comp) || contains(cached, pages) {
		t.Errorf("components/pages should be invalidated; cached=%v", cached)
	}
	if !contains(cached, util) || !contains(cached, solo) {
		t.Errorf("util/solo must stay warm; cached=%v", cached)
	}
}

func TestEditLeafInvalidatesOnlyItself(t *testing.T) {
	m, root := setupChainModule(t)
	pages := filepath.Join(root, "pages")
	if _, err := m.Package(pages); err != nil {
		t.Fatal(err)
	}
	m.SetOverride(filepath.Join(pages, "index.gsx"), pagesEdited)
	m.applyDirty()
	// util + components (pages' deps, nothing imports pages) stay cached.
	if !contains(m.cachedDirs(), filepath.Join(root, "util")) ||
		!contains(m.cachedDirs(), filepath.Join(root, "components")) {
		t.Errorf("editing pages (a leaf importer) must not invalidate its deps; cached=%v", m.cachedDirs())
	}
}

func TestNoOpEditInvalidatesNothing(t *testing.T) {
	m, root := setupChainModule(t)
	comp := filepath.Join(root, "components")
	if _, err := m.Package(filepath.Join(root, "pages")); err != nil {
		t.Fatal(err)
	}
	before := m.cachedDirs()
	disk, _ := os.ReadFile(filepath.Join(comp, "card.gsx"))
	m.SetOverride(filepath.Join(comp, "card.gsx"), disk) // identical
	m.applyDirty()
	if got := m.cachedDirs(); !sameSet(got, before) {
		t.Errorf("no-op edit must not invalidate; before=%v after=%v", before, got)
	}
}

func TestImporterReResolvesAgainstEditedDependency(t *testing.T) {
	m, root := setupChainModule(t)
	util := filepath.Join(root, "util")
	comp := filepath.Join(root, "components")
	if _, err := m.Package(comp); err != nil {
		t.Fatal(err)
	} // warms util+components
	// Change a util export that components depends on, then re-analyze components
	// WITHOUT explicitly invalidating: applyDirty (inside Package) must do it.
	m.SetOverride(filepath.Join(util, "helper.gsx"), utilWithNewExport)
	pr, err := m.Package(comp)
	if err != nil {
		t.Fatal(err)
	}
	assertResolvesUtilSymbol(t, pr) // new util symbol typed in components
}

func TestPackageCachesEditedPackageForImporters(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	utilDir := filepath.Join(root, "util")
	// Analyze util alone (as an LSP target).
	if _, err := m.Package(utilDir); err != nil {
		t.Fatal(err)
	}
	if !contains(m.cachedDirs(), utilDir) {
		t.Fatalf("Package(util) must populate pkgTypes[util]; cached=%v", m.cachedDirs())
	}
	// Add a new exported symbol to util via override (helper.gsx → component Z).
	m.SetOverride(filepath.Join(utilDir, "helper.gsx"), utilWithNewExport)
	// Override card.gsx to reference util.Z so the type-checker exercises the new symbol.
	m.SetOverride(filepath.Join(root, "components", "card.gsx"), componentsUsingZ)
	if _, err := m.Package(utilDir); err != nil {
		t.Fatal(err)
	}
	// components imports util: analyzing it must resolve the NEW symbol's type,
	// proving it read the fresh cached util (not a stale one).
	pr, err := m.Package(filepath.Join(root, "components"))
	if err != nil {
		t.Fatal(err)
	}
	assertResolvesUtilSymbol(t, pr) // zero error diagnostics ⇒ util.Z resolved in components
}
