package lsp

import (
	"go/token"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
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
func (symbolFileAnalyzer) AnalyzeModule(string, map[string][]byte) ([]CrossRef, error) {
	return nil, nil
}
func (symbolFileAnalyzer) ModuleSymbols(string, map[string][]byte) ([]Symbol, error) {
	return nil, nil
}
func (symbolFileAnalyzer) PrintWidth(string) int { return 80 }

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
