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

// gd on the `comments` attribute name in `<CommentsList comments={nil}/>`
// resolves to the `comments` parameter of `component CommentsList(comments []Comment)`.
func TestDefinitionAttrParam(t *testing.T) {
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
	must("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("types.go", "package x\n\ntype Comment struct{ Body string }\n")
	src := "package x\n\ncomponent CommentsList(comments []Comment) {\n\t<ul></ul>\n}\n\ncomponent Page() {\n\t<CommentsList comments={nil}/>\n}\n"
	must("comp.gsx", src)
	uri := "file://" + filepath.Join(dir, "comp.gsx")

	// Cursor on the 'c' of the `comments` ATTRIBUTE (the one followed by '={').
	lines := strings.Split(src, "\n")
	var line, character int
	for i, l := range lines {
		if c := strings.Index(l, "comments={"); c >= 0 {
			line, character = i, c
			break
		}
	}

	frame := func(v any) string {
		b, _ := json.Marshal(v)
		return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
	}
	in := frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": src}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/definition",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri},
			"position": map[string]any{"line": line, "character": character}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	loc := definitionResult(t, out.String(), 2)
	if loc == nil {
		t.Fatalf("definition returned null; out:\n%s\nstderr:\n%s", out.String(), errBuf.String())
	}
	if !strings.HasSuffix(loc.URI, "comp.gsx") {
		t.Fatalf("resolved to %q, want comp.gsx", loc.URI)
	}
	// The decl line is "component CommentsList(comments []Comment) {" (line index 2).
	// Expect the cursor to land on the `comments` PARAMETER there.
	declLine := 2
	declCol := strings.Index(lines[declLine], "comments")
	if loc.Range.Start.Line != declLine || loc.Range.Start.Character != declCol {
		t.Fatalf("landed at L%d:C%d, want L%d:C%d (the comments param)",
			loc.Range.Start.Line, loc.Range.Start.Character, declLine, declCol)
	}
}
