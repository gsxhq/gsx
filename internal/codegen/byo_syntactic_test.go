package codegen

import (
	"go/token"
	"os"
	"path/filepath"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	gsxparser "github.com/gsxhq/gsx/parser"
)

func TestLoadExternalStructFieldsSyntactic(t *testing.T) {
	dir := t.TempDir()
	// A BYO props struct with node, attrs, plain, and unexported fields.
	if err := os.WriteFile(filepath.Join(dir, "card.go"), []byte(`package views

import "github.com/gsxhq/gsx"

type CardProps struct {
	Title    string
	Children gsx.Node
	Attrs    gsx.Attrs
	hidden   int
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	// A sibling file that does NOT type-check (undefined symbol). A type-load
	// would fail on the package; a syntactic parse is unaffected. Also proves
	// _test.go / .x.go files are skipped.
	if err := os.WriteFile(filepath.Join(dir, "broken.go"), []byte(`package views

func Broken() { undefinedSymbol() }
`), 0o644); err != nil {
		t.Fatal(err)
	}

	fields, nodeFields, structs := loadExternalStructFields(dir, map[string]bool{"CardProps": true})

	f := fields["CardProps"]
	if !f["Title"] || !f["Children"] || !f["Attrs"] {
		t.Errorf("fields=%v, want Title+Children+Attrs present", f)
	}
	if f["hidden"] {
		t.Errorf("unexported field 'hidden' must be excluded, got fields=%v", f)
	}
	if !nodeFields["CardProps"]["Children"] {
		t.Errorf("Children must be classified as gsx.Node, got nodeFields=%v", nodeFields["CardProps"])
	}
	if s := structs["CardProps"]; !s.hasChildren || !s.hasAttrs {
		t.Errorf("want hasChildren+hasAttrs, got %+v", s)
	}
}

// TestTypeNames pins package-level type-name facts (byoData.typeNames /
// hasTypeName): every package-level TypeSpec (struct, alias, defined type)
// declared in a sibling hand-written .go file OR a .gsx GoChunk is visible,
// a func name is not, and _test.go/.x.go declarations are excluded — mirroring
// packageNullaryFuncs' skip rules exactly.
func TestTypeNames(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFile(t, dir, "types.go", `package views

type FooProps struct{}

type Alias = int

func Helper() string { return "" }
`)
	// _test.go and .x.go declarations must be excluded, same as packageNullaryFuncs.
	writeFile(t, dir, "types_test.go", `package views

type ShouldSkipTest struct{}
`)
	writeFile(t, dir, "types.x.go", `package views

type ShouldSkipGen struct{}
`)

	src := `package views

type GsxAlias = int

type GsxDefined int

type Widget struct {
	Label string
}

component C(w Widget) { <div>{w.Label}</div> }
`
	file, err := gsxparser.ParseFile(token.NewFileSet(), "views.gsx", []byte(src), 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, byo, err := componentPropFieldsFor(dir, map[string]*gsxast.File{"views.gsx": file})
	if err != nil {
		t.Fatalf("componentPropFieldsFor: %v", err)
	}

	for _, name := range []string{"FooProps", "Alias", "GsxAlias", "GsxDefined", "Widget"} {
		if !byo.hasTypeName(name) {
			t.Errorf("hasTypeName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"Helper", "ShouldSkipTest", "ShouldSkipGen"} {
		if byo.hasTypeName(name) {
			t.Errorf("hasTypeName(%q) = true, want false", name)
		}
	}

	var nilByo *byoData
	if nilByo.hasTypeName("FooProps") {
		t.Errorf("nil *byoData.hasTypeName must be nil-safe (false)")
	}
}

func TestByoStructFoundInGoWithElementsRegion(t *testing.T) {
	t.Parallel()
	src := `package views

import "github.com/gsxhq/gsx"

type props struct {
	label gsx.Node
}

var items = []props{{label: <p>x</p>}}

component C(p props) { <div>{p.label}</div> }
`
	file, err := gsxparser.ParseFile(token.NewFileSet(), "views.gsx", []byte(src), 0)
	if err != nil {
		t.Fatal(err)
	}
	propFields, _, _, byo, err := componentPropFieldsFor("", map[string]*gsxast.File{"views.gsx": file})
	if err != nil {
		t.Fatalf("componentPropFieldsFor: %v", err)
	}
	if got, ok := byo.structTypeName(".C"); !ok || got != "props" {
		t.Fatalf("byo struct for .C = (%q, %v), want (props, true)", got, ok)
	}
	if _, ok := propFields["props"]; !ok {
		t.Fatalf("props field set not discovered; keys=%v", keysOf(propFields))
	}
	if _, ok := propFields["CProps"]; ok {
		t.Fatalf("unexpected generated CProps for BYO component; keys=%v", keysOf(propFields))
	}
}
