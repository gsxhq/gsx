package parser

import (
	"go/token"
	"testing"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/attrclass"
)

func parseAttrType(t *testing.T, src string, cls *attrclass.Classifier) ast.Attr {
	t.Helper()
	// gsx components use the `component` keyword, not `func`.
	full := "package p\n\ncomponent C() {\n\t" + src + "\n}\n"
	f, err := ParseFileWithClassifier(token.NewFileSet(), "c.gsx", []byte(full), 0, cls)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	// Walk to the first element's first attribute.
	var found ast.Attr
	ast.Inspect(f, func(n ast.Node) bool {
		if el, ok := n.(*ast.Element); ok && len(el.Attrs) > 0 && found == nil {
			found = el.Attrs[0]
			return false
		}
		return true
	})
	if found == nil {
		t.Fatalf("no attribute found in %q", src)
	}
	return found
}

// With a custom JS rule, a holey custom-framework attribute parses as *ast.JSAttr
// (holes split), not *ast.StaticAttr.
func TestCustomJSRuleSplitsHoles(t *testing.T) {
	cls := attrclass.New(attrclass.Rules{JS: []attrclass.Rule{{Prefix: "wire:"}}}, nil)
	got := parseAttrType(t, `<div wire:click="@{ action }"></div>`, cls)
	if _, ok := got.(*ast.JSAttr); !ok {
		t.Fatalf("with rule: got %T, want *ast.JSAttr", got)
	}
}

// Built-ins only: the same attribute is a plain StaticAttr (holes NOT split).
func TestBuiltinLeavesCustomAttrStatic(t *testing.T) {
	got := parseAttrType(t, `<div wire:click="@{ action }"></div>`, attrclass.Builtin())
	if _, ok := got.(*ast.StaticAttr); !ok {
		t.Fatalf("built-in: got %T, want *ast.StaticAttr", got)
	}
}
