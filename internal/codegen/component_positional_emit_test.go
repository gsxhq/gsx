package codegen

import (
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
)

func TestNormalizePositionalAttrsContributor(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	attr := &gsxast.ExprAttr{Name: "attrs", Expr: "bag"}
	value := componentInputValue{
		node: attr,
		attrsNode: &componentAttrsStreamNode{
			kind: componentAttrsStreamContributor,
			attr: attr,
		},
	}

	t.Run("canonical bag stays direct", func(t *testing.T) {
		plan := componentPositionalSitePlan{
			runtime:         fx.runtime,
			expressionFacts: map[gsxast.Node]expressionFact{attr: {tv: types.TypeAndValue{Type: fx.runtime.attrs}}},
		}
		got := normalizePositionalAttrsContributor("bag", value, plan, positionalEmitContext{rt: rtImports{}})
		if got != "bag" {
			t.Fatalf("canonical attrs = %q, want direct bag", got)
		}
	})

	t.Run("defined bag converts at its authored position", func(t *testing.T) {
		pkg := types.NewPackage("example.test/page", "page")
		defined := types.NewNamed(
			types.NewTypeName(token.NoPos, pkg, "LocalAttrs", nil),
			types.NewSlice(fx.runtime.attr),
			nil,
		)
		plan := componentPositionalSitePlan{
			runtime:         fx.runtime,
			expressionFacts: map[gsxast.Node]expressionFact{attr: {tv: types.TypeAndValue{Type: defined}}},
		}
		got := normalizePositionalAttrsContributor("bag", value, plan, positionalEmitContext{rt: rtImports{}})
		if got != "_gsxrt.Attrs(bag)" {
			t.Fatalf("defined attrs = %q, want canonical conversion", got)
		}
	})
}

func TestApplyPositionalOperandAdapter(t *testing.T) {
	rt := rtImports{}
	tests := []struct {
		name    string
		adapter componentOperandAdapter
		expr    string
		want    string
	}{
		{name: "identity", adapter: componentAdapterIdentity, expr: "node", want: "node"},
		{name: "NodeText", adapter: componentAdapterNodeText, expr: "value", want: "_gsxrt.Text(value)"},
		{name: "NodeVal", adapter: componentAdapterNodeVal, expr: "_gsxa0", want: "_gsxrt.Val(_gsxa0)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := applyPositionalOperandAdapter(tc.expr, tc.adapter, rt); got != tc.want {
				t.Fatalf("adapted expression = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPositionalNodeAdapterEmissionOrderAndCrossPackageAlias(t *testing.T) {
	tmp := tempModule(t, "example.com/nodeadapter")
	componentsDir := filepath.Join(tmp, "components")
	if err := os.MkdirAll(componentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	filtersDir := filepath.Join(tmp, "filters")
	if err := os.MkdirAll(filtersDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filtersDir, "filters.go", `package filters

func Check(value string) (string, error) { return value, nil }
`)
	writeFile(t, componentsDir, "badge.gsx", `package components

import "github.com/gsxhq/gsx"

type NodeAlias = gsx.Node

component Badge(label NodeAlias) {
	<span>{label}</span>
}
`)
	writeFile(t, tmp, "model.go", `package nodeadapter

type label string

func (l label) String() string { return string(l) }

func makeLabel() (label, error) { return "tuple", nil }
`)
	writeFile(t, tmp, "page.gsx", `package nodeadapter

import "example.com/nodeadapter/components"

component Page(name string) {
	<p>{f`+"`"+`leaf-@{name}`+"`"+`}</p>
	<components.Badge label="plain"/>
	<components.Badge label=f`+"`"+`formatted-@{name}`+"`"+`/>
	<components.Badge label={makeLabel()}/>
	<components.Badge label={f`+"`"+`checked-@{name}`+"`"+` |> check}/>
}
`)

	result, err := GenerateDirs(tmp, []string{tmp, componentsDir}, Options{
		FilterPkgs: []string{"example.com/nodeadapter/filters"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if diagnostics := result[componentsDir].Diags; len(diagnostics) != 0 {
		t.Fatalf("component diagnostics = %+v", diagnostics)
	}
	if diagnostics := result[tmp].Diags; len(diagnostics) != 0 {
		t.Fatalf("page diagnostics = %+v", diagnostics)
	}
	var generated string
	for _, source := range result[tmp].Files {
		generated += string(source)
	}
	for _, want := range []string{
		`_gsxgw.S("leaf-")`,
		`components.Badge(_gsxrt.Text("plain"))`,
		`components.Badge(_gsxrt.Text("formatted-"+string(name)))`,
		`_gsxa0, _gsxerr := makeLabel()`,
		`components.Badge(_gsxrt.Val(_gsxa0))`,
		`_gsxv1, _gsxerr := _gsxf0.Check(("checked-" + string(name)))`,
		`components.Badge(_gsxrt.Text(string(_gsxv1)))`,
	} {
		if !strings.Contains(generated, want) {
			t.Fatalf("generated source missing %q:\n%s", want, generated)
		}
	}
	if strings.Count(generated, "makeLabel()") != 1 {
		t.Fatalf("fallible Node value must be evaluated exactly once:\n%s", generated)
	}
	unwrap := strings.Index(generated, `_gsxa0, _gsxerr := makeLabel()`)
	wrap := strings.Index(generated, `components.Badge(_gsxrt.Val(_gsxa0))`)
	if unwrap < 0 || wrap < unwrap {
		t.Fatalf("NodeVal must wrap the unwrapped temporary:\n%s", generated)
	}
	pipeline := strings.Index(generated, `_gsxv1, _gsxerr := _gsxf0.Check(("checked-" + string(name)))`)
	text := strings.Index(generated, `components.Badge(_gsxrt.Text(string(_gsxv1)))`)
	if pipeline < 0 || text < pipeline {
		t.Fatalf("NodeText must wrap the fallible f-literal pipeline temporary:\n%s", generated)
	}
}
