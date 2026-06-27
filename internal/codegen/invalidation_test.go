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
