package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/diag"
)

func externalBackedgeTestModule(t *testing.T) (root, externalRoot, modelDir string) {
	t.Helper()
	root = t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	externalRoot = filepath.Join(root, "external")
	modelDir = filepath.Join(root, "model")
	for _, dir := range []string{externalRoot, modelDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire (\n\tgithub.com/gsxhq/gsx v0.0.0\n\texample.com/external v0.0.0\n)\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\nreplace example.com/external => ./external\n")
	writeFile(t, externalRoot, "go.mod", "module example.com/external\n\ngo 1.26.1\n\nrequire example.com/app v0.0.0\n")
	writeFile(t, modelDir, "model.go", "package model\n\ntype Value struct { Label string }\n")
	writeFile(t, externalRoot, "external.go", `package external

import "example.com/app/model"

func Identity(value model.Value) model.Value { return value }
func Upper(value string) string { return value }
`)
	return root, externalRoot, modelDir
}

func TestExternalPackageImportingMainModuleIsPositionedSemanticBoundary(t *testing.T) {
	root, _, _ := externalBackedgeTestModule(t)
	uiDir := filepath.Join(root, "ui")
	writeFile(t, uiDir, "ui.gsx", `package ui

import (
	"example.com/app/model"
	"example.com/external"
)

component Show(value model.Value) { <p>{external.Identity(value).Label}</p> }
`)
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := m.Package(uiDir)
	if err != nil {
		t.Fatal(err)
	}
	diagnostic := requireExternalBackedgeDiagnostic(t, result.Diags)
	if !strings.HasSuffix(diagnostic.Start.Filename, "ui.gsx") || diagnostic.Start.Line != 5 {
		t.Fatalf("boundary position = %s:%d, want ui.gsx:5", diagnostic.Start.Filename, diagnostic.Start.Line)
	}
	if !strings.Contains(diagnostic.Message, `external package "example.com/external"`) ||
		!strings.Contains(diagnostic.Message, `"example.com/app/model"`) {
		t.Fatalf("boundary diagnostic = %q", diagnostic.Message)
	}
	output, diagnostics, err := m.Generate(uiDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(output) != 0 {
		t.Fatalf("Generate emitted files across unsupported boundary: %v", keysOfGenerated(output))
	}
	requireExternalBackedgeDiagnostic(t, diagnostics)
}

func TestExternalBackedgeThroughLocalGoBridgePointsAtBridgeImport(t *testing.T) {
	root, _, _ := externalBackedgeTestModule(t)
	bridgeDir := filepath.Join(root, "bridge")
	pageDir := filepath.Join(root, "page")
	writeFile(t, bridgeDir, "bridge.go", `package bridge

import (
	"example.com/app/model"
	"example.com/external"
)

func Identity(value model.Value) model.Value { return external.Identity(value) }
`)
	writeFile(t, pageDir, "page.gsx", `package page

import (
	"example.com/app/bridge"
	"example.com/app/model"
)

component Page(value model.Value) { <p>{bridge.Identity(value).Label}</p> }
`)
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := m.Package(pageDir)
	if err != nil {
		t.Fatal(err)
	}
	diagnostic := requireExternalBackedgeDiagnostic(t, result.Diags)
	if !strings.HasSuffix(diagnostic.Start.Filename, "bridge.go") || diagnostic.Start.Line != 5 {
		t.Fatalf("boundary position = %s:%d, want bridge.go:5", diagnostic.Start.Filename, diagnostic.Start.Line)
	}
}

func TestConfiguredExternalBackedgeIsHardConfigurationError(t *testing.T) {
	root, _, _ := externalBackedgeTestModule(t)
	pageDir := filepath.Join(root, "page")
	writeFile(t, pageDir, "page.gsx", `package page

component Page(value string) { <p>{value |> upper}</p> }
`)
	m, err := Open(Options{
		ModuleRoot: root,
		ModulePath: "example.com/app",
		FilterPkgs: []string{"example.com/external"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Generate(pageDir); err == nil ||
		!strings.Contains(err.Error(), "crosses the external-to-main-module semantic boundary") {
		t.Fatalf("Generate error = %v, want explicit configured-package boundary", err)
	}
}

func requireExternalBackedgeDiagnostic(t *testing.T, diagnostics []diag.Diagnostic) diag.Diagnostic {
	t.Helper()
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == externalMainModuleBackedgeCode && diagnostic.Severity == diag.Error {
			return diagnostic
		}
	}
	t.Fatalf("diagnostics = %v, want %s", diagnostics, externalMainModuleBackedgeCode)
	return diag.Diagnostic{}
}
