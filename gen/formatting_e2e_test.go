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

// TestFormattingReformats drives textDocument/formatting through the real server:
// a non-canonical .gsx buffer yields a single whole-document TextEdit whose text
// is the canonical form.
func TestFormattingReformats(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "hi.gsx")
	if err := os.WriteFile(path, []byte(unformattedGsx), 0o644); err != nil {
		t.Fatal(err)
	}
	uri := "file://" + path

	edits := formattingEdits(t, uri, unformattedGsx)
	if len(edits) != 1 {
		t.Fatalf("want exactly 1 edit, got %d: %+v", len(edits), edits)
	}
	want, err := formatGsx(path, []byte(unformattedGsx))
	if err != nil {
		t.Fatal(err)
	}
	if edits[0].NewText != string(want) {
		t.Fatalf("edit NewText mismatch:\ngot:\n%q\nwant:\n%q", edits[0].NewText, string(want))
	}
	// The replacement must start at the document origin and cover at least to the
	// last line — a whole-document replace.
	if edits[0].Range.Start.Line != 0 || edits[0].Range.Start.Character != 0 {
		t.Fatalf("edit start = %+v, want 0:0", edits[0].Range.Start)
	}
	lastLine := strings.Count(unformattedGsx, "\n")
	if edits[0].Range.End.Line != lastLine {
		t.Fatalf("edit end line = %d, want %d (document end)", edits[0].Range.End.Line, lastLine)
	}
}

// TestFormattingAlreadyCanonical: formatting a canonical buffer returns no edits.
func TestFormattingAlreadyCanonical(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "hi.gsx")
	canonical, err := formatGsx(path, []byte(unformattedGsx))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, canonical, 0o644); err != nil {
		t.Fatal(err)
	}
	edits := formattingEdits(t, "file://"+path, string(canonical))
	if len(edits) != 0 {
		t.Fatalf("canonical buffer should yield 0 edits, got %d: %+v", len(edits), edits)
	}
}

// TestFormattingAdvertised: the server advertises documentFormattingProvider.
func TestFormattingAdvertised(t *testing.T) {
	t.Parallel()
	frame := func(v any) string {
		b, _ := json.Marshal(v)
		return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
	}
	in := frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})
	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), `"documentFormattingProvider":true`) {
		t.Fatalf("initialize did not advertise documentFormattingProvider:\n%s", out.String())
	}
}

// TestFormattingRemovesUnusedImport: textDocument/formatting on a .gsx with an
// unused import returns an edit whose text drops that import.
func TestFormattingRemovesUnusedImport(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must := func(p, c string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/u\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	src := "package u\n\nimport \"strings\"\n\ncomponent C() {\n\t<p>hi</p>\n}\n"
	must("c.gsx", src)
	uri := "file://" + filepath.Join(dir, "c.gsx")

	edits := formattingEdits(t, uri, src)
	if len(edits) != 1 {
		t.Fatalf("want 1 edit, got %d: %+v", len(edits), edits)
	}
	if strings.Contains(edits[0].NewText, "import \"strings\"") {
		t.Fatalf("formatting did not drop the unused import:\n%s", edits[0].NewText)
	}
}

// TestFormattingGoimportsModeMergesImports: with no gsx.toml (default
// goimports), textDocument/formatting merges and dedups import declarations.
func TestFormattingGoimportsModeMergesImports(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must := func(p, c string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/u\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	src := "package u\n\nimport \"strings\"\n\nimport (\n\t\"fmt\"\n\n\t\"strings\"\n)\n\n" +
		"component C() {\n\t<p>{ fmt.Sprint(strings.ToUpper(\"x\")) }</p>\n}\n"
	must("c.gsx", src)
	uri := "file://" + filepath.Join(dir, "c.gsx")

	edits := formattingEdits(t, uri, src)
	if len(edits) != 1 {
		t.Fatalf("want 1 edit, got %d", len(edits))
	}
	if n := strings.Count(edits[0].NewText, "\"strings\""); n != 1 {
		t.Fatalf("formatting did not dedup strings (%d):\n%s", n, edits[0].NewText)
	}
}

// TestFormattingGofmtModeLeavesImportsAlone: [formatter] imports = "gofmt" makes
// textDocument/formatting stop removing AND stop reordering imports.
func TestFormattingGofmtModeLeavesImportsAlone(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must := func(p, c string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/u\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("gsx.toml", "[formatter]\nimports = \"gofmt\"\n")
	// An unused import: goimports mode would drop it, gofmt mode must not.
	src := "package u\n\nimport \"strings\"\n\ncomponent C() {\n\t<p>hi</p>\n}\n"
	must("c.gsx", src)
	uri := "file://" + filepath.Join(dir, "c.gsx")

	edits := formattingEdits(t, uri, src)
	// Already canonical under gofmt mode ⇒ no edits at all.
	if len(edits) != 0 {
		t.Fatalf("gofmt mode must not touch imports, got edit:\n%s", edits[0].NewText)
	}
}

// formattingEdits opens uri with the given text and returns the edits from a
// textDocument/formatting request (id 2).
func formattingEdits(t *testing.T, uri, text string) []lsp.TextEdit {
	t.Helper()
	frame := func(v any) string {
		b, _ := json.Marshal(v)
		return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
	}
	in := frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": text}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/formatting",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri},
			"options": map[string]any{"tabSize": 4, "insertSpaces": false}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	marker := `"id":2,`
	for part := range strings.SplitSeq(out.String(), "Content-Length:") {
		_, after, ok := strings.Cut(part, "\r\n\r\n")
		if !ok {
			continue
		}
		body := after
		if !strings.Contains(body, marker) {
			continue
		}
		var resp struct {
			Result []lsp.TextEdit `json:"result"`
		}
		if err := json.Unmarshal([]byte(body), &resp); err != nil {
			t.Fatalf("decode formatting response: %v\nbody=%q", err, body)
		}
		return resp.Result
	}
	t.Fatalf("no formatting response (id 2) in:\n%s", out.String())
	return nil
}
