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
	if code := runLSP(strings.NewReader(in), &out, &errBuf, nil); code != 0 {
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

// TestDefinitionPipedReturnsNull: a piped expression (`{ u.Name |> upper }`)
// lowers to a wrapped call in the skeleton, so go-to-def cannot use the
// byte-identical relative-offset bridge. Rather than jump into generated code
// (a wrong answer), the handler returns null. Tracks the deferred seed-node fix.
func TestDefinitionPipedReturnsNull(t *testing.T) {
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
	cardSrc := "package x\n\ncomponent Card(u User) {\n\t<div>{ u.Name |> upper }</div>\n}\n"
	must("card.gsx", cardSrc)
	uri := "file://" + filepath.Join(dir, "card.gsx")

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
	if code := runLSP(strings.NewReader(in), &out, &errBuf, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, `"id":2,"result":null`) {
		t.Fatalf("piped definition should return null; out:\n%s", s)
	}
	if strings.Contains(s, ".x.go") {
		t.Fatalf("piped definition leaked a generated-code location; out:\n%s", s)
	}
}

// TestDefinitionParam (D3): go-to-definition on a component param reference (the
// `u` in `{ u.Name }`) resolves back to the param's declaration in card.gsx — not
// null, and never into generated .x.go.
func TestDefinitionParam(t *testing.T) {
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
	if code := runLSP(strings.NewReader(in), &out, &errBuf, nil); code != 0 {
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

// definitionResult extracts the textDocument/definition result for the given
// request id from a server's framed output, or nil if the result was null.
func definitionResult(t *testing.T, out string, id int) *lsp.Location {
	t.Helper()
	marker := `"id":` + strconv.Itoa(id) + `,`
	for _, part := range strings.Split(out, "Content-Length:") {
		i := strings.Index(part, "\r\n\r\n")
		if i < 0 {
			continue
		}
		body := part[i+4:]
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
