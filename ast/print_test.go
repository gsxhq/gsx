package ast_test

import (
	"bytes"
	"go/token"
	"strings"
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

func TestFprintPart2(t *testing.T) {
	tree := &ast.Component{Name: "X", Body: []ast.Markup{
		&ast.GoBlock{Code: "n := 1"},
		&ast.IfMarkup{Cond: "a > 0",
			Then: []ast.Markup{&ast.Text{Value: "yes"}},
			Else: []ast.Markup{&ast.Interp{Expr: "fallback"}}},
		&ast.ForMarkup{Clause: "_, r := range rows", Body: []ast.Markup{&ast.Element{Tag: "li"}}},
		&ast.SwitchMarkup{Tag: "k", Cases: []*ast.CaseClause{
			{List: `"warn"`, Body: []ast.Markup{&ast.Element{Tag: "span"}}},
			{Default: true, Body: []ast.Markup{&ast.Text{Value: "info"}}},
		}},
		&ast.Element{Tag: "div", Attrs: []ast.Attr{
			&ast.CondAttr{Cond: `id != ""`, Then: []ast.Attr{&ast.BoolAttr{Name: "hidden"}}},
			&ast.ClassAttr{Name: "class", Parts: []ast.ClassPart{
				{Expr: `"btn"`},
				{Expr: `"on"`, Cond: "active"},
			}},
		}},
	}}
	var b strings.Builder
	if err := ast.Fprint(&b, tree); err != nil {
		t.Fatal(err)
	}
	want := `Component name=X recv="" params=""
  GoBlock code="n := 1"
  IfMarkup cond="a > 0"
    then:
      Text value="yes"
    else:
      Interp expr="fallback" try=false
  ForMarkup clause="_, r := range rows"
    Element tag=li void=false
  SwitchMarkup tag="k"
    CaseClause list="\"warn\"" default=false
      Element tag=span void=false
    CaseClause list="" default=true
      Text value="info"
  Element tag=div void=false
    CondAttr cond="id != \"\""
      then:
        BoolAttr name=hidden
    ClassAttr name=class
      ClassPart expr="\"btn\"" cond=""
      ClassPart expr="\"on\"" cond="active"
`
	if b.String() != want {
		t.Fatalf("Fprint mismatch:\n--- got ---\n%s\n--- want ---\n%s", b.String(), want)
	}
}

func TestFprintInterpWithPipeStages(t *testing.T) {
	n := &ast.Interp{
		Expr: "name",
		Stages: []ast.PipeStage{
			{Name: "upper"},
			{Name: "truncate", Args: "20", HasArgs: true},
		},
	}
	var b strings.Builder
	if err := ast.Fprint(&b, n); err != nil {
		t.Fatal(err)
	}
	want := `Interp expr="name" try=false
  PipeStage name=upper args="" hasArgs=false try=false
  PipeStage name=truncate args="20" hasArgs=true try=false
`
	if b.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", b.String(), want)
	}
}
