package lsp

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/sourceintel"
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
// EVERY build-tag variant's declaration, not just the primary one. The cursor
// is placed on the SECOND variant's declaration (icon_b.gsx).
func TestReferencesIncludesAllVariantDecls(t *testing.T) {
	pageDir, _, _, _, iconBSrc, pr := buildIconVariantFixture(t)

	graph := sourceintel.NewSymbolGraph()
	graph.AddIndex(pr.SourceIndex, sourceintel.NewKeyer(pr.Types))
	a := &moduleRefsAnalyzer{moduleGraph: graph}

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
// EVERY build-tag variant's declaration, not just the primary one. Two
// same-signature Icon components live under disjoint //go:build tags
// (icon_a.gsx / icon_b.gsx) and a Page component in page.gsx uses <Icon/>. A
// cursor on the tag name must resolve to both variant declarations, one per
// file.
func TestDefinitionShowsAllVariants(t *testing.T) {
	pageDir, pagePath, pageSrc, _, _, pr := buildIconVariantFixture(t)

	pkg := &Package{
		GSXFset:          pr.GSXFset,
		Fset:             pr.Fset,
		Position:         pr.PositionFor,
		PositionPhysical: pr.PositionForPhysical,
		Info:             pr.Info,
		SourceIndex:      pr.SourceIndex,
		Types:            pr.Types,
		Files:            pr.GSXFiles,
		ExprMap:          pr.ExprMap,
	}

	off := strings.Index(pageSrc, "<Icon") + 1 // cursor on 'I' of the tag name
	decls, ok := componentTagDeclAt(pkg, pagePath, []byte(pageSrc), off)
	if !ok {
		t.Fatal("componentTagDeclAt returned false for the Icon tag")
	}
	if len(decls) != 2 {
		t.Fatalf("componentTagDeclAt returned %d decls, want 2 (one per variant): %+v", len(decls), decls)
	}
	got := map[string]bool{}
	for _, d := range decls {
		if filepath.Dir(d.Path) != pageDir {
			t.Errorf("decl %+v is outside the analyzed package dir %s", d, pageDir)
		}
		got[filepath.Base(d.Path)] = true
	}
	if !got["icon_a.gsx"] || !got["icon_b.gsx"] {
		t.Fatalf("decls = %+v, want one per variant file (icon_a.gsx and icon_b.gsx)", decls)
	}
}
