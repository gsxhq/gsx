package codegen

import (
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBundleProjectBoundaryRespectsNestedModuleOwnership(t *testing.T) {
	root := t.TempDir()
	nestedRoot := filepath.Join(root, "nested")
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	writeFile(t, nestedRoot, "go.mod", "module example.com/app/nested\n\ngo 1.26.1\n")

	outerUI := types.NewPackage("example.com/app/ui", "ui")
	outerUI.MarkComplete()
	outerBridge := types.NewPackage("example.com/app/bridge", "bridge")
	outerBridge.SetImports([]*types.Package{outerUI})
	outerBridge.MarkComplete()
	writeFile(t, filepath.Join(root, "ui"), "card.gsx", "package ui\ncomponent Card() { <p/> }\n")
	writeFile(t, filepath.Join(root, "bridge"), "bridge.go", "package bridge\n")

	nestedUI := types.NewPackage("example.com/app/nested/ui", "ui")
	nestedUI.MarkComplete()
	nestedBridge := types.NewPackage("example.com/app/nested/bridge", "bridge")
	nestedBridge.SetImports([]*types.Package{nestedUI})
	nestedBridge.MarkComplete()
	writeFile(t, filepath.Join(nestedRoot, "ui"), "card.gsx", "package ui\ncomponent Card() { <p/> }\n")
	writeFile(t, filepath.Join(nestedRoot, "bridge"), "bridge.go", "package bridge\n")

	imports := mapImporter{
		outerUI.Path():      outerUI,
		outerBridge.Path():  outerBridge,
		nestedUI.Path():     nestedUI,
		nestedBridge.Path(): nestedBridge,
	}
	module, err := Open(Options{
		ModuleRoot: root,
		ModulePath: "example.com/app",
		Bundle:     testBundle(imports, funcTables{}),
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("direct nested GSX is prebuilt external", func(t *testing.T) {
		got, err := module.importWithBundleProjectBoundary(nestedUI.Path(), imports)
		if err != nil {
			t.Fatalf("Import(%q): %v", nestedUI.Path(), err)
		}
		if got != nestedUI {
			t.Fatalf("Import(%q) = %p, want prebuilt package %p", nestedUI.Path(), got, nestedUI)
		}
	})

	t.Run("nested Go-only bridge to nested GSX is prebuilt external", func(t *testing.T) {
		got, err := module.importWithBundleProjectBoundary(nestedBridge.Path(), imports)
		if err != nil {
			t.Fatalf("Import(%q): %v", nestedBridge.Path(), err)
		}
		if got != nestedBridge {
			t.Fatalf("Import(%q) = %p, want prebuilt package %p", nestedBridge.Path(), got, nestedBridge)
		}
	})

	t.Run("outer Go-only bridge to outer GSX remains rejected", func(t *testing.T) {
		if _, err := module.importWithBundleProjectBoundary(outerBridge.Path(), imports); err == nil {
			t.Fatalf("Import(%q) succeeded, want project-GSX boundary rejection", outerBridge.Path())
		}
	})
}

func TestSourceOnlyBundleImportDoesNotInspectProjectOwnership(t *testing.T) {
	root := t.TempDir()
	// If ownership inspection reaches this path, Stat("dep/go.mod") fails with
	// EACCES. SourceOnly's virtual package has no host-owned dependency graph, so
	// the prebuilt import must not touch it.
	trap := filepath.Join(root, "dep")
	if err := os.Mkdir(trap, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(trap, 0o700) })
	if err := os.Chmod(trap, 0); err != nil {
		t.Fatal(err)
	}
	dep := types.NewPackage("example.com/virtual/dep", "dep")
	dep.MarkComplete()
	imports := mapImporter{dep.Path(): dep}
	module, err := Open(Options{
		ModuleRoot: root,
		ModulePath: "example.com/virtual",
		SourceOnly: true,
		Bundle:     testBundle(imports, funcTables{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := module.importWithBundleProjectBoundary(dep.Path(), imports)
	if err != nil {
		t.Fatalf("Import(%q): %v", dep.Path(), err)
	}
	if got != dep {
		t.Fatalf("Import(%q) = %p, want prebuilt package %p", dep.Path(), got, dep)
	}
}

func TestBundleRejectsExternalBackedgeAtAuthoredImport(t *testing.T) {
	root := t.TempDir()
	pageDir := filepath.Join(root, "page")
	modelDir := filepath.Join(root, "model")
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	writeFile(t, modelDir, "model.gsx", "package model\ncomponent Value(label string) { <span>{label}</span> }\n")
	writeFile(t, pageDir, "page.gsx", `package page

import "example.com/external"

component Page() { <main>{external.Use}</main> }
`)

	model := types.NewPackage("example.com/app/model", "model")
	model.MarkComplete()
	external := types.NewPackage("example.com/external", "external")
	external.Scope().Insert(types.NewVar(token.NoPos, external, "Use", types.Typ[types.String]))
	external.SetImports([]*types.Package{model})
	external.MarkComplete()
	imports := targetTestImporter().(mapImporter)
	imports[model.Path()] = model
	imports[external.Path()] = external
	module, err := Open(Options{
		ModuleRoot: root,
		ModulePath: "example.com/app",
		Bundle:     testBundle(imports, funcTables{}),
	})
	if err != nil {
		t.Fatal(err)
	}

	output, diagnostics, err := module.Generate(pageDir)
	if err != nil {
		t.Fatalf("Generate infrastructure error: %v", err)
	}
	if len(output) != 0 {
		t.Fatalf("Generate emitted files across external backedge: %v", keysOfGenerated(output))
	}
	diagnostic := requireExternalBackedgeDiagnostic(t, diagnostics)
	if !strings.HasSuffix(diagnostic.Start.Filename, "page.gsx") || diagnostic.Start.Line != 3 {
		t.Fatalf("boundary position = %s:%d, want page.gsx:3", diagnostic.Start.Filename, diagnostic.Start.Line)
	}
	if !strings.Contains(diagnostic.Message, `external package "example.com/external"`) ||
		!strings.Contains(diagnostic.Message, `"example.com/app/model"`) {
		t.Fatalf("boundary diagnostic = %q", diagnostic.Message)
	}
}

func TestBundleRejectsExternalBackedgeThroughProjectBridgeAtAuthoredImport(t *testing.T) {
	root := t.TempDir()
	pageDir := filepath.Join(root, "page")
	modelDir := filepath.Join(root, "model")
	bridgeDir := filepath.Join(root, "bridge")
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	writeFile(t, modelDir, "model.gsx", "package model\ncomponent Value(label string) { <span>{label}</span> }\n")
	writeFile(t, bridgeDir, "bridge.go", `package bridge

import "example.com/external"

var Use = external.Use
`)
	writeFile(t, pageDir, "page.gsx", `package page

import "example.com/app/bridge"

component Page() { <main>{bridge.Use}</main> }
`)

	model := types.NewPackage("example.com/app/model", "model")
	model.MarkComplete()
	external := types.NewPackage("example.com/external", "external")
	external.Scope().Insert(types.NewVar(token.NoPos, external, "Use", types.Typ[types.String]))
	external.SetImports([]*types.Package{model})
	external.MarkComplete()
	bridge := types.NewPackage("example.com/app/bridge", "bridge")
	bridge.Scope().Insert(types.NewVar(token.NoPos, bridge, "Use", types.Typ[types.String]))
	bridge.SetImports([]*types.Package{external})
	bridge.MarkComplete()
	imports := targetTestImporter().(mapImporter)
	imports[model.Path()] = model
	imports[external.Path()] = external
	imports[bridge.Path()] = bridge
	module, err := Open(Options{
		ModuleRoot: root,
		ModulePath: "example.com/app",
		Bundle:     testBundle(imports, funcTables{}),
	})
	if err != nil {
		t.Fatal(err)
	}

	output, diagnostics, err := module.Generate(pageDir)
	if err != nil {
		t.Fatalf("Generate infrastructure error: %v", err)
	}
	if len(output) != 0 {
		t.Fatalf("Generate emitted files across bridged external backedge: %v", keysOfGenerated(output))
	}
	diagnostic := requireExternalBackedgeDiagnostic(t, diagnostics)
	if !strings.HasSuffix(diagnostic.Start.Filename, "page.gsx") || diagnostic.Start.Line != 3 {
		t.Fatalf("boundary position = %s:%d, want page.gsx:3", diagnostic.Start.Filename, diagnostic.Start.Line)
	}
	if !strings.Contains(diagnostic.Message, `external package "example.com/external"`) ||
		!strings.Contains(diagnostic.Message, `"example.com/app/model"`) {
		t.Fatalf("boundary diagnostic = %q", diagnostic.Message)
	}
}
