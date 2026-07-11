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
	for i := 0; i < 50; i++ {
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
