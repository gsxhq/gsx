package gen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/lsp"
)

func TestLSPLocalhostFileURIIdentityE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "space ünicode")
	pagePath := filepath.Join(root, "page", "page.gsx")
	if err := os.MkdirAll(filepath.Dir(pagePath), 0o755); err != nil {
		t.Fatal(err)
	}
	goMod := "module example.test/localuri\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => " + repoRoot + "\n"
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}
	const saved = "package page\n\ncomponent Saved() { <p/> }\n"
	if err := os.WriteFile(pagePath, []byte(saved), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	pagePath, err = filepath.EvalSymlinks(pagePath)
	if err != nil {
		t.Fatal(err)
	}
	const opened = `package page

type Model string

func helper(value Model) Model { return value }

component Live() {
	<p>{helper(Model("x"))}</p>
}
`
	changed := strings.Replace(opened, "component Live", "component Changed", 1)
	canonicalURI := (&url.URL{Scheme: "file", Path: pagePath}).String()
	localhostURI := (&url.URL{Scheme: "file", Host: "LOCALHOST", Path: pagePath}).String()
	closeURI := strings.Replace(localhostURI, "file://LOCALHOST", "FILE://localhost", 1)
	rootURI := (&url.URL{Scheme: "FILE", Host: "LOCALHOST", Path: root}).String()

	frames := []string{frameMsg(t, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"rootUri":          rootURI,
			"workspaceFolders": []map[string]any{{"uri": rootURI, "name": "local"}},
			"capabilities":     map[string]any{},
		},
	})}
	frames = append(frames, frameMsg(t, map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{
			"uri": localhostURI, "version": 1, "text": opened,
		}},
	}))
	helperCall := strings.Index(opened, "helper(Model")
	frames = append(frames,
		lspPositionRequestFrame(t, 2, "textDocument/definition", localhostURI, opened, helperCall),
		lspPositionRequestFrame(t, 3, "textDocument/hover", localhostURI, opened, helperCall),
		frameMsg(t, map[string]any{
			"jsonrpc": "2.0", "id": 4, "method": "textDocument/documentSymbol",
			"params": map[string]any{"textDocument": map[string]any{"uri": localhostURI}},
		}),
		frameMsg(t, map[string]any{
			"jsonrpc": "2.0", "id": 5, "method": "workspace/symbol",
			"params": map[string]any{"query": "Live"},
		}),
		frameMsg(t, map[string]any{
			"jsonrpc": "2.0", "method": "textDocument/didChange",
			"params": map[string]any{
				"textDocument":   map[string]any{"uri": canonicalURI, "version": 2},
				"contentChanges": []map[string]any{{"text": changed}},
			},
		}),
		frameMsg(t, map[string]any{
			"jsonrpc": "2.0", "id": 6, "method": "workspace/symbol",
			"params": map[string]any{"query": "Changed"},
		}),
		frameMsg(t, map[string]any{
			"jsonrpc": "2.0", "method": "textDocument/didClose",
			"params": map[string]any{"textDocument": map[string]any{"uri": closeURI}},
		}),
		frameMsg(t, map[string]any{
			"jsonrpc": "2.0", "id": 7, "method": "workspace/symbol",
			"params": map[string]any{"query": "Saved"},
		}),
		frameMsg(t, map[string]any{"jsonrpc": "2.0", "method": "exit"}),
	)

	var output, stderr bytes.Buffer
	if code := runLSP(strings.NewReader(strings.Join(frames, "")), &output, &stderr, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, stderr.String())
	}
	stream := output.String()
	assertLocalURIDiagnostics(t, stream, canonicalURI)

	var definition lsp.Location
	decodeLSPTestResult(t, stream, 2, &definition)
	helperDeclaration := strings.Index(opened, "helper(value")
	wantDefinitionRange := sourceRange(opened, helperDeclaration, helperDeclaration+len("helper"))
	if definition != (lsp.Location{URI: canonicalURI, Range: wantDefinitionRange}) {
		t.Fatalf("definition = %+v, want canonical helper location %+v; output:\n%s", definition, wantDefinitionRange, stream)
	}

	var hover lsp.Hover
	decodeLSPTestResult(t, stream, 3, &hover)
	wantHoverRange := sourceRange(opened, helperCall, helperCall+len("helper"))
	wantHoverContents := lsp.MarkupContent{Kind: "markdown", Value: "```go\nfunc helper(value Model) Model\n```"}
	if hover.Range == nil || *hover.Range != wantHoverRange || hover.Contents != wantHoverContents {
		t.Fatalf("hover = %+v, want exact contents %+v and range %+v", hover, wantHoverContents, wantHoverRange)
	}

	var documentSymbols []lsp.DocumentSymbol
	decodeLSPTestResult(t, stream, 4, &documentSymbols)
	wantDocumentSymbols := []lsp.DocumentSymbol{
		exactDocumentSymbol(opened, "Model", 5, "type Model string"),
		exactDocumentSymbol(opened, "helper", 12, "func helper(value Model) Model { return value }"),
		exactDocumentSymbol(opened, "Live", 12, "component Live() {\n\t<p>{helper(Model(\"x\"))}</p>\n}"),
	}
	if !slices.Equal(documentSymbols, wantDocumentSymbols) {
		t.Fatalf("document symbols = %+v, want exact %+v", documentSymbols, wantDocumentSymbols)
	}

	assertExactWorkspaceSymbol(t, stream, 5, "Live", canonicalURI, opened, "page")
	assertExactWorkspaceSymbol(t, stream, 6, "Changed", canonicalURI, changed, "page")
	assertExactWorkspaceSymbol(t, stream, 7, "Saved", canonicalURI, saved, "page")
	for _, message := range lspTestMessages(t, stream) {
		if message.Method == "textDocument/publishDiagnostics" {
			var params struct {
				URI string `json:"uri"`
			}
			if err := json.Unmarshal(message.Params, &params); err != nil {
				t.Fatal(err)
			}
			if params.URI != canonicalURI {
				t.Fatalf("diagnostic URI = %q, want canonical identity %q", params.URI, canonicalURI)
			}
		}
	}
}

func exactDocumentSymbol(source, name string, kind int, declaration string) lsp.DocumentSymbol {
	declarationStart := strings.Index(source, declaration)
	nameStart := declarationStart + strings.Index(declaration, name)
	return lsp.DocumentSymbol{
		Name:           name,
		Kind:           kind,
		Range:          sourceRange(source, declarationStart, declarationStart+len(declaration)),
		SelectionRange: sourceRange(source, nameStart, nameStart+len(name)),
	}
}

type lspTestMessage struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func lspTestMessages(t *testing.T, stream string) []lspTestMessage {
	t.Helper()
	var messages []lspTestMessage
	for len(stream) != 0 {
		headerEnd := strings.Index(stream, "\r\n\r\n")
		if headerEnd < 0 {
			break
		}
		var length int
		if _, err := fmt.Sscanf(stream[:headerEnd], "Content-Length: %d", &length); err != nil {
			t.Fatal(err)
		}
		bodyStart := headerEnd + 4
		if bodyStart+length > len(stream) {
			t.Fatal("truncated LSP frame")
		}
		var message lspTestMessage
		if err := json.Unmarshal([]byte(stream[bodyStart:bodyStart+length]), &message); err != nil {
			t.Fatal(err)
		}
		messages = append(messages, message)
		stream = stream[bodyStart+length:]
	}
	return messages
}

func decodeLSPTestResult(t *testing.T, stream string, id int, target any) {
	t.Helper()
	response := lspTestResponse(t, stream, id)
	if len(response.Error) != 0 && !bytes.Equal(response.Error, []byte("null")) {
		t.Fatalf("response %d error: %s", id, response.Error)
	}
	if err := json.Unmarshal(response.Result, target); err != nil {
		t.Fatalf("decode response %d: %v: %s", id, err, response.Result)
	}
}

func assertLocalURIDiagnostics(t *testing.T, stream, wantURI string) {
	t.Helper()
	for _, message := range lspTestMessages(t, stream) {
		if message.Method != "textDocument/publishDiagnostics" {
			continue
		}
		var params struct {
			URI         string           `json:"uri"`
			Diagnostics []lsp.Diagnostic `json:"diagnostics"`
		}
		if err := json.Unmarshal(message.Params, &params); err != nil {
			t.Fatal(err)
		}
		if params.URI == wantURI && params.Diagnostics != nil {
			return
		}
	}
	t.Fatalf("no canonical diagnostic publication for %q in:\n%s", wantURI, stream)
}

func assertExactWorkspaceSymbol(t *testing.T, stream string, id int, name, uri, source, container string) {
	t.Helper()
	var symbols []lsp.SymbolInformation
	decodeLSPTestResult(t, stream, id, &symbols)
	if len(symbols) != 1 {
		t.Fatalf("workspace symbols %d = %+v, want one %s", id, symbols, name)
	}
	start := strings.Index(source, name)
	want := lsp.SymbolInformation{
		Name:          name,
		Kind:          12,
		ContainerName: container,
		Location:      lsp.Location{URI: uri, Range: sourceRange(source, start, start+len(name))},
	}
	if symbols[0] != want {
		t.Fatalf("workspace symbol %d = %+v, want %+v", id, symbols[0], want)
	}
}
