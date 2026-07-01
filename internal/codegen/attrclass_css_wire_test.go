package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/attrclass"
)

func TestCustomCSSAttrRuleDoesNotChangePlainExprAttr(t *testing.T) {
	t.Parallel()
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxcsswire\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	dir := filepath.Join(tmp, "views")
	os.MkdirAll(dir, 0o755)
	src := `package views

component Widget(userStyle string) {
	<div data-style={ userStyle }></div>
}
`
	if err := os.WriteFile(filepath.Join(dir, "views.gsx"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cls := attrclass.New(attrclass.Rules{CSS: []attrclass.Rule{{Prefix: "data-style"}}}, nil)
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
	if !strings.Contains(gen, "_gsxgw.AttrValue(string(userStyle))") {
		t.Errorf("plain data-style expr should use AttrValue, generated:\n%s", gen)
	}
	if strings.Contains(gen, "_gsxgw.CSS(string(userStyle))") {
		t.Errorf("plain data-style expr must not use gw.CSS, generated:\n%s", gen)
	}
}

func TestExplicitCSSAttrLiteralFiltersInterpolations(t *testing.T) {
	t.Parallel()
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxcssliteral\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	dir := filepath.Join(tmp, "views")
	os.MkdirAll(dir, 0o755)
	src := `package views

component Widget(userStyle string) {
	<div data-style=css` + "`" + `color:@{userStyle}` + "`" + `></div>
}
`
	if err := os.WriteFile(filepath.Join(dir, "views.gsx"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cls := attrclass.New(attrclass.Rules{CSS: []attrclass.Rule{{Prefix: "data-style"}}}, nil)
	res2, err := GenerateDirs(tmp, []string{dir}, Options{FilterPkgs: []string{stdImportPath}, Classifier: cls, CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	dr2 := res2[dir]
	if hasDiagErrors(dr2.Diags) {
		t.Fatalf("generate: unexpected errors: %v", dr2.Diags)
	}
	var gen string
	for _, b := range dr2.Files {
		gen += string(b)
	}
	if !strings.Contains(gen, "_gsxgw.AttrValue(gsx.StyleValue(string(userStyle)))") {
		t.Errorf("explicit CSS attr literal should filter and attr-escape interpolated string, generated:\n%s", gen)
	}
	if strings.Contains(gen, "_gsxgw.AttrValue(string(userStyle))") {
		t.Errorf("explicit CSS attr literal must not render its hole as an ordinary attr value, generated:\n%s", gen)
	}
}
