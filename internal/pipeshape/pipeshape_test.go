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

// TestWalkCases pins the single-stage shapes lsp's original walkPipe test
// covered directly: a ctx-injected stage with multiple user args, a
// non-ctx-injected stage with args (subject at args[0]), and a non-call
// outermost expression (ok=false on the very first shape check).
func TestWalkCases(t *testing.T) {
	cases := []struct {
		name string
		src  string
		n    int
		want func(t *testing.T, selSel []*ast.Ident, selArgs [][]ast.Expr, seed ast.Expr, ok bool)
	}{
		{
			name: "single stage, ctx-injected, multiple stage args",
			src:  `p.URLFor(ctx, (seed), "id", x)`,
			n:    1,
			want: func(t *testing.T, selSel []*ast.Ident, selArgs [][]ast.Expr, seed ast.Expr, ok bool) {
				if !ok || selSel[0].Name != "URLFor" {
					t.Fatalf("ctx walk: ok=%v sel=%v", ok, selSel)
				}
				if len(selArgs[0]) != 2 { // "id", x — the user stage args, after the subject
					t.Fatalf("ctx stage args = %v; want 2", selArgs[0])
				}
				id, isIdent := seed.(*ast.Ident)
				if !isIdent || id.Name != "seed" {
					t.Fatalf("ctx seed = %#v; want ident `seed`", seed)
				}
			},
		},
		{
			name: "single stage, non-ctx, subject at args[0] with trailing args",
			src:  `p.Truncate((seed), 5)`,
			n:    1,
			want: func(t *testing.T, selSel []*ast.Ident, selArgs [][]ast.Expr, seed ast.Expr, ok bool) {
				if !ok || selSel[0].Name != "Truncate" {
					t.Fatalf("non-ctx walk: ok=%v sel=%v", ok, selSel)
				}
				if len(selArgs[0]) != 1 {
					t.Fatalf("non-ctx stage args = %v; want 1", selArgs[0])
				}
				id, isIdent := seed.(*ast.Ident)
				if !isIdent || id.Name != "seed" {
					t.Fatalf("non-ctx seed = %#v; want ident `seed`", seed)
				}
			},
		},
		{
			name: "outermost expression not a call",
			src:  `1 + 2`,
			n:    1,
			want: func(t *testing.T, selSel []*ast.Ident, selArgs [][]ast.Expr, seed ast.Expr, ok bool) {
				if ok {
					t.Fatal("non-call walk should be ok=false")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			selSel, selArgs, seed, ok := Walk(mustExpr(t, tc.src), tc.n)
			tc.want(t, selSel, selArgs, seed, ok)
		})
	}
}
