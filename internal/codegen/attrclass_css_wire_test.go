package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/attrclass"
)

// A custom CSS rule must route a custom-framework attribute through CSS-context
// emission (gw.CSS value-filter), not plain attr escaping. We use the ={ expr }
// form (ExprAttr) since the parser routes the built-in `style`/`class` to a
// composable ClassAttr; a user CSS-context attribute reaches emitExprAttr, whose
// CtxCSS branch was a fail-closed error before this slice unlocked it.
func TestCustomCSSAttrRuleEmitsCSSContext(t *testing.T) {
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
	out, err := GeneratePackageWithFilters(dir, nil, nil, cls, nil, nil, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	var gen string
	for _, b := range out {
		gen += string(b)
	}
	// String value in a CSS-context attribute goes through the gw.CSS value-filter.
	if !strings.Contains(gen, "_gsxgw.CSS(string(userStyle))") {
		t.Errorf("expected gw.CSS for data-style string value, generated:\n%s", gen)
	}
	// Must NOT fall back to plain attr escaping (gw.AttrValue).
	if strings.Contains(gen, "_gsxgw.AttrValue(userStyle)") {
		t.Errorf("CSS-context attr must not use plain AttrValue, generated:\n%s", gen)
	}
}

// A gsx.RawCSS value in a custom CSS-context attribute opts out of the value
// filter: codegen emits it raw via gw.S, the author's vouch.
func TestCustomCSSAttrRuleRawCSSOptsOut(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxcssraw\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	dir := filepath.Join(tmp, "views")
	os.MkdirAll(dir, 0o755)
	src := `package views

import "github.com/gsxhq/gsx"

component Widget(raw gsx.RawCSS) {
	<div data-style={ raw }></div>
}
`
	if err := os.WriteFile(filepath.Join(dir, "views.gsx"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cls := attrclass.New(attrclass.Rules{CSS: []attrclass.Rule{{Prefix: "data-style"}}}, nil)
	out, err := GeneratePackageWithFilters(dir, nil, nil, cls, nil, nil, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	var gen string
	for _, b := range out {
		gen += string(b)
	}
	if !strings.Contains(gen, "_gsxgw.S(string(raw))") {
		t.Errorf("RawCSS value should be emitted raw via gw.S, generated:\n%s", gen)
	}
	if strings.Contains(gen, "_gsxgw.CSS(string(raw))") {
		t.Errorf("RawCSS value must not be CSS-filtered, generated:\n%s", gen)
	}
}
