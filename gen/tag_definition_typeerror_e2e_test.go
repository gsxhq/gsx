package gen

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTagDefinitionWithTypeErrorElsewhere pins that `gd` on a component tag
// survives an unrelated type error in the same package.
//
// Positional call planning is skipped for the WHOLE package as soon as any type
// error sits outside a component-target marker (analyze's targetPlanningReady),
// which is the ordinary state of a file mid-edit. The tag→declaration edge is
// therefore resolved by name resolution — package scope for `<Card/>`, the
// file's imports for `<ui.Panel/>` — and not from the plan
// (codegen/symbol_extras.go's componentTagOccurrences). Broken() below supplies
// the type error; the two tags it must not take down are in the same file.
//
// One module open, three definition requests on one LSP session.
func TestTagDefinitionWithTypeErrorElsewhere(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip()
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must := func(p, c string) {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("card.gsx", "package x\n\ncomponent Card(title string) {\n\t<div>{ title }</div>\n}\n")
	must("ui/panel.gsx", "package ui\n\ncomponent Panel() {\n\t<section/>\n}\n")
	// Broken() does not type-check: undefinedIdent is declared nowhere. The two
	// tags above it, and their declarations, are otherwise well-formed.
	pageSrc := "package x\n\nimport \"example.com/x/ui\"\n\ncomponent Page() {\n\t<main><Card title=\"hi\"/><ui.Panel/></main>\n}\n\ncomponent Broken() {\n\t<div>{ undefinedIdent }</div>\n}\n"
	must("page.gsx", pageSrc)
	pageURI := "file://" + filepath.Join(dir, "page.gsx")

	// cursorAt returns the LSP position of the byte at needle+delta in page.gsx.
	cursorAt := func(needle string, delta int) map[string]any {
		off := strings.Index(pageSrc, needle)
		if off < 0 {
			t.Fatalf("needle %q not in page.gsx", needle)
		}
		off += delta
		line := strings.Count(pageSrc[:off], "\n")
		lineStart := strings.LastIndexByte(pageSrc[:off], '\n') + 1
		return map[string]any{"line": line, "character": off - lineStart}
	}

	in := lspFrame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += lspFrame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": pageURI, "version": 1, "text": pageSrc, "languageId": "gsx"}}})
	requests := []struct {
		id            int
		needle        string
		delta         int
		wantFile, why string
	}{
		// Inside the tag name, past the '<' and the first letter.
		{2, "<Card", 2, "card.gsx", "same-package tag resolves through package scope"},
		{3, "<ui.Panel", 5, "ui/panel.gsx", "dotted tag resolves through the file's imports"},
		// The closing tag of the enclosing element is not a component tag; this
		// is the control that the edge is not published for arbitrary offsets.
		{4, "</main>", 3, "", "a non-component tag name resolves nothing"},
	}
	for _, request := range requests {
		in += lspFrame(map[string]any{"jsonrpc": "2.0", "id": request.id, "method": "textDocument/definition",
			"params": map[string]any{"textDocument": map[string]any{"uri": pageURI}, "position": cursorAt(request.needle, request.delta)}})
	}
	// Hover on the same <Card cursor. Tag hover is answered by the AST-only
	// branch (componentAtTag / renderComponentSig) long before the symbol index
	// is consulted, so moving the tag EDGE onto the index must leave it byte for
	// byte alone — including here, with the type error present. Pinned so a later
	// reordering of the hover cascade cannot quietly change it.
	in += lspFrame(map[string]any{"jsonrpc": "2.0", "id": 5, "method": "textDocument/hover",
		"params": map[string]any{"textDocument": map[string]any{"uri": pageURI}, "position": cursorAt("<Card", 2)}})
	in += lspFrame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "undefined: undefinedIdent") {
		t.Fatalf("fixture no longer carries the unrelated type error this test is about:\n%s", s)
	}
	if hover := string(lspTestResponse(t, s, 5).Result); !strings.Contains(hover, "component Card(title string)") {
		t.Errorf("hover on <Card with a type error present = %s, want the component signature", hover)
	}
	for _, request := range requests {
		got := lspLocationPaths(t, dir, lspTestResponse(t, s, request.id).Result)
		var want []string
		if request.wantFile != "" {
			want = []string{request.wantFile}
		}
		if len(got) != len(want) || (len(want) == 1 && got[0] != want[0]) {
			t.Errorf("gd at %q (%s) = %v, want %v", request.needle, request.why, got, want)
		}
	}
}
