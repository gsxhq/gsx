package codegen

import (
	"bytes"
	"errors"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func localRendererModule(t *testing.T) (root, rendererDir, viewsDir string) {
	t.Helper()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	root = t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, root, "pg/pg.go", `package pg

type Timestamptz struct {
	Label string
}
`)
	rendererDir = filepath.Join(root, "renderers")
	// Keep the package visible to the pre-bootstrap go/packages load while the
	// renderer declaration itself exists only in GSX and no generated Go exists.
	writeFile(t, rendererDir, "package.go", "package renderers\n")
	writeFile(t, rendererDir, "renderers.gsx", `package renderers

import (
	"example.com/app/pg"
	"github.com/gsxhq/gsx"
)

func Timestamptz(v pg.Timestamptz) gsx.Node {
	return <time>{v.Label}</time>
}

component Preview(sample pg.Timestamptz) {
	<div>{sample}</div>
}
`)
	viewsDir = filepath.Join(root, "views")
	writeFile(t, viewsDir, "views.gsx", `package views

import "example.com/app/pg"

component Show(sample pg.Timestamptz) {
	<div>{sample}</div>
}
`)
	return root, rendererDir, viewsDir
}

func localRendererOptions() Options {
	return Options{
		FilterPkgs: []string{stdImportPath},
		Renderers: []RendererAlias{{
			TypeKey:  "example.com/app/pg.Timestamptz",
			PkgPath:  "example.com/app/renderers",
			FuncName: "Timestamptz",
		}},
	}
}

func generatedFor(t *testing.T, result DirResult, name string) string {
	t.Helper()
	for path, src := range result.Files {
		if filepath.Base(path) == name {
			return string(src)
		}
	}
	t.Fatalf("generated file for %q not found in %v", name, result.Files)
	return ""
}

func TestGenerateLocalRendererPackageWithoutXGo(t *testing.T) {
	root, rendererDir, _ := localRendererModule(t)
	res, err := GenerateDirs(root, []string{rendererDir}, localRendererOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	dr := res[rendererDir]
	if hasDiagErrors(dr.Diags) {
		t.Fatalf("diags = %v", dr.Diags)
	}
	src := generatedFor(t, dr, "renderers.gsx")
	if !strings.Contains(src, "Timestamptz((sample))") {
		t.Fatalf("generated:\n%s", src)
	}
	if strings.Contains(src, `"example.com/app/renderers"`) {
		t.Fatalf("self import:\n%s", src)
	}
	if _, err := os.Stat(filepath.Join(rendererDir, "renderers.x.go")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clean generation wrote intermediate output: %v", err)
	}
}

func TestGenerateCrossPackageRendererWithoutXGo(t *testing.T) {
	root, rendererDir, viewsDir := localRendererModule(t)
	opts := localRendererOptions()
	forward, err := GenerateDirs(root, []string{viewsDir, rendererDir}, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{viewsDir, rendererDir} {
		if hasDiagErrors(forward[dir].Diags) {
			t.Fatalf("%s diags = %v", dir, forward[dir].Diags)
		}
	}
	viewSrc := generatedFor(t, forward[viewsDir], "views.gsx")
	if !strings.Contains(viewSrc, `_gsxf0 "example.com/app/renderers"`) || !strings.Contains(viewSrc, "_gsxf0.Timestamptz((sample))") {
		t.Fatalf("cross-package renderer call/import missing:\n%s", viewSrc)
	}
	for _, path := range []string{filepath.Join(rendererDir, "renderers.x.go"), filepath.Join(viewsDir, "views.x.go")} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("batched generation wrote intermediate %s: %v", path, err)
		}
	}
	reverse, err := GenerateDirs(root, []string{rendererDir, viewsDir}, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{viewsDir, rendererDir} {
		if !equalGenerated(forward[dir].Files, reverse[dir].Files) {
			t.Fatalf("generated output for %s depends on directory order", dir)
		}
	}
}

func TestGenerateLocalRendererPackageWithoutGoCompanion(t *testing.T) {
	root, rendererDir, _ := localRendererModule(t)
	if err := os.Remove(filepath.Join(rendererDir, "package.go")); err != nil {
		t.Fatal(err)
	}
	res, err := GenerateDirs(root, []string{rendererDir}, localRendererOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	dr := res[rendererDir]
	if hasDiagErrors(dr.Diags) {
		t.Fatalf("diags = %v", dr.Diags)
	}
	if src := generatedFor(t, dr, "renderers.gsx"); !strings.Contains(src, "Timestamptz((sample))") {
		t.Fatalf("generated:\n%s", src)
	}
}

func TestGenerateRendererLastWinnerControlsPackageResolution(t *testing.T) {
	root, rendererDir, _ := localRendererModule(t)
	opts := localRendererOptions()
	opts.Renderers = append([]RendererAlias{{
		TypeKey:  "example.com/app/pg.Timestamptz",
		PkgPath:  "example.com/missing",
		FuncName: "Missing",
	}}, opts.Renderers...)
	res, err := GenerateDirs(root, []string{rendererDir}, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	dr := res[rendererDir]
	if hasDiagErrors(dr.Diags) {
		t.Fatalf("diags = %v", dr.Diags)
	}
	if src := generatedFor(t, dr, "renderers.gsx"); !strings.Contains(src, "Timestamptz((sample))") {
		t.Fatalf("generated:\n%s", src)
	}
}

func equalGenerated(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for path, src := range a {
		if !bytes.Equal(src, b[path]) {
			return false
		}
	}
	return true
}

func TestGenerateLocalRendererMissingTarget(t *testing.T) {
	root, rendererDir, _ := localRendererModule(t)
	opts := localRendererOptions()
	opts.Renderers[0].FuncName = "Missing"
	_, err := GenerateDirs(root, []string{rendererDir}, opts, nil)
	if err == nil || !strings.Contains(err.Error(), `func "Missing" not found in package "example.com/app/renderers"`) {
		t.Fatalf("GenerateDirs error = %v", err)
	}
}

func TestGenerateLocalRendererWrongSignature(t *testing.T) {
	root, rendererDir, _ := localRendererModule(t)
	writeFile(t, rendererDir, "renderers.gsx", `package renderers

import (
	"example.com/app/pg"
	"github.com/gsxhq/gsx"
)

func Timestamptz(v string) gsx.Node {
	return <time>{v}</time>
}

component Preview(sample pg.Timestamptz) {
	<div>{sample}</div>
}
`)
	_, err := GenerateDirs(root, []string{rendererDir}, localRendererOptions(), nil)
	if err == nil || !strings.Contains(err.Error(), `renderer "Timestamptz" takes string; registered for "example.com/app/pg.Timestamptz"`) {
		t.Fatalf("GenerateDirs error = %v", err)
	}
}

func TestGenerateRendererChainSpansLocalAndExternalPackages(t *testing.T) {
	root, rendererDir, _ := localRendererModule(t)
	writeFile(t, root, "wrapped/wrapped.go", "package wrapped\n\ntype Value struct { Label string }\n")
	writeFile(t, root, "gorender/gorender.go", `package gorender

import "example.com/app/wrapped"

func Value(v wrapped.Value) string { return v.Label }
`)
	writeFile(t, rendererDir, "renderers.gsx", `package renderers

import (
	"example.com/app/pg"
	"example.com/app/wrapped"
)

func Timestamptz(v pg.Timestamptz) wrapped.Value {
	return wrapped.Value{Label: v.Label}
}
`)
	opts := localRendererOptions()
	opts.Renderers = append(opts.Renderers, RendererAlias{
		TypeKey:  "example.com/app/wrapped.Value",
		PkgPath:  "example.com/app/gorender",
		FuncName: "Value",
	})
	_, err := GenerateDirs(root, []string{rendererDir}, opts, nil)
	if err == nil || !strings.Contains(err.Error(), "renderers apply once and never chain") {
		t.Fatalf("GenerateDirs error = %v", err)
	}
}

func TestGenerateExternalModuleGsxRendererStillRequiresGoDeclaration(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	extRoot := filepath.Join(root, "external")
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire (\n\tgithub.com/gsxhq/gsx v0.0.0\n\texample.com/external v0.0.0\n)\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\nreplace example.com/external => ./external\n")
	writeFile(t, extRoot, "go.mod", "module example.com/external\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, extRoot, "renderers/renderers.gsx", `package renderers

import "example.com/app/pg"

func Timestamptz(v pg.Timestamptz) string { return v.Label }
`)
	writeFile(t, root, "pg/pg.go", "package pg\n\ntype Timestamptz struct { Label string }\n")
	viewsDir := filepath.Join(root, "views")
	writeFile(t, viewsDir, "views.gsx", "package views\n\ncomponent View() { <p>ok</p> }\n")
	opts := localRendererOptions()
	opts.Renderers[0].PkgPath = "example.com/external/renderers"
	_, err = GenerateDirs(root, []string{viewsDir}, opts, nil)
	if err == nil || (!strings.Contains(err.Error(), "was not loaded") && !strings.Contains(err.Error(), "type resolution failed")) {
		t.Fatalf("GenerateDirs error = %v, want external buildability failure", err)
	}
}

func TestModuleBundleRendererTableRemainsAuthoritative(t *testing.T) {
	root, rendererDir, viewsDir := localRendererModule(t)
	writeFile(t, root, "prebuilt/prebuilt.go", `package prebuilt

import "example.com/app/pg"

func Timestamptz(v pg.Timestamptz) string { return v.Label }
`)
	// Any filesystem bootstrap would parse this replacement and fail. Bundle mode
	// must instead use only its prebuilt importer and renderer table.
	writeFile(t, rendererDir, "renderers.gsx", "package renderers\n\nfunc Broken( {\n")
	bundle, err := newCachedResolver(root, []string{stdImportPath}, nil, []string{
		"example.com/app/pg",
		"example.com/app/prebuilt",
	})
	if err != nil {
		t.Fatal(err)
	}
	bundle.table.renderers = rendererTable{
		"example.com/app/pg.Timestamptz": {
			funcName: "Timestamptz",
			alias:    "_gsxf0",
			pkgPath:  "example.com/app/prebuilt",
			result:   types.Typ[types.String],
		},
	}
	opts := localRendererOptions()
	m, err := Open(Options{
		ModuleRoot: root,
		ModulePath: "example.com/app",
		FilterPkgs: opts.FilterPkgs,
		Renderers:  opts.Renderers,
		Bundle:     bundle,
	})
	if err != nil {
		t.Fatal(err)
	}
	out, diags, err := m.Generate(viewsDir)
	if err != nil {
		t.Fatal(err)
	}
	if hasDiagErrors(diags) {
		t.Fatalf("diags = %v", diags)
	}
	src := string(out[filepath.Join(viewsDir, "views.gsx")])
	if !strings.Contains(src, `_gsxf0 "example.com/app/prebuilt"`) || !strings.Contains(src, "_gsxf0.Timestamptz((sample))") {
		t.Fatalf("prebuilt renderer not used:\n%s", src)
	}
	if got := m.externalLoads(); got != 0 {
		t.Fatalf("bundle Module did an external packages.Load: %d", got)
	}
}

func TestModuleRendererCachesClearWithFileSet(t *testing.T) {
	root, _, viewsDir := localRendererModule(t)
	m, err := Open(Options{
		ModuleRoot: root,
		ModulePath: "example.com/app",
		FilterPkgs: localRendererOptions().FilterPkgs,
		Renderers:  localRendererOptions().Renderers,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Generate(viewsDir); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	before := m.rendererPkgs["example.com/app/renderers"]
	populated := m.rendererPkgsDone && m.rendererTblDone
	m.mu.Unlock()
	if before == nil || !populated {
		t.Fatal("renderer caches were not populated")
	}
	m.rebuildFset()
	m.mu.Lock()
	cleared := m.rendererPkgs == nil && m.rendererTbl == nil && !m.rendererPkgsDone && !m.rendererTblDone
	m.mu.Unlock()
	if !cleared {
		t.Fatal("rebuildFset retained renderer types tied to the old FileSet")
	}
	if _, _, err := m.Generate(viewsDir); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	after := m.rendererPkgs["example.com/app/renderers"]
	m.mu.Unlock()
	if after == nil || after == before {
		t.Fatal("renderer declarations were not rebuilt into the fresh FileSet")
	}
}
