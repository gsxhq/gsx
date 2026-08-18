package gen

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/lsp"
)

// crossPkgModule writes a three-package module fixture:
//   - components/input.gsx declares component Input(name string)
//   - post.gsx uses <components.Input name="a"/>
//   - use.go calls components.Input directly
//   - cmd/main.go is a Go-ONLY package (no .gsx at all) that imports
//     components — the reverse-dependency case: nothing in package main is
//     gsx, so it only reaches the symbol graph through the module-wide
//     reverse-dependency walk.
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
	must("cmd/main.go", "package main\n\nimport \"example.com/x/components\"\n\nfunc main() { _ = components.Input }\n")
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
	t.Parallel()
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
// the result includes the cross-package .gsx reference site only — gopls owns
// .go->.go, so use.go's own reference site is filtered out.
func TestReferencesCrossPkgFromGoCursor(t *testing.T) {
	t.Parallel()
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
	if strings.Contains(s, "use.go") {
		t.Errorf("cross-pkg references from .go cursor returned a .go location (gopls owns .go->.go); out:\n%s", s)
	}
}

// lspLocationPaths decodes a definition/references result — null, a single
// Location, or a []Location — into the file paths it names, relative to root.
// It fails the test if any location points at generated code (a .x.go file):
// the whole point of the module symbol graph is authored-source answers.
func lspLocationPaths(t *testing.T, root string, result json.RawMessage) []string {
	t.Helper()
	if len(result) == 0 || string(result) == "null" {
		return nil
	}
	var locations []lsp.Location
	if err := json.Unmarshal(result, &locations); err != nil {
		var single lsp.Location
		if err2 := json.Unmarshal(result, &single); err2 != nil {
			t.Fatalf("result is neither Location nor []Location: %s (%v / %v)", result, err, err2)
		}
		locations = []lsp.Location{single}
	}
	paths := make([]string, 0, len(locations))
	for _, l := range locations {
		path := strings.TrimPrefix(l.URI, "file://")
		if strings.HasSuffix(path, ".x.go") {
			t.Errorf("location leaked generated code: %s", path)
		}
		if rel, err := filepath.Rel(root, path); err == nil {
			path = filepath.ToSlash(rel)
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// TestCrossPkgFromGoOnlyPackage drives references AND definition from a cursor
// in cmd/main.go — a package with NO .gsx files of its own that imports the gsx
// package `components`. Both requests ride ONE runLSP session (one Module open).
//
//   - `gr` on `Input` must list every site in the module: the components/input.gsx
//     declaration, the post.gsx <components.Input/> tag, use.go, and cmd/main.go.
//   - `gd` on the same cursor must resolve to components/input.gsx, never to the
//     generated components/input.x.go.
func TestCrossPkgFromGoOnlyPackage(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip()
	}
	root := crossPkgModule(t)
	mainPath := filepath.Join(root, "cmd", "main.go")
	mainSrc, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	mainURI := "file://" + mainPath

	var line, character int
	for i, l := range strings.Split(string(mainSrc), "\n") {
		if c := strings.Index(l, "components.Input"); c >= 0 {
			line, character = i, c+len("components.") // the 'I' of Input
			break
		}
	}

	in := lspFrame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += lspFrame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{
			"uri": mainURI, "version": 1, "text": string(mainSrc), "languageId": "go"}}})
	in += lspFrame(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/references",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": mainURI},
			"position":     map[string]any{"line": line, "character": character},
			"context":      map[string]any{"includeDeclaration": true},
		}})
	in += lspFrame(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "textDocument/definition",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": mainURI},
			"position":     map[string]any{"line": line, "character": character},
		}})
	in += lspFrame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	stream := out.String()

	// Cursor is in cmd/main.go: gopls owns .go->.go, so the reply carries the
	// .gsx sites only — use.go and cmd/main.go's own reference are filtered.
	refs := lspLocationPaths(t, root, lspTestResponse(t, stream, 2).Result)
	for _, want := range []string{"components/input.gsx", "post.gsx"} {
		if !slices.Contains(refs, want) {
			t.Errorf("references from the Go-only cmd package missing %s; got %v", want, refs)
		}
	}
	for _, unwanted := range []string{"use.go", "cmd/main.go"} {
		if slices.Contains(refs, unwanted) {
			t.Errorf("references from the Go-only cmd package returned a .go location %s (gopls owns .go->.go); got %v", unwanted, refs)
		}
	}

	defs := lspLocationPaths(t, root, lspTestResponse(t, stream, 3).Result)
	if len(defs) != 1 || defs[0] != "components/input.gsx" {
		t.Errorf("definition from the Go-only cmd package = %v, want [components/input.gsx]", defs)
	}
}
