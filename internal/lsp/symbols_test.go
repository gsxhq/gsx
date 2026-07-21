package lsp

import (
	"go/token"
	"slices"
	"strings"
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
	syms := FileSymbols("/m/page.gsx", []byte(src), f, fset, nil)

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
	syms := FileSymbols("/m/page.gsx", []byte(src), f, fset, nil)

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

func TestSemanticSymbols(t *testing.T) {
	const source = `package page

type (
	Model struct{ N int }
	Reader interface{ Read() }
	Alias string
)

const (
	First = 1
	Second = 2
)

var (
	Third int
	Fourth string
)

func helper() {}

func (m *Model) Size() int { return m.N }

func nested() any {
	node := <div/>
	return node
}

component Card() {
	<section/>
}
`
	pkg, path := analyzedLSPPackage(t, source)
	syms := FileSymbols(path, []byte(source), pkg.Files[path], pkg.GSXFset, pkg.SourceIndex)

	wantNames := []string{"Alias", "Reader", "Model", "First", "Second", "Fourth", "Third", "helper", "Size", "nested", "Card"}
	gotNames := make([]string, len(syms))
	for i, symbol := range syms {
		gotNames[i] = symbol.Name
	}
	if !slices.Equal(gotNames, wantNames) {
		t.Fatalf("symbol names = %v, want deterministic declaration order %v", gotNames, wantNames)
	}

	wantKinds := map[string]int{
		"Model": symKindStruct, "Reader": symKindInterface, "Alias": symKindClass,
		"First": symKindConstant, "Second": symKindConstant,
		"Third": symKindVariable, "Fourth": symKindVariable,
		"helper": symKindFunction, "Size": symKindMethod, "nested": symKindFunction,
		"Card": symKindFunction,
	}
	for name, wantKind := range wantKinds {
		symbol, ok := symByName(syms, name)
		if !ok {
			t.Fatalf("missing %q in %+v", name, syms)
		}
		if symbol.Kind != wantKind {
			t.Errorf("%s kind = %d, want %d", name, symbol.Kind, wantKind)
		}
		wantContainer := "page"
		if name == "Size" {
			wantContainer = "Model"
		}
		if symbol.Container != wantContainer {
			t.Errorf("%s container = %q, want %q", name, symbol.Container, wantContainer)
		}
		wantNameOffset := strings.Index(source, name)
		if symbol.NamePos.Offset != wantNameOffset {
			t.Errorf("%s NamePos.Offset = %d, want %d", name, symbol.NamePos.Offset, wantNameOffset)
		}
	}

	typeStart := strings.Index(source, "type (")
	typeEnd := strings.Index(source, "\n)\n\nconst") + len("\n)")
	for _, name := range []string{"Model", "Reader", "Alias"} {
		symbol, _ := symByName(syms, name)
		if symbol.DeclStart.Offset != typeStart || symbol.DeclEnd.Offset != typeEnd {
			t.Errorf("%s declaration range = [%d,%d), want grouped type range [%d,%d)", name, symbol.DeclStart.Offset, symbol.DeclEnd.Offset, typeStart, typeEnd)
		}
	}
	if count := countSymbolsNamed(syms, "Card"); count != 1 {
		t.Fatalf("Card symbol count = %d, want one AST-owned component", count)
	}
	if nested, ok := symByName(syms, "nested"); !ok || nested.NamePos.Offset != strings.Index(source, "nested") {
		t.Fatalf("GoWithElements declaration missing or misplaced: %+v", nested)
	}
}

func TestSemanticSymbolsRejectMismatchedIndex(t *testing.T) {
	const analyzed = "package page\n\nfunc stale() {}\n\ncomponent Old() { <div/> }\n"
	pkg, path := analyzedLSPPackage(t, analyzed)
	const current = "package page\n\nfunc fresh() {}\n\ncomponent New() { <div/> }\n"

	syms := FileSymbols(path, []byte(current), pkg.Files[path], pkg.GSXFset, pkg.SourceIndex)
	for _, stale := range []string{"stale", "Old"} {
		if _, ok := symByName(syms, stale); ok {
			t.Fatalf("mismatched retained declaration %q escaped into current symbols: %+v", stale, syms)
		}
	}
	for _, current := range []string{"fresh", "New"} {
		if _, ok := symByName(syms, current); !ok {
			t.Fatalf("current declaration %q not recovered from parser fallback: %+v", current, syms)
		}
	}
}

func countSymbolsNamed(symbols []Symbol, name string) int {
	count := 0
	for _, symbol := range symbols {
		if symbol.Name == name {
			count++
		}
	}
	return count
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
