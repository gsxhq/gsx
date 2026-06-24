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

func frameMsg(t *testing.T, v any) string {
	t.Helper()
	b, _ := json.Marshal(v)
	return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
}

// TestLSPEndToEndDiagnostics drives the real analyzer through lsp.Server over an
// in-memory stream and asserts a publishDiagnostics for a .gsx with a type error.
func TestLSPEndToEndDiagnostics(t *testing.T) {
	repoRoot, _ := filepath.Abs("..")
	dir := t.TempDir()
	goMod := "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => " + repoRoot + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}
	gsxPath := filepath.Join(dir, "page.gsx")
	// Valid on disk so discovery/glob finds it; the open buffer adds the error.
	if err := os.WriteFile(gsxPath, []byte("package x\n\ncomponent Page() {\n\t<div>hi</div>\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	uri := "file://" + gsxPath

	in := frameMsg(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += frameMsg(t, map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{
			"uri": uri, "version": 1,
			"text": "package x\n\ncomponent Page() {\n\t<div>{ nope }</div>\n}\n",
		}},
	})
	in += frameMsg(t, map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, nil); code != 0 {
		t.Fatalf("runLSP exit = %d, stderr = %s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "publishDiagnostics") || !strings.Contains(out.String(), "nope") {
		t.Fatalf("expected a diagnostic mentioning 'nope'; out:\n%s", out.String())
	}
}
