package lsp

import (
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
)

func TestAttrNameCoversEveryNamedAttributeNode(t *testing.T) {
	tests := []struct {
		attr gsxast.Attr
		want string
	}{
		{&gsxast.StaticAttr{Name: "static"}, "static"},
		{&gsxast.ExprAttr{Name: "expr"}, "expr"},
		{&gsxast.BoolAttr{Name: "bool"}, "bool"},
		{&gsxast.MarkupAttr{Name: "markup"}, "markup"},
		{&gsxast.EmbeddedAttr{Name: "embedded"}, "embedded"},
		{&gsxast.ClassAttr{Name: "class"}, "class"},
		{&gsxast.OrderedAttrsAttr{Name: "ordered"}, "ordered"},
	}
	for _, tt := range tests {
		got, ok := attrName(tt.attr)
		if !ok || got != tt.want {
			t.Errorf("attrName(%T) = %q, %v; want %q, true", tt.attr, got, ok, tt.want)
		}
	}
	if got, ok := attrName(&gsxast.SpreadAttr{}); ok || got != "" {
		t.Fatalf("attrName(SpreadAttr) = %q, %v; want unnamed", got, ok)
	}
}
