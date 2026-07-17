package codegen

import (
	"strings"
	"testing"
)

// TestSpreadPipelineElement proves a `|>` pipeline works in an element HTML-attr
// spread `<div { attrs |> f... }>`: the spread subject is lowered through the SAME
// lowerPipe every other value context uses, then handed to _gsxgw.Spread. The custom
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
	return append(a, gsx.Attr{Key: "title", Value: t})
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
		`p.C(gsx.Attrs{{Key: "id", Value: "m"}})`)
	if !strings.Contains(got, `id="m"`) || !strings.Contains(got, `title="hi"`) {
		t.Fatalf("expected spread pipeline to add title; got:\n%s", got)
	}
}
