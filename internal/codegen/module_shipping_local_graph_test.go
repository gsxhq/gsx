package codegen

import (
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestModuleImporterClearsLoadingStackAfterFailedDagImport(t *testing.T) {
	root := t.TempDir()
	badDir := filepath.Join(root, "bad")
	badPath := "example.com/app/bad"
	m := &Module{
		opts:                 Options{ModuleRoot: root, ModulePath: "example.com/app"},
		fset:                 token.NewFileSet(),
		pkgTypes:             map[string]*types.Package{},
		sourceInventoryReady: true,
		sourcePackageDirs:    map[string]string{badPath: badDir},
		sourcePackages: map[string]projectSourcePackage{
			badDir: {
				pkgPath:         badPath,
				name:            "bad",
				compiledGoFiles: []string{filepath.Join(badDir, "bad.go")},
				invariantErrors: []string{"loaded syntax is missing for bad.go"},
			},
		},
		imports:    map[string][]string{},
		importedBy: map[string]map[string]bool{},
	}
	importer := &moduleImporter{m: m, external: mapImporter{}, seen: map[string]bool{}}
	for attempt := 1; attempt <= 2; attempt++ {
		_, err := importer.Import(badPath)
		if err == nil || !strings.Contains(err.Error(), "incomplete active companion syntax inventory") {
			t.Fatalf("attempt %d error = %v, want the retained-source failure", attempt, err)
		}
		if strings.Contains(err.Error(), "import cycle") {
			t.Fatalf("attempt %d turned a repeated failing DAG import into a cycle: %v", attempt, err)
		}
	}
	if len(importer.seen) != 0 {
		t.Fatalf("failed imports leaked loading stack entries: %v", importer.seen)
	}
}

func TestTargetAndConfiguredDeclarationGraphsKeepIndependentEdges(t *testing.T) {
	root := t.TempDir()
	consumerDir := filepath.Join(root, "consumer")
	targetDir := filepath.Join(root, "target")
	configuredDir := filepath.Join(root, "configured")
	m := &Module{
		opts:                 Options{ModuleRoot: root, ModulePath: "example.com/app"},
		sourceInventoryReady: true,
		sourcePackageDirs: map[string]string{
			"example.com/app/target":     targetDir,
			"example.com/app/configured": configuredDir,
		},
		targetImports:        map[string][]string{},
		targetImportedBy:     map[string]map[string]bool{},
		sourceDeclImports:    map[string][]string{},
		sourceDeclImportedBy: map[string]map[string]bool{},
		imports:              map[string][]string{},
		importedBy:           map[string]map[string]bool{},
	}
	m.recordTargetImports(consumerDir, []string{"example.com/app/target"})
	m.recordSourceDeclImports(consumerDir, []string{"example.com/app/configured"})
	m.mu.Lock()
	targetClosure := m.reverseClosure([]string{targetDir})
	configuredClosure := m.reverseClosure([]string{configuredDir})
	m.mu.Unlock()
	if !targetClosure[consumerDir] || !configuredClosure[consumerDir] {
		t.Fatalf("independent closures target=%v configured=%v, want consumer in both", targetClosure, configuredClosure)
	}

	// Refreshing one phase's successful boundary must replace only that phase's
	// edge set. The configured resolver may publish before or after exact-target
	// analysis, so a shared forward map would make invalidation order-dependent.
	m.recordTargetImports(consumerDir, nil)
	m.mu.Lock()
	configuredClosure = m.reverseClosure([]string{configuredDir})
	m.mu.Unlock()
	if !configuredClosure[consumerDir] {
		t.Fatalf("target refresh erased configured declaration provenance: %v", configuredClosure)
	}
}

func TestShippingResolverRechecksGoOnlyIntermediaryInOneSourceGraph(t *testing.T) {
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")

	leafDir := filepath.Join(root, "leaf")
	bridgeDir := filepath.Join(root, "bridge")
	filterDir := filepath.Join(root, "filters")
	pageDir := filepath.Join(root, "page")
	for _, dir := range []string{leafDir, bridgeDir, filterDir, pageDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	leafPath := filepath.Join(leafDir, "leaf.gsx")
	writeFile(t, leafDir, "leaf.gsx", `package leaf

type CardData struct { Title string }
component Card(data CardData) { <span>{data.Title}</span> }
`)
	// The cold inventory hides this paired output from authoritative source
	// selection. It remains deliberately incompatible so any package imported
	// from the cold external universe cannot accidentally satisfy this test.
	writeFile(t, leafDir, "leaf.x.go", `package leaf

import "github.com/gsxhq/gsx"

type CardProps struct { Poison int }
func Card(CardProps) gsx.Node { return nil }
`)
	writeFile(t, bridgeDir, "bridge.go", `package bridge

import "example.com/app/leaf"

type Model = leaf.CardData
`)
	writeFile(t, filterDir, "filters.go", `package filters

func Identity(value string) string { return value }
`)
	writeFile(t, pageDir, "page.gsx", `package page

import "example.com/app/bridge"

type Model = bridge.Model

component Page(value Model) { <main/> }
`)

	m, err := Open(Options{
		ModuleRoot: root,
		ModulePath: "example.com/app",
		FilterPkgs: []string{"example.com/app/filters"},
	})
	if err != nil {
		t.Fatal(err)
	}
	external, err := m.externalImporter()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := external.Import("example.com/app/bridge"); err == nil {
		t.Fatal("cold external importer published a module-local Go-only package")
	}
	first, err := m.Package(pageDir)
	if err != nil {
		t.Fatal(err)
	}
	assertShippingModelField(t, first.Types, "Title")
	fwd, _ := m.importGraphSnapshot()
	if got := fwd[bridgeDir]; len(got) != 1 || got[0] != leafDir {
		t.Fatalf("Go-only bridge dependencies = %v, want only authored leaf edge", got)
	}
	if got := fwd[pageDir]; !slices.Contains(got, filterDir) {
		t.Fatalf("GSX consumer dependencies = %v, want configured filter edge", got)
	}
	if got := m.externalLoads(); got != 1 {
		t.Fatalf("external loads after cold analysis = %d, want one", got)
	}

	m.SetOverride(leafPath, []byte("package leaf\n\ntype CardData struct { Count int }\ncomponent Card(data CardData) { <span>{data.Count}</span> }\n"))
	second, err := m.Package(pageDir)
	if err != nil {
		t.Fatal(err)
	}
	if second.Types == first.Types {
		t.Fatal("leaf edit retained the shipping page package through the Go-only intermediary")
	}
	assertShippingModelField(t, second.Types, "Count")
	if got := m.externalLoads(); got != 1 {
		t.Fatalf("external loads after warm leaf edit = %d, want one authoritative cold inventory", got)
	}
}

func assertShippingModelField(t *testing.T, pkg *types.Package, want string) {
	t.Helper()
	object := pkg.Scope().Lookup("Model")
	if object == nil {
		t.Fatal("shipping package has no Model alias")
	}
	structure, ok := types.Unalias(object.Type()).Underlying().(*types.Struct)
	if !ok || structure.NumFields() != 1 || structure.Field(0).Name() != want {
		t.Fatalf("shipping Model = %v, want one %s field", object.Type(), want)
	}
}

func TestShippingSignatureUsesOnlyActiveCompanionSyntax(t *testing.T) {
	t.Setenv("GOFLAGS", "-tags=feature")
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	uiDir := filepath.Join(root, "ui")
	writeFile(t, uiDir, "active.go", `//go:build feature

package ui

type CardData struct {
	Title string
}
`)
	writeFile(t, uiDir, "zz_inactive.go", `//go:build !feature

package ui

type CardData struct {
	Count int
}
`)
	writeFile(t, uiDir, "card.gsx", `package ui

component Card(data CardData) { <article>{data.Title}</article> }

component Page() { <Card data={CardData{Title: "hello"}}/> }
`)

	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	output, diagnostics, err := m.Generate(uiDir)
	if err != nil {
		t.Fatal(err)
	}
	if hasDiagErrors(diagnostics) {
		t.Fatalf("Generate diagnostics = %v", diagnostics)
	}
	if len(output[filepath.Join(uiDir, "card.gsx")]) == 0 {
		t.Fatal("Generate produced no output for active component signature")
	}
}

func TestLowercaseTagClassificationUsesOnlyActiveCompanionDeclarations(t *testing.T) {
	t.Setenv("GOFLAGS", "-tags=feature")
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	viewsDir := filepath.Join(root, "views")
	writeFile(t, viewsDir, "active.go", "//go:build feature\n\npackage views\n\nvar Active = true\n")
	writeFile(t, viewsDir, "inactive.go", `//go:build !feature

package views

import "github.com/gsxhq/gsx"

func widget() gsx.Node { return nil }
`)
	writeFile(t, viewsDir, "page.gsx", "package views\n\ncomponent Page() { <widget/> }\n")
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	output, diagnostics, err := m.Generate(viewsDir)
	if err != nil {
		t.Fatal(err)
	}
	if hasDiagErrors(diagnostics) {
		t.Fatalf("inactive widget declaration changed lowercase tag classification: %v", diagnostics)
	}
	generated := string(output[filepath.Join(viewsDir, "page.gsx")])
	if strings.Contains(generated, "widget(") {
		t.Fatalf("lowercase leaf was emitted as inactive companion call:\n%s", generated)
	}
}
