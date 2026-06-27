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
	t.Parallel()
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

	frame := func(v any) string {
		b, _ := json.Marshal(v)
		return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
	}
	in := frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": cardURI, "version": 1, "text": cardSrc}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/references",
		"params": map[string]any{"textDocument": map[string]any{"uri": cardURI},
			"position": map[string]any{"line": line, "character": ch},
			"context":  map[string]any{"includeDeclaration": false}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
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

// refSetup writes the shared 3-file gsx package (Card declared, used from main.go
// and as <Card/> in page.gsx) and returns the dir.
func refSetup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must := func(p, c string) {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("card.gsx", "package x\n\ncomponent Card(title string) {\n\t<div>{ title }</div>\n}\n")
	must("page.gsx", "package x\n\ncomponent Page() {\n\t<main><Card title=\"hi\"/></main>\n}\n")
	must("main.go", "package x\n\nfunc use() { _ = Card }\n")
	return dir
}

func refFrame(v any) string {
	b, _ := json.Marshal(v)
	return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
}

// TestReferencesFromGoCursor: references invoked from a .go call site (cursor on
// Card in main.go) returns both the .go and .gsx use sites.
func TestReferencesFromGoCursor(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip()
	}
	dir := refSetup(t)
	mainSrc := "package x\n\nfunc use() { _ = Card }\n"
	uri := "file://" + filepath.Join(dir, "main.go")
	line, ch := 2, strings.Index(strings.Split(mainSrc, "\n")[2], "_ = Card")+4 // the 'C'
	in := refFrame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += refFrame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": mainSrc}}})
	in += refFrame(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/references",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri},
			"position": map[string]any{"line": line, "character": ch}, "context": map[string]any{"includeDeclaration": false}}})
	in += refFrame(map[string]any{"jsonrpc": "2.0", "method": "exit"})
	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "main.go") || !strings.Contains(s, "page.gsx") {
		t.Fatalf("references from .go cursor missing a site; out:\n%s", s)
	}
}

// TestReferencesTagCursorEmpty documents the deferred case: references from a
// .gsx <Card/> TAG cursor returns empty (component-tag resolution is a follow-up;
// invoke references from the declaration or a .go site instead).
func TestReferencesTagCursorEmpty(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip()
	}
	dir := refSetup(t)
	pageSrc := "package x\n\ncomponent Page() {\n\t<main><Card title=\"hi\"/></main>\n}\n"
	uri := "file://" + filepath.Join(dir, "page.gsx")
	line, ch := 3, strings.Index(strings.Split(pageSrc, "\n")[3], "<Card")+1 // the 'C' of the tag
	in := refFrame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += refFrame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": pageSrc}}})
	in += refFrame(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/references",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri},
			"position": map[string]any{"line": line, "character": ch}, "context": map[string]any{"includeDeclaration": false}}})
	in += refFrame(map[string]any{"jsonrpc": "2.0", "method": "exit"})
	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), `"id":2,"result":[]`) {
		t.Fatalf("references from a .gsx tag cursor should be empty (deferred); out:\n%s", out.String())
	}
}

// TestGoScopingNonGsxPackage: a .go file in a package with NO .gsx files — gsx-LSP
// must return null (it defers entirely to gopls).
func TestGoScopingNonGsxPackage(t *testing.T) {
	t.Parallel()
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
	mainSrc := "package x\n\ntype Foo struct{}\n\nfunc use() { _ = Foo{} }\n"
	must("main.go", mainSrc) // NO .gsx in this package
	uri := "file://" + filepath.Join(dir, "main.go")
	line, ch := 4, strings.Index(strings.Split(mainSrc, "\n")[4], "_ = Foo")+4
	in := refFrame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += refFrame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": mainSrc}}})
	in += refFrame(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/definition",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri},
			"position": map[string]any{"line": line, "character": ch}}})
	in += refFrame(map[string]any{"jsonrpc": "2.0", "method": "exit"})
	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), `"id":2,"result":null`) {
		t.Fatalf("definition in a non-gsx package should be null (gopls owns it); out:\n%s", out.String())
	}
}
