// internal/parser/file_test.go
package parser

import (
	"testing"

	"github.com/gsxhq/gsx/ast"
)

func TestParseFile(t *testing.T) {
	src := `package views

import "github.com/gsxhq/gsx"

type Item struct{ Name string }

component Card(title string) {
	<section>{title}</section>
}

func helper() string { return "x" }

component Spinner() {
	<svg></svg>
}
`
	f, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if f.Package != "views" {
		t.Fatalf("package = %q", f.Package)
	}
	var comps []string
	var chunks int
	for _, d := range f.Decls {
		switch v := d.(type) {
		case *ast.Component:
			comps = append(comps, v.Name)
		case *ast.GoChunk:
			chunks++
		}
	}
	if len(comps) != 2 || comps[0] != "Card" || comps[1] != "Spinner" {
		t.Fatalf("components = %v", comps)
	}
	if chunks == 0 {
		t.Fatalf("expected Go chunks (import/type/func) to be captured")
	}
}
