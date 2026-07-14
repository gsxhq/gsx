package codegen

import (
	"go/token"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// componentFromSrc parses a .gsx source, stamps Element.IsComponent via the
// canonical package preprocessor (as module_importer does before the reservation pass runs
// — the walker reads the stamp to treat component-element children as closure
// scope), and returns the FIRST component plus the FileSet (for offset→ident
// verification).
func componentFromSrc(t *testing.T, src string) (*gsxast.Component, *token.FileSet) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := gsxparser.ParseFile(fset, "test.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	declNames := map[string]bool{}
	var first *gsxast.Component
	for _, d := range f.Decls {
		if c, ok := d.(*gsxast.Component); ok {
			declNames[c.Name] = true
			if first == nil {
				first = c
			}
		}
	}
	if first == nil {
		t.Fatalf("no component parsed from %q", src)
	}
	preprocessTagsForTest(t, fset, f, declNames, diag.NewBag(fset))
	return first, fset
}

func flaggedNames(decls []reservedDecl) []string {
	out := make([]string, len(decls))
	for i, d := range decls {
		out[i] = d.name
	}
	return out
}

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestCheckReservedBodyBindings is the detector table: the common body-scope
// binding shapes flag; nested-scope shadows (under for/if/switch markup, in a
// func literal, in an inner block) and non-bindings (reassignment, clause vars,
// selectors) do NOT. Each `body` is spliced into a bare `component C() { … }`.
func TestCheckReservedBodyBindings(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		// ---- body-scope bindings: FLAG ----
		{"shortvar_attrs", `{{ attrs := 1 }}`, []string{"attrs"}},
		{"shortvar_children", `{{ children := 1 }}`, []string{"children"}},
		{"shortvar_ctx", `{{ ctx := 1 }}`, []string{"ctx"}},
		{"tuple_attrs", `{{ attrs, ok := f() }}`, []string{"attrs"}},
		{"var_attrs", `{{ var attrs int }}`, []string{"attrs"}},
		{"const_attrs", `{{ const attrs = "x" }}`, []string{"attrs"}},
		{"var_multi_name", `{{ var attrs, other int }}`, []string{"attrs"}},
		// An element opens no Go scope — a GoBlock child is still body-scope.
		{"inside_element", `<div>{{ attrs := 1 }}</div>`, []string{"attrs"}},
		// A fragment opens no Go scope either.
		{"inside_fragment", `<>{{ attrs := 1 }}</>`, []string{"attrs"}},
		// Two reserved names in one GoBlock, reported in source order.
		{"two_in_one_block", `{{ ctx := 1; attrs := 2 }}`, []string{"ctx", "attrs"}},
		// A GoBlock directly under the body, then one under an element: both flag.
		{"sibling_blocks", "{{ ctx := 1 }}\n<div>{{ children := 2 }}</div>", []string{"ctx", "children"}},

		// ---- nested scope / non-binding: do NOT flag ----
		// A `for` markup emits a real Go `for { }` block; a GoBlock inside is a
		// nested shadow (legal Go), never flagged.
		{"nested_under_for", `{ for _, x := range xs { {{ attrs := 1 }} } }`, nil},
		{"nested_under_if", `{ if cond { {{ attrs := 1 }} } }`, nil},
		{"nested_under_if_else", `{ if cond { <br/> } else { {{ ctx := 1 }} } }`, nil},
		{"nested_under_switch", `{ switch x { case 1: {{ attrs := 1 }} } }`, nil},
		// The range-var shadow idiom: the clause binds `attrs`, not a GoBlock —
		// clause bindings are nested by construction and are not reported.
		{"clause_range_var", `{ for _, attrs := range xs { <span/> } }`, nil},
		// A COMPONENT element's children lower into a nested gsx.Func slot
		// closure (emitSlotClosure) — a new Go scope; a binding there is a legal
		// shadow of the captured parent local, never flagged.
		{"nested_under_component", `<Wrap>{{ attrs := 1 }}</Wrap>`, nil},
		// Transitive: a plain element INSIDE component children is still inside
		// the slot closure — nested all the way down.
		{"nested_under_component_transitive", `<Wrap><div>{{ attrs := 1 }}</div></Wrap>`, nil},
		// A func-literal parameter named `attrs` is nested; `f` (the only body-scope
		// bind) is not reserved.
		{"funclit_param", `{{ f := func(attrs int) int { return attrs } }}`, nil},
		// An inner block scopes its `:=`; fragmentBindings never descends into it.
		{"inner_block", `{{ { attrs := 1 } }}`, nil},
		// Reassignment (`=`, not `:=`) is a use of an existing binding, not a new
		// binding — the `attrs = attrs.Without(...)` filtering idiom is legal.
		{"reassignment", `{{ attrs = attrs.Without("id") }}`, nil},
		// Non-reserved names never flag.
		{"non_reserved", `{{ x := 1 }}`, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Wrap is a real component declaration so <Wrap> stamps IsComponent.
			src := "package p\n\ncomponent C() {\n" + tc.body + "\n}\n\ncomponent Wrap() {\n<div>{children}</div>\n}\n"
			c, _ := componentFromSrc(t, src)
			got := flaggedNames(checkReservedBodyBindings(c))
			if !eqStrings(got, tc.want) {
				t.Errorf("checkReservedBodyBindings(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// TestCheckReservedBodyBindingsPosition verifies the reported position lands on
// the binding identifier itself (offset-precise), not the enclosing GoBlock.
func TestCheckReservedBodyBindingsPosition(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t{{ attrs := 1 }}\n}\n"
	c, fset := componentFromSrc(t, src)
	decls := checkReservedBodyBindings(c)
	if len(decls) != 1 {
		t.Fatalf("want 1 binding, got %d (%v)", len(decls), flaggedNames(decls))
	}
	off := fset.Position(decls[0].pos).Offset
	if got := src[off : off+len("attrs")]; got != "attrs" {
		t.Errorf("position %d maps to %q, want %q", off, got, "attrs")
	}
}

// TestCheckReservedBodyBindingsBackstop documents the design's Go-backstop
// expectation: a GoBlock whose Go does not parse yields NO gsx diagnostic (the
// syntactic walker cannot see its bindings), so the raw Go compiler error is the
// single backstop — soundness over completeness, never a false rejection. This
// pins that fragmentBindings, and therefore checkReservedBodyBindings, stays
// silent on the exotic/unparseable shapes rather than guessing.
func TestCheckReservedBodyBindingsBackstop(t *testing.T) {
	// An incomplete statement (`attrs := 1 +`) parses at the gsx layer (Go is an
	// opaque blob there) but fails go/parser: fragmentBindings returns nothing.
	if bs := fragmentBindings("attrs := 1 +", fragStmts); bs != nil {
		t.Fatalf("unparseable fragment should yield no bindings, got %v", bs)
	}
	src := "package p\n\ncomponent C() {\n\t{{ attrs := 1 + }}\n}\n"
	c, _ := componentFromSrc(t, src)
	if got := flaggedNames(checkReservedBodyBindings(c)); len(got) != 0 {
		t.Errorf("unparseable GoBlock should not flag (Go backstops it), got %v", got)
	}
}
