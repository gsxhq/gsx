package ast_test

import (
	"bytes"
	"go/token"
	"testing"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/parser"
)

// src is a tiny gsx file with one component containing one element with one
// static attr and one interpolation child.
const printTestSrc = `package views

component Greet(name string) {
  <div class="hello">{name}</div>
}
`

func TestFprintGolden(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "t.gsx", printTestSrc, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var buf bytes.Buffer
	if err := ast.Fprint(&buf, f); err != nil {
		t.Fatalf("Fprint: %v", err)
	}
	got := buf.String()
	want := "File package=views\n" +
		"  Component name=Greet recv=\"\" params=\"name string\"\n" +
		"    Element tag=div void=false\n" +
		"      StaticAttr name=class value=\"hello\"\n" +
		"      Interp expr=\"name\" try=false\n"
	if got != want {
		t.Errorf("Fprint output mismatch.\nGot:\n%s\nWant:\n%s", got, want)
	}
}
