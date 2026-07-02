package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/attrclass"
)

func TestCustomJSAttrRuleDoesNotChangePlainExprAttr(t *testing.T) {
	t.Parallel()
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxwire\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	dir := filepath.Join(tmp, "views")
	os.MkdirAll(dir, 0o755)
	src := `package views

component Widget(action string) {
	<div wire:click={ action }></div>
}
`
	if err := os.WriteFile(filepath.Join(dir, "views.gsx"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cls := attrclass.New(attrclass.Rules{JS: []attrclass.Rule{{Prefix: "wire:"}}}, nil)
	res, err := GenerateDirs(tmp, []string{dir}, Options{FilterPkgs: []string{stdImportPath}, Classifier: cls, CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	dr := res[dir]
	if hasDiagErrors(dr.Diags) {
		t.Fatalf("generate: unexpected errors: %v", dr.Diags)
	}
	var gen string
	for _, b := range dr.Files {
		gen += string(b)
	}
	if !strings.Contains(gen, "_gsxgw.AttrValue(string(action))") {
		t.Errorf("plain wire:click expr should use AttrValue, generated:\n%s", gen)
	}
	if strings.Contains(gen, "JSValAttr") {
		t.Errorf("plain wire:click expr must not use JSValAttr, generated:\n%s", gen)
	}
}

func TestExplicitJSAttrLiteralEmitsJSContext(t *testing.T) {
	t.Parallel()
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxwire\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	dir := filepath.Join(tmp, "views")
	os.MkdirAll(dir, 0o755)
	src := `package views

component Widget(action string) {
	<div wire:click=js` + "`" + `save(@{action})` + "`" + `></div>
}
`
	if err := os.WriteFile(filepath.Join(dir, "views.gsx"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := GenerateDirs(tmp, []string{dir}, Options{FilterPkgs: []string{stdImportPath}, Classifier: attrclass.Builtin(), CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	dr := res[dir]
	if hasDiagErrors(dr.Diags) {
		t.Fatalf("generate: unexpected errors: %v", dr.Diags)
	}
	var gen string
	for _, b := range dr.Files {
		gen += string(b)
	}
	if !strings.Contains(gen, "JSValAttr(action)") {
		t.Errorf("explicit JS attr literal should use JSValAttr, generated:\n%s", gen)
	}
	if strings.Contains(gen, "_gsxgw.AttrValue(string(action))") {
		t.Errorf("explicit JS attr literal must not use plain AttrValue for its hole, generated:\n%s", gen)
	}
}
