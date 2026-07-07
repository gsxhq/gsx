package lsp

import (
	"go/token"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// parseGSX parses src into a gsx File and its FileSet for symbol tests.
func parseGSX(t *testing.T, name, src string) (*gsxast.File, *token.FileSet) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := gsxparser.ParseFile(fset, name, []byte(src), 0)
	if err != nil {
		t.Fatal(err)
	}
	return f, fset
}

func symByName(syms []Symbol, name string) (Symbol, bool) {
	for _, s := range syms {
		if s.Name == name {
			return s, true
		}
	}
	return Symbol{}, false
}

func TestFileSymbolsComponents(t *testing.T) {
	src := "package page\n\ncomponent Card(title string) {\n\t<div>{title}</div>\n}\n\ncomponent (f *Form) Field() {\n\t<input/>\n}\n"
	f, fset := parseGSX(t, "/m/page.gsx", src)
	syms := FileSymbols("/m/page.gsx", f, fset)

	card, ok := symByName(syms, "Card")
	if !ok {
		t.Fatalf("Card not found in %+v", syms)
	}
	if card.Kind != symKindFunction {
		t.Errorf("Card kind = %d, want %d", card.Kind, symKindFunction)
	}
	if card.Container != "page" {
		t.Errorf("Card container = %q, want %q", card.Container, "page")
	}
	if card.NamePos.Line != 3 || card.NamePos.Column != 11 {
		t.Errorf("Card NamePos = %d:%d, want 3:11", card.NamePos.Line, card.NamePos.Column)
	}

	field, ok := symByName(syms, "Field")
	if !ok {
		t.Fatalf("Field not found")
	}
	if field.Kind != symKindMethod {
		t.Errorf("Field kind = %d, want %d (method)", field.Kind, symKindMethod)
	}
	if field.Container != "Form" {
		t.Errorf("Field container = %q, want receiver type %q", field.Container, "Form")
	}
}
