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

// sigTypeDefAt drives the real LSP over a temp module and returns the
// textDocument/definition Location for the cursor at (line, character) of the
// .gsx file at uri (whose source is src). Used by the signature-parameter-type
// go-to-definition tests below.
func sigTypeDefAt(t *testing.T, dir, gsxPath, src string, line, character int) *lsp.Location {
	t.Helper()
	uri := "file://" + filepath.Join(dir, gsxPath)
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
	return definitionResult(t, out.String(), 2)
}

// sigTypeDefLocations is like sigTypeDefAt but decodes a definition result that
// may be a single Location OR a Location[] (a package qualifier resolves to the
// list of its files' package clauses). Returns the locations (possibly empty).
func sigTypeDefLocations(t *testing.T, dir, gsxPath, src string, line, character int) []lsp.Location {
	t.Helper()
	uri := "file://" + filepath.Join(dir, gsxPath)
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
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal([]byte(body), &resp); err != nil {
			t.Fatalf("decode definition response: %v\nbody=%q", err, body)
		}
		if len(resp.Result) == 0 || string(resp.Result) == "null" {
			return nil
		}
		if resp.Result[0] == '[' {
			var locs []lsp.Location
			if err := json.Unmarshal(resp.Result, &locs); err != nil {
				t.Fatalf("decode definition list: %v", err)
			}
			return locs
		}
		var loc lsp.Location
		if err := json.Unmarshal(resp.Result, &loc); err != nil {
			t.Fatalf("decode definition single: %v", err)
		}
		return []lsp.Location{loc}
	}
	t.Fatalf("no definition response (id 2) in:\n%s", out.String())
	return nil
}

// gd on a component PARAMETER TYPE in the signature resolves the Go identifiers:
// the package qualifier `store` jumps to the import; the type name `Comment`
// jumps to its definition in the imported package — even through a slice prefix
// (`[]store.Comment`).
func TestDefinitionSignatureParamTypeCrossPkg(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must := func(p, c string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("store/store.go", "package store\n\ntype Comment struct{ Body string }\n")
	src := "package blog\n\nimport \"example.com/x/store\"\n\ncomponent CommentsList(comments []store.Comment) {\n\t<ul></ul>\n}\n"
	must("blog/comp.gsx", src)
	lines := strings.Split(src, "\n")

	// Locate the signature line and the columns of `store` and `Comment` within
	// `[]store.Comment`.
	var sigLine int
	for i, l := range lines {
		if strings.Contains(l, "[]store.Comment") {
			sigLine = i
			break
		}
	}
	storeCol := strings.Index(lines[sigLine], "store.Comment")
	commentCol := strings.Index(lines[sigLine], "Comment)") // the type name, not the import

	// Case 1: `Comment` → the type definition in store/store.go.
	loc := sigTypeDefAt(t, dir, "blog/comp.gsx", src, sigLine, commentCol)
	if loc == nil {
		t.Fatalf("gd on `Comment` param type returned null")
	}
	if !strings.HasSuffix(loc.URI, filepath.Join("store", "store.go")) {
		t.Fatalf("`Comment` resolved to %q, want store/store.go", loc.URI)
	}

	// Case 2: `store` qualifier → into the package (gopls-style): the `package
	// store` clause of store/store.go, landing on the package NAME (not the .gsx
	// import, and not mid-path).
	locs := sigTypeDefLocations(t, dir, "blog/comp.gsx", src, sigLine, storeCol)
	if len(locs) == 0 {
		t.Fatalf("gd on `store` qualifier returned no locations")
	}
	found := false
	for _, l := range locs {
		if strings.HasSuffix(l.URI, filepath.Join("store", "store.go")) {
			found = true
			// store.go is `package store\n…` → the name is on line 0, after "package ".
			if l.Range.Start.Line != 0 || l.Range.Start.Character != len("package ") {
				t.Fatalf("`store` landed at L%d:C%d, want L0:C%d (the package name)",
					l.Range.Start.Line, l.Range.Start.Character, len("package "))
			}
		}
	}
	if !found {
		t.Fatalf("`store` resolved to %v, want store/store.go package clause", locs)
	}
}

// gd on a SAME-package type in a parameter signature resolves to that type's
// declaration in a sibling .go file of the same package — covering the
// props-struct path (a bare type) and the BYO path (a sole struct param whose
// type is the author struct itself).
func TestDefinitionSignatureParamTypeSamePkg(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must := func(p, c string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("types.go", "package x\n\ntype Post struct{ Title string }\n\ntype Form struct{ Name string }\n")
	// PostList takes a slice of a same-package type (props-struct path); FormView
	// takes the sole struct param Form directly (BYO path).
	src := "package x\n\ncomponent PostList(posts []Post) {\n\t<ul></ul>\n}\n\ncomponent FormView(f Form) {\n\t<form>{ f.Name }</form>\n}\n"
	must("comp.gsx", src)
	lines := strings.Split(src, "\n")

	col := func(needle, line string) int { return strings.Index(line, needle) }
	find := func(sub string) (lineIdx int) {
		for i, l := range lines {
			if strings.Contains(l, sub) {
				return i
			}
		}
		t.Fatalf("line containing %q not found", sub)
		return -1
	}

	// props-struct path: `Post` in `[]Post` → types.go.
	pl := find("posts []Post")
	loc := sigTypeDefAt(t, dir, "comp.gsx", src, pl, col("Post)", lines[pl]))
	if loc == nil || !strings.HasSuffix(loc.URI, "types.go") {
		t.Fatalf("`Post` (slice param) resolved to %v, want types.go", loc)
	}

	// BYO path: `Form` in `f Form` → types.go.
	fv := find("f Form)")
	loc = sigTypeDefAt(t, dir, "comp.gsx", src, fv, col("Form)", lines[fv]))
	if loc == nil || !strings.HasSuffix(loc.URI, "types.go") {
		t.Fatalf("`Form` (BYO param) resolved to %v, want types.go", loc)
	}
}
