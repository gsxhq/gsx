package codegen

import (
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/parser"
	"go/token"
)

// mustParseComponent parses a single-component .gsx source and returns the
// first *gsxast.Component declaration.
func mustParseComponent(t *testing.T, src string) *gsxast.Component {
	t.Helper()
	fset := token.NewFileSet()
	file, errs := parser.ParseFileWithClassifier(fset, "input.gsx", []byte(src), 0, nil)
	if len(errs) > 0 {
		t.Fatalf("parse: %v", errs)
	}
	for _, d := range file.Decls {
		if c, ok := d.(*gsxast.Component); ok {
			return c
		}
	}
	t.Fatal("no component")
	return nil
}

func TestComponentSignature(t *testing.T) {
	// Same props, different body → SAME signature (drop-in variant).
	a := mustParseComponent(t, "package v\ncomponent Icon(name string) {\n\t<span>{ name }</span>\n}\n")
	b := mustParseComponent(t, "package v\ncomponent Icon(name string) {\n\t<b>{ name }</b>\n}\n")
	if componentSignature(a) != componentSignature(b) {
		t.Fatalf("same-props variants must share a signature:\n a=%q\n b=%q", componentSignature(a), componentSignature(b))
	}

	// Different prop type → DIFFERENT signature.
	c := mustParseComponent(t, "package v\ncomponent Icon(name int) {\n\t<span>{ name }</span>\n}\n")
	if componentSignature(a) == componentSignature(c) {
		t.Fatalf("different prop type must differ: %q", componentSignature(a))
	}

	// Param order does not matter (props map by name).
	d := mustParseComponent(t, "package v\ncomponent Icon(x string, y string) { <i/> }\n")
	e := mustParseComponent(t, "package v\ncomponent Icon(y string, x string) { <i/> }\n")
	if componentSignature(d) != componentSignature(e) {
		t.Fatalf("param order must not affect signature")
	}

	// Children presence is part of the signature.
	f := mustParseComponent(t, "package v\ncomponent Box() { <div>{ children }</div> }\n")
	g := mustParseComponent(t, "package v\ncomponent Box() { <div/> }\n")
	if componentSignature(f) == componentSignature(g) {
		t.Fatalf("children presence must differ")
	}
}
