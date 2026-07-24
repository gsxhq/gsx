package printer

import (
	"testing"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/pretty"
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

func TestIsSpacingInterp(t *testing.T) {
	cases := []struct {
		expr string
		want bool
	}{
		{`" "`, true},
		{`"  "`, true},
		{"` `", true},
		{`""`, false},        // empty renders nothing, not a space
		{`" x "`, false},     // not only spaces
		{`"\t"`, false},      // tab is not the idiom
		{`name`, false},      // not a literal
		{`" " + " "`, false}, // not a single literal
	}
	for _, c := range cases {
		n := &ast.Interp{Expr: c.expr}
		if got := isSpacingInterp(n); got != c.want {
			t.Errorf("isSpacingInterp({%s}) = %v, want %v", c.expr, got, c.want)
		}
	}
	if isSpacingInterp(&ast.Interp{Expr: `" "`, Stages: []ast.PipeStage{{Name: "f"}}}) {
		t.Error("interp with pipe stages must not be spacing")
	}
	if isSpacingInterp(&ast.Text{Value: " "}) {
		t.Error("Text is not a spacing interp")
	}
}

func TestGluedSpacingInterp(t *testing.T) {
	see := &ast.Text{Value: "see"}
	sp := &ast.Interp{Expr: `" "`}
	if !glued(see, sp) {
		t.Error("spacing interp must glue to its left neighbor")
	}
	if glued(sp, see) {
		t.Error("spacing interp must not glue rightward without a space")
	}
}

// fillAt prints the element's children as a Fill at the given width.
func fillAt(t *testing.T, src string, width int) string {
	t.Helper()
	p := &printer{width: width, tabWidth: 2}
	e := firstElement(t, src)
	return pretty.Print(pretty.Fill(p.fillParts(e.Children)...), width, 2)
}

func TestFillParts(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		width int
		want  string
	}{
		// Everything fits: flat output is the normalized one-line form.
		{"flat", `<p>alpha beta <code>x</code> gamma</p>`, 80,
			"alpha beta <code>x</code> gamma"},
		// Word gaps break; the tag-adjacent spaces bond beta/code/gamma into
		// one unbreakable cluster.
		{"wrap", `<p>alpha beta <code>x</code> gamma delta epsilon</p>`, 27,
			"alpha\nbeta <code>x</code> gamma\ndelta epsilon"},
		// Direct adjacency is a safe gap: over-narrow width may break after
		// </code> before the bonded-nothing period cluster... but greedy fill
		// keeps 1-char punctuation attached at any sane width.
		{"punct", `<p>uses <code>x</code>, and <code>y</code>.</p>`, 80,
			"uses <code>x</code>, and <code>y</code>."},
		// {" "} bonds left, safe gap right.
		{"spacing", `<p>see{ " " }<a href="/x">docs</a> now</p>`, 12,
			"see{ \" \" }\n<a href=\"/x\">docs</a> now"},
		// An interp bonded by significant spaces joins the cluster.
		{"interp bond", `<p>count: { n } items left today</p>`, 16,
			"count: { n } items\nleft today"},
	}
	for _, c := range cases {
		if got := fillAt(t, c.src, c.width); got != c.want {
			t.Errorf("%s:\n--- got ---\n%s\n--- want ---\n%s", c.name, got, c.want)
		}
	}
}
