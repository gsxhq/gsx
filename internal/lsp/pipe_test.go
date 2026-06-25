package lsp

import (
	"go/ast"
	"go/parser"
	"testing"
)

func mustExpr(t *testing.T, src string) ast.Expr {
	t.Helper()
	e, err := parser.ParseExpr(src)
	if err != nil {
		t.Fatalf("ParseExpr(%q): %v", src, err)
	}
	return e
}

func TestWalkPipe(t *testing.T) {
	// non-ctx, bare + args: Upper(Truncate((seed), 5)) — stage0=Truncate, stage1=Upper.
	sel, args, seed, ok := walkPipe(mustExpr(t, `p.Upper(p.Truncate((seed), 5))`), 2)
	if !ok {
		t.Fatal("walkPipe ok=false")
	}
	if sel[0].Name != "Truncate" || sel[1].Name != "Upper" {
		t.Fatalf("sels = %q, %q; want Truncate, Upper", sel[0].Name, sel[1].Name)
	}
	if len(args[0]) != 1 || len(args[1]) != 0 {
		t.Fatalf("args = %v; want stage0 one arg, stage1 none", args)
	}
	if id, _ := seed.(*ast.Ident); id == nil || id.Name != "seed" {
		t.Fatalf("seed = %#v; want ident `seed`", seed)
	}

	// ctx-injected: URLFor(ctx, (seed), "id", x) — subject at args[1], stage args after.
	sel2, args2, seed2, ok := walkPipe(mustExpr(t, `p.URLFor(ctx, (seed), "id", x)`), 1)
	if !ok || sel2[0].Name != "URLFor" {
		t.Fatalf("ctx walk: ok=%v sel=%v", ok, sel2)
	}
	if len(args2[0]) != 2 { // "id", x — the user stage args, after the subject
		t.Fatalf("ctx stage args = %v; want 2", args2[0])
	}
	if id, _ := seed2.(*ast.Ident); id == nil || id.Name != "seed" {
		t.Fatalf("ctx seed = %#v; want ident `seed`", seed2)
	}

	// shape mismatch → ok=false, no panic.
	if _, _, _, ok := walkPipe(mustExpr(t, `1 + 2`), 1); ok {
		t.Fatal("non-call walk should be ok=false")
	}
}
