package codegen

import (
	"go/token"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
)

func TestWrapperCycleWarning(t *testing.T) {
	f := parseGSXForTest(t, `package views

component div() {
	<div><span>{children}</span></div>
}

component span() {
	<span><div>{children}</div></span>
}
`)
	bag := diag.NewBag(token.NewFileSet())
	declNames := map[string]bool{"div": true, "span": true}
	resolveComponentTags(f, declNames, bag)
	reportWrapperCycles(map[string]*gsxast.File{"a.gsx": f}, bag)
	var warns []diag.Diagnostic
	for _, d := range bag.Sorted() {
		if d.Severity == diag.Warning && d.Code == "wrapper-cycle" {
			warns = append(warns, d)
		}
	}
	if len(warns) != 1 {
		t.Fatalf("want exactly 1 wrapper-cycle warning, got %d: %v", len(warns), warns)
	}
	if !strings.Contains(warns[0].Message, "div") || !strings.Contains(warns[0].Message, "span") {
		t.Errorf("cycle message should name both components: %s", warns[0].Message)
	}
}

func TestWrapperCycleConditionalEdgeExempt(t *testing.T) {
	f := parseGSXForTest(t, `package views

component div(deep bool) {
	if deep {
		<span>{children}</span>
	}
	<div>{children}</div>
}

component span() {
	<span><div>{children}</div></span>
}
`)
	bag := diag.NewBag(token.NewFileSet())
	declNames := map[string]bool{"div": true, "span": true}
	resolveComponentTags(f, declNames, bag)
	reportWrapperCycles(map[string]*gsxast.File{"a.gsx": f}, bag)
	for _, d := range bag.Sorted() {
		if d.Code == "wrapper-cycle" {
			t.Fatalf("conditional edge must exempt the cycle: %v", d)
		}
	}
}

// TestWrapperCycleDetourSingleWitness pins the CURRENT single-witness
// behavior on an overlapping-cycle "detour" topology — it documents the
// guarantee reportWrapperCycles actually makes, NOT an ideal. Edges a→b,
// a→c, b→c, c→e, e→a contain TWO elementary cycles (a→b→c→e→a and
// a→c→e→a); the DFS deterministically witnesses only the first (sorted edge
// order visits b before c, so by the time the a→c edge is examined, c is
// already black and no back edge ever fires for the shorter cycle). Exactly
// one warning, the same one every run. If a future algorithm change makes
// this report both cycles (or a different witness), this test failing is the
// signal that the documented single-witness guarantee is being consciously
// changed — update the reportWrapperCycles doc comment together with this
// pin.
func TestWrapperCycleDetourSingleWitness(t *testing.T) {
	src := `package views

component a() {
	<b>x</b>
	<c>x</c>
}

component b() {
	<c>x</c>
}

component c() {
	<e>x</e>
}

component e() {
	<a>x</a>
}
`
	declNames := map[string]bool{"a": true, "b": true, "c": true, "e": true}
	want := []string{"wrapper cycle a → b → c → e → a will recurse infinitely at render"}
	for i := range 20 {
		f := parseGSXForTest(t, src)
		bag := diag.NewBag(token.NewFileSet())
		resolveComponentTags(f, declNames, bag)
		reportWrapperCycles(map[string]*gsxast.File{"a.gsx": f}, bag)
		var got []string
		for _, d := range bag.Sorted() {
			if d.Code == "wrapper-cycle" {
				got = append(got, d.Message)
			}
		}
		if len(got) != 1 || got[0] != want[0] {
			t.Fatalf("iteration %d: single-witness pin broken\n  want: %q\n  got:  %q", i, want, got)
		}
	}
}

// TestWrapperCycleDeterministic asserts that repeated detection over the same
// input produces byte-identical diagnostics, even though nodes/edges/files
// are built from maps: map iteration order is intentionally NOT allowed to
// influence which entry point discovers the cycle, the path rotation used in
// the message, or the reported position. This guards the corpus goldens
// (which pin the diagnostic text and position) against flakiness.
func TestWrapperCycleDeterministic(t *testing.T) {
	src := `package views

component div() {
	<div><span>{children}</span></div>
}

component span() {
	<span><div>{children}</div></span>
}
`
	var first string
	for i := range 50 {
		f := parseGSXForTest(t, src)
		bag := diag.NewBag(token.NewFileSet())
		declNames := map[string]bool{"div": true, "span": true}
		resolveComponentTags(f, declNames, bag)
		reportWrapperCycles(map[string]*gsxast.File{"a.gsx": f}, bag)
		var got string
		for _, d := range bag.Sorted() {
			if d.Code == "wrapper-cycle" {
				got += d.Message + "\n"
			}
		}
		if i == 0 {
			first = got
			if first == "" {
				t.Fatalf("iteration 0: expected a wrapper-cycle diagnostic, got none")
			}
			continue
		}
		if got != first {
			t.Fatalf("iteration %d: diagnostics differ from iteration 0\n  first: %q\n  got:   %q", i, first, got)
		}
	}
}
