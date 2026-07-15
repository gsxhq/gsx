package codegen

import (
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
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
	if slices.Contains(g[from], to) {
		return
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

// TestImportGraphIncludesGoFileImports guards the whole-branch reviewer's
// under-invalidation finding: a gsx package whose .gsx does NOT import a sibling
// gsx package, but whose hand-written .go (a model.go) DOES, is still type-checked
// against that sibling's skeleton — so its reverse edge must be recorded, or
// editing the sibling would not invalidate it (stale importer served).
func TestImportGraphIncludesGoFileImports(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
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
	// withgo's .gsx does NOT import util; only its companion model.go does.
	must("withgo/comp.gsx", "package withgo\n\ncomponent W() {\n\t<div>w</div>\n}\n")
	must("withgo/model.go", "package withgo\n\nimport \"example.com/x/util\"\n\nvar _ = util.Y\n")

	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/x", FilterPkgs: []string{StdImportPath}})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	utilDir := filepath.Join(root, "util")
	withgoDir := filepath.Join(root, "withgo")
	if _, err := m.Package(withgoDir); err != nil {
		t.Fatalf("analyze withgo: %v", err)
	}
	// The reverse edge must exist even though util is imported only via model.go.
	_, rev := m.importGraphSnapshot()
	assertEdge(t, rev, utilDir, withgoDir)

	// And editing util must invalidate withgo via the reverse closure.
	m.SetOverride(filepath.Join(utilDir, "util.gsx"),
		[]byte("package util\n\ncomponent Y(label string) {\n\t<em>{label}</em>\n}\n"))
	m.applyDirty()
	if contains(m.cachedDirs(), withgoDir) {
		t.Errorf("editing util must invalidate withgo (imports util via model.go); cachedDirs=%v", m.cachedDirs())
	}
}

func contains(ss []string, s string) bool {
	return slices.Contains(ss, s)
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

	// Sub-case: new file not on disk, empty content (src==nil, no base) is still
	// present editor source. Membership changed from absent to present, so it is
	// dirty even though its byte length is zero.
	t.Run("new_empty_file_changes_membership", func(t *testing.T) {
		m, root := setupChainModule(t)
		utilDir := filepath.Join(root, "util")
		helper := filepath.Join(utilDir, "helper.gsx")
		disk, _ := os.ReadFile(helper) // helper.gsx does not exist → disk == nil
		m.SetOverride(helper, disk)
		if got := m.dirtyDirs(); !slices.Equal(got, []string{utilDir}) {
			t.Errorf("new-file nil-content override dirty dirs = %v, want [%s]", got, utilDir)
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
	// Override util to add component Z, and override components to USE util.Z so
	// that a stale pkgTypes[util] (without Z) produces an "undefined" diagnostic —
	// making the assertion genuinely depend on invalidation having run.
	m.SetOverride(filepath.Join(util, "helper.gsx"), utilWithNewExport)
	m.SetOverride(filepath.Join(comp, "card.gsx"), componentsUsingZ)
	pr, err := m.Package(comp)
	if err != nil {
		t.Fatal(err)
	}
	assertResolvesUtilSymbol(t, pr) // new util symbol typed in components
}

func TestConcurrentSetOverrideAndPackage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	m, root := setupChainModule(t)
	comp := filepath.Join(root, "components")
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(2)
		go func() { defer wg.Done(); m.SetOverride(filepath.Join(comp, "card.gsx"), componentsEdited) }()
		go func() { defer wg.Done(); _, _ = m.Package(comp) }()
	}
	wg.Wait()
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

func TestDependentsReverseClosure(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	if _, err := m.Package(filepath.Join(root, "pages")); err != nil {
		t.Fatal(err)
	} // warm graph
	if _, err := m.Package(filepath.Join(root, "solo")); err != nil {
		t.Fatal(err)
	}
	got := m.Dependents(filepath.Join(root, "util"))
	// util + everything that transitively imports it: components, pages. NOT solo.
	want := map[string]bool{
		filepath.Join(root, "util"):       true,
		filepath.Join(root, "components"): true,
		filepath.Join(root, "pages"):      true,
	}
	if len(got) != len(want) {
		t.Fatalf("Dependents(util) = %v, want keys %v", got, want)
	}
	for _, d := range got {
		if !want[d] {
			t.Errorf("unexpected dependent %s", d)
		}
	}
	// A leaf nothing imports: just itself.
	if d := m.Dependents(filepath.Join(root, "solo")); len(d) != 1 || d[0] != filepath.Join(root, "solo") {
		t.Errorf("Dependents(solo) = %v, want [solo]", d)
	}
}

func rendererTableIdentities(t *testing.T, m *Module, dir string) (base, localized uintptr) {
	t.Helper()
	pkgPath, ok := importPathForDir(m.opts.ModuleRoot, m.opts.ModulePath, dir)
	if !ok {
		t.Fatalf("package dir %s is outside module root %s", dir, m.opts.ModuleRoot)
	}
	filterPkgs := m.opts.FilterPkgs
	if d, ok := m.dirOptionsFor(dir); ok && d.FilterPkgs != nil {
		filterPkgs = d.FilterPkgs
	}
	key := pkgPath + "\x00" + strings.Join(dedupFilterPkgs(filterPkgs), "\x00")
	m.mu.Lock()
	defer m.mu.Unlock()
	base = reflect.ValueOf(m.rendererTbl).Pointer()
	table, ok := m.dirFuncTbls[key]
	if !ok {
		t.Fatalf("localized func table for %s not cached; keys=%v", dir, slices.Sorted(maps.Keys(m.dirFuncTbls)))
	}
	localized = reflect.ValueOf(table.renderers).Pointer()
	if base == 0 || localized == 0 {
		t.Fatalf("renderer table identities for %s = base:%#x localized:%#x, want both non-zero", dir, base, localized)
	}
	return base, localized
}

func TestModuleRendererOverrideInvalidatesClassification(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	root, rendererDir, viewsDir := localRendererModule(t)
	rendererPath := filepath.Join(rendererDir, "renderers.gsx")
	stringRenderer := `package renderers

import "example.com/app/pg"

func Timestamptz(v pg.Timestamptz) string {
	return v.Label
}
`
	nodeRenderer := `package renderers

import (
	"example.com/app/pg"
	"github.com/gsxhq/gsx"
)

func Timestamptz(v pg.Timestamptz) gsx.Node {
	return <time>{v.Label}</time>
}
`
	writeFile(t, rendererDir, "renderers.gsx", stringRenderer)

	opts := localRendererOptions()
	m, err := Open(Options{
		ModuleRoot: root,
		ModulePath: "example.com/app",
		FilterPkgs: opts.FilterPkgs,
		Renderers:  opts.Renderers,
	})
	if err != nil {
		t.Fatal(err)
	}
	beforeResult, err := m.Package(viewsDir)
	if err != nil {
		t.Fatal(err)
	}
	if hasDiagErrors(beforeResult.Diags) {
		t.Fatalf("initial Package diags = %v", beforeResult.Diags)
	}
	m.mu.Lock()
	beforeDecl := m.rendererPkgs["example.com/app/renderers"]
	declDone := m.rendererPkgsDone && m.rendererTblDone
	m.mu.Unlock()
	if beforeDecl == nil || !declDone {
		t.Fatal("initial Package did not populate renderer declaration/table caches")
	}
	if got := m.externalLoads(); got != 1 {
		t.Fatalf("externalLoads after initial Package = %d, want 1", got)
	}
	if _, err := m.Package(rendererDir); err != nil {
		t.Fatal(err)
	}
	fwd, _ := m.importGraphSnapshot()
	if got, want := fwd[viewsDir], []string{filepath.Join(root, "pg"), rendererDir}; !slices.Equal(got, want) {
		t.Fatalf("consumer dependencies = %v, want authored model plus local renderer %v", got, want)
	}
	if got := fwd[rendererDir]; slices.Contains(got, rendererDir) {
		t.Fatalf("renderer package has a self dependency: %v", got)
	}
	beforeBaseTable, beforeLocalizedTable := rendererTableIdentities(t, m, viewsDir)

	// An unrelated consumer edit must refresh only the ordinary package result;
	// renderer declarations and the external importer stay warm.
	viewsPath := filepath.Join(viewsDir, "views.gsx")
	m.SetOverride(viewsPath, []byte(`package views

import "example.com/app/pg"

component Show(sample pg.Timestamptz) {
	<section>{sample}</section>
}
`))
	unrelatedResult, err := m.Package(viewsDir)
	if err != nil {
		t.Fatal(err)
	}
	if unrelatedResult == beforeResult {
		t.Fatal("unrelated override retained the stale consumer PackageResult")
	}
	m.mu.Lock()
	afterUnrelatedDecl := m.rendererPkgs["example.com/app/renderers"]
	declStillDone := m.rendererPkgsDone && m.rendererTblDone
	m.mu.Unlock()
	if afterUnrelatedDecl != beforeDecl || !declStillDone {
		t.Fatal("unrelated override cleared the renderer declaration/table cache")
	}
	if got := m.externalLoads(); got != 1 {
		t.Fatalf("externalLoads after unrelated override = %d, want 1", got)
	}
	afterBaseTable, afterLocalizedTable := rendererTableIdentities(t, m, viewsDir)
	if afterBaseTable != beforeBaseTable {
		t.Fatalf("unrelated override replaced completed base renderer table: before=%#x after=%#x", beforeBaseTable, afterBaseTable)
	}
	if afterLocalizedTable != beforeLocalizedTable {
		t.Fatalf("unrelated override replaced localized renderer table: before=%#x after=%#x", beforeLocalizedTable, afterLocalizedTable)
	}

	// Changing only the renderer declaration changes its result classification
	// from string to gsx.Node. Both the retained LSP PackageResult and generated
	// lowering must be rebuilt from the new declaration without another external
	// packages.Load.
	m.SetOverride(rendererPath, []byte(nodeRenderer))
	afterRendererResult, err := m.Package(viewsDir)
	if err != nil {
		t.Fatal(err)
	}
	if afterRendererResult == unrelatedResult {
		t.Fatal("renderer override retained the stale consumer PackageResult")
	}
	m.mu.Lock()
	afterRendererDecl := m.rendererPkgs["example.com/app/renderers"]
	m.mu.Unlock()
	if afterRendererDecl == nil || afterRendererDecl == beforeDecl {
		t.Fatal("renderer override did not rebuild the renderer declaration package")
	}
	if got := m.externalLoads(); got != 1 {
		t.Fatalf("externalLoads after renderer override = %d, want 1", got)
	}

	out, diags, err := m.Generate(viewsDir)
	if err != nil {
		t.Fatal(err)
	}
	if hasDiagErrors(diags) {
		t.Fatalf("Generate diags = %v", diags)
	}
	src := string(out[viewsPath])
	if !strings.Contains(src, `_gsxgw.Node(ctx, _gsxf0.Timestamptz((sample)))`) {
		t.Fatalf("consumer retained string lowering after renderer override:\n%s", src)
	}
	if strings.Contains(src, `_gsxgw.Text(string(_gsxf0.Timestamptz((sample))))`) {
		t.Fatalf("consumer still contains stale string lowering after renderer override:\n%s", src)
	}
}

func TestRendererImplicitDependenciesUseAuthoredAndFinalLocalOwners(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}

	t.Run("module-local Go-only owner", func(t *testing.T) {
		root, _, viewsDir := localRendererModule(t)
		goRendererDir := filepath.Join(root, "gorender")
		writeFile(t, goRendererDir, "renderer.go", `package gorender

import "example.com/app/pg"

func Timestamptz(v pg.Timestamptz) string { return v.Label }
`)
		m, err := Open(Options{
			ModuleRoot: root,
			ModulePath: "example.com/app",
			Renderers: []RendererAlias{{
				TypeKey:  "example.com/app/pg.Timestamptz",
				PkgPath:  "example.com/app/gorender",
				FuncName: "Timestamptz",
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		result, err := m.Package(viewsDir)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Diags) != 0 {
			t.Fatalf("module-local Go-only renderer fixture produced diagnostics: %v", result.Diags)
		}
		if !m.rendererDirs[goRendererDir] {
			t.Fatalf("resolved module-local Go-only renderer dir missing from rendererDirs: %v", m.rendererDirs)
		}
		fwd, _ := m.importGraphSnapshot()
		if got, want := fwd[viewsDir], []string{goRendererDir, filepath.Join(root, "pg")}; !slices.Equal(got, want) {
			t.Fatalf("consumer dependencies = %v, want authored model plus Go-only renderer %v", got, want)
		}
		m.mu.Lock()
		local := m.rendererLocal["example.com/app/gorender"]
		m.mu.Unlock()
		if local {
			t.Fatal("module-local Go-only renderer classified as local GSX")
		}
	})

	t.Run("external owner", func(t *testing.T) {
		repoRoot, err := filepath.Abs("../..")
		if err != nil {
			t.Fatal(err)
		}
		root := t.TempDir()
		extRoot := filepath.Join(root, "external")
		writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire (\n\tgithub.com/gsxhq/gsx v0.0.0\n\texample.com/renderext v0.0.0\n)\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\nreplace example.com/renderext => ./external\n")
		writeFile(t, extRoot, "go.mod", "module example.com/renderext\n\ngo 1.26.1\n")
		writeFile(t, extRoot, "model/model.go", "package model\n\ntype Moment struct { Label string }\n")
		writeFile(t, extRoot, "renderers/renderers.go", `package renderers

import "example.com/renderext/model"

func Moment(v model.Moment) string { return v.Label }
`)
		viewsDir := filepath.Join(root, "views")
		writeFile(t, viewsDir, "views.gsx", `package views

import "example.com/renderext/model"

component Show(sample model.Moment) {
	<div>{sample}</div>
}
`)
		m, err := Open(Options{
			ModuleRoot: root,
			ModulePath: "example.com/app",
			Renderers: []RendererAlias{{
				TypeKey:  "example.com/renderext/model.Moment",
				PkgPath:  "example.com/renderext/renderers",
				FuncName: "Moment",
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		result, err := m.Package(viewsDir)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Diags) != 0 {
			t.Fatalf("external renderer fixture produced diagnostics: %v", result.Diags)
		}
		if len(m.rendererDirs) != 0 {
			t.Fatalf("external renderer populated rendererDirs after resolution: %v", m.rendererDirs)
		}
		fwd, _ := m.importGraphSnapshot()
		if got := fwd[viewsDir]; len(got) != 0 {
			t.Fatalf("external renderer created implicit dependencies: %v", got)
		}
	})

	t.Run("main-prefix nested module owner", func(t *testing.T) {
		repoRoot, err := filepath.Abs("../..")
		if err != nil {
			t.Fatal(err)
		}
		root := t.TempDir()
		extRoot := filepath.Join(root, "nested-renderext")
		writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire (\n\tgithub.com/gsxhq/gsx v0.0.0\n\texample.com/app/renderext v0.0.0\n)\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\nreplace example.com/app/renderext => ./nested-renderext\n")
		writeFile(t, extRoot, "go.mod", "module example.com/app/renderext\n\ngo 1.26.1\n")
		writeFile(t, extRoot, "model/model.go", "package model\n\ntype Moment struct { Label string }\n")
		writeFile(t, extRoot, "renderers/renderers.go", `package renderers

import "example.com/app/renderext/model"

func Moment(v model.Moment) string { return v.Label }
`)
		viewsDir := filepath.Join(root, "views")
		writeFile(t, viewsDir, "views.gsx", `package views

import "example.com/app/renderext/model"

component Show(sample model.Moment) { <div>{sample}</div> }
`)
		m, err := Open(Options{
			ModuleRoot: root,
			ModulePath: "example.com/app",
			Renderers: []RendererAlias{{
				TypeKey:  "example.com/app/renderext/model.Moment",
				PkgPath:  "example.com/app/renderext/renderers",
				FuncName: "Moment",
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(m.rendererDirs) != 0 {
			t.Fatalf("Open made a lexical renderer ownership claim: %v", m.rendererDirs)
		}
		result, err := m.Package(viewsDir)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Diags) != 0 {
			t.Fatalf("nested-module renderer fixture produced diagnostics: %v", result.Diags)
		}
		if len(m.rendererDirs) != 0 {
			t.Fatalf("main-prefix nested module renderer classified as module-owned: %v", m.rendererDirs)
		}
	})

	t.Run("shadowed local owner", func(t *testing.T) {
		root, winnerDir, viewsDir := localRendererModule(t)
		shadowedDir := filepath.Join(root, "shadowed")
		writeFile(t, shadowedDir, "package.go", "package shadowed\n")
		writeFile(t, shadowedDir, "renderers.gsx", `package shadowed

import "example.com/app/pg"

func Timestamptz(v pg.Timestamptz) string { return v.Label }
`)
		opts := localRendererOptions()
		opts.Renderers = append([]RendererAlias{{
			TypeKey:  "example.com/app/pg.Timestamptz",
			PkgPath:  "example.com/app/shadowed",
			FuncName: "Timestamptz",
		}}, opts.Renderers...)
		m, err := Open(Options{
			ModuleRoot: root,
			ModulePath: "example.com/app",
			FilterPkgs: opts.FilterPkgs,
			Renderers:  opts.Renderers,
		})
		if err != nil {
			t.Fatal(err)
		}
		result, err := m.Package(viewsDir)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Diags) != 0 {
			t.Fatalf("shadowed/winning renderer fixture produced diagnostics: %v", result.Diags)
		}
		if m.rendererDirs[shadowedDir] || !m.rendererDirs[winnerDir] || len(m.rendererDirs) != 1 {
			t.Fatalf("rendererDirs after resolution = %v, want only winning owner %s", m.rendererDirs, winnerDir)
		}
		fwd, _ := m.importGraphSnapshot()
		if got, want := fwd[viewsDir], []string{filepath.Join(root, "pg"), winnerDir}; !slices.Equal(got, want) {
			t.Fatalf("consumer dependencies = %v, want authored model plus only winning renderer %v", got, want)
		}
		m.mu.Lock()
		_, shadowedResolved := m.rendererLocal["example.com/app/shadowed"]
		winnerResolved := m.rendererLocal["example.com/app/renderers"]
		m.mu.Unlock()
		if shadowedResolved || !winnerResolved {
			t.Fatalf("rendererLocal shadowed=%t winner=%t, want false,true", shadowedResolved, winnerResolved)
		}
	})
}
