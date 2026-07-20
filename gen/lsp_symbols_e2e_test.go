package gen

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/lsp"
)

func TestDocumentSymbolsAuthoredSourceE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) string {
		t.Helper()
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pagePath := write("page/page.gsx", "package page\n\ncomponent Saved() { <p/> }\n")
	const source = `package page

// 😀 unsaved buffer shifts UTF-16 columns and is authoritative.
type Model struct{ N int }
type Reader interface{ Read() }

const (
	First = 1
	Second = 2
)

var /*😀*/ Third int

func helper() {}

func (m *Model) Size() int { return m.N }

func nested() any {
	node := <div>{"😀"}</div>
	return node
}

component Card() {
	<section/>
}
`
	uri := "file://" + pagePath
	input := frameMsg(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	input += frameMsg(t, map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": source}},
	})
	input += frameMsg(t, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "textDocument/documentSymbol",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri}},
	})
	input += frameMsg(t, map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var output, stderr bytes.Buffer
	if code := runLSP(strings.NewReader(input), &output, &stderr, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, stderr.String())
	}
	symbols := documentSymbolResult(t, output.String(), 2)
	wantNames := []string{"Model", "Reader", "First", "Second", "Third", "helper", "Size", "nested", "Card"}
	if len(symbols) != len(wantNames) {
		t.Fatalf("document symbols = %+v, want names %v", symbols, wantNames)
	}
	lineSpan := func(prefix string) [2]int {
		start := strings.Index(source, prefix)
		return [2]int{start, start + strings.IndexByte(source[start:], '\n')}
	}
	constStart := strings.Index(source, "const (")
	constEnd := strings.Index(source, "\n)\n\nvar") + len("\n)")
	nestedStart := strings.Index(source, "func nested")
	nestedEnd := strings.Index(source[nestedStart:], "\n}\n\ncomponent") + nestedStart + len("\n}")
	cardStart := strings.Index(source, "component Card")
	cardEnd := strings.LastIndex(source, "}") + len("}")
	declarationSpans := map[string][2]int{
		"Model":  lineSpan("type Model"),
		"Reader": lineSpan("type Reader"),
		"First":  {constStart, constEnd},
		"Second": {constStart, constEnd},
		"Third":  lineSpan("var /*😀*/ Third"),
		"helper": lineSpan("func helper"),
		"Size":   lineSpan("func (m *Model) Size"),
		"nested": {nestedStart, nestedEnd},
		"Card":   {cardStart, cardEnd},
	}
	for i, wantName := range wantNames {
		if symbols[i].Name != wantName {
			t.Fatalf("symbol[%d].Name = %q, want %q; all=%+v", i, symbols[i].Name, wantName, symbols)
		}
		nameStart := strings.Index(source, wantName)
		wantSelection := lsp.Range{
			Start: lspUTF16PositionAt(source, nameStart),
			End:   lspUTF16PositionAt(source, nameStart+len(wantName)),
		}
		if symbols[i].SelectionRange != wantSelection {
			t.Errorf("%s selection = %+v, want %+v", wantName, symbols[i].SelectionRange, wantSelection)
		}
		span := declarationSpans[wantName]
		wantRange := lsp.Range{Start: lspUTF16PositionAt(source, span[0]), End: lspUTF16PositionAt(source, span[1])}
		if symbols[i].Range != wantRange {
			t.Errorf("%s range = %+v, want %+v", wantName, symbols[i].Range, wantRange)
		}
	}
	if symbols[0].Kind != 23 || symbols[7].Kind != 12 {
		t.Errorf("semantic symbol kinds = Model:%d nested:%d, want struct/function", symbols[0].Kind, symbols[7].Kind)
	}
	cardCount := 0
	for _, symbol := range symbols {
		if symbol.Name == "Card" {
			cardCount++
		}
		if symbol.Name == "Saved" {
			t.Errorf("document symbols used saved disk source: %+v", symbols)
		}
	}
	if cardCount != 1 {
		t.Fatalf("Card symbol count = %d, want one AST-owned component", cardCount)
	}

	analyzer := newLSPAnalyzer(config{}, io.Discard)
	if _, err := analyzer.SetOverride(pagePath, []byte(source)); err != nil {
		t.Fatal(err)
	}
	moduleSymbols, err := analyzer.ModuleSymbols(filepath.Dir(pagePath), map[string][]byte{pagePath: []byte(source)})
	if err != nil {
		t.Fatal(err)
	}
	containers := map[string]string{}
	counts := map[string]int{}
	for _, symbol := range moduleSymbols {
		containers[symbol.Name] = symbol.Container
		counts[symbol.Name]++
	}
	for name, want := range map[string]string{"Model": "page", "Size": "Model", "nested": "page", "Card": "page"} {
		if containers[name] != want {
			t.Errorf("ModuleSymbols %s container = %q, want %q; all=%+v", name, containers[name], want, moduleSymbols)
		}
	}
	if counts["Card"] != 1 {
		t.Errorf("ModuleSymbols Card count = %d, want one", counts["Card"])
	}
	if counts["nested"] != 1 {
		t.Errorf("ModuleSymbols nested count = %d, want one", counts["nested"])
	}

	generated := strings.TrimSuffix(pagePath, ".gsx") + ".x.go"
	if _, err := os.Stat(generated); !os.IsNotExist(err) {
		t.Fatalf("physical generated file exists: %s", generated)
	}
}

func documentSymbolResult(t *testing.T, output string, id int) []lsp.DocumentSymbol {
	t.Helper()
	for part := range strings.SplitSeq(output, "Content-Length:") {
		_, body, ok := strings.Cut(part, "\r\n\r\n")
		if !ok {
			continue
		}
		var response struct {
			ID     int                  `json:"id"`
			Result []lsp.DocumentSymbol `json:"result"`
		}
		if err := json.Unmarshal([]byte(body), &response); err != nil || response.ID != id {
			continue
		}
		return response.Result
	}
	t.Fatalf("no document symbol response for id %d in:\n%s", id, output)
	return nil
}
