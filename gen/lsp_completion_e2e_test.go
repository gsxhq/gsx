package gen

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/lsp"
)

// newCompletionE2EFixture builds a temp module with a page package (User{Name
// string} in types.go, a page.gsx opened via SetOverride with a valid { user.Name
// } body) and returns the warm lspAnalyzer alongside the package dir and the
// page.gsx absolute path. It mirrors the hover e2e fixture's temp-module
// scaffolding (gen/lsp_hover_e2e_test.go) but drives the analyzer directly
// rather than through the JSON-RPC framing, since completion tasks call
// Analyzer methods straight. Grows through later completion tasks.
func newCompletionE2EFixture(t *testing.T) (a lspAnalyzer, dir, pagePath string) {
	t.Helper()
	root := t.TempDir()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) string {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	write("page/types.go", "package page\n\ntype User struct{ Name string }\n")
	pagePath = write("page/page.gsx", "package page\n\ncomponent Home(user User) {\n\t<div>{ user.Name }</div>\n}\n")

	a = newLSPAnalyzer(config{}, io.Discard)
	if _, err := a.SetOverride(pagePath, []byte("package page\n\ncomponent Home(user User) {\n\t<div>{ user.Name }</div>\n}\n")); err != nil {
		t.Fatalf("SetOverride: %v", err)
	}
	dir = filepath.Dir(pagePath)
	return a, dir, pagePath
}

func TestAnalyzeEphemeralViaAnalyzer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	a, dir, pagePath := newCompletionE2EFixture(t)
	patched := []byte("package page\n\ncomponent Home(user User) {\n\t<div>{ user._ }</div>\n}\n")
	pkg, err := a.AnalyzeEphemeral(dir, pagePath, patched)
	if err != nil {
		t.Fatalf("AnalyzeEphemeral: %v", err)
	}
	if pkg.Info == nil || len(pkg.ExprMap) == 0 {
		t.Fatal("ephemeral lsp.Package missing Info/ExprMap")
	}
	if len(pkg.Filters) == 0 {
		t.Fatal("ephemeral lsp.Package missing Filters")
	}

	generated := filepath.Join(dir, "page.x.go")
	if _, err := os.Stat(generated); !os.IsNotExist(err) {
		t.Fatalf("physical generated file exists: %s", generated)
	}
}

// TestGoCompletionE2E drives textDocument/completion through the full JSON-RPC
// server (runLSP) against a real temp module, exercising repair → classify →
// ephemeral analysis → scope bridge → enumeration end to end across all four Go
// identifier-position bridges:
//   - `{ us▮ }` (ExprMap) yields the `user` parameter (a local),
//   - `{{ ▮ }}` (CtrlMap/GoBlock) yields Go keywords plus locals,
//   - a top-level `func helper() { return Us▮ }` (GoChunk) yields package-scope
//     names plus keywords,
//   - a `component Home(user User▮)` type position (SigTypes) yields type names
//     without statement keywords.
func TestGoCompletionE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	root := t.TempDir()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) string {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	write("page/types.go", "package page\n\ntype User struct{ Name string }\n")
	// On-disk page is valid; the buffer opened below carries the mid-edit content.
	pagePath := write("page/page.gsx", "package page\n\ncomponent Home(user User) {\n\t<div>{ user.Name }</div>\n}\n")
	uri := "file://" + pagePath

	// Buffer under edit: a top-level GoChunk func body, an expression cursor after
	// `us`, and a statement-context `{{ }}` GoBlock inside a second component.
	source := "package page\n\n" +
		"func helper() User {\n\treturn Us\n}\n\n" +
		"component Home(user User) {\n\t<div>{ us }</div>\n}\n\n" +
		"component Block(item User) {\n\t{{  }}\n\t<span>{ item.Name }</span>\n}\n"

	chunkCursor := strings.Index(source, "return Us") + len("return Us")  // after `Us`
	exprCursor := strings.Index(source, "{ us }") + len("{ us")           // right after `us`
	blockCursor := strings.Index(source, "{{  }}") + len("{{ ")           // between the two spaces
	sigCursor := strings.Index(source, "(user User)") + len("(user User") // on the signature type `User`

	frame := func(value any) string {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return "Content-Length: " + strconv.Itoa(len(data)) + "\r\n\r\n" + string(data)
	}
	var input strings.Builder
	input.WriteString(frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}}))
	input.WriteString(frame(map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": source}},
	}))
	req := func(id, cursor int) {
		pos := lspUTF16PositionAt(source, cursor)
		input.WriteString(frame(map[string]any{
			"jsonrpc": "2.0", "id": id, "method": "textDocument/completion",
			"params": map[string]any{
				"textDocument": map[string]any{"uri": uri},
				"position":     map[string]any{"line": pos.Line, "character": pos.Character},
			},
		}))
	}
	req(2, exprCursor)
	req(3, blockCursor)
	req(4, chunkCursor)
	req(5, sigCursor)
	input.WriteString(frame(map[string]any{"jsonrpc": "2.0", "method": "exit"}))

	var output, stderr bytes.Buffer
	if code := runLSP(strings.NewReader(input.String()), &output, &stderr, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, stderr.String())
	}
	if strings.Contains(output.String(), ".x.go") {
		t.Fatalf("completion response exposed virtual generated Go:\n%s", output.String())
	}

	exprItems := completionLabels(t, output.String(), 2)
	if !exprItems["user"] {
		t.Errorf("expression completion missing local `user`; labels=%v", exprItems)
	}

	blockItems := completionLabels(t, output.String(), 3)
	if !blockItems["user"] && !blockItems["item"] {
		t.Errorf("statement completion missing a local (`user`/`item`); labels=%v", blockItems)
	}
	for _, kw := range []string{"return", "if", "for"} {
		if !blockItems[kw] {
			t.Errorf("statement completion missing keyword %q; labels=%v", kw, blockItems)
		}
	}

	// GoChunk: inside a top-level func body, package-scope names (the sibling
	// component `Home`, the func `helper`, the type `User`) are visible.
	chunkItems := completionLabels(t, output.String(), 4)
	for _, name := range []string{"User", "helper", "Home"} {
		if !chunkItems[name] {
			t.Errorf("GoChunk completion missing package-scope %q; labels=%v", name, chunkItems)
		}
	}
	// Statement context → keywords are offered here too.
	if !chunkItems["return"] {
		t.Errorf("GoChunk completion missing keyword `return`; labels=%v", chunkItems)
	}

	// Signature type: a cursor on a component parameter type enumerates visible
	// type names (the package type `User`). Type positions are not statement
	// contexts, so Go statement keywords are NOT offered.
	sigItems := completionLabels(t, output.String(), 5)
	if !sigItems["User"] {
		t.Errorf("signature-type completion missing type `User`; labels=%v", sigItems)
	}
	if sigItems["return"] {
		t.Errorf("signature-type completion should not offer statement keywords; labels=%v", sigItems)
	}
}

// completionLabels extracts the set of item labels from the completion response
// with the given id.
func completionLabels(t *testing.T, output string, id int) map[string]bool {
	t.Helper()
	for part := range strings.SplitSeq(output, "Content-Length:") {
		_, body, ok := strings.Cut(part, "\r\n\r\n")
		if !ok {
			continue
		}
		var response struct {
			ID     int                 `json:"id"`
			Result *lsp.CompletionList `json:"result"`
		}
		if err := json.Unmarshal([]byte(body), &response); err != nil || response.ID != id {
			continue
		}
		if response.Result == nil {
			t.Fatalf("completion response id %d has null result:\n%s", id, output)
		}
		labels := map[string]bool{}
		for _, item := range response.Result.Items {
			labels[item.Label] = true
		}
		return labels
	}
	t.Fatalf("no completion response for id %d in:\n%s", id, output)
	return nil
}
