package codegen

import (
	"go/types"
	"path/filepath"
	"testing"
)

func requireArrayLength(t *testing.T, typ types.Type, want int64) {
	t.Helper()
	typ = types.Unalias(typ)
	if named, ok := typ.(*types.Named); ok {
		typ = named.Underlying()
	}
	array, ok := typ.Underlying().(*types.Array)
	if !ok || array.Len() != want {
		t.Fatalf("type = %s (%T), want array length %d", typ, typ, want)
	}
}

func TestManualCheckersUseRetained386TypeSizes(t *testing.T) {
	t.Setenv("GOOS", "linux")
	t.Setenv("GOARCH", "386")
	t.Setenv("CGO_ENABLED", "0")

	root := t.TempDir()
	repo := repoRoot(t)
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repo+"\n")
	writeFile(t, filepath.Join(root, "width"), "width.go", "package width\nimport \"unsafe\"\ntype Width [unsafe.Sizeof(uint(0))]byte\n")
	writeFile(t, filepath.Join(root, "bridge"), "bridge.go", "package bridge\nimport \"example.com/app/width\"\ntype Width = width.Width\n")
	uiDir := filepath.Join(root, "ui")
	writeFile(t, uiDir, "card.gsx", "package ui\nimport \"example.com/app/bridge\"\ncomponent Card(value bridge.Width) { <span/> }\n")
	pagesDir := filepath.Join(root, "pages")
	writeFile(t, pagesDir, "page.gsx", "package pages\nimport (\"example.com/app/bridge\"; \"example.com/app/ui\")\ncomponent Page(value bridge.Width) { <ui.Card value={value}/> }\n")
	rendererDir := filepath.Join(root, "renderers")
	writeFile(t, rendererDir, "render.gsx", "package renderers\nimport \"example.com/app/bridge\"\nfunc Width(value bridge.Width) string { return \"width\" }\n")

	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app", FilterPkgs: []string{stdImportPath}})
	if err != nil {
		t.Fatal(err)
	}
	shipping, err := module.Package(pagesDir)
	if err != nil {
		t.Fatal(err)
	}
	page := shipping.Types.Scope().Lookup("Page").(*types.Func).Type().(*types.Signature)
	requireArrayLength(t, page.Params().At(0).Type(), 4)

	external, err := module.externalImporter()
	if err != nil {
		t.Fatal(err)
	}
	exact, err := newComponentTargetImporter(module, external).Import("example.com/app/ui")
	if err != nil {
		t.Fatal(err)
	}
	card := exact.Scope().Lookup("Card").(*types.Func).Type().(*types.Signature)
	requireArrayLength(t, card.Params().At(0).Type(), 4)

	rendererPackage, err := newSourceDeclResolver(module, external).packageForDir(rendererDir)
	if err != nil {
		t.Fatal(err)
	}
	renderer := rendererPackage.Scope().Lookup("Width").(*types.Func).Type().(*types.Signature)
	requireArrayLength(t, renderer.Params().At(0).Type(), 4)

	registered, err := Open(Options{
		ModuleRoot: root, ModulePath: "example.com/app", FilterPkgs: []string{stdImportPath},
		Renderers: []RendererAlias{{TypeKey: "example.com/app/width.Width", PkgPath: "example.com/app/renderers", FuncName: "Width"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := registered.rendererPackagesFromExt(); err != nil {
		t.Fatalf("registered renderer did not share the authoritative target type universe: %v", err)
	}
	if _, err := registered.rendererBaseTable(); err != nil {
		t.Fatalf("registered renderer did not preserve canonical target identity: %v", err)
	}
}

func TestBundleManualCheckerUsesExplicit386TypeSizes(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/bundle\n\ngo 1.26.1\n")
	writeFile(t, root, "width.gsx", "package views\nimport \"unsafe\"\ncomponent Width(value [unsafe.Sizeof(uint(0))]byte) { <span/> }\n")
	imports := targetTestImporter().(mapImporter)
	imports["unsafe"] = types.Unsafe
	bundle := &Bundle{imp: imports, table: funcTables{}, sizes: types.SizesFor("gc", "386"), goVersion: "go1.26"}
	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/bundle", Bundle: bundle})
	if err != nil {
		t.Fatal(err)
	}
	result, err := module.Package(root)
	if err != nil {
		t.Fatal(err)
	}
	width := result.Types.Scope().Lookup("Width").(*types.Func).Type().(*types.Signature)
	requireArrayLength(t, width.Params().At(0).Type(), 4)

	// The exact-target checker shares the same Bundle environment.
	exact, err := newComponentTargetImporter(module, imports).Import("example.com/bundle")
	if err != nil {
		t.Fatal(err)
	}
	signature := exact.Scope().Lookup("Width").(*types.Func).Type().(*types.Signature)
	requireArrayLength(t, signature.Params().At(0).Type(), 4)
}
