package codegen

import (
	"path/filepath"
	"testing"
)

func TestResolveImportPackageNameUsesUnsavedAuthoritativePackageClause(t *testing.T) {
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	modelDir := filepath.Join(root, "model")
	modelPath := filepath.Join(modelDir, "model.gsx")
	writeFile(t, modelDir, "model.gsx", "package stale\n\ntype Flag string\n")

	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	m.SetOverride(modelPath, []byte("package current\n\ntype Flag string\n"))
	if _, err := m.externalImporter(); err != nil {
		t.Fatal(err)
	}
	if got, ok := m.resolveImportPackageName("example.com/app/model"); !ok || got != "current" {
		t.Fatalf("resolved package name = (%q, %v), want unsaved authoritative name current", got, ok)
	}
}

func TestImportSpecsByQualifierUsesExternalPackageAcrossNestedModuleBoundary(t *testing.T) {
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire (\n\tgithub.com/gsxhq/gsx v0.0.0\n\texample.com/app/nested v0.0.0\n)\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\nreplace example.com/app/nested => ./nested\n")
	nestedRoot := filepath.Join(root, "nested")
	writeFile(t, nestedRoot, "go.mod", "module example.com/app/nested\n\ngo 1.26.1\n")
	nestedDir := filepath.Join(nestedRoot, "ui")
	writeFile(t, nestedDir, "ui.go", "package externalui\n\ntype Value int\n")
	// This GSX file is physically below the parent root but belongs to the nested
	// module. A lexical import-prefix check would incorrectly merge its props.
	writeFile(t, nestedDir, "poison.gsx", "package poison\n\ncomponent Card(poison string) { <p>{poison}</p> }\n")
	pagesDir := filepath.Join(root, "pages")
	pagePath := filepath.Join(pagesDir, "page.gsx")
	writeFile(t, pagesDir, "page.gsx", "package pages\n\nimport \"example.com/app/nested/ui\"\n\ncomponent Page() { <p/> }\n")

	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.externalImporter(); err != nil {
		t.Fatal(err)
	}
	parsed, err := m.parsePackageWithFset(pagesDir, m.fset)
	if err != nil {
		t.Fatal(err)
	}
	file := parsed.files[pagePath]
	if file == nil {
		t.Fatalf("parsed package omitted %s", pagePath)
	}
	byQualifier := m.importSpecsByQualifier(fileImportSpecs(file, m.fset))
	if _, ok := byQualifier["externalui"]; !ok || len(byQualifier) != 1 {
		t.Fatalf("qualifiers=%v, want only the nested module's compiled package name externalui", byQualifier)
	}
	if _, ok := byQualifier["poison"]; ok {
		t.Fatalf("nested-module GSX source leaked into the parent package-name universe: %v", byQualifier)
	}
}
