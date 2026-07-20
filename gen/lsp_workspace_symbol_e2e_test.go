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

	t.Run("open-only GSX directories", func(t *testing.T) {
		if testing.Short() {
			t.Skip("skipping module-resolution test in -short mode")
		}
		root := t.TempDir()
		repoRoot, err := filepath.Abs("..")
		if err != nil {
			t.Fatal(err)
		}
		write := func(name, source string) string {
			t.Helper()
			path := filepath.Join(root, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
				t.Fatal(err)
			}
			return path
		}
		write("go.mod", "module example.test/openonly\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
		const poison = "not valid Go\x00\xff"
		alphaPoison := write("alpha/page.x.go", poison)
		betaPoison := write("beta/view.x.go", poison)
		const handwritten = "package alpha\n\nfunc Handwritten() string { return \"hand\" }\n"
		const ordinary = "package alpha\n\nfunc Ordinary() string { return \"ordinary\" }\n"
		handwrittenPath := write("alpha/hand.x.go", handwritten)
		ordinaryPath := write("alpha/ordinary.go", ordinary)

		alphaPath := filepath.Join(root, "alpha/page.gsx")
		const alphaSource = `package alpha

var /*😀*/ AstralTarget = 1

func helper() string { return "ok" }

component Page() {
	<p>{helper()} {Handwritten()} {Ordinary()} {AstralTarget}</p>
}
`
		betaPath := filepath.Join(root, "beta/view.gsx")
		const betaSource = `package beta

var /*😀*/ BetaTarget = 1

component Panel() { <aside>{BetaTarget}</aside> }
`
		diskSavedPath := write("saved/saved.gsx", "package saved\n\ncomponent SavedOnly() { <p/> }\n")
		const openSaved = "package saved\n\ncomponent OpenSaved() { <p/> }\n"

		alphaURI, betaURI, savedURI := lspTestPathURI(alphaPath), lspTestPathURI(betaPath), lspTestPathURI(diskSavedPath)
		frames := []string{frameMsg(t, map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "initialize",
			"params": map[string]any{"rootUri": lspTestPathURI(root), "capabilities": map[string]any{}},
		})}
		for _, document := range []struct {
			uri, source string
		}{{alphaURI, alphaSource}, {betaURI, betaSource}, {savedURI, openSaved}} {
			frames = append(frames, frameMsg(t, map[string]any{
				"jsonrpc": "2.0", "method": "textDocument/didOpen",
				"params": map[string]any{"textDocument": map[string]any{"uri": document.uri, "version": 1, "text": document.source}},
			}))
		}
		handwrittenUse := strings.Index(alphaSource, "Handwritten()")
		ordinaryUse := strings.Index(alphaSource, "Ordinary()")
		frames = append(frames,
			lspPositionRequestFrame(t, 2, "textDocument/definition", alphaURI, alphaSource, handwrittenUse),
			lspPositionRequestFrame(t, 3, "textDocument/hover", alphaURI, alphaSource, handwrittenUse),
			lspPositionRequestFrame(t, 4, "textDocument/definition", alphaURI, alphaSource, ordinaryUse),
			lspPositionRequestFrame(t, 5, "textDocument/hover", alphaURI, alphaSource, ordinaryUse),
			frameMsg(t, map[string]any{
				"jsonrpc": "2.0", "id": 6, "method": "textDocument/documentSymbol",
				"params": map[string]any{"textDocument": map[string]any{"uri": alphaURI}},
			}),
		)
		for _, id := range []int{7, 8} {
			frames = append(frames, frameMsg(t, map[string]any{
				"jsonrpc": "2.0", "id": id, "method": "workspace/symbol", "params": map[string]any{"query": ""},
			}))
		}
		frames = append(frames,
			frameMsg(t, map[string]any{
				"jsonrpc": "2.0", "method": "textDocument/didClose",
				"params": map[string]any{"textDocument": map[string]any{"uri": alphaURI}},
			}),
			frameMsg(t, map[string]any{
				"jsonrpc": "2.0", "id": 9, "method": "workspace/symbol", "params": map[string]any{"query": ""},
			}),
			frameMsg(t, map[string]any{"jsonrpc": "2.0", "method": "exit"}),
		)

		var output, stderr bytes.Buffer
		if code := runLSP(strings.NewReader(strings.Join(frames, "")), &output, &stderr, config{}, nil); code != 0 {
			t.Fatalf("runLSP=%d stderr=%s", code, stderr.String())
		}
		for _, probe := range []struct {
			definitionID, hoverID int
			use                   int
			name, path, source    string
		}{
			{2, 3, handwrittenUse, "Handwritten", handwrittenPath, handwritten},
			{4, 5, ordinaryUse, "Ordinary", ordinaryPath, ordinary},
		} {
			definitions := definitionLocationList(t, output.String(), probe.definitionID)
			decl := strings.Index(probe.source, probe.name)
			wantDefinition := lsp.Location{URI: lspTestPathURI(probe.path), Range: sourceRange(probe.source, decl, decl+len(probe.name))}
			if len(definitions) != 1 || definitions[0] != wantDefinition {
				t.Fatalf("%s definition = %+v, want %+v", probe.name, definitions, wantDefinition)
			}
			assertHoverResult(t, lspTestResponse(t, output.String(), probe.hoverID).Result, "func "+probe.name+"() string", "", alphaSource, probe.use)
		}
		assertContainsDocumentSymbol(t, lspTestResponse(t, output.String(), 6).Result, alphaSource, "Page", "component Page", 12)

		first := lspTestResponse(t, output.String(), 7).Result
		if repeated := lspTestResponse(t, output.String(), 8).Result; !bytes.Equal(first, repeated) {
			t.Fatalf("open-only workspace ordering changed:\nfirst=%s\nsecond=%s", first, repeated)
		}
		var symbols []lsp.SymbolInformation
		if err := json.Unmarshal(first, &symbols); err != nil {
			t.Fatal(err)
		}
		wantNames := []string{"AstralTarget", "BetaTarget", "OpenSaved", "Page", "Panel", "helper"}
		gotNames := make([]string, len(symbols))
		for index, symbol := range symbols {
			gotNames[index] = symbol.Name
		}
		if !slices.Equal(gotNames, wantNames) {
			t.Fatalf("workspace symbols = %v, want exact deterministic inventory %v; symbols=%+v", gotNames, wantNames, symbols)
		}
		for _, symbol := range symbols {
			if symbol.Name != "AstralTarget" {
				continue
			}
			start := strings.Index(alphaSource, "AstralTarget")
			want := lsp.Location{URI: alphaURI, Range: sourceRange(alphaSource, start, start+len("AstralTarget"))}
			if symbol.Location != want {
				t.Errorf("AstralTarget UTF-16 location = %+v, want %+v", symbol.Location, want)
			}
		}

		var afterClose []lsp.SymbolInformation
		if err := json.Unmarshal(lspTestResponse(t, output.String(), 9).Result, &afterClose); err != nil {
			t.Fatal(err)
		}
		afterCloseNames := make([]string, len(afterClose))
		for index, symbol := range afterClose {
			afterCloseNames[index] = symbol.Name
		}
		if want := []string{"BetaTarget", "OpenSaved", "Panel"}; !slices.Equal(afterCloseNames, want) {
			t.Errorf("workspace symbols after close = %v, want exact remaining inventory %v; symbols=%+v", afterCloseNames, want, afterClose)
		}
		for _, path := range []string{alphaPoison, betaPoison} {
			contents, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(contents, []byte(poison)) {
				t.Errorf("paired poison mutated: %s = %q", path, contents)
			}
		}
	})
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
