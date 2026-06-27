package gen

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/lsp"
)

// setupAPINavModule creates a temp module with three files:
//
//	card.gsx  — component Card(title string){ <div>{ title }</div> }
//	page.gsx  — component Page(){ <main><Card title="hi"/></main> }
//	render.go — var _ = Card(CardProps{Title: "x"})
//
// It returns the dir, the card.gsx source, and the page.gsx source.
func setupAPINavModule(t *testing.T) (dir, cardSrc, pageSrc, renderSrc string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir = t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must := func(p, c string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/nav\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	cardSrc = "package nav\n\ncomponent Card(title string) {\n\t<div>{ title }</div>\n}\n"
	pageSrc = "package nav\n\ncomponent Page() {\n\t<main><Card title=\"hi\"/></main>\n}\n"
	renderSrc = "package nav\n\nvar _ = Card(CardProps{Title: \"x\"})\n"
	must("card.gsx", cardSrc)
	must("page.gsx", pageSrc)
	must("render.go", renderSrc)
	return dir, cardSrc, pageSrc, renderSrc
}

// lspRequest builds a JSON-RPC frame string.
func lspRequest(v any) string {
	b, _ := json.Marshal(v)
	return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
}

// TestAPINavCardProps: gd on `CardProps` in render.go → resolves to the START OF
// THE ARGUMENT LIST in card.gsx (the props ARE the params, so the props struct
// lands on the param list rather than the component name).
func TestAPINavCardProps(t *testing.T) {
	t.Parallel()
	dir, cardSrc, _, renderSrc := setupAPINavModule(t)
	renderURI := "file://" + filepath.Join(dir, "render.go")

	// Find cursor position on `CardProps` in render.go.
	lines := strings.Split(renderSrc, "\n")
	var line, ch int
	for i, l := range lines {
		if c := strings.Index(l, "CardProps"); c >= 0 {
			line, ch = i, c // on the 'C'
			break
		}
	}

	in := lspRequest(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += lspRequest(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": renderURI, "version": 1, "text": renderSrc, "languageId": "go"}}})
	in += lspRequest(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/definition",
		"params": map[string]any{"textDocument": map[string]any{"uri": renderURI},
			"position": map[string]any{"line": line, "character": ch}}})
	in += lspRequest(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	loc := definitionResult(t, out.String(), 2)
	if loc == nil {
		t.Fatalf("CardProps definition returned null; out:\n%s\nstderr:\n%s", out.String(), errBuf.String())
	}
	if !strings.HasSuffix(loc.URI, "card.gsx") {
		t.Fatalf("CardProps resolved to %q, want card.gsx", loc.URI)
	}
	// Exact landing: the start of the argument list — the `title` param in
	// `component Card(title string)`.
	wantLine, wantChar := paramListStart(t, cardSrc)
	if loc.Range.Start.Line != wantLine || loc.Range.Start.Character != wantChar {
		t.Fatalf("CardProps landed at L%d:C%d, want L%d:C%d (start of the argument list)",
			loc.Range.Start.Line, loc.Range.Start.Character, wantLine, wantChar)
	}
}

// paramListStart returns the 0-based (line, character) of the first param's name
// in `component Card(title string)` — the start of the argument list.
func paramListStart(t *testing.T, cardSrc string) (line, character int) {
	t.Helper()
	for i, l := range strings.Split(cardSrc, "\n") {
		if c := strings.Index(l, "(title "); c >= 0 {
			return i, c + len("(")
		}
	}
	t.Fatalf("could not locate the param list in card.gsx:\n%s", cardSrc)
	return -1, -1
}

// TestAPINavTitle: gd on `Title` field in render.go → resolves to card.gsx at the `title` param.
func TestAPINavTitle(t *testing.T) {
	t.Parallel()
	dir, cardSrc, _, renderSrc := setupAPINavModule(t)
	renderURI := "file://" + filepath.Join(dir, "render.go")

	// Find cursor position on `Title` in render.go.
	lines := strings.Split(renderSrc, "\n")
	var line, ch int
	for i, l := range lines {
		if c := strings.Index(l, "Title:"); c >= 0 {
			line, ch = i, c // on the 'T'
			break
		}
	}

	in := lspRequest(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += lspRequest(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": renderURI, "version": 1, "text": renderSrc, "languageId": "go"}}})
	in += lspRequest(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/definition",
		"params": map[string]any{"textDocument": map[string]any{"uri": renderURI},
			"position": map[string]any{"line": line, "character": ch}}})
	in += lspRequest(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	loc := definitionResult(t, out.String(), 2)
	if loc == nil {
		t.Fatalf("Title definition returned null; out:\n%s\nstderr:\n%s", out.String(), errBuf.String())
	}
	if !strings.HasSuffix(loc.URI, "card.gsx") {
		t.Fatalf("Title resolved to %q, want card.gsx", loc.URI)
	}
	// Exact landing: the `title` param in `component Card(title string)`.
	wantLine, wantChar := paramListStart(t, cardSrc)
	if loc.Range.Start.Line != wantLine || loc.Range.Start.Character != wantChar {
		t.Fatalf("Title landed at L%d:C%d, want L%d:C%d (the 'title' param)",
			loc.Range.Start.Line, loc.Range.Start.Character, wantLine, wantChar)
	}
}

// TestAPINavComponentTag: gd on `Card` tag in page.gsx → resolves to card.gsx component declaration.
func TestAPINavComponentTag(t *testing.T) {
	t.Parallel()
	dir, _, pageSrc, _ := setupAPINavModule(t)
	pageURI := "file://" + filepath.Join(dir, "page.gsx")

	// Find cursor position on `Card` in <Card title="hi"/> in page.gsx.
	// The tag name starts right after '<'.
	lines := strings.Split(pageSrc, "\n")
	var line, ch int
	for i, l := range lines {
		if c := strings.Index(l, "<Card"); c >= 0 {
			line, ch = i, c+1 // the 'C' of Card (right after '<')
			break
		}
	}

	in := lspRequest(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += lspRequest(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": pageURI, "version": 1, "text": pageSrc}}})
	in += lspRequest(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/definition",
		"params": map[string]any{"textDocument": map[string]any{"uri": pageURI},
			"position": map[string]any{"line": line, "character": ch}}})
	in += lspRequest(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	loc := definitionResult(t, out.String(), 2)
	if loc == nil {
		t.Fatalf("<Card/> tag definition returned null; out:\n%s\nstderr:\n%s", out.String(), errBuf.String())
	}
	if !strings.HasSuffix(loc.URI, "card.gsx") {
		t.Fatalf("<Card/> tag resolved to %q, want card.gsx", loc.URI)
	}

	_ = lsp.Package{} // keep import
}
