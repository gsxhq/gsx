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

// pipeNavModule writes a temp module with a local `greeting` and a .gsx using
// std `truncate`/`upper`: { greeting() |> truncate(5) |> upper }. Returns dir + src.
func pipeNavModule(t *testing.T) (dir, cardSrc string) {
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
	must("go.mod", "module example.com/p\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("u.go", "package p\n\nfunc Greeting(name string) string { return name }\n")
	cardSrc = "package p\n\ncomponent Card(name string) {\n\t<div>{ Greeting(name) |> truncate(5) |> upper }</div>\n}\n"
	must("card.gsx", cardSrc)
	return dir, cardSrc
}

// pipeDefAt opens card.gsx and returns the definition result for a cursor at the
// first occurrence of needle + off (or nil for null).
func pipeDefAt(t *testing.T, dir, src, needle string, off int) *lsp.Location {
	t.Helper()
	uri := "file://" + filepath.Join(dir, "card.gsx")
	var line, ch int
	found := false
	for i, l := range strings.Split(src, "\n") {
		if c := strings.Index(l, needle); c >= 0 {
			line, ch = i, c+off
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("needle %q not found in src", needle)
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
			"position": map[string]any{"line": line, "character": ch}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	if strings.Contains(out.String(), ".x.go") {
		t.Fatalf("piped definition leaked a generated path; out:\n%s", out.String())
	}
	return definitionResult(t, out.String(), 2) // helper from definition_e2e_test.go
}

func TestPipeDefSeed(t *testing.T) {
	t.Parallel()
	dir, src := pipeNavModule(t)
	loc := pipeDefAt(t, dir, src, "Greeting(name)", 0) // on `Greeting` (the seed call)
	if loc == nil || !strings.HasSuffix(loc.URI, "u.go") {
		t.Fatalf("seed def → %v, want u.go", loc)
	}
}

func TestPipeDefFilter(t *testing.T) {
	t.Parallel()
	dir, src := pipeNavModule(t)
	loc := pipeDefAt(t, dir, src, "|> upper", len("|> ")) // on `upper`
	if loc == nil || !strings.HasSuffix(loc.URI, "std.go") {
		t.Fatalf("filter def → %v, want std/std.go", loc)
	}
}

func TestPipeDefArg(t *testing.T) {
	t.Parallel()
	dir, _ := pipeNavModule(t)
	// { name |> truncate(n) } with a param n.
	src := "package p\n\ncomponent Card(name string, n int) {\n\t<div>{ name |> truncate(n) }</div>\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "card.gsx"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	loc := pipeDefAt(t, dir, src, "truncate(n)", len("truncate(")) // on `n`
	if loc == nil || !strings.HasSuffix(loc.URI, "card.gsx") {
		t.Fatalf("arg def → %v, want card.gsx (the n param)", loc)
	}
}

func TestPipeDefOnOperatorNull(t *testing.T) {
	t.Parallel()
	dir, src := pipeNavModule(t)
	loc := pipeDefAt(t, dir, src, "|> upper", 0) // on the `|` of `|>`
	if loc != nil {
		t.Fatalf("def on `|>` must be null, got %v", loc)
	}
}

// pipeHoverAt mirrors pipeDefAt for textDocument/hover.
func pipeHoverAt(t *testing.T, dir, src, needle string, off int) *lsp.Hover {
	t.Helper()
	uri := "file://" + filepath.Join(dir, "card.gsx")
	var line, ch int
	found := false
	for i, l := range strings.Split(src, "\n") {
		if c := strings.Index(l, needle); c >= 0 {
			line, ch = i, c+off
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("needle %q not found in src", needle)
	}
	frame := func(v any) string {
		b, _ := json.Marshal(v)
		return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
	}
	in := frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": src}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/hover",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri},
			"position": map[string]any{"line": line, "character": ch}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
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

func TestPipeHoverFilter(t *testing.T) {
	t.Parallel()
	dir, src := pipeNavModule(t)
	h := pipeHoverAt(t, dir, src, "|> upper", len("|> ")) // on `upper`
	if h == nil || !strings.Contains(h.Contents.Value, "func std.Upper(") {
		t.Fatalf("hover on filter `upper` → %+v, want func std.Upper(...)", h)
	}
}

func TestPipeHoverSeed(t *testing.T) {
	t.Parallel()
	dir, src := pipeNavModule(t)
	h := pipeHoverAt(t, dir, src, "Greeting(name)", 0) // on `Greeting`
	if h == nil || !strings.Contains(h.Contents.Value, "func Greeting(name string) string") {
		t.Fatalf("hover on seed `Greeting` → %+v", h)
	}
}

func TestPipeHoverArg(t *testing.T) {
	t.Parallel()
	dir, _ := pipeNavModule(t)
	src := "package p\n\ncomponent Card(name string, n int) {\n\t<div>{ name |> truncate(n) }</div>\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "card.gsx"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	h := pipeHoverAt(t, dir, src, "truncate(n)", len("truncate(")) // on `n`
	if h == nil || !strings.Contains(h.Contents.Value, "var n int") {
		t.Fatalf("hover on arg `n` → %+v, want var n int", h)
	}
}
