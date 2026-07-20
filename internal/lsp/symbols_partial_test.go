package lsp

import (
	"slices"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
)

func TestPartialGoSymbolsKeepRecoveredDeclarations(t *testing.T) {
	const source = `package page

func Before() {}

var Broken = []int{1 2}

func After() {}

var _ = "func Fabricated() {}"
`
	file, fset := parseGSX(t, "/m/recovery.gsx", source)
	syms := FileSymbols("/m/recovery.gsx", []byte(source), file, fset, nil)

	for _, name := range []string{"Before", "Broken", "After"} {
		if _, ok := symByName(syms, name); !ok {
			t.Errorf("recovered declarations missing %q: %+v", name, syms)
		}
	}
	if _, ok := symByName(syms, "Fabricated"); ok {
		t.Errorf("merely declaration-like text fabricated a symbol: %+v", syms)
	}
}

func TestPartialGoWithElementsSymbolsKeepRecoveredDeclarations(t *testing.T) {
	const source = `package page

func Before() any { return <div/> }

var Broken = []int{1 2}

func After() any { return <span>
	recovered
</span> }

var _ = "func Fabricated() {}"
`
	file, fset := parseGSX(t, "/m/mixed-recovery.gsx", source)
	syms := FileSymbols("/m/mixed-recovery.gsx", []byte(source), file, fset, nil)

	gotNames := make([]string, len(syms))
	for i, symbol := range syms {
		gotNames[i] = symbol.Name
	}
	wantNames := []string{"Before", "Broken", "After"}
	if !slices.Equal(gotNames, wantNames) {
		t.Fatalf("recovered mixed-region symbols = %v, want %v; all=%+v", gotNames, wantNames, syms)
	}
	declarations := map[string]string{
		"Before": "func Before() any { return <div/> }",
		"Broken": "var Broken = []int{1 2}",
		"After":  "func After() any { return <span>\n\trecovered\n</span> }",
	}
	for _, name := range wantNames {
		symbol, _ := symByName(syms, name)
		nameOffset := strings.Index(source, name)
		declarationStart := strings.Index(source, declarations[name])
		declarationEnd := declarationStart + len(declarations[name])
		if symbol.NamePos.Offset != nameOffset || symbol.DeclStart.Offset != declarationStart || symbol.DeclEnd.Offset != declarationEnd {
			t.Errorf("%s positions = name %d range [%d,%d), want name %d range [%d,%d)", name, symbol.NamePos.Offset, symbol.DeclStart.Offset, symbol.DeclEnd.Offset, nameOffset, declarationStart, declarationEnd)
		}
	}
	if _, ok := symByName(syms, "Fabricated"); ok {
		t.Errorf("declaration-like string text fabricated a symbol: %+v", syms)
	}
}

func TestPartialGoWithElementsSymbolsPreserveCRLFOffsets(t *testing.T) {
	const source = "package page\r\n\r\n" +
		"var Inline = <span>\r\n\trecovered\r\n</span>\r\n\r\n" +
		"func After() {}\r\n"
	file, fset := parseGSX(t, "/m/crlf-recovery.gsx", source)
	syms := FileSymbols("/m/crlf-recovery.gsx", []byte(source), file, fset, nil)

	wantDeclarations := map[string]string{
		"Inline": "var Inline = <span>\r\n\trecovered\r\n</span>",
		"After":  "func After() {}",
	}
	for name, declaration := range wantDeclarations {
		symbol, ok := symByName(syms, name)
		if !ok {
			t.Errorf("CRLF recovery missing %q: %+v", name, syms)
			continue
		}
		start := strings.Index(source, declaration)
		end := start + len(declaration)
		nameOffset := strings.Index(source, name)
		if symbol.NamePos.Offset != nameOffset || symbol.DeclStart.Offset != start || symbol.DeclEnd.Offset != end {
			t.Errorf("%s CRLF positions = name %d range [%d,%d), want name %d range [%d,%d)", name, symbol.NamePos.Offset, symbol.DeclStart.Offset, symbol.DeclEnd.Offset, nameOffset, start, end)
		}
	}
}

func TestPartialExpressionPlaceholderNormalizesCRWithoutMovingOffsets(t *testing.T) {
	source := []byte("<span>\r\n\trecovered\r\n</span>")
	var reconstructed partialGoSource
	if !reconstructed.appendExpressionPlaceholder(source, 0, len(source)) {
		t.Fatal("appendExpressionPlaceholder rejected a structurally valid element span")
	}
	placeholder := reconstructed.text.String()
	if len(placeholder) != len(source) {
		t.Fatalf("placeholder length = %d, want authored width %d", len(placeholder), len(source))
	}
	if strings.ContainsRune(placeholder, '\r') {
		t.Fatalf("placeholder retained CR bytes that Go may remove from raw literal token values: %q", placeholder)
	}
	for offset, b := range source {
		if b == '\n' && placeholder[offset] != '\n' {
			t.Errorf("placeholder byte %d = %q, want preserved LF", offset, placeholder[offset])
		}
	}
	if start, ok := reconstructed.sourceOffset(0); !ok || start != 0 {
		t.Errorf("placeholder start maps to (%d,%t), want (0,true)", start, ok)
	}
	if end, ok := reconstructed.sourceOffset(len(placeholder)); !ok || end != len(source) {
		t.Errorf("placeholder end maps to (%d,%t), want (%d,true)", end, ok, len(source))
	}
	if _, ok := reconstructed.sourceOffset(1); ok {
		t.Error("placeholder interior unexpectedly maps to authored source")
	}
}

func TestPartialGoWithElementsSymbolsFailClosedOnUnprovenMapping(t *testing.T) {
	const source = "package page\n\nfunc Mixed() any { return <div/> }\n"
	file, fset := parseGSX(t, "/m/unproven.gsx", source)
	var declaration *gsxast.GoWithElements
	for _, candidate := range file.Decls {
		if mixed, ok := candidate.(*gsxast.GoWithElements); ok {
			declaration = mixed
			break
		}
	}
	if declaration == nil {
		t.Fatal("fixture did not parse as GoWithElements")
	}
	corrupted := false
	for i, part := range declaration.Parts {
		text, ok := part.(gsxast.GoText)
		if !ok || text.Src == "" {
			continue
		}
		text.Src += "x"
		declaration.Parts[i] = text
		corrupted = true
		break
	}
	if !corrupted {
		t.Fatal("fixture has no GoText part to corrupt")
	}
	if symbols := partialGoWithElementsSymbols(file, fset, []byte(source), declaration); len(symbols) != 0 {
		t.Fatalf("unproven GoWithElements mapping emitted symbols: %+v", symbols)
	}
}
