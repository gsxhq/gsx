package lsp

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/codegen"
)

// buildIconVariantFixture writes a two-variant Icon fixture (icon_a.gsx /
// icon_b.gsx under disjoint //go:build tags, plus a Page component using
// <Icon/>) into a fresh temp module and returns the analyzed *codegen.
// PackageResult for pageDir. Shared by TestDefinitionShowsAllVariants-style
// tests and TestReferencesIncludesAllVariantDecls.
func buildIconVariantFixture(t *testing.T) (pageDir string, pagePath string, pageSrc string, iconASrc string, iconBSrc string, pr *codegen.PackageResult) {
	t.Helper()
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeLSPTestFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")

	pageDir = filepath.Join(root, "page")
	iconASrc = "//go:build !never\n\npackage page\n\ncomponent Icon(name string) { <a>{ name }</a> }\n"
	iconBSrc = "//go:build never\n\npackage page\n\ncomponent Icon(name string) { <b>{ name }</b> }\n"
	writeLSPTestFile(t, pageDir, "icon_a.gsx", iconASrc)
	writeLSPTestFile(t, pageDir, "icon_b.gsx", iconBSrc)
	pageSrc = "package page\n\ncomponent Page() {\n\t<Icon name=\"hi\"/>\n}\n"
	pagePath = filepath.Join(pageDir, "page.gsx")
	writeLSPTestFile(t, pageDir, "page.gsx", pageSrc)

	m, err := codegen.Open(codegen.Options{ModuleRoot: root, ModulePath: "example.com/app", FilterPkgs: []string{codegen.StdImportPath}})
	if err != nil {
		t.Fatal(err)
	}
	pr, err = m.Package(pageDir)
	if err != nil {
		t.Fatalf("Package: %v", err)
	}
	if len(pr.Diags) > 0 {
		t.Fatalf("unexpected diagnostics: %+v", pr.Diags)
	}
	return pageDir, pagePath, pageSrc, iconASrc, iconBSrc, pr
}

// TestReferencesIncludesAllVariantDecls is Task 8: find-references must list
// EVERY build-tag variant's declaration, not just the primary one (cr.Decl,
// which is always Decls[0] — the alphabetically-first file, icon_a.gsx).
// The cursor is placed on the SECOND variant's declaration (icon_b.gsx, which
// is NOT cr.Decl) to exercise identifyCrossRef's new cr.Decls loop: before
// this task, only cr.Decl was checked, so a cursor on a non-primary variant
// would fail to identify the component at all (empty result). After the fix,
// the result must contain both variant declarations plus the page.gsx usage
// site.
func TestReferencesIncludesAllVariantDecls(t *testing.T) {
	pageDir, _, _, _, iconBSrc, pr := buildIconVariantFixture(t)

	// Mirror gen/lsp.go AnalyzeModule's Phase-3 CrossIndex → lsp.CrossRef copy
	// (Decls included, this task) rather than importing gen (which would
	// import lsp back and cycle).
	var moduleRefs []CrossRef
	for _, v := range pr.CrossIndex {
		moduleRefs = append(moduleRefs, CrossRef{Name: v.Name, Decl: v.Decl, Decls: v.Decls, Refs: v.Refs})
	}

	a := &moduleRefsAnalyzer{moduleRefs: moduleRefs}

	iconBPath := filepath.Join(pageDir, "icon_b.gsx")
	uri := pathToURI(iconBPath)
	// Cursor on the "I" of "Icon" in `component Icon(...)` — line 5 (1-based),
	// column 11 (1-based) → 0-based line 4, character 10 ("component " is 10
	// bytes).
	refFrame := jsonFrame(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "textDocument/references",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"position":     map[string]any{"line": 4, "character": 10},
			"context":      map[string]any{"includeDeclaration": true},
		},
	})
	frames := initFrame() + didOpenFrame(uri, iconBSrc) + refFrame + exitFrame()

	out := drive(t, a, frames)

	if !strings.Contains(out, "icon_a.gsx") {
		t.Fatalf("result missing icon_a.gsx variant declaration:\n%s", out)
	}
	if !strings.Contains(out, "icon_b.gsx") {
		t.Fatalf("result missing icon_b.gsx variant declaration:\n%s", out)
	}
	if !strings.Contains(out, "page.gsx") {
		t.Fatalf("result missing page.gsx reference site:\n%s", out)
	}
}

// TestDefinitionShowsAllVariants is Task 7: componentTagDeclAt must surface
// EVERY build-tag variant's declaration, not just the primary one (Task 6
// made codegen.CrossRef.Decls carry all of them; this test proves the LSP
// mirror + tag lookup thread that all the way through). Two same-signature
// Icon components live under disjoint //go:build tags (icon_a.gsx / icon_b.gsx,
// mirroring internal/codegen/crossnav_test.go's TestCrossIndexMultiValuedVariants
// fixture) and a Page component in page.gsx uses <Icon/>. A cursor on the tag
// name must resolve to both variant declarations, one per file.
func TestDefinitionShowsAllVariants(t *testing.T) {
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeLSPTestFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")

	pageDir := filepath.Join(root, "page")
	writeLSPTestFile(t, pageDir, "icon_a.gsx", "//go:build !never\n\npackage page\n\ncomponent Icon(name string) { <a>{ name }</a> }\n")
	writeLSPTestFile(t, pageDir, "icon_b.gsx", "//go:build never\n\npackage page\n\ncomponent Icon(name string) { <b>{ name }</b> }\n")
	pageSrc := "package page\n\ncomponent Page() {\n\t<Icon name=\"hi\"/>\n}\n"
	pagePath := filepath.Join(pageDir, "page.gsx")
	writeLSPTestFile(t, pageDir, "page.gsx", pageSrc)

	m, err := codegen.Open(codegen.Options{ModuleRoot: root, ModulePath: "example.com/app", FilterPkgs: []string{codegen.StdImportPath}})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := m.Package(pageDir)
	if err != nil {
		t.Fatalf("Package: %v", err)
	}
	if len(pr.Diags) > 0 {
		t.Fatalf("unexpected diagnostics: %+v", pr.Diags)
	}

	// Mirror gen/lsp.go's adaptPackageResult CrossIndex conversion (Decls included,
	// Task 7) rather than importing the gen package (which would import lsp back
	// and cycle); this is the same field-by-field copy adaptPackageResult does.
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

	off := strings.Index(pageSrc, "<Icon") + 1 // cursor on 'I' of the tag name
	decls, ok := componentTagDeclAt(pkg, pagePath, off)
	if !ok {
		t.Fatal("componentTagDeclAt returned false for the Icon tag")
	}
	if len(decls) != 2 {
		t.Fatalf("componentTagDeclAt returned %d decls, want 2 (one per variant): %+v", len(decls), decls)
	}
	got := map[string]bool{}
	for _, d := range decls {
		got[filepath.Base(d.Filename)] = true
	}
	if !got["icon_a.gsx"] || !got["icon_b.gsx"] {
		t.Fatalf("decls = %+v, want one per variant file (icon_a.gsx and icon_b.gsx)", decls)
	}
}
