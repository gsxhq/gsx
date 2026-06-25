package lsp

import (
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
)

func TestRenderComponentSig(t *testing.T) {
	fn := &gsxast.Component{Name: "Card", Params: "title string"}
	if got, want := renderComponentSig(fn), "component Card(title string)"; got != want {
		t.Errorf("func component: got %q, want %q", got, want)
	}
	method := &gsxast.Component{Recv: "(p UsersPage)", Name: "Row", Params: "u User"}
	if got, want := renderComponentSig(method), "component (p UsersPage) Row(u User)"; got != want {
		t.Errorf("method component: got %q, want %q", got, want)
	}
	nullary := &gsxast.Component{Name: "Page"}
	if got, want := renderComponentSig(nullary), "component Page()"; got != want {
		t.Errorf("nullary component: got %q, want %q", got, want)
	}
}
