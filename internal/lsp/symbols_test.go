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

func TestFileSymbolsGoChunkDecls(t *testing.T) {
	src := "package page\n\n" +
		"type Widget struct{ N int }\n\n" +
		"type Reader interface{ Read() }\n\n" +
		"type ID string\n\n" +
		"const Max = 10\n\n" +
		"var count int\n\n" +
		"func helper() int { return 1 }\n\n" +
		"func (w Widget) Size() int { return w.N }\n\n" +
		"component Card() {\n\t<div/>\n}\n"
	f, fset := parseGSX(t, "/m/page.gsx", src)
	syms := FileSymbols("/m/page.gsx", f, fset)

	cases := map[string]int{
		"Widget": symKindStruct,
		"Reader": symKindInterface,
		"ID":     symKindClass,
		"Max":    symKindConstant,
		"count":  symKindVariable,
		"helper": symKindFunction,
		"Size":   symKindMethod,
		"Card":   symKindFunction,
	}
	for name, wantKind := range cases {
		s, ok := symByName(syms, name)
		if !ok {
			t.Errorf("%s not found in %+v", name, syms)
			continue
		}
		if s.Kind != wantKind {
			t.Errorf("%s kind = %d, want %d", name, s.Kind, wantKind)
		}
	}

	// Position mapping is exact: "Widget" name starts at line 3, column 6.
	w, _ := symByName(syms, "Widget")
	if w.NamePos.Line != 3 || w.NamePos.Column != 6 {
		t.Errorf("Widget NamePos = %d:%d, want 3:6", w.NamePos.Line, w.NamePos.Column)
	}
	// Method receiver becomes the container.
	size, _ := symByName(syms, "Size")
	if size.Container != "Widget" {
		t.Errorf("Size container = %q, want %q", size.Container, "Widget")
	}
}

// TestReceiverTypeName exercises the go/parser-based receiver parsing
// directly, including shapes a string-splitting heuristic would mis-handle
// (irregular spacing). See gsx-tag-variant-analysis / review of Task 1.
func TestReceiverTypeName(t *testing.T) {
	cases := []struct {
		recv string
		want string
	}{
		{"(f *Form)", "Form"},
		{"(p UsersPage)", "UsersPage"},
		{"( f   *Form )", "Form"},
	}
	for _, c := range cases {
		got := receiverTypeName(c.recv)
		if got != c.want {
			t.Errorf("receiverTypeName(%q) = %q, want %q", c.recv, got, c.want)
		}
	}
}
