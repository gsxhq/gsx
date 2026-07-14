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
	pkg, err := newRendererDeclResolver(m, ext).packageForDir(dir)
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
	_, err = newRendererDeclResolver(m, ext).packageForDir(dir)
	if err == nil || !strings.Contains(err.Error(), "renderers.go") {
		t.Fatalf("packageForDir error = %v, want malformed active companion error", err)
	}
}

func TestRendererDeclResolverRejectsImportDirErrors(t *testing.T) {
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
	_, err = newRendererDeclResolver(m, ext).packageForDir(dir)
	if err == nil || !strings.Contains(err.Error(), "found packages") {
		t.Fatalf("packageForDir error = %v, want ImportDir package mismatch", err)
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
	_, err = newRendererDeclResolver(m, ext).packageForDir(aDir)
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
