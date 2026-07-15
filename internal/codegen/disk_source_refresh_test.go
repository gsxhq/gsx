package codegen

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func writeDiskRefreshTestModule(t *testing.T) (root, pageDir, pagePath string) {
	t.Helper()
	root = t.TempDir()
	pageDir = filepath.Join(root, "page")
	depRoot := filepath.Join(root, "dep")
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire (\n\tgithub.com/gsxhq/gsx v0.0.0\n\texample.com/dep v0.0.0\n)\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\nreplace example.com/dep => ./dep\n")
	writeFile(t, depRoot, "go.mod", "module example.com/dep\n\ngo 1.26.1\n")
	writeFile(t, depRoot, "dep.go", "package dep\n\ntype Value string\n")
	pagePath = filepath.Join(pageDir, "page.gsx")
	writeFile(t, pageDir, "page.gsx", "package page\n\ncomponent Page(value string) { <p>{value}</p> }\n")
	return root, pageDir, pagePath
}

func TestRefreshDiskSourcesReloadsPreviouslyUnseenImport(t *testing.T) {
	root, pageDir, pagePath := writeDiskRefreshTestModule(t)
	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	if _, diagnostics, err := module.Generate(pageDir); err != nil || len(diagnostics) != 0 {
		t.Fatalf("cold Generate error=%v diagnostics=%v", err, diagnostics)
	}
	if module.externalImportPaths["example.com/dep"] {
		t.Fatal("future import unexpectedly present in cold importer")
	}

	updated := []byte(`package page

import "example.com/dep"

component Page(value dep.Value) { <p>{value}</p> }
`)
	if err := os.WriteFile(pagePath, updated, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := module.RefreshDiskSources(pageDir); err != nil {
		t.Fatal(err)
	}
	module.Invalidate(pageDir)
	output, diagnostics, err := module.Generate(pageDir)
	if err != nil || len(diagnostics) != 0 {
		t.Fatalf("warm Generate error=%v diagnostics=%v", err, diagnostics)
	}
	if !bytes.Contains(output[pagePath], []byte("dep.Value")) {
		t.Fatalf("generated output did not use refreshed import:\n%s", output[pagePath])
	}
	if got := module.externalLoads(); got != 2 {
		t.Fatalf("external loads = %d, want cold load plus required manifest reload", got)
	}
}

func TestRefreshDiskSourcesKeepsBodyOnlyEditWarm(t *testing.T) {
	root, pageDir, pagePath := writeDiskRefreshTestModule(t)
	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	if _, diagnostics, err := module.Generate(pageDir); err != nil || len(diagnostics) != 0 {
		t.Fatalf("cold Generate error=%v diagnostics=%v", err, diagnostics)
	}
	if err := os.WriteFile(pagePath, []byte("package page\n\ncomponent Page(value string) { <strong>{value}</strong> }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := module.RefreshDiskSources(pageDir); err != nil {
		t.Fatal(err)
	}
	module.Invalidate(pageDir)
	output, diagnostics, err := module.Generate(pageDir)
	if err != nil || len(diagnostics) != 0 {
		t.Fatalf("warm Generate error=%v diagnostics=%v", err, diagnostics)
	}
	if !bytes.Contains(output[pagePath], []byte("strong")) {
		t.Fatalf("generated output did not observe body edit:\n%s", output[pagePath])
	}
	if got := module.externalLoads(); got != 1 {
		t.Fatalf("external loads = %d, want body-only edit to stay warm", got)
	}
}

func TestRefreshDiskSourcesRebuildsNewPackageAndPackageClause(t *testing.T) {
	t.Run("new package", func(t *testing.T) {
		root, pageDir, _ := writeDiskRefreshTestModule(t)
		module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
		if err != nil {
			t.Fatal(err)
		}
		if _, diagnostics, err := module.Generate(pageDir); err != nil || len(diagnostics) != 0 {
			t.Fatalf("cold Generate error=%v diagnostics=%v", err, diagnostics)
		}
		widgetDir := filepath.Join(root, "widget")
		widgetPath := filepath.Join(widgetDir, "card.gsx")
		writeFile(t, widgetDir, "card.gsx", "package widget\n\ncomponent Card(label string) { <p>{label}</p> }\n")
		if err := module.RefreshDiskSources(widgetDir); err != nil {
			t.Fatal(err)
		}
		module.Invalidate(widgetDir)
		output, diagnostics, err := module.Generate(widgetDir)
		if err != nil || len(diagnostics) != 0 || len(output[widgetPath]) == 0 {
			t.Fatalf("new-package Generate output=%v error=%v diagnostics=%v", output, err, diagnostics)
		}
		if got := module.externalLoads(); got != 2 {
			t.Fatalf("external loads = %d, want new-package manifest reload", got)
		}
	})

	t.Run("package clause", func(t *testing.T) {
		root, pageDir, pagePath := writeDiskRefreshTestModule(t)
		module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
		if err != nil {
			t.Fatal(err)
		}
		if _, diagnostics, err := module.Generate(pageDir); err != nil || len(diagnostics) != 0 {
			t.Fatalf("cold Generate error=%v diagnostics=%v", err, diagnostics)
		}
		if err := os.WriteFile(pagePath, []byte("package renamed\n\ncomponent Page(value string) { <p>{value}</p> }\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := module.RefreshDiskSources(pageDir); err != nil {
			t.Fatal(err)
		}
		module.Invalidate(pageDir)
		output, diagnostics, err := module.Generate(pageDir)
		if err != nil || len(diagnostics) != 0 {
			t.Fatalf("package-clause Generate error=%v diagnostics=%v", err, diagnostics)
		}
		if !bytes.Contains(output[pagePath], []byte("package renamed")) {
			t.Fatalf("generated output retained old package clause:\n%s", output[pagePath])
		}
		if got := module.externalLoads(); got != 2 {
			t.Fatalf("external loads = %d, want package-clause manifest reload", got)
		}
	})
}
