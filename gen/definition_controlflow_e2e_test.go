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

// TestDefinitionControlFlowForClause drives the real runLSP harness over a
// single-package .gsx file containing a for-range control-flow block, with NO
// .x.go on disk.  It asserts three go-to-def targets inside the clause:
//
//		{ for _, post := range props.Posts { <li>{post.Title}</li> } }
//
//	  - cursor on "Posts" (in props.Posts)  → the Posts field declaration in page.gsx
//	  - cursor on "props" (in props.Posts)  → the props param in page.gsx
//	  - cursor on "post"  (in post.Title)   → the post binding in the for clause
func TestDefinitionControlFlowForClause(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	mk := func(p, c string) {
		t.Helper()
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")

	// page.gsx — deliberately no Generate call; no .x.go on disk.
	pageSrc := "package x\n\ntype Props struct{ Posts []Post }\ntype Post struct{ Title string }\n\ncomponent P(props Props) {\n\t{ for _, post := range props.Posts {\n\t\t<li>{post.Title}</li>\n\t} }\n}\n"
	mk("page.gsx", pageSrc)
	uri := "file://" + filepath.Join(dir, "page.gsx")

	lines := strings.Split(pageSrc, "\n")

	// --- cursor positions (reference sites) ---

	// "Posts" in "props.Posts {" on the for-clause line
	var postsRefLine, postsRefCol int
	for i, l := range lines {
		if c := strings.Index(l, "props.Posts"); c >= 0 {
			postsRefLine = i
			postsRefCol = c + len("props.") // column of 'P' in Posts
			break
		}
	}

	// "props" in "props.Posts" on the for-clause line (same line as above)
	var propsRefLine, propsRefCol int
	for i, l := range lines {
		if c := strings.Index(l, "props.Posts"); c >= 0 {
			propsRefLine = i
			propsRefCol = c // column of 'p' in props
			break
		}
	}

	// "post" in "{post.Title}" in the loop body
	var postRefLine, postRefCol int
	for i, l := range lines {
		if c := strings.Index(l, "{post.Title}"); c >= 0 {
			postRefLine = i
			postRefCol = c + 1 // skip '{', column of 'p' in post
			break
		}
	}

	// --- expected declaration positions ---

	// "Posts" field: "type Props struct{ Posts []Post }" (line 2 of pageSrc)
	var postsFieldLine, postsFieldCol int
	for i, l := range lines {
		if c := strings.Index(l, "struct{ Posts"); c >= 0 {
			postsFieldLine = i
			postsFieldCol = c + len("struct{ ") // column of 'P' in Posts
			break
		}
	}

	// "props" param: "component P(props Props) {" (line 5 of pageSrc)
	var propsParamLine, propsParamCol int
	for i, l := range lines {
		if c := strings.Index(l, "(props Props)"); c >= 0 {
			propsParamLine = i
			propsParamCol = c + 1 // skip '(', column of 'p' in props
			break
		}
	}

	// "post" binding: "for _, post :=" in the for-clause line (line 6 of pageSrc)
	var postBindLine, postBindCol int
	for i, l := range lines {
		if c := strings.Index(l, "for _, post :="); c >= 0 {
			postBindLine = i
			postBindCol = c + len("for _, ") // column of 'p' in post
			break
		}
	}

	// --- build LSP session with three definition requests ---

	frame := func(v any) string {
		b, _ := json.Marshal(v)
		return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
	}
	defReq := func(id, line, char int) string {
		return frame(map[string]any{
			"jsonrpc": "2.0", "id": id, "method": "textDocument/definition",
			"params": map[string]any{
				"textDocument": map[string]any{"uri": uri},
				"position":     map[string]any{"line": line, "character": char},
			},
		})
	}

	in := frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": pageSrc}}})
	in += defReq(2, postsRefLine, postsRefCol) // "Posts" reference
	in += defReq(3, propsRefLine, propsRefCol) // "props" reference
	in += defReq(4, postRefLine, postRefCol)   // "post" reference
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	outStr := out.String()

	// No .x.go location should ever appear in the responses.
	if strings.Contains(outStr, ".x.go") {
		t.Errorf("go-to-def response leaked a generated-code location; out:\n%s", outStr)
	}

	// --- sub-assertions ---

	t.Run("Posts_field", func(t *testing.T) {
		loc := definitionResult(t, outStr, 2)
		if loc == nil {
			t.Fatalf("Posts go-to-def returned null;\ncursor=L%d:C%d\nout:\n%s\nstderr:\n%s",
				postsRefLine, postsRefCol, outStr, errBuf.String())
		}
		if !strings.HasSuffix(loc.URI, "page.gsx") {
			t.Fatalf("Posts resolved to %q, want page.gsx", loc.URI)
		}
		if loc.Range.Start.Line != postsFieldLine || loc.Range.Start.Character != postsFieldCol {
			t.Fatalf("Posts landed at L%d:C%d, want L%d:C%d (the Posts field decl)",
				loc.Range.Start.Line, loc.Range.Start.Character, postsFieldLine, postsFieldCol)
		}
	})

	t.Run("props_param", func(t *testing.T) {
		loc := definitionResult(t, outStr, 3)
		if loc == nil {
			t.Fatalf("props go-to-def returned null;\ncursor=L%d:C%d\nout:\n%s\nstderr:\n%s",
				propsRefLine, propsRefCol, outStr, errBuf.String())
		}
		if !strings.HasSuffix(loc.URI, "page.gsx") {
			t.Fatalf("props resolved to %q, want page.gsx", loc.URI)
		}
		if loc.Range.Start.Line != propsParamLine || loc.Range.Start.Character != propsParamCol {
			t.Fatalf("props landed at L%d:C%d, want L%d:C%d (the props param decl)",
				loc.Range.Start.Line, loc.Range.Start.Character, propsParamLine, propsParamCol)
		}
	})

	t.Run("post_loopvar", func(t *testing.T) {
		loc := definitionResult(t, outStr, 4)
		if loc == nil {
			t.Fatalf("post go-to-def returned null;\ncursor=L%d:C%d\nout:\n%s\nstderr:\n%s",
				postRefLine, postRefCol, outStr, errBuf.String())
		}
		if !strings.HasSuffix(loc.URI, "page.gsx") {
			t.Fatalf("post resolved to %q, want page.gsx", loc.URI)
		}
		if loc.Range.Start.Line != postBindLine || loc.Range.Start.Character != postBindCol {
			t.Fatalf("post landed at L%d:C%d, want L%d:C%d (the post binding in for clause)",
				loc.Range.Start.Line, loc.Range.Start.Character, postBindLine, postBindCol)
		}
	})

	// Assert no .x.go exists on disk.
	xgoPath := strings.TrimSuffix(filepath.Join(dir, "page.gsx"), ".gsx") + ".x.go"
	if _, err := os.Stat(xgoPath); err == nil {
		t.Errorf(".x.go exists at %q; the warm Module must not write generated code", xgoPath)
	}
}
