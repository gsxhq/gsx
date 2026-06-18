// parser/inspect_test.go
package parser

import (
	"go/token"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

func TestInspectFindsComponents(t *testing.T) {
	src := `package views

component Header(title string) {
	<h1>{title}</h1>
}

component Footer() {
	<footer>Copyright</footer>
}
`
	fset := token.NewFileSet()
	f, err := ParseFile(fset, "test.gsx", src, 0)
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	ast.Inspect(f, func(n ast.Node) bool {
		if c, ok := n.(*ast.Component); ok {
			names = append(names, c.Name)
		}
		return true
	})

	if len(names) != 2 {
		t.Fatalf("expected 2 components, got %d: %v", len(names), names)
	}
	if names[0] != "Header" {
		t.Errorf("names[0] = %q, want Header", names[0])
	}
	if names[1] != "Footer" {
		t.Errorf("names[1] = %q, want Footer", names[1])
	}
}
