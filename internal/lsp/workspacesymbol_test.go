package lsp

import (
	"go/token"
	"strings"
	"testing"
)

func wsSymFrame(id int, query string) string {
	return jsonFrame(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "workspace/symbol",
		"params": map[string]any{"query": query},
	})
}

// wsSymAnalyzer serves a fixed symbol list and counts ModuleSymbols calls.
type wsSymAnalyzer struct {
	calls int
	syms  []Symbol
}

func (a *wsSymAnalyzer) Analyze(string, map[string][]byte) (*Package, error) { return &Package{}, nil }
func (a *wsSymAnalyzer) AnalyzeModule(string, map[string][]byte) ([]CrossRef, error) {
	return nil, nil
}
func (a *wsSymAnalyzer) ModuleSymbols(string, map[string][]byte) ([]Symbol, error) {
	a.calls++
	return a.syms, nil
}
func (a *wsSymAnalyzer) PrintWidth(string) int { return 80 }

func TestWorkspaceSymbolQueryAndCache(t *testing.T) {
	uri := "file:///m/a.gsx"
	text := "package x\n\ncomponent Card() {\n\t<div/>\n}\n"
	pos := func(line, col int) token.Position {
		return token.Position{Filename: "/m/a.gsx", Line: line, Column: col}
	}
	a := &wsSymAnalyzer{syms: []Symbol{
		{Name: "Card", Kind: symKindFunction, Container: "x", NamePos: pos(3, 11), DeclStart: pos(3, 1), DeclEnd: pos(5, 2)},
		{Name: "Button", Kind: symKindFunction, Container: "x", NamePos: pos(1, 1), DeclStart: pos(1, 1), DeclEnd: pos(1, 1)},
	}}

	// Query "car" (case-insensitive substring) → only Card. Two queries with no
	// edit between → one ModuleSymbols call (cached).
	out := drive(t, a, initFrame()+didOpenFrame(uri, text)+
		wsSymFrame(2, "car")+wsSymFrame(3, "car")+exitFrame())
	if !strings.Contains(out, `"name":"Card"`) {
		t.Fatalf("query 'car' should match Card:\n%s", out)
	}
	if strings.Contains(out, `"name":"Button"`) {
		t.Fatalf("query 'car' should not match Button:\n%s", out)
	}
	if a.calls != 1 {
		t.Fatalf("cached: want 1 ModuleSymbols call, got %d", a.calls)
	}

	// A didChange between two queries → two calls (cache invalidated).
	a2 := &wsSymAnalyzer{syms: a.syms}
	drive(t, a2, initFrame()+didOpenFrame(uri, text)+
		wsSymFrame(2, "")+didChangeFrame(uri, text+"\n")+wsSymFrame(3, "")+exitFrame())
	if a2.calls != 2 {
		t.Fatalf("invalidated: want 2 ModuleSymbols calls, got %d", a2.calls)
	}
}
