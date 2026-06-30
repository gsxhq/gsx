package codegen

import (
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

// TestSpreadPipelineElement proves a `|>` pipeline works in an element HTML-attr
// spread `<div { attrs |> f... }>`: the spread subject is lowered through the SAME
// lowerPipe every other value context uses, then handed to gw.Spread. The custom
// filter operates on gsx.Attrs (std has no Attrs filter), so this exercises a real
// filter package via the multi-filter render harness.
func TestSpreadPipelineElement(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping go-run render test in -short mode")
	}
	myfilters := `package myfilters

import "github.com/gsxhq/gsx"

// WithTitle adds a title attribute to a bag (seed-first: subject is the Attrs).
func WithTitle(a gsx.Attrs, t string) gsx.Attrs {
	out := gsx.Attrs{}
	for k, v := range a {
		out[k] = v
	}
	out["title"] = t
	return out
}
`
	views := map[string]string{
		"views.gsx": `package views

import "github.com/gsxhq/gsx"

component C(extra gsx.Attrs) {
	<div { extra |> withTitle("hi")... }>x</div>
}
`,
	}
	got := renderWithFilters(t, myfilters, views,
		[]string{stdImportPath, "gsxmf/myfilters"},
		`p.C(p.CProps{Extra: gsx.Attrs{"id": "m"}})`)
	if !strings.Contains(got, `id="m"`) || !strings.Contains(got, `title="hi"`) {
		t.Fatalf("expected spread pipeline to add title; got:\n%s", got)
	}
}

// TestSplatPipelineComponent proves a `|>` pipeline lowers in a byo component
// whole-struct splat `<Card { d |> f... }/>`: childPropsLiteral returns the LOWERED
// splat subject (the SAME lowerPipe output the probe path produces) and reports the
// filter package, with no other attrs. It drives childPropsLiteral directly because
// a render here would need a byo Props struct AND a seed-first filter over that exact
// struct type — and the dir-scoped byo enumeration only sees same-package structs,
// while a cross-package filter cannot name a package-local type (import cycle). The
// lowering itself (the unit under test) is package-agnostic.
func TestSplatPipelineComponent(t *testing.T) {
	t.Parallel()
	table := filterTable{
		"loud": {funcName: "Loud", alias: "_gsxf0", pkgPath: "gsxmf/myfilters"},
	}
	byo := newByoData()
	byo.structs["cardData"] = byoStruct{} // mark cardData as a byo struct (no fields needed)

	// <Card { d |> loud... }/> — the sole attr is a piped whole-struct splat.
	el := &ast.Element{
		Tag:   "Card",
		Void:  true,
		Attrs: []ast.Attr{&ast.SpreadAttr{Expr: "d", Stages: []ast.PipeStage{{Name: "loud"}}}},
	}
	_, splatExpr, usedPkgs, err := childPropsLiteral(el, "cardData", "gsx", "gsx.DefaultClassMerge", table, nil, nil, byo, nil,
		func(nodes []ast.Markup) (string, error) { return "", nil }, false)
	if err != nil {
		t.Fatalf("childPropsLiteral: %v", err)
	}
	if want := "_gsxf0.Loud((d))"; splatExpr != want {
		t.Fatalf("splat lowering = %q, want %q", splatExpr, want)
	}
	if usedPkgs["_gsxf0"] != "gsxmf/myfilters" {
		t.Fatalf("expected filter package recorded; got %v", usedPkgs)
	}
}
