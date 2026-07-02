package codegen

import (
	"os"
	"path/filepath"
	"testing"
)

// writeDepFactsModule lays out a two-package module on disk:
// ui/card.gsx defines Card(title string) using { attrs... };
// pages/home.gsx imports ui.
func writeDepFactsModule(t *testing.T) (root, uiDir, pagesDir string) {
	t.Helper()
	root = t.TempDir()
	uiDir = filepath.Join(root, "ui")
	pagesDir = filepath.Join(root, "pages")
	for _, d := range []string{uiDir, pagesDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	writeFile(t, uiDir, "card.gsx", `package ui

component Card(title string) {
	<div class="card" { attrs... }>{title}</div>
}
`)
	writeFile(t, pagesDir, "home.gsx", `package pages

import "example.com/app/ui"

component Home() {
	<ui.Card title="t" class="x"/>
}
`)
	return root, uiDir, pagesDir
}

func TestImportedPropFactsCachedAndInvalidated(t *testing.T) {
	root, uiDir, _ := writeDepFactsModule(t)
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	f1, err := m.importedPropFacts(uiDir)
	if err != nil {
		t.Fatal(err)
	}
	if !f1.propFields["CardProps"]["Title"] || !f1.propFields["CardProps"]["Attrs"] {
		t.Fatalf("CardProps fields = %v; want Title and Attrs", f1.propFields["CardProps"])
	}
	f2, err := m.importedPropFacts(uiDir)
	if err != nil {
		t.Fatal(err)
	}
	if f1 != f2 {
		t.Fatal("second lookup did not hit the cache (different *depPropFacts)")
	}
	// A content change to the dep invalidates its cached facts.
	m.SetOverride(filepath.Join(uiDir, "card.gsx"), []byte(`package ui

component Card(title string, variant string) {
	<div class="card" { attrs... }>{title}</div>
}
`))
	m.Invalidate(uiDir)
	f3, err := m.importedPropFacts(uiDir)
	if err != nil {
		t.Fatal(err)
	}
	if f3 == f1 {
		t.Fatal("facts not recomputed after invalidation")
	}
	if !f3.propFields["CardProps"]["Variant"] {
		t.Fatalf("recomputed CardProps fields = %v; want Variant", f3.propFields["CardProps"])
	}
}
