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

// hoverModule writes a temp module (card.gsx + user.go) and returns dir + the
// card.gsx source.
func hoverModule(t *testing.T) (dir, cardSrc string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir = t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must := func(p, c string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/h\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("user.go", "package h\n\ntype User struct {\n\tName  string\n\tEmail string\n}\n\nfunc Greeting(name string) string {\n\treturn \"Hello, \" + name\n}\n")
	cardSrc = "package h\n\ncomponent Card(u User) {\n\t<div>{ u.Name }</div>\n\t<p>{ Greeting(u.Email) }</p>\n\t<i>{ \"hi\" }</i>\n}\n"
	must("card.gsx", cardSrc)
	return dir, cardSrc
}

// hoverAt opens uri with text and returns the hover result for a cursor at the
// first occurrence of needle plus cursorOff bytes into it (or nil if null).
func hoverAt(t *testing.T, dir, file, text, needle string, cursorOff int) *lsp.Hover {
	t.Helper()
	uri := "file://" + filepath.Join(dir, file)
	var line, character int
	for i, l := range strings.Split(text, "\n") {
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
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": text}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/hover",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri},
			"position": map[string]any{"line": line, "character": character}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	if strings.Contains(out.String(), ".x.go") {
		t.Fatalf("hover leaked a generated-code path; out:\n%s", out.String())
	}
	marker := `"id":2,`
	for _, part := range strings.Split(out.String(), "Content-Length:") {
		i := strings.Index(part, "\r\n\r\n")
		if i < 0 {
			continue
		}
		body := part[i+4:]
		if !strings.Contains(body, marker) {
			continue
		}
		var resp struct {
			Result *lsp.Hover `json:"result"`
		}
		if err := json.Unmarshal([]byte(body), &resp); err != nil {
			t.Fatalf("decode hover: %v\nbody=%q", err, body)
		}
		return resp.Result
	}
	t.Fatalf("no hover response (id 2) in:\n%s", out.String())
	return nil
}

func TestHoverField(t *testing.T) {
	dir, src := hoverModule(t)
	h := hoverAt(t, dir, "card.gsx", src, "{ u.Name }", len("{ u."))
	if h == nil || !strings.Contains(h.Contents.Value, "field Name string") {
		t.Fatalf("want 'field Name string', got %+v", h)
	}
	if !strings.Contains(h.Contents.Value, "```go") {
		t.Fatalf("want a go fenced block, got %q", h.Contents.Value)
	}
}

func TestHoverVar(t *testing.T) {
	dir, src := hoverModule(t)
	h := hoverAt(t, dir, "card.gsx", src, "{ u.Name }", len("{ ")) // on 'u'
	if h == nil || !strings.Contains(h.Contents.Value, "var u User") {
		t.Fatalf("want 'var u User', got %+v", h)
	}
}

func TestHoverFunc(t *testing.T) {
	dir, src := hoverModule(t)
	h := hoverAt(t, dir, "card.gsx", src, "Greeting(u.Email)", 0) // on 'Greeting'
	if h == nil || !strings.Contains(h.Contents.Value, "func Greeting(name string) string") {
		t.Fatalf("want 'func Greeting(name string) string', got %+v", h)
	}
}

func TestHoverWholeExprType(t *testing.T) {
	dir, src := hoverModule(t)
	h := hoverAt(t, dir, "card.gsx", src, `{ "hi" }`, len("{ ")) // on the string literal
	if h == nil || !strings.Contains(h.Contents.Value, "string") {
		t.Fatalf("want type 'string' for the literal expression, got %+v", h)
	}
}

func TestHoverGoFileNull(t *testing.T) {
	dir, _ := hoverModule(t)
	goSrc, _ := os.ReadFile(filepath.Join(dir, "user.go"))
	h := hoverAt(t, dir, "user.go", string(goSrc), "Greeting", 0)
	if h != nil {
		t.Fatalf("hover on a .go file must be null (gopls owns it), got %+v", h)
	}
}

func TestHoverNonExprNull(t *testing.T) {
	dir, src := hoverModule(t)
	h := hoverAt(t, dir, "card.gsx", src, "<div>", 1) // on the 'div' tag text, not an expr
	if h != nil {
		t.Fatalf("hover on plain markup must be null, got %+v", h)
	}
}

func TestHoverPipedNull(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must := func(p, c string) {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/h\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("user.go", "package h\n\ntype User struct {\n\tName string\n}\n")
	src := "package h\n\ncomponent Card(u User) {\n\t<div>{ u.Name |> upper }</div>\n}\n"
	must("card.gsx", src)
	h := hoverAt(t, dir, "card.gsx", src, "{ u.Name", len("{ u.")) // on 'Name' inside a piped expr
	if h != nil {
		t.Fatalf("hover on a piped expression must be null, got %+v", h)
	}
}

func TestHoverCapabilityAdvertised(t *testing.T) {
	frame := func(v any) string {
		b, _ := json.Marshal(v)
		return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
	}
	in := frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})
	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), `"hoverProvider":true`) {
		t.Fatalf("initialize did not advertise hoverProvider:\n%s", out.String())
	}
}
