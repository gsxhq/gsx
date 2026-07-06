package codegen

import (
	"os"
	"path/filepath"
	"testing"
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
