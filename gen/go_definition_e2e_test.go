package gen

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestGoToGsxDefinition drives every ".go cursor → .gsx declaration" shape over
// ONE runLSP session (one codegen.Module open), plus the `gr` counterpart from
// the .gsx declaration itself:
//
//	id 2  gd  main.go  on Card   → card.gsx  (a .gsx-declared component func)
//	id 3  gd  main.go  on Model  → card.gsx  (a .gsx-declared TYPE)
//	id 4  gr  card.gsx on Model  → the .go site AND both .gsx sites
//
// No location may name a generated .x.go, and generation stays in memory.
func TestGoToGsxDefinition(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skips module resolution in -short")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must := func(p, c string) {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	cardSrc := "package x\n\ntype Model struct{ Title string }\n\ncomponent Card(m Model) {\n\t<div>{ m.Title }</div>\n}\n"
	must("card.gsx", cardSrc)
	// page.gsx uses Model (and Card) from a second .gsx in the same package.
	must("page.gsx", "package x\n\ncomponent Page() {\n\t<main><Card m={ Model{} }/></main>\n}\n")
	mainSrc := "package x\n\nfunc use() { _ = Card; _ = Model{} }\n"
	must("main.go", mainSrc)
	goURI := "file://" + filepath.Join(dir, "main.go")
	cardURI := "file://" + filepath.Join(dir, "card.gsx")

	// cursorOn returns the (line, character) of the first occurrence of needle
	// in src, offset by skip bytes into the match.
	cursorOn := func(src, needle string, skip int) (int, int) {
		t.Helper()
		for i, l := range strings.Split(src, "\n") {
			if c := strings.Index(l, needle); c >= 0 {
				return i, c + skip
			}
		}
		t.Fatalf("needle %q not found", needle)
		return 0, 0
	}
	cardLine, cardChar := cursorOn(mainSrc, "_ = Card", 4)      // the 'C'
	modelLine, modelChar := cursorOn(mainSrc, "_ = Model{}", 4) // the 'M'
	declLine, declChar := cursorOn(cardSrc, "type Model", len("type "))

	frame := func(v any) string {
		b, _ := json.Marshal(v)
		return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
	}
	in := frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": goURI, "version": 1, "text": mainSrc, "languageId": "go"}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": cardURI, "version": 1, "text": cardSrc, "languageId": "gsx"}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/definition",
		"params": map[string]any{"textDocument": map[string]any{"uri": goURI},
			"position": map[string]any{"line": cardLine, "character": cardChar}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "textDocument/definition",
		"params": map[string]any{"textDocument": map[string]any{"uri": goURI},
			"position": map[string]any{"line": modelLine, "character": modelChar}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "id": 4, "method": "textDocument/references",
		"params": map[string]any{"textDocument": map[string]any{"uri": cardURI},
			"position": map[string]any{"line": declLine, "character": declChar},
			"context":  map[string]any{"includeDeclaration": true}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	s := out.String()

	componentDef := lspLocationPaths(t, dir, lspTestResponse(t, s, 2).Result)
	if len(componentDef) != 1 || componentDef[0] != "card.gsx" {
		t.Errorf("gd from main.go on the component Card = %v, want [card.gsx]", componentDef)
	}
	typeDef := lspLocationPaths(t, dir, lspTestResponse(t, s, 3).Result)
	if len(typeDef) != 1 || typeDef[0] != "card.gsx" {
		t.Errorf("gd from main.go on the .gsx-declared type Model = %v, want [card.gsx]", typeDef)
	}
	typeRefs := lspLocationPaths(t, dir, lspTestResponse(t, s, 4).Result)
	for _, want := range []string{"card.gsx", "main.go", "page.gsx"} {
		if !slices.Contains(typeRefs, want) {
			t.Errorf("gr on the .gsx-declared type Model missing %s; got %v", want, typeRefs)
		}
	}

	if strings.Contains(s, ".x.go") {
		t.Fatalf("leaked a generated-code location; out:\n%s", s)
	}
	// no .x.go written to disk (in-memory only)
	if _, err := os.Stat(filepath.Join(dir, "card.x.go")); !os.IsNotExist(err) {
		t.Fatalf("card.x.go must NOT be written to disk")
	}
}
