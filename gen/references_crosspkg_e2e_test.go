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

// crossPkgModule writes a two-package module fixture:
//   - components/input.gsx declares component Input(name string)
//   - post.gsx uses <components.Input name="a"/>
//   - use.go calls components.Input directly
//
// Returns the module root (temp dir).
func crossPkgModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must := func(p, c string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("components/input.gsx", "package components\n\ncomponent Input(name string) {\n\t<input name={ name }/>\n}\n")
	must("post.gsx", "package x\n\nimport \"example.com/x/components\"\n\ncomponent Post() {\n\t<main><components.Input name=\"a\"/></main>\n}\n")
	must("use.go", "package x\n\nimport \"example.com/x/components\"\n\nfunc use() { _ = components.Input }\n")
	return root
}

func lspFrame(v any) string {
	b, _ := json.Marshal(v)
	return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
}

// TestReferencesCrossPkgFromDecl invokes textDocument/references with the
// cursor on the Input declaration in components/input.gsx and asserts the
// result includes both the cross-package .gsx tag (post.gsx) and the .go
// direct reference (use.go).
func TestReferencesCrossPkgFromDecl(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	root := crossPkgModule(t)
	inputSrc, err := os.ReadFile(filepath.Join(root, "components", "input.gsx"))
	if err != nil {
		t.Fatal(err)
	}
	inputURI := "file://" + filepath.Join(root, "components", "input.gsx")

	// Cursor on "Input" in "component Input(...)": line 2 (0-based), char 10.
	// "component " is 10 chars, so the name begins at character 10.
	inputLines := strings.Split(string(inputSrc), "\n")
	var line, character int
	for i, l := range inputLines {
		if c := strings.Index(l, "component Input"); c >= 0 {
			line, character = i, c+len("component ")
			break
		}
	}

	in := lspFrame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += lspFrame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": inputURI, "version": 1, "text": string(inputSrc)}}})
	in += lspFrame(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/references",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": inputURI},
			"position":     map[string]any{"line": line, "character": character},
			"context":      map[string]any{"includeDeclaration": false},
		}})
	in += lspFrame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "post.gsx") {
		t.Errorf("cross-pkg references missing post.gsx; out:\n%s", s)
	}
	if !strings.Contains(s, "use.go") {
		t.Errorf("cross-pkg references missing use.go; out:\n%s", s)
	}
}

// TestReferencesCrossPkgFromGoCursor invokes textDocument/references with the
// cursor on the `Input` identifier in use.go (`components.Input`) and asserts
// the result includes both cross-package reference sites.
func TestReferencesCrossPkgFromGoCursor(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	root := crossPkgModule(t)
	useSrc, err := os.ReadFile(filepath.Join(root, "use.go"))
	if err != nil {
		t.Fatal(err)
	}
	useURI := "file://" + filepath.Join(root, "use.go")

	// Cursor on "Input" within "components.Input": find the line, then skip past "components.".
	useLines := strings.Split(string(useSrc), "\n")
	var useLine, useChar int
	for i, l := range useLines {
		if c := strings.Index(l, "components.Input"); c >= 0 {
			useLine, useChar = i, c+len("components.")
			break
		}
	}

	in := lspFrame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += lspFrame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": useURI, "version": 1, "text": string(useSrc)}}})
	in += lspFrame(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/references",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": useURI},
			"position":     map[string]any{"line": useLine, "character": useChar},
			"context":      map[string]any{"includeDeclaration": false},
		}})
	in += lspFrame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "post.gsx") {
		t.Errorf("cross-pkg references from .go cursor missing post.gsx; out:\n%s", s)
	}
	if !strings.Contains(s, "use.go") {
		t.Errorf("cross-pkg references from .go cursor missing use.go; out:\n%s", s)
	}
}
