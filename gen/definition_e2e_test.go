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

// TestDefinitionD1 drives the real analyzer through lsp.Server: go-to-def on the
// `Name` field in `{ u.Name }` resolves to its declaration in user.go.
func TestDefinitionD1(t *testing.T) {
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
	must("user.go", "package x\n\ntype User struct {\n\tName string\n}\n")
	cardSrc := "package x\n\ncomponent Card(u User) {\n\t<div>{ u.Name }</div>\n}\n"
	must("card.gsx", cardSrc)
	uri := "file://" + filepath.Join(dir, "card.gsx")

	// position of 'N' in "u.Name" within card.gsx (line index, char index).
	lines := strings.Split(cardSrc, "\n")
	var line, character int
	for i, l := range lines {
		if c := strings.Index(l, "u.Name"); c >= 0 {
			line, character = i, c+2 // the 'N'
			break
		}
	}

	frame := func(v any) string {
		b, _ := json.Marshal(v)
		return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
	}
	in := frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": cardSrc}}})
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
	if !strings.HasSuffix(loc.URI, "user.go") {
		t.Fatalf("definition resolved to %q, want user.go", loc.URI)
	}
	// Exact landing: the `Name` field in user.go (`\tName string` on line 3).
	if loc.Range.Start.Line != 3 || loc.Range.Start.Character != 1 {
		t.Fatalf("D1 landed at L%d:C%d, want L3:C1 (the Name field)", loc.Range.Start.Line, loc.Range.Start.Character)
	}
}

// TestDefinitionParam (D3): go-to-definition on a component param reference (the
// `u` in `{ u.Name }`) resolves back to the param's declaration in card.gsx — not
// null, and never into generated .x.go.
func TestDefinitionParam(t *testing.T) {
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
	must("user.go", "package x\n\ntype User struct {\n\tName string\n}\n")
	cardSrc := "package x\n\ncomponent Card(u User) {\n\t<div>{ u.Name }</div>\n}\n"
	must("card.gsx", cardSrc)
	uri := "file://" + filepath.Join(dir, "card.gsx")

	// position of the param reference 'u' in "{ u.Name }".
	lines := strings.Split(cardSrc, "\n")
	var line, character int
	for i, l := range lines {
		if c := strings.Index(l, "{ u.Name"); c >= 0 {
			line, character = i, c+2 // the 'u'
			break
		}
	}

	frame := func(v any) string {
		b, _ := json.Marshal(v)
		return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
	}
	in := frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": cardSrc}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/definition",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri},
			"position": map[string]any{"line": line, "character": character}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	s := out.String()
	if strings.Contains(s, ".x.go") {
		t.Fatalf("param definition leaked a generated-code location; out:\n%s", s)
	}
	loc := definitionResult(t, s, 2)
	if loc == nil {
		t.Fatalf("param definition should resolve to the .gsx param (D3), not null; out:\n%s", s)
	}
	if !strings.HasSuffix(loc.URI, "card.gsx") {
		t.Fatalf("param resolved to %q, want card.gsx", loc.URI)
	}
	// Exact landing: on the `u` in `component Card(u User)`.
	wantLine, wantChar := -1, -1
	for i, l := range lines {
		if c := strings.Index(l, "component Card(u "); c >= 0 {
			wantLine, wantChar = i, c+len("component Card(") // 0-based index of 'u'
			break
		}
	}
	if loc.Range.Start.Line != wantLine || loc.Range.Start.Character != wantChar {
		t.Fatalf("param landed at L%d:C%d, want L%d:C%d (the 'u' in the signature)",
			loc.Range.Start.Line, loc.Range.Start.Character, wantLine, wantChar)
	}
}

// TestDefinitionRawGoSymbol: a top-level Go helper declared in the .gsx file
// itself (`func greeting()`), referenced from an interpolation, resolves back to
// its declaration in the SAME .gsx — never null, never into generated .x.go. The
// raw-Go body is emitted under a //line directive in the skeleton, which is what
// makes this resolve to source.
func TestDefinitionRawGoSymbol(t *testing.T) {
	t.Parallel()
	src := "package x\n\nfunc greeting() string {\n\treturn \"hi\"\n}\n\ncomponent Page() {\n\t<p>{ greeting() }</p>\n}\n"
	loc := rawGoDefinition(t, src, "{ greeting()", 2 /* skip "{ " to the 'g' */)
	if loc == nil {
		t.Fatal("raw-Go definition should resolve to the .gsx declaration, not null")
	}
	if !strings.HasSuffix(loc.URI, "page.gsx") {
		t.Fatalf("raw-Go symbol resolved to %q, want page.gsx", loc.URI)
	}
	// Exact landing: the 'g' of `func greeting`.
	wantLine, wantChar := declStart(t, src, "func greeting", len("func "))
	if loc.Range.Start.Line != wantLine || loc.Range.Start.Character != wantChar {
		t.Fatalf("raw-Go symbol landed at L%d:C%d, want L%d:C%d (the 'greeting' decl)",
			loc.Range.Start.Line, loc.Range.Start.Character, wantLine, wantChar)
	}
}

// TestDefinitionRawGoSymbolWithImport: same as above, but the raw-Go chunk also
// carries an import. splitChunk excises the import (hoisting it ahead of all
// decls) and reports the body's offset, so the //line still anchors the func to
// its true .gsx line despite the removed import lines.
func TestDefinitionRawGoSymbolWithImport(t *testing.T) {
	t.Parallel()
	src := "package x\n\nimport \"strings\"\n\nfunc shout(s string) string {\n\treturn strings.ToUpper(s)\n}\n\ncomponent Page() {\n\t<p>{ shout(\"hi\") }</p>\n}\n"
	loc := rawGoDefinition(t, src, "{ shout(", 2 /* skip "{ " to the 's' */)
	if loc == nil {
		t.Fatal("raw-Go (with import) definition should resolve to the .gsx declaration, not null")
	}
	if !strings.HasSuffix(loc.URI, "page.gsx") {
		t.Fatalf("raw-Go symbol resolved to %q, want page.gsx", loc.URI)
	}
	// Exact landing: the 's' of `func shout` — proves the import excision did not
	// shift the //line mapping.
	wantLine, wantChar := declStart(t, src, "func shout", len("func "))
	if loc.Range.Start.Line != wantLine || loc.Range.Start.Character != wantChar {
		t.Fatalf("raw-Go symbol (with import) landed at L%d:C%d, want L%d:C%d (the 'shout' decl)",
			loc.Range.Start.Line, loc.Range.Start.Character, wantLine, wantChar)
	}
}

// rawGoDefinition writes src as page.gsx in a temp module, opens it, and issues
// textDocument/definition at the cursor (the first occurrence of needle, plus
// cursorOff bytes into it). It fails the test if the response leaks a .x.go
// location.
func rawGoDefinition(t *testing.T, src, needle string, cursorOff int) *lsp.Location {
	t.Helper()
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
	must("page.gsx", src)
	uri := "file://" + filepath.Join(dir, "page.gsx")

	var line, character int
	for i, l := range strings.Split(src, "\n") {
		if c := strings.Index(l, needle); c >= 0 {
			line, character = i, c+cursorOff
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
	if strings.Contains(out.String(), ".x.go") {
		t.Fatalf("definition leaked a generated-code location; out:\n%s", out.String())
	}
	return definitionResult(t, out.String(), 2)
}

// declStart returns the 0-based (line, character) of a declared name: the first
// line containing decl, offset by nameOff bytes (e.g. past "func ").
func declStart(t *testing.T, src, decl string, nameOff int) (line, character int) {
	t.Helper()
	for i, l := range strings.Split(src, "\n") {
		if c := strings.Index(l, decl); c >= 0 {
			return i, c + nameOff
		}
	}
	t.Fatalf("could not locate %q in:\n%s", decl, src)
	return -1, -1
}

// definitionResult extracts the textDocument/definition result for the given
// request id from a server's framed output, or nil if the result was null.
func definitionResult(t *testing.T, out string, id int) *lsp.Location {
	t.Helper()
	marker := `"id":` + strconv.Itoa(id) + `,`
	for part := range strings.SplitSeq(out, "Content-Length:") {
		_, after, ok := strings.Cut(part, "\r\n\r\n")
		if !ok {
			continue
		}
		body := after
		if !strings.Contains(body, marker) {
			continue
		}
		var resp struct {
			Result *lsp.Location `json:"result"`
		}
		if err := json.Unmarshal([]byte(body), &resp); err != nil {
			t.Fatalf("decode definition response: %v\nbody=%q", err, body)
		}
		return resp.Result
	}
	t.Fatalf("no response with id %d found in:\n%s", id, out)
	return nil
}
