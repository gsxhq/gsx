package pipeshape

import (
	"go/ast"
	"go/parser"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
)

func mustExpr(t *testing.T, src string) ast.Expr {
	t.Helper()
	e, err := parser.ParseExpr(src)
	if err != nil {
		t.Fatalf("ParseExpr(%q): %v", src, err)
	}
	return e
}

func TestWalk(t *testing.T) {
	// ctx-injected, nested: pkg.upper(ctx, pkg.lower(x), "a") — stage0=lower, stage1=upper.
	e := mustExpr(t, `pkg.upper(ctx, pkg.lower(x), "a")`)

	selSel, selArgs, seed, ok := Walk(e, 2)
	if !ok {
		t.Fatal("Walk ok=false")
	}
	if selSel[0].Name != "lower" || selSel[1].Name != "upper" {
		t.Fatalf("sels = %q, %q; want lower, upper", selSel[0].Name, selSel[1].Name)
	}
	if len(selArgs[1]) != 1 {
		t.Fatalf("selArgs[1] = %v; want length 1", selArgs[1])
	}
	seedID, isIdent := seed.(*ast.Ident)
	if !isIdent || seedID.Name != "x" {
		t.Fatalf("seed = %#v; want ident `x`", seed)
	}

	if _, _, _, ok := Walk(e, 3); ok {
		t.Fatal("Walk(e, 3) ok=true; want false — only 2 stages in the chain")
	}
}

func TestStages(t *testing.T) {
	interp := &gsxast.Interp{Stages: []gsxast.PipeStage{{Name: "a"}}}
	if got := len(Stages(interp)); got != 1 {
		t.Fatalf("Stages(Interp) len = %d; want 1", got)
	}

	if got := Stages(&gsxast.Element{}); got != nil {
		t.Fatalf("Stages(Element) = %v; want nil", got)
	}
}
