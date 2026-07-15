package codegen

import (
	"errors"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func rendererDeclTestModule(t *testing.T) string {
	t.Helper()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeMultiFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pgDir := filepath.Join(root, "pg")
	if err := os.MkdirAll(pgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMultiFile(t, pgDir, "pg.go", `package pg

type Timestamptz struct {
	Label string
}
`)
	return root
}

func rendererDeclPackage(t *testing.T, m *Module, dir string) *types.Package {
	t.Helper()
	ext, err := m.externalImporter()
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := newSourceDeclResolver(m, ext).packageForDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

func TestRendererDeclResolverSeesGoWithElementsFuncWithoutXGo(t *testing.T) {
	root := rendererDeclTestModule(t)
	dir := filepath.Join(root, "renderers")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMultiFile(t, dir, "renderers.gsx", `package renderers

import (
	"github.com/gsxhq/gsx"
	"example.com/app/pg"
)

func Timestamptz(v pg.Timestamptz) gsx.Node {
	return <time>{v.Label}</time>
}
`)
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	pkg := rendererDeclPackage(t, m, dir)
	fn, ok := pkg.Scope().Lookup("Timestamptz").(*types.Func)
	if !ok {
		t.Fatalf("Timestamptz = %T", pkg.Scope().Lookup("Timestamptz"))
	}
	if got := fn.Type().String(); !strings.Contains(got, "func(v example.com/app/pg.Timestamptz) github.com/gsxhq/gsx.Node") {
		t.Fatalf("signature = %s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "renderers.x.go")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("renderer declaration resolution wrote output: %v", err)
	}
}

func TestRendererDeclResolverIncludesHandwrittenGoCompanion(t *testing.T) {
	root := rendererDeclTestModule(t)
	dir := filepath.Join(root, "renderers")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMultiFile(t, dir, "view.gsx", `package renderers

component Badge(label string) {
	<span>{label}</span>
}
`)
	writeMultiFile(t, dir, "renderers.go", `package renderers

import (
	"github.com/gsxhq/gsx"
	"example.com/app/pg"
)

func Handwritten(v pg.Timestamptz) gsx.Node { return nil }
`)
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	pkg := rendererDeclPackage(t, m, dir)
	fn, ok := pkg.Scope().Lookup("Handwritten").(*types.Func)
	if !ok {
		t.Fatalf("Handwritten = %T", pkg.Scope().Lookup("Handwritten"))
	}
	if got := fn.Type().String(); !strings.Contains(got, "func(v example.com/app/pg.Timestamptz) github.com/gsxhq/gsx.Node") {
		t.Fatalf("signature = %s", got)
	}
}

func TestRendererDeclResolverRunsCanonicalPreprocessor(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantCode   string
		wantSource string
	}{
		{
			name:       "malformed embedded markup",
			body:       `{ wrap(<Broken></Other>) }`,
			wantCode:   "parse-error",
			wantSource: "parser",
		},
		{
			name:       "JavaScript failure after expansion",
			body:       `{ wrap(<script>let @{ value } = 1</script>) }`,
			wantCode:   "jsx-identifier-position",
			wantSource: "jsx",
		},
		{
			name:       "unsupported Go block element",
			body:       `{{ value := <div/> }}`,
			wantCode:   "unsupported-node",
			wantSource: "codegen",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := rendererDeclTestModule(t)
			dir := filepath.Join(root, "renderers")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			writeMultiFile(t, dir, "broken.gsx", "package renderers\n\ncomponent Broken(value string) {\n\t"+tc.body+"\n}\n")

			m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
			if err != nil {
				t.Fatal(err)
			}
			ext, err := m.externalImporter()
			if err != nil {
				t.Fatal(err)
			}
			_, err = newSourceDeclResolver(m, ext).packageForDir(dir)
			if err == nil {
				t.Fatal("packageForDir succeeded; want canonical preprocessing failure")
			}
			diags, ok := diagnosticsFromSourceError(err)
			if !ok {
				t.Fatalf("packageForDir error = %T %v; want structured diagnostics", err, err)
			}
			if len(diags) != 1 || diags[0].Code != tc.wantCode || diags[0].Source != tc.wantSource {
				t.Fatalf("diagnostics = %+v; want one %s/%s diagnostic", diags, tc.wantSource, tc.wantCode)
			}
			if diags[0].Start.Filename != filepath.Join(dir, "broken.gsx") || diags[0].Start.Line == 0 {
				t.Fatalf("diagnostic is not positioned in renderer source: %+v", diags[0])
			}
		})
	}
}

func TestRendererDeclResolverRejectsMalformedActiveGoCompanion(t *testing.T) {
	root := rendererDeclTestModule(t)
	dir := filepath.Join(root, "renderers")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMultiFile(t, dir, "view.gsx", `package renderers

component Badge(label string) {
	<span>{label}</span>
}
`)
	writeMultiFile(t, dir, "renderers.go", `package renderers

func Broken( {
`)
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	ext, err := m.externalImporter()
	if err != nil {
		t.Fatal(err)
	}
	_, err = newSourceDeclResolver(m, ext).packageForDir(dir)
	if err == nil || !strings.Contains(err.Error(), "renderers.go") {
		t.Fatalf("packageForDir error = %v, want malformed active companion error", err)
	}
}

func TestRendererDeclResolverRejectsAuthoritativePackageMetadataErrors(t *testing.T) {
	root := rendererDeclTestModule(t)
	dir := filepath.Join(root, "renderers")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMultiFile(t, dir, "view.gsx", `package renderers

component Badge(label string) {
	<span>{label}</span>
}
`)
	writeMultiFile(t, dir, "first.go", "package first\n")
	writeMultiFile(t, dir, "second.go", "package second\n")
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	ext, err := m.externalImporter()
	if err != nil {
		t.Fatal(err)
	}
	_, err = newSourceDeclResolver(m, ext).packageForDir(dir)
	if err == nil || !strings.Contains(err.Error(), "found packages") {
		t.Fatalf("packageForDir error = %v, want retained package metadata mismatch", err)
	}
}

func TestRendererDeclResolverRecursivelyImportsLocalGsxDeclarations(t *testing.T) {
	root := rendererDeclTestModule(t)
	modelDir := filepath.Join(root, "model")
	rendererDir := filepath.Join(root, "renderers")
	for _, dir := range []string{modelDir, rendererDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeMultiFile(t, modelDir, "model.gsx", `package model

type Moment struct {
	Label string
}
`)
	writeMultiFile(t, rendererDir, "renderers.gsx", `package renderers

import (
	"github.com/gsxhq/gsx"
	"example.com/app/model"
)

func Moment(v model.Moment) gsx.Node {
	return <time>{v.Label}</time>
}
`)
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	pkg := rendererDeclPackage(t, m, rendererDir)
	fn, ok := pkg.Scope().Lookup("Moment").(*types.Func)
	if !ok {
		t.Fatalf("Moment = %T", pkg.Scope().Lookup("Moment"))
	}
	if got := fn.Type().String(); !strings.Contains(got, "func(v example.com/app/model.Moment) github.com/gsxhq/gsx.Node") {
		t.Fatalf("signature = %s", got)
	}
}

func TestRendererDeclResolverRechecksGoOnlyIntermediaryInOneDeclarationUniverse(t *testing.T) {
	root := rendererDeclTestModule(t)
	leafDir := filepath.Join(root, "leaf")
	bridgeDir := filepath.Join(root, "bridge")
	rendererDir := filepath.Join(root, "renderers")
	for _, dir := range []string{leafDir, bridgeDir, rendererDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeMultiFile(t, leafDir, "leaf.gsx", `package leaf

component Card(title string) { <span>{title}</span> }
`)
	// The cold inventory must hide this paired output. The renderer resolver
	// still has to re-check bridge.go against the exact GSX declaration rather
	// than consume the partial/stale bridge package from the external load.
	writeMultiFile(t, leafDir, "leaf.x.go", `package leaf

import "github.com/gsxhq/gsx"

type CardProps struct { Poison int }
func Card(CardProps) gsx.Node { return nil }
`)
	writeMultiFile(t, bridgeDir, "bridge.go", `package bridge

import (
	"example.com/app/leaf"
	"github.com/gsxhq/gsx"
)

type Props = leaf.CardProps
func Card(p Props) gsx.Node { return leaf.Card(p) }
`)
	writeMultiFile(t, rendererDir, "renderers.gsx", `package renderers

import (
	"example.com/app/bridge"
	"github.com/gsxhq/gsx"
)

func Moment(v bridge.Props) gsx.Node { return <time>{v.Title}</time> }
`)

	rendererPath := "example.com/app/renderers"
	m, err := Open(Options{
		ModuleRoot: root,
		ModulePath: "example.com/app",
		Renderers: []RendererAlias{{
			TypeKey:  "example.com/app/bridge.Props",
			PkgPath:  rendererPath,
			FuncName: "Moment",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	packages, _, err := m.rendererPackagesFromExt()
	if err != nil {
		t.Fatal(err)
	}
	pkg := packages[rendererPath]
	fn, ok := pkg.Scope().Lookup("Moment").(*types.Func)
	if !ok {
		t.Fatalf("Moment = %T", pkg.Scope().Lookup("Moment"))
	}
	signature := fn.Type().(*types.Signature)
	structure, ok := types.Unalias(signature.Params().At(0).Type()).Underlying().(*types.Struct)
	if !ok || structure.NumFields() != 1 || structure.Field(0).Name() != "Title" {
		t.Fatalf("Moment parameter = %v, want current shipping CardProps through retained bridge source", signature.Params().At(0).Type())
	}

	firstPackage := pkg
	m.SetOverride(filepath.Join(leafDir, "leaf.gsx"), []byte("package leaf\n\ncomponent Card(count int) { <span>{count}</span> }\n"))
	m.applyDirty()
	packages, _, err = m.rendererPackagesFromExt()
	if err != nil {
		t.Fatal(err)
	}
	pkg = packages[rendererPath]
	if pkg == firstPackage {
		t.Fatal("leaf edit retained the renderer package cached through the exact intermediary graph")
	}
	fn = pkg.Scope().Lookup("Moment").(*types.Func)
	signature = fn.Type().(*types.Signature)
	structure, ok = types.Unalias(signature.Params().At(0).Type()).Underlying().(*types.Struct)
	if !ok || structure.NumFields() != 1 || structure.Field(0).Name() != "Count" {
		t.Fatalf("warm Moment parameter = %v, want current shipping CardProps Count field", signature.Params().At(0).Type())
	}
	if got := m.externalLoads(); got != 1 {
		t.Fatalf("external loads after warm leaf edit = %d, want one authoritative cold inventory", got)
	}
}

func TestRendererDeclResolverReportsLocalGsxImportCycle(t *testing.T) {
	root := rendererDeclTestModule(t)
	aDir := filepath.Join(root, "a")
	bDir := filepath.Join(root, "b")
	for _, dir := range []string{aDir, bDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeMultiFile(t, aDir, "a.gsx", `package a

import "example.com/app/b"

type A struct {
	B b.B
}
`)
	writeMultiFile(t, bDir, "b.gsx", `package b

import "example.com/app/a"

type B struct {
	A a.A
}
`)
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	ext, err := m.externalImporter()
	if err != nil {
		t.Fatal(err)
	}
	_, err = newSourceDeclResolver(m, ext).packageForDir(aDir)
	if err == nil || !strings.Contains(err.Error(), "import cycle through "+aDir) {
		t.Fatalf("packageForDir error = %v, want import cycle through %s", err, aDir)
	}
}

func TestRendererDeclResolverIgnoresGoWithElementsBodies(t *testing.T) {
	root := rendererDeclTestModule(t)
	dir := filepath.Join(root, "renderers")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMultiFile(t, dir, "renderers.gsx", `package renderers

import (
	"strings"
	"github.com/gsxhq/gsx"
	"example.com/app/pg"
)

func Label(v pg.Timestamptz) gsx.Node {
	label := strings.ToUpper(v.Label)
	missing(label)
	return <time>{label}</time>
}
`)
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	pkg := rendererDeclPackage(t, m, dir)
	if _, ok := pkg.Scope().Lookup("Label").(*types.Func); !ok {
		t.Fatalf("Label = %T", pkg.Scope().Lookup("Label"))
	}
}

func TestRendererDeclResolverStubsGoWithElementsFunctionLiteralBodies(t *testing.T) {
	root := rendererDeclTestModule(t)
	dir := filepath.Join(root, "renderers")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMultiFile(t, dir, "renderers.gsx", `package renderers

import (
	"strings"
	"github.com/gsxhq/gsx"
	"example.com/app/pg"
)

var Label = func(v pg.Timestamptz) gsx.Node {
	label := strings.ToUpper(v.Label)
	missing(label)
	return <time>{label}</time>
}
`)
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	pkg := rendererDeclPackage(t, m, dir)
	variable, ok := pkg.Scope().Lookup("Label").(*types.Var)
	if !ok {
		t.Fatalf("Label = %T, want package variable", pkg.Scope().Lookup("Label"))
	}
	signature, ok := variable.Type().(*types.Signature)
	if !ok || signature.Params().Len() != 1 {
		t.Fatalf("Label type = %v, want func(pg.Timestamptz) gsx.Node", variable.Type())
	}
}
