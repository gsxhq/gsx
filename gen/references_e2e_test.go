package gen

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestReferences: references on Card (from its card.gsx declaration) returns the
// main.go call site AND the page.gsx <Card/> tag.
func TestReferences(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must := func(p, c string) {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	cardSrc := "package x\n\ncomponent Card(title string) {\n\t<div>{ title }</div>\n}\n"
	must("card.gsx", cardSrc)
	must("page.gsx", "package x\n\ncomponent Page() {\n\t<main><Card title=\"hi\"/></main>\n}\n")
	must("main.go", "package x\n\nfunc use() { _ = Card }\n")
	cardURI := "file://" + filepath.Join(dir, "card.gsx")

	lines := strings.Split(cardSrc, "\n")
	var line, ch int
	for i, l := range lines {
		if c := strings.Index(l, "component Card"); c >= 0 {
			line, ch = i, c+len("component ")
			break
		}
	}

	frame := func(v any) string { b, _ := json.Marshal(v); return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b) }
	in := frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": cardURI, "version": 1, "text": cardSrc}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/references",
		"params": map[string]any{"textDocument": map[string]any{"uri": cardURI},
			"position": map[string]any{"line": line, "character": ch},
			"context": map[string]any{"includeDeclaration": false}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "main.go") {
		t.Errorf("references missing main.go; out:\n%s", s)
	}
	if !strings.Contains(s, "page.gsx") {
		t.Errorf("references missing page.gsx; out:\n%s", s)
	}
}
