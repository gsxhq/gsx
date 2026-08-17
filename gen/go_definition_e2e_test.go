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
	must("card.gsx", "package x\n\ncomponent Card(title string) {\n\t<div>{ title }</div>\n}\n")
	mainSrc := "package x\n\nfunc use() { _ = Card }\n"
	must("main.go", mainSrc)
	goURI := "file://" + filepath.Join(dir, "main.go")

	// cursor on `Card` in main.go
	lines := strings.Split(mainSrc, "\n")
	var line, ch int
	for i, l := range lines {
		if c := strings.Index(l, "_ = Card"); c >= 0 {
			line, ch = i, c+4 // the 'C'
			break
		}
	}

	frame := func(v any) string {
		b, _ := json.Marshal(v)
		return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
	}
	in := frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": goURI, "version": 1, "text": mainSrc, "languageId": "go"}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/definition",
		"params": map[string]any{"textDocument": map[string]any{"uri": goURI},
			"position": map[string]any{"line": line, "character": ch}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "card.gsx") {
		t.Fatalf("definition from main.go did not resolve to card.gsx; out:\n%s", s)
	}
	if strings.Contains(s, ".x.go") {
		t.Fatalf("leaked a generated-code location; out:\n%s", s)
	}
	// no .x.go written to disk (in-memory only)
	if _, err := os.Stat(filepath.Join(dir, "card.x.go")); !os.IsNotExist(err) {
		t.Fatalf("card.x.go must NOT be written to disk")
	}
	_ = lsp.Package{}
}
