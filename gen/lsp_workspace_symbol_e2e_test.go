package gen

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/lsp"
)

func TestWorkspaceSymbolsMultiModuleAuthoredSourceE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	workspace := t.TempDir()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, source string) string {
		t.Helper()
		path := filepath.Join(workspace, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	goMod := func(module string) string {
		return "module " + module + "\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => " + repoRoot + "\n"
	}
	write("go.work", "go 1.26.1\n\nuse (\n\t./first\n\t./second\n)\n")
	write("first/go.mod", goMod("example.test/first"))
	write("second/go.mod", goMod("example.test/second"))
	firstPath := write("first/page/page.gsx", "package shared\n\ncomponent SavedOnly() { <div/> }\n")
	const firstOpen = `package shared

var /*😀*/ AstralTarget = 1

func Mixed() any {
	node := <div>{AstralTarget}</div>
	return node
}

component Card() {
	<section/>
}
`
	const secondSource = `package shared

type Model struct{}

func (Model) Size() int { return 0 }

component Card() {
	<article/>
}
`
	secondPath := write("second/view/view.gsx", secondSource)
	firstURI := lspTestPathURI(firstPath)
	var input strings.Builder
	input.WriteString(frameMsg(t, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"rootUri": lspTestPathURI(workspace), "capabilities": map[string]any{}},
	}))
	input.WriteString(frameMsg(t, map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": firstURI, "version": 1, "text": firstOpen}},
	}))
	for id := 2; id <= 4; id++ {
		input.WriteString(frameMsg(t, map[string]any{
			"jsonrpc": "2.0", "id": id, "method": "workspace/symbol", "params": map[string]any{"query": ""},
		}))
	}
	input.WriteString(frameMsg(t, map[string]any{"jsonrpc": "2.0", "method": "exit"}))

	var output, stderr bytes.Buffer
	if code := runLSP(strings.NewReader(input.String()), &output, &stderr, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, stderr.String())
	}
	firstResult := lspTestResponse(t, output.String(), 2).Result
	for _, id := range []int{3, 4} {
		if got := lspTestResponse(t, output.String(), id).Result; !bytes.Equal(got, firstResult) {
			t.Fatalf("workspace symbol result %d is not byte-identical:\nfirst=%s\ngot=%s", id, firstResult, got)
		}
	}

	var symbols []lsp.SymbolInformation
	if err := json.Unmarshal(firstResult, &symbols); err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(symbols))
	for i, symbol := range symbols {
		names[i] = symbol.Name
	}
	for _, want := range []string{"AstralTarget", "Card", "Mixed", "Model", "Size"} {
		if !slices.Contains(names, want) {
			t.Errorf("workspace symbols missing %q: %+v", want, symbols)
		}
	}
	if slices.Contains(names, "SavedOnly") {
		t.Errorf("workspace symbols used stale saved source: %+v", symbols)
	}
	cardCount := 0
	for _, symbol := range symbols {
		if symbol.Name == "Card" {
			cardCount++
		}
		if symbol.Name == "AstralTarget" {
			start := strings.Index(firstOpen, "AstralTarget")
			want := lsp.Range{
				Start: lspUTF16PositionAt(firstOpen, start),
				End:   lspUTF16PositionAt(firstOpen, start+len("AstralTarget")),
			}
			if symbol.Location.URI != firstURI || symbol.Location.Range != want {
				t.Errorf("AstralTarget location = %+v, want %s %+v", symbol.Location, firstURI, want)
			}
			if symbol.ContainerName != "example.test/first/page" {
				t.Errorf("AstralTarget container = %q, want unambiguous module package path", symbol.ContainerName)
			}
		}
		if symbol.Name == "Size" && symbol.ContainerName != "Model" {
			t.Errorf("method container = %q, want receiver identity Model", symbol.ContainerName)
		}
	}
	if cardCount != 2 {
		t.Errorf("Card count = %d, want both modules", cardCount)
	}

	for _, path := range []string{firstPath, secondPath} {
		generated := strings.TrimSuffix(path, ".gsx") + ".x.go"
		if _, err := os.Stat(generated); !os.IsNotExist(err) {
			t.Fatalf("physical generated file exists: %s", generated)
		}
	}
}
