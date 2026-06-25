package parser_test

import (
	"go/token"
	"testing"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/parser"
)

func FuzzParseFile(f *testing.F) {
	// seed corpus: valid inputs, edge cases, and deliberately broken inputs
	f.Add([]byte("package p\ncomponent X() { <div class=\"a\">{x}</div> }"))
	f.Add([]byte("package p\ncomponent X() { <>{a}<b/></> }"))
	f.Add([]byte(""))
	f.Add([]byte("package p\ncomponent X() { <div></span> }"))
	f.Add([]byte("package p\ncomponent X() { <p>Hi {name}! {greeting(name)?}</p> }"))
	f.Add([]byte("package p\ncomponent X() { <input type=\"text\" id={id} disabled { rest... } /> }"))
	f.Add([]byte("package p\ncomponent X() { <Panel header={<h1>Hi</h1>}></Panel> }"))
	f.Add([]byte("package p\ntype T struct{}\ncomponent (t T) M() { <main>x</main> }"))
	// pipeline operator `|>` (interp + attribute, valid and malformed)
	f.Add([]byte("package p\ncomponent X() { <p>{ name |> upper |> truncate(20) }</p> }"))
	f.Add([]byte("package p\ncomponent X() { <a href={ u |> absolute()? }>x</a> }"))
	f.Add([]byte("package p\ncomponent X() { <p>{ a |> }</p> }"))
	f.Add([]byte("package p\ncomponent X() { <p>{ x |>|> y }</p> }"))

	f.Fuzz(func(t *testing.T, src []byte) {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "fuzz.gsx", src, 0) // MUST NOT PANIC on any bytes
		if err != nil || file == nil {
			return // invalid input is fine
		}
		ast.Inspect(file, func(n ast.Node) bool { // MUST NOT PANIC
			if n != nil && n.End() < n.Pos() {
				t.Fatalf("node End() < Pos(): %T %d < %d", n, n.End(), n.Pos())
			}
			return true
		})
	})
}
