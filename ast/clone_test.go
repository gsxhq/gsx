package ast_test

import (
	"bytes"
	"go/token"
	"reflect"
	"testing"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/parser"
)

// cloneTestSrc exercises a broad spread of node families: a top-level
// var-with-element (GoWithElements + GoText), a component body with an element
// carrying static/expr/bool/class/spread attributes and an in-tag conditional,
// interpolation with a pipe stage, control-flow markup (if/for/switch), a
// fragment, an ordered-attrs bag, a {{ }} block, and an embedded css literal.
const cloneTestSrc = "package views\n" +
	"\n" +
	"var help = <a href={u}>{ label }</a>\n" +
	"\n" +
	"component Page(title string, items []string, on bool) {\n" +
	"\t<div id=\"x\" hidden>\n" +
	"\t\t{ title |> upper }\n" +
	"\t\t{ if on { <b>hi</b> } else { <i>bye</i> } }\n" +
	"\t\t{ for _, it := range items { <li>{ it }</li> } }\n" +
	"\t\t<Card attrs={{ \"data-x\": 1 }}>text</Card>\n" +
	"\t\t<>frag</>\n" +
	"\t\t<!-- keep -->\n" +
	"\t</div>\n" +
	"}\n"

// collectPtrNodes returns the set of pointer-backed nodes reachable from n
// (interface values whose dynamic kind is Ptr). Value nodes (GoText) are
// excluded — they are immutable and legitimately shared by value.
func collectPtrNodes(n ast.Node) map[ast.Node]bool {
	set := map[ast.Node]bool{}
	ast.Inspect(n, func(x ast.Node) bool {
		if x == nil {
			return false
		}
		if reflect.ValueOf(x).Kind() == reflect.Pointer {
			set[x] = true
		}
		return true
	})
	return set
}

func TestCloneFileIndependentAndEqual(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "t.gsx", cloneTestSrc, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	clone := ast.CloneFile(f)

	// Structural + value equality: the clone prints identically to the original.
	var a, b bytes.Buffer
	if err := ast.Fprint(&a, f); err != nil {
		t.Fatalf("Fprint original: %v", err)
	}
	if err := ast.Fprint(&b, clone); err != nil {
		t.Fatalf("Fprint clone: %v", err)
	}
	if a.String() != b.String() {
		t.Fatalf("clone AST prints differently:\n--- original ---\n%s\n--- clone ---\n%s", a.String(), b.String())
	}

	// Deep independence: no pointer-backed node is shared between the two trees.
	orig := collectPtrNodes(f)
	cl := collectPtrNodes(clone)
	if len(orig) == 0 {
		t.Fatal("no pointer nodes collected; test is not exercising the tree")
	}
	if len(orig) != len(cl) {
		t.Fatalf("node count differs: original=%d clone=%d (structure not preserved)", len(orig), len(cl))
	}
	for n := range cl {
		if orig[n] {
			t.Fatalf("clone shares node pointer %T with the original tree", n)
		}
	}

	// Mutating the clone must not touch the original: stamp IsComponent on the
	// first element found in the clone and confirm the original's counterpart
	// stays false.
	var cloneEl, origEl *ast.Element
	ast.Inspect(clone, func(x ast.Node) bool {
		if e, ok := x.(*ast.Element); ok && cloneEl == nil {
			cloneEl = e
		}
		return true
	})
	ast.Inspect(f, func(x ast.Node) bool {
		if e, ok := x.(*ast.Element); ok && origEl == nil {
			origEl = e
		}
		return true
	})
	if cloneEl == nil || origEl == nil {
		t.Fatal("no element found in tree")
	}
	cloneEl.IsComponent = true
	if origEl.IsComponent {
		t.Fatal("mutating the clone's Element.IsComponent leaked into the original tree")
	}
}

// TestCloneFileCopiesEmbeddedOverlays pins that every codegen-only
// Go-expression overlay (see Interp.Embedded) is deep-copied: the codegen
// split writes these slices and their nested nodes, so a shared backing array
// or node pointer would let one analysis contaminate the cached pristine tree.
func TestCloneFileCopiesEmbeddedOverlays(t *testing.T) {
	overlay := func() []ast.GoPart {
		return []ast.GoPart{
			ast.GoText{Src: "wrap("},
			&ast.EmbeddedInterp{Lang: ast.EmbeddedText, Segments: []ast.Markup{&ast.Text{Value: "a"}}},
		}
	}
	elem := func(attrs ...ast.Attr) *ast.File {
		return &ast.File{Decls: []ast.Decl{&ast.Component{Body: []ast.Markup{&ast.Element{Tag: "div", Attrs: attrs}}}}}
	}
	body := func(m ast.Markup) *ast.File {
		return &ast.File{Decls: []ast.Decl{&ast.Component{Body: []ast.Markup{m}}}}
	}
	firstAttr := func(f *ast.File) ast.Attr {
		return f.Decls[0].(*ast.Component).Body[0].(*ast.Element).Attrs[0]
	}
	firstMarkup := func(f *ast.File) ast.Markup {
		return f.Decls[0].(*ast.Component).Body[0]
	}
	composed := func(p ast.ComposedPart) *ast.File {
		return elem(&ast.ComposedAttr{Name: "class", Parts: []ast.ComposedPart{p}})
	}
	firstPart := func(f *ast.File) *ast.ComposedPart {
		return &firstAttr(f).(*ast.ComposedAttr).Parts[0]
	}

	cases := []struct {
		name  string
		build func() *ast.File
		get   func(*ast.File) []ast.GoPart
	}{
		{"ExprAttr.Embedded",
			func() *ast.File { return elem(&ast.ExprAttr{Name: "title", Embedded: overlay()}) },
			func(f *ast.File) []ast.GoPart { return firstAttr(f).(*ast.ExprAttr).Embedded }},
		{"SpreadAttr.Embedded",
			func() *ast.File { return elem(&ast.SpreadAttr{Embedded: overlay()}) },
			func(f *ast.File) []ast.GoPart { return firstAttr(f).(*ast.SpreadAttr).Embedded }},
		{"OrderedPair.Embedded",
			func() *ast.File {
				return elem(&ast.OrderedAttrsAttr{Name: "attrs", Pairs: []ast.OrderedPair{{Key: "k", Embedded: overlay()}}})
			},
			func(f *ast.File) []ast.GoPart { return firstAttr(f).(*ast.OrderedAttrsAttr).Pairs[0].Embedded }},
		{"ComposedPart.ExprEmbedded",
			func() *ast.File { return composed(ast.ComposedPart{ExprEmbedded: overlay()}) },
			func(f *ast.File) []ast.GoPart { return firstPart(f).ExprEmbedded }},
		{"ComposedPart.CondEmbedded",
			func() *ast.File { return composed(ast.ComposedPart{CondEmbedded: overlay()}) },
			func(f *ast.File) []ast.GoPart { return firstPart(f).CondEmbedded }},
		{"ValueArm.Embedded",
			func() *ast.File {
				return composed(ast.ComposedPart{CF: &ast.ValueCF{If: &ast.ValueIf{Then: &ast.ValueArm{Embedded: overlay()}}}})
			},
			func(f *ast.File) []ast.GoPart { return firstPart(f).CF.If.Then.Embedded }},
		{"ValueIf.CondEmbedded",
			func() *ast.File {
				return composed(ast.ComposedPart{CF: &ast.ValueCF{If: &ast.ValueIf{CondEmbedded: overlay(), Then: &ast.ValueArm{}}}})
			},
			func(f *ast.File) []ast.GoPart { return firstPart(f).CF.If.CondEmbedded }},
		{"ValueSwitch.TagEmbedded",
			func() *ast.File {
				return composed(ast.ComposedPart{CF: &ast.ValueCF{Switch: &ast.ValueSwitch{TagEmbedded: overlay()}}})
			},
			func(f *ast.File) []ast.GoPart { return firstPart(f).CF.Switch.TagEmbedded }},
		{"ValueSwitchCase.ListEmbedded",
			func() *ast.File {
				return composed(ast.ComposedPart{CF: &ast.ValueCF{Switch: &ast.ValueSwitch{Cases: []*ast.ValueSwitchCase{{ListEmbedded: overlay()}}}}})
			},
			func(f *ast.File) []ast.GoPart { return firstPart(f).CF.Switch.Cases[0].ListEmbedded }},
		{"CondAttr.CondEmbedded",
			func() *ast.File { return elem(&ast.CondAttr{CondEmbedded: overlay()}) },
			func(f *ast.File) []ast.GoPart { return firstAttr(f).(*ast.CondAttr).CondEmbedded }},
		{"SwitchAttr.TagEmbedded",
			func() *ast.File { return elem(&ast.SwitchAttr{TagEmbedded: overlay()}) },
			func(f *ast.File) []ast.GoPart { return firstAttr(f).(*ast.SwitchAttr).TagEmbedded }},
		{"AttrCaseClause.ListEmbedded",
			func() *ast.File {
				return elem(&ast.SwitchAttr{Cases: []*ast.AttrCaseClause{{ListEmbedded: overlay()}}})
			},
			func(f *ast.File) []ast.GoPart { return firstAttr(f).(*ast.SwitchAttr).Cases[0].ListEmbedded }},
		{"IfMarkup.CondEmbedded",
			func() *ast.File { return body(&ast.IfMarkup{CondEmbedded: overlay()}) },
			func(f *ast.File) []ast.GoPart { return firstMarkup(f).(*ast.IfMarkup).CondEmbedded }},
		{"ForMarkup.ClauseEmbedded",
			func() *ast.File { return body(&ast.ForMarkup{ClauseEmbedded: overlay()}) },
			func(f *ast.File) []ast.GoPart { return firstMarkup(f).(*ast.ForMarkup).ClauseEmbedded }},
		{"SwitchMarkup.TagEmbedded",
			func() *ast.File { return body(&ast.SwitchMarkup{TagEmbedded: overlay()}) },
			func(f *ast.File) []ast.GoPart { return firstMarkup(f).(*ast.SwitchMarkup).TagEmbedded }},
		{"CaseClause.ListEmbedded",
			func() *ast.File {
				return body(&ast.SwitchMarkup{Cases: []*ast.CaseClause{{ListEmbedded: overlay()}}})
			},
			func(f *ast.File) []ast.GoPart { return firstMarkup(f).(*ast.SwitchMarkup).Cases[0].ListEmbedded }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orig := tc.build()
			clone := ast.CloneFile(orig)
			o, c := tc.get(orig), tc.get(clone)
			if len(c) != 2 {
				t.Fatalf("clone overlay has %d parts, want 2", len(c))
			}
			oInterp := o[1].(*ast.EmbeddedInterp)
			cInterp, ok := c[1].(*ast.EmbeddedInterp)
			if !ok {
				t.Fatalf("clone overlay part 1 is %T, want *ast.EmbeddedInterp", c[1])
			}
			if cInterp == oInterp {
				t.Fatal("clone shares the *EmbeddedInterp pointer with the original")
			}
			if cInterp.Segments[0] == oInterp.Segments[0] {
				t.Fatal("clone shares the nested literal's segment with the original")
			}
			c[0] = ast.GoText{Src: "mutated("}
			c[1] = &ast.EmbeddedInterp{Lang: ast.EmbeddedJS}
			if got := o[0].(ast.GoText).Src; got != "wrap(" {
				t.Fatalf("mutating the clone's overlay leaked into the original: part 0 = %q", got)
			}
			if o[1] != oInterp {
				t.Fatal("mutating the clone's overlay replaced the original's part 1")
			}
		})
	}
}
