package lsp

import (
	"encoding/json"
	"errors"
	"go/token"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/gsxfmt"
	"github.com/gsxhq/gsx/internal/pretty"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// docSymFrame builds a textDocument/documentSymbol request frame.
func docSymFrame(id int, uri string) string {
	return jsonFrame(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "textDocument/documentSymbol",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri}},
	})
}

// symbolFileAnalyzer returns a Package built by parsing the open buffer, so
// s.pkgs[dir] carries Files + GSXFset for documentSymbol.
type symbolFileAnalyzer struct{}

func (symbolFileAnalyzer) SetOverride(string, []byte) ([]string, error) { return nil, nil }
func (symbolFileAnalyzer) ClearOverride(string) ([]string, error)       { return nil, nil }

func (symbolFileAnalyzer) Analyze(_ string, override map[string][]byte) (*Package, error) {
	fset := token.NewFileSet()
	files := map[string]*gsxast.File{}
	for path, src := range override {
		if f, err := gsxparser.ParseFile(fset, path, src, 0); err == nil {
			files[path] = f
		}
	}
	return &Package{GSXFset: fset, Files: files}, nil
}
func (symbolFileAnalyzer) AnalyzeEphemeral(string, string, []byte) (*Package, error) {
	return nil, errors.New("not implemented")
}
func (symbolFileAnalyzer) AnalyzeEphemeralNonBlocking(string, string, []byte) (*Package, bool, error) {
	return nil, true, errors.New("not implemented")
}
func (symbolFileAnalyzer) AnalyzeModule(string, map[string][]byte) ([]CrossRef, error) {
	return nil, nil
}
func (symbolFileAnalyzer) AnalyzeModuleParams(string, map[string][]byte) ([]ComponentParamRenameFact, error) {
	return nil, nil
}
func (symbolFileAnalyzer) ModuleSymbols(string, map[string][]byte) ([]Symbol, error) {
	return nil, nil
}
func (symbolFileAnalyzer) FormatSettings(string) gsxfmt.FormatSettings {
	return gsxfmt.FormatSettings{Width: 80, TabWidth: pretty.DefaultTabWidth}
}
func (symbolFileAnalyzer) ImportsMode(string) gsxfmt.ImportsMode {
	return gsxfmt.ImportsGoimports
}
func (symbolFileAnalyzer) ResolveImport(string, string, string) []string { return nil }

func TestDocumentSymbol(t *testing.T) {
	uri := "file:///m/page.gsx"
	text := "package page\n\ncomponent Card() {\n\t<div/>\n}\n"
	out := drive(t, symbolFileAnalyzer{}, initFrame()+didOpenFrame(uri, text)+docSymFrame(2, uri)+exitFrame())
	if !strings.Contains(out, `"name":"Card"`) {
		t.Fatalf("documentSymbol missing Card:\n%s", out)
	}
	if !strings.Contains(out, `"selectionRange"`) {
		t.Fatalf("documentSymbol missing selectionRange:\n%s", out)
	}
}

// TestDocumentSymbolMultibyteUTF16Columns asserts that selectionRange.character
// is counted in UTF-16 code units (the default negotiated encoding), not raw
// bytes, for a symbol whose declaration line has multibyte content both before
// and inside the name. This guards nameSelectionRange's
// "NamePos.Column + len(sym.Name)" byte-column arithmetic in documentsymbol.go:
// the addition itself must stay byte-based (matching token.Position.Column),
// but both ends must still go through convertPos's encoding-aware conversion
// before being reported. A regression that reported raw byte columns as
// `character` (skipping UTF-16 conversion) would produce different numbers
// than asserted below.
//
// Declaration line (3rd line of the buffer, 0-based line 2):
//
//	var π, Namé = 3.14, "x"
//
// "π" (U+03C0) is 2 UTF-8 bytes / 1 UTF-16 unit; "é" (U+00E9) likewise. The
// target symbol is the second declared name, "Namé":
//
//	byte column of 'N'  (1-based) = 9   → prefix "var π, " is 8 bytes / 7 UTF-16 units
//	byte column past 'é' (1-based) = 14 → prefix "var π, Namé" is 13 bytes / 11 UTF-16 units
//
// So selectionRange should be {start: 7, end: 11} in UTF-16 characters — a
// byte-column implementation would instead emit {start: 8, end: 13}.
func TestDocumentSymbolMultibyteUTF16Columns(t *testing.T) {
	uri := "file:///m/multibyte.gsx"
	text := "package page\n\nvar π, Namé = 3.14, \"x\"\n"
	out := drive(t, symbolFileAnalyzer{}, initFrame()+didOpenFrame(uri, text)+docSymFrame(2, uri)+exitFrame())

	frames := readFrames(t, out)
	var reply map[string]json.RawMessage
	for _, f := range frames {
		var id int
		if err := json.Unmarshal(f["id"], &id); err == nil && id == 2 {
			reply = f
			break
		}
	}
	if reply == nil {
		t.Fatalf("no reply with id=2 in output:\n%s", out)
	}

	var syms []DocumentSymbol
	if err := json.Unmarshal(reply["result"], &syms); err != nil {
		t.Fatalf("decode documentSymbol result: %v\nraw: %s", err, out)
	}

	var got *DocumentSymbol
	for i := range syms {
		if syms[i].Name == "Namé" {
			got = &syms[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("no symbol named Namé in result: %+v", syms)
	}

	wantStart := Position{Line: 2, Character: 7}
	wantEnd := Position{Line: 2, Character: 11}
	if got.SelectionRange.Start != wantStart || got.SelectionRange.End != wantEnd {
		t.Fatalf("selectionRange = %+v, want {Start:%+v End:%+v} (UTF-16 columns, not byte columns %d/%d)",
			got.SelectionRange, wantStart, wantEnd, 8, 13)
	}
}
