package codegen

import (
	"go/token"
	"go/types"
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
