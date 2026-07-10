package lsp

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/codegen"
)

// TestAttrsOnlyTagDeclAtSamePackage mirrors
// internal/corpus/testdata/cases/attrsonly/factory_var.txtar: HomeIcon is a
// package-level var (initialized from a factory func) whose type is
// func(gsx.Attrs) gsx.Node, declared in a sibling .go file — never a
// `component` declaration, so componentTagDeclAt's CrossIndex lookup misses it
// entirely. A cursor on <HomeIcon/> must resolve to `var HomeIcon` in icons.go.
func TestAttrsOnlyTagDeclAtSamePackage(t *testing.T) {
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeLSPTestFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	viewsDir := filepath.Join(root, "views")
	pageSrc := "package views\n\n" +
		"import \"github.com/gsxhq/gsx\"\n\n" +
		"type iconProps struct {\n\tName  string\n\tAttrs gsx.Attrs\n}\n\n" +
		"component renderIcon(p iconProps) {\n\t<svg { gsx.Attrs{{Key: \"class\", Value: \"w-5 h-5\"}}.Merge(p.Attrs)... }>{p.Name}</svg>\n}\n\n" +
		"component Page() {\n\t<div>\n\t\t<HomeIcon class=\"h-3 w-3\"/>\n\t</div>\n}\n"
	writeLSPTestFile(t, viewsDir, "page.gsx", pageSrc)
	iconsSrc := "package views\n\n" +
		"import \"github.com/gsxhq/gsx\"\n\n" +
		"func namedIcon(name string) func(gsx.Attrs) gsx.Node {\n" +
		"\treturn func(attrs gsx.Attrs) gsx.Node {\n" +
		"\t\treturn renderIcon(iconProps{Name: name, Attrs: attrs})\n\t}\n}\n\n" +
		"var HomeIcon = namedIcon(\"house\")\n"
	writeLSPTestFile(t, viewsDir, "icons.go", iconsSrc)

	m, err := codegen.Open(codegen.Options{ModuleRoot: root, ModulePath: "example.com/app", FilterPkgs: []string{codegen.StdImportPath}})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := m.Package(viewsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.Diags) > 0 {
		t.Fatalf("unexpected diagnostics: %+v", pr.Diags)
	}
	cross := make(map[string]CrossRef, len(pr.CrossIndex))
	for k, v := range pr.CrossIndex {
		cross[k] = CrossRef{Name: v.Name, Decl: v.Decl, Decls: v.Decls, Refs: v.Refs}
	}
	pkg := &Package{
		GSXFset:    pr.GSXFset,
		Fset:       pr.Fset,
		Info:       pr.Info,
		Types:      pr.Types,
		Files:      pr.GSXFiles,
		ExprMap:    pr.ExprMap,
		CrossIndex: cross,
	}
	gsxPath := filepath.Join(viewsDir, "page.gsx")

	// Sanity: CrossIndex has no entry for HomeIcon (it's not a `component`
	// declaration), so the existing componentTagDeclAt must miss it — proving
	// this really needs the new parallel lookup, not the existing one.
	if _, ok := componentTagDeclAt(pkg, gsxPath, strings.Index(pageSrc, "<HomeIcon")+1); ok {
		t.Fatal("componentTagDeclAt unexpectedly resolved an attrs-only tag — CrossIndex should not carry it")
	}

	tagStart := strings.Index(pageSrc, "<HomeIcon") + 1 // +1 to skip '<'
	if tagStart < 1 {
		t.Fatal("could not find <HomeIcon in src")
	}
	dp, ok := attrsOnlyTagDeclAt(pkg, gsxPath, tagStart)
	if !ok {
		t.Fatalf("attrsOnlyTagDeclAt returned false for cursor on 'H' of HomeIcon tag")
	}
	if !strings.HasSuffix(dp.Filename, "icons.go") {
		t.Errorf("attrsOnlyTagDeclAt filename = %q, want suffix icons.go", dp.Filename)
	}
	wantLine := strings.Count(iconsSrc[:strings.Index(iconsSrc, "var HomeIcon")], "\n") + 1
	if dp.Line != wantLine {
		t.Errorf("attrsOnlyTagDeclAt line = %d, want %d (the `var HomeIcon` decl line)", dp.Line, wantLine)
	}
	nameCol := strings.Index(iconsSrc, "HomeIcon = namedIcon") + 1 // 1-based column of "HomeIcon" on its line
	lineStart := strings.LastIndexByte(iconsSrc[:strings.Index(iconsSrc, "HomeIcon = namedIcon")], '\n') + 1
	wantCol := strings.Index(iconsSrc, "HomeIcon = namedIcon") - lineStart + 1
	_ = nameCol
	if dp.Column != wantCol {
		t.Errorf("attrsOnlyTagDeclAt column = %d, want %d", dp.Column, wantCol)
	}
}

// TestAttrsOnlyTagDeclAtCrossPackage mirrors
// internal/corpus/testdata/cases/attrsonly/imported.txtar: a dotted tag
// <ui.HomeIcon/> resolves to the `var HomeIcon` declared (via a top-level Go
// decl embedded in a .gsx file — a GoChunk) in the imported "ui" package.
func TestAttrsOnlyTagDeclAtCrossPackage(t *testing.T) {
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeLSPTestFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	uiDir := filepath.Join(root, "ui")
	uiSrc := "package ui\n\n" +
		"import \"github.com/gsxhq/gsx\"\n\n" +
		"type iconProps struct {\n\tName  string\n\tAttrs gsx.Attrs\n}\n\n" +
		"component renderIcon(p iconProps) {\n\t<svg { p.Attrs... }>{p.Name}</svg>\n}\n\n" +
		"func namedIcon(name string) func(gsx.Attrs) gsx.Node {\n" +
		"\treturn func(attrs gsx.Attrs) gsx.Node {\n" +
		"\t\treturn renderIcon(iconProps{Name: name, Attrs: attrs})\n\t}\n}\n\n" +
		"var HomeIcon = namedIcon(\"house\")\n"
	writeLSPTestFile(t, uiDir, "icons.gsx", uiSrc)
	pagesDir := filepath.Join(root, "pages")
	pageSrc := "package pages\n\n" +
		"import \"example.com/app/ui\"\n\n" +
		"component Home() {\n\t<ui.HomeIcon class=\"h-3 w-3\"/>\n}\n"
	writeLSPTestFile(t, pagesDir, "home.gsx", pageSrc)

	m, err := codegen.Open(codegen.Options{ModuleRoot: root, ModulePath: "example.com/app", FilterPkgs: []string{codegen.StdImportPath}})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := m.Package(pagesDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.Diags) > 0 {
		t.Fatalf("unexpected diagnostics: %+v", pr.Diags)
	}
	cross := make(map[string]CrossRef, len(pr.CrossIndex))
	for k, v := range pr.CrossIndex {
		cross[k] = CrossRef{Name: v.Name, Decl: v.Decl, Decls: v.Decls, Refs: v.Refs}
	}
	pkg := &Package{
		GSXFset:    pr.GSXFset,
		Fset:       pr.Fset,
		Info:       pr.Info,
		Types:      pr.Types,
		Files:      pr.GSXFiles,
		ExprMap:    pr.ExprMap,
		CrossIndex: cross,
	}
	gsxPath := filepath.Join(pagesDir, "home.gsx")

	if _, ok := crossPkgTagDeclAt(pkg, gsxPath, strings.Index(pageSrc, "<ui.HomeIcon")+1); ok {
		t.Fatal("crossPkgTagDeclAt unexpectedly resolved an attrs-only dotted tag — it only knows `component` decls")
	}

	tagStart := strings.Index(pageSrc, "<ui.HomeIcon") + 1 // +1 to skip '<'
	if tagStart < 1 {
		t.Fatal("could not find <ui.HomeIcon in src")
	}
	dp, ok := attrsOnlyTagDeclAt(pkg, gsxPath, tagStart)
	if !ok {
		t.Fatalf("attrsOnlyTagDeclAt returned false for cursor on dotted attrs-only tag")
	}
	if !strings.HasSuffix(dp.Filename, "icons.gsx") {
		t.Errorf("attrsOnlyTagDeclAt filename = %q, want suffix icons.gsx (the dep's .gsx source)", dp.Filename)
	}
	wantLine := strings.Count(uiSrc[:strings.Index(uiSrc, "var HomeIcon")], "\n") + 1
	if dp.Line != wantLine {
		t.Errorf("attrsOnlyTagDeclAt line = %d, want %d (the `var HomeIcon` decl line in ui/icons.gsx)", dp.Line, wantLine)
	}
}
