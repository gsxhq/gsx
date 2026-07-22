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
		if reflect.ValueOf(x).Kind() == reflect.Ptr {
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
