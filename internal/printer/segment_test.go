package printer

import (
	"testing"

	"github.com/gsxhq/gsx/ast"
)

func txt(s string) *ast.Text       { return &ast.Text{Value: s} }
func interp() *ast.Interp          { return &ast.Interp{Expr: "x"} }
func elem(tag string) *ast.Element { return &ast.Element{Tag: tag} }

func segWords(segs []segment) []int {
	out := make([]int, len(segs))
	for i, s := range segs {
		out[i] = len(s.nodes)
	}
	return out
}

func TestSegmentSafeBoundaryBreakable(t *testing.T) {
	// [Text("by "), Interp, IfMarkup] — "by " glues to Interp; Interp|IfMarkup
	// is a safe boundary → two segments, breakable.
	nodes := []ast.Markup{txt("by "), interp(), &ast.IfMarkup{Cond: "c"}}
	segs, breakable := segmentChildren(nodes)
	if !breakable {
		t.Fatal("want breakable")
	}
	if got := segWords(segs); len(got) != 2 || got[0] != 2 || got[1] != 1 {
		t.Fatalf("segments = %v, want [2 1]", got)
	}
}

func TestSegmentAllGluedSingleSegment(t *testing.T) {
	// [Text("a "), <b>, Text(" b")] — both boundaries glued → one segment,
	// and edge-safe (no significant leading/trailing space) so breakable=true.
	nodes := []ast.Markup{txt("a "), elem("b"), txt(" b")}
	segs, breakable := segmentChildren(nodes)
	if !breakable {
		t.Fatal("want breakable (one segment, edge-safe)")
	}
	if got := segWords(segs); len(got) != 1 || got[0] != 3 {
		t.Fatalf("segments = %v, want [3]", got)
	}
}

func TestSegmentTwoBlocksBreakable(t *testing.T) {
	// [<p>, <p>] — no text, safe boundary between → two segments, breakable.
	nodes := []ast.Markup{elem("p"), elem("p")}
	segs, breakable := segmentChildren(nodes)
	if !breakable || len(segs) != 2 {
		t.Fatalf("want breakable 2 segments, got breakable=%v segs=%v", breakable, segWords(segs))
	}
}

func TestSegmentLeadingSpaceEdgeGuardForcesInline(t *testing.T) {
	// First child has a significant leading space → block opener would absorb it.
	nodes := []ast.Markup{txt(" x"), elem("p")}
	_, breakable := segmentChildren(nodes)
	if breakable {
		t.Fatal("leading significant space must force inline")
	}
}

func TestSegmentTrailingSpaceEdgeGuardForcesInline(t *testing.T) {
	// Last child has a significant trailing space → block closer would absorb it.
	nodes := []ast.Markup{elem("p"), txt("x ")}
	_, breakable := segmentChildren(nodes)
	if breakable {
		t.Fatal("trailing significant space must force inline")
	}
}

func TestSegmentSingleInterpIsEdgeSafe(t *testing.T) {
	// A single Interp — one segment, edge-safe (no significant boundary space)
	// so breakable=true; the element/body layer decides block-vs-inline via hasBlockChild.
	_, breakable := segmentChildren([]ast.Markup{interp()})
	if !breakable {
		t.Fatal("single Interp is edge-safe so breakable")
	}
}
