package corpus

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/gsxhq/gsx/internal/codegen"
)

func TestLoadCaseSinglePackage(t *testing.T) {
	c, err := loadCase("testdata/loadertest/single.txtar")
	if err != nil {
		t.Fatal(err)
	}
	if c.name != "loadertest/single" {
		t.Errorf("name = %q, want loadertest/single", c.name)
	}
	if c.dir != "loadertest_single" {
		t.Errorf("dir = %q, want loadertest_single", c.dir)
	}
	if c.multiPkg {
		t.Errorf("multiPkg = true, want false")
	}
	if string(c.invoke) != "Greeting(GreetingProps{Name: \"X\"})\n" {
		t.Errorf("invoke = %q", c.invoke)
	}
	if _, ok := c.files["input.gsx"]; !ok {
		t.Errorf("missing input.gsx in files")
	}
	if _, ok := c.goldens["render.golden"]; !ok {
		t.Errorf("missing render.golden in goldens")
	}
	if !c.renderable() {
		t.Errorf("renderable() = false, want true")
	}
}

func TestLoadCaseMultiPackage(t *testing.T) {
	c, err := loadCase("testdata/loadertest/multi.txtar")
	if err != nil {
		t.Fatal(err)
	}
	if !c.multiPkg {
		t.Errorf("multiPkg = false, want true")
	}
	if c.modulePath != "example.com/app" {
		t.Errorf("modulePath = %q, want example.com/app", c.modulePath)
	}
	if _, ok := c.files["ui/button.gsx"]; !ok {
		t.Errorf("missing ui/button.gsx")
	}
}

func TestLoadCaseFilterPackages(t *testing.T) {
	dir := t.TempDir()
	src := `-- gsx.toml --
filter_packages = ["./filters", "github.com/gsxhq/gsx/std"]
-- filters/filters.go --
package filters
-- input.gsx --
package views

component C() { <p>hi</p> }
`
	path := filepath.Join(dir, "testdata", "cases", "pipeerr", "fp.txtar")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := loadCase(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"corpustest/cases/pipeerr_fp/filters", "github.com/gsxhq/gsx/std"}
	if !slices.Equal(c.filterPkgs, want) {
		t.Fatalf("filterPkgs = %v, want %v", c.filterPkgs, want)
	}
	if _, hasToml := c.files["gsx.toml"]; hasToml {
		t.Fatal("gsx.toml must not be written to disk")
	}
}

// TestLoadCaseRenderers pins the [renderers] resolution rules: "./"-prefixed
// package parts resolve against caseImportRoot on BOTH the key's package part
// (respecting an optional "*" pointer prefix) and the value's package part,
// absolute import paths pass through untouched, and the resulting list is
// sorted by TypeKey (TOML map iteration order is random).
func TestLoadCaseRenderers(t *testing.T) {
	dir := t.TempDir()
	src := `-- gsx.toml --
[renderers]
"./model.User" = "./render.RenderUser"
"*./model.Card" = "./render.RenderCardPtr"
"example.com/ext.Thing" = "example.com/ext.RenderThing"
-- model/model.go --
package model
-- render/render.go --
package render
-- input.gsx --
package views

component C() { <p>hi</p> }
`
	path := filepath.Join(dir, "testdata", "cases", "renderers", "rl.txtar")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := loadCase(path)
	if err != nil {
		t.Fatal(err)
	}
	root := "corpustest/cases/renderers_rl"
	want := []codegen.RendererAlias{
		{TypeKey: "*" + root + "/model.Card", PkgPath: root + "/render", FuncName: "RenderCardPtr"},
		{TypeKey: root + "/model.User", PkgPath: root + "/render", FuncName: "RenderUser"},
		{TypeKey: "example.com/ext.Thing", PkgPath: "example.com/ext", FuncName: "RenderThing"},
	}
	if !slices.Equal(c.renderers, want) {
		t.Fatalf("renderers = %v, want %v", c.renderers, want)
	}
}

// TestLoadCaseRenderersBadEntry: an entry side with no package-qualified name
// must be a load error (never a silently dropped registration).
func TestLoadCaseRenderersBadEntry(t *testing.T) {
	dir := t.TempDir()
	src := "-- gsx.toml --\n[renderers]\n\"NoDotAtAll\" = \"./render.RenderUser\"\n-- input.gsx --\npackage views\n\ncomponent C() { <p>hi</p> }\n"
	path := filepath.Join(dir, "testdata", "cases", "renderers", "bad.txtar")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCase(path); err == nil {
		t.Fatal("expected an error for a renderer key with no package-qualified name")
	}
}

func TestLoadCaseDocSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ex.txtar")
	src := "-- doc --\nname: Demo\norder: 5\n-- input.gsx --\npackage views\n\ncomponent A() { <p>x</p> }\n-- invoke --\nA(AProps{})\n-- render.golden --\n<p>x</p>\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := loadCase(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, isFile := c.files["doc"]; isFile {
		t.Fatal("doc must not be a source file")
	}
	if m := parseDocMeta(c.doc); m.Name != "Demo" || m.Order != 5 {
		t.Fatalf("doc meta = %+v", m)
	}
}
