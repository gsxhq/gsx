package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/attrclass"
)

// A custom JS rule must route a custom-framework attribute through JS-context
// emission (gw.JSValAttr), not plain attr escaping. We use the ={ expr } form
// (ExprAttr) since the parser does not yet know about user-defined JS attrs
// (that is Task 3); codegen classifies the ExprAttr via the custom Classifier.
func TestCustomJSAttrRuleEmitsJSContext(t *testing.T) {
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
	out, err := GeneratePackageWithFilters(dir, nil, nil, cls, nil, nil, nil, true, true)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	var gen string
	for _, b := range out {
		gen += string(b)
	}
	if !strings.Contains(gen, "JSValAttr") {
		t.Errorf("expected a gw.JSValAttr call for wire:click, generated:\n%s", gen)
	}
}

// Built-ins-only (nil-equivalent) classifier keeps wire:click as a plain attr
// (AttrValue, not JSValAttr) — proving the rule, not a regression, caused the
// change above.
func TestBuiltinClassifierLeavesCustomAttrPlain(t *testing.T) {
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
	out, err := GeneratePackageWithFilters(dir, nil, nil, attrclass.Builtin(), nil, nil, nil, true, true)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	var gen string
	for _, b := range out {
		gen += string(b)
	}
	if strings.Contains(gen, "JSValAttr") {
		t.Errorf("built-in classifier must not JS-classify wire:click, generated:\n%s", gen)
	}
}
