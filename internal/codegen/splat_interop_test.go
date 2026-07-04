package codegen

import (
	"testing"

	"github.com/gsxhq/gsx/ast"
)

// TestSplatComponentNonByo proves a whole-struct splat `<C { f... }/>` on a
// NON-byo component (a templ-interop / cross-package convention component whose
// Props type gsx did not enumerate as a byo struct) lowers to a whole-struct
// splat — childPropsLiteral returns the splat subject `f`, NOT an Attrs-bag merge.
//
// This is the bug this branch fixes: previously the splat fell through to the
// generic attr loop and emitted `CProps{Attrs: gsx.Attrs{}.Merge(f)}`, which
// fails to compile because an interop Props struct (e.g. CheckboxPopupSelectProps)
// has no `Attrs gsx.Attrs` field. `{ f... }` on a component tag is a whole-struct
// splat by definition (docs/guide/syntax/props.md) — the byo path already lowers
// it correctly, and a non-byo component has no synthesized fallthrough Attrs bag,
// so the splat is unambiguously the whole prop value.
func TestSplatComponentNonByo(t *testing.T) {
	t.Parallel()
	byo := newByoData() // CProps is deliberately NOT registered as a byo struct.
	// CProps is an ENUMERATED interop struct: fields known, but NO `Attrs gsx.Attrs`
	// bag — so a spread can only mean the whole prop value.
	propFields := map[string]map[string]bool{"CProps": {"Name": true, "Label": true}}

	// <C { f... }/> — the sole attr is a whole-struct splat; no children.
	el := &ast.Element{
		Tag:   "C",
		Void:  true,
		Attrs: []ast.Attr{&ast.SpreadAttr{Expr: "f"}},
	}
	fields, splatExpr, _, err := childPropsLiteral(el, "CProps", "gsx", "gsx.DefaultClassMerge", nil, propFields, nil, byo, nil,
		func(nodes []ast.Markup) (string, error) { return "", nil }, false, nil, nil, nil)
	if err != nil {
		t.Fatalf("childPropsLiteral: %v", err)
	}
	if splatExpr != "f" {
		t.Fatalf("splatExpr = %q, want %q (whole-struct splat); fields=%v", splatExpr, "f", fields)
	}
	if len(fields) != 0 {
		t.Fatalf("expected no props fields for a whole-struct splat, got %v", fields)
	}
}

// TestSplatComponentNonByoMixedErrors proves the all-or-nothing rule holds for a
// non-byo component too: a splat mixed with another attr is a positioned error,
// exactly as on the byo path — not a silent Attrs merge.
func TestSplatComponentNonByoMixedErrors(t *testing.T) {
	t.Parallel()
	byo := newByoData()
	propFields := map[string]map[string]bool{"CProps": {"Name": true, "Label": true}}

	// <C name="x" { f... }/> — splat mixed with a field attr must error.
	el := &ast.Element{
		Tag:  "C",
		Void: true,
		Attrs: []ast.Attr{
			&ast.StaticAttr{Name: "name", Value: "x"},
			&ast.SpreadAttr{Expr: "f"},
		},
	}
	_, _, _, err := childPropsLiteral(el, "CProps", "gsx", "gsx.DefaultClassMerge", nil, propFields, nil, byo, nil,
		func(nodes []ast.Markup) (string, error) { return "", nil }, false, nil, nil, nil)
	if err == nil {
		t.Fatalf("expected an error for a splat mixed with other attrs, got nil")
	}
}
