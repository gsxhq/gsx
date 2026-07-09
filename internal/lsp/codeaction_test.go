package lsp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/gsxfmt"
)

// codeActions drives one textDocument/codeAction request against a server backed
// by nilAnalyzer, and returns the decoded result.
func codeActions(t *testing.T, uri, src string, only []string) []CodeAction {
	t.Helper()
	return codeActionsWith(t, uri, src, only, nilAnalyzer{})
}

// codeActionsWith is codeActions with an explicit Analyzer, so a test can vary
// the reported [formatter] imports mode.
func codeActionsWith(t *testing.T, uri, src string, only []string, a Analyzer) []CodeAction {
	t.Helper()
	in := framed(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += framed(t, map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": "gsx", "version": 1, "text": src}},
	})
	ctx := map[string]any{"diagnostics": []any{}}
	if only != nil {
		ctx["only"] = only
	}
	in += framed(t, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "textDocument/codeAction",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"range":        map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 0}},
			"context":      ctx,
		},
	})
	in += framed(t, map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out bytes.Buffer
	srv := NewServer(strings.NewReader(in), &out, a)
	if err := srv.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, m := range readFrames(t, out.String()) {
		raw, ok := m["result"]
		if !ok {
			continue
		}
		var actions []CodeAction
		if err := json.Unmarshal(raw, &actions); err == nil && len(actions) > 0 {
			return actions
		}
		// An empty result array decodes to len 0; distinguish it from the
		// initialize result (an object, which fails to unmarshal into a slice).
		if string(raw) == "[]" {
			return nil
		}
	}
	return nil
}

const dupImportSrc = "package x\n\nimport \"strings\"\n\nimport (\n\t\"fmt\"\n\n\t\"strings\"\n)\n\n" +
	"component C() {\n\t<p>{ fmt.Sprint(strings.ToUpper(\"x\")) }</p>\n}\n"

// TestCodeActionOrganizeImports: the action is offered and its edit merges and
// dedups the imports.
func TestCodeActionOrganizeImports(t *testing.T) {
	actions := codeActions(t, "file:///tmp/c.gsx", dupImportSrc, []string{"source.organizeImports"})
	if len(actions) != 1 {
		t.Fatalf("want 1 action, got %d", len(actions))
	}
	a := actions[0]
	if a.Kind != "source.organizeImports" {
		t.Fatalf("kind = %q", a.Kind)
	}
	if a.Edit == nil || len(a.Edit.Changes["file:///tmp/c.gsx"]) != 1 {
		t.Fatalf("want one whole-document edit, got %+v", a.Edit)
	}
	got := a.Edit.Changes["file:///tmp/c.gsx"][0].NewText
	if n := strings.Count(got, "\"strings\""); n != 1 {
		t.Fatalf("duplicate not deduped (%d):\n%s", n, got)
	}
	if n := strings.Count(got, "import"); n != 1 {
		t.Fatalf("declarations not merged (%d):\n%s", n, got)
	}
}

// TestCodeActionOrganizeImportsIgnoresGofmtMode: the action organizes even when
// the configured formatter mode is gofmt — organizing is its entire purpose.
func TestCodeActionOrganizeImportsIgnoresGofmtMode(t *testing.T) {
	// gofmtAnalyzer reports ImportsMode = gofmt; the action must ignore it.
	actions := codeActionsWith(t, "file:///tmp/c.gsx", dupImportSrc, []string{"source.organizeImports"}, gofmtAnalyzer{})
	if len(actions) != 1 {
		t.Fatalf("action must be offered under gofmt mode, got %d", len(actions))
	}
	got := actions[0].Edit.Changes["file:///tmp/c.gsx"][0].NewText
	if n := strings.Count(got, "\"strings\""); n != 1 {
		t.Fatalf("action must organize under gofmt mode (%d):\n%s", n, got)
	}
}

// TestCodeActionNoOpWhenAlreadyOrganized: an already-canonical document yields
// no action, so codeActionsOnSave is a no-op.
func TestCodeActionNoOpWhenAlreadyOrganized(t *testing.T) {
	src := "package x\n\nimport (\n\t\"fmt\"\n\n\t\"strings\"\n)\n\n" +
		"component C() {\n\t<p>{ fmt.Sprint(strings.ToUpper(\"x\")) }</p>\n}\n"
	if got := codeActions(t, "file:///tmp/c.gsx", src, []string{"source.organizeImports"}); len(got) != 0 {
		t.Fatalf("want no action for canonical doc, got %+v", got)
	}
}

// TestCodeActionSkipsNonGsx: gopls owns .go files.
func TestCodeActionSkipsNonGsx(t *testing.T) {
	if got := codeActions(t, "file:///tmp/c.go", dupImportSrc, []string{"source.organizeImports"}); len(got) != 0 {
		t.Fatalf("want no action for .go, got %+v", got)
	}
}

// TestCodeActionHonorsOnlyFilter: a request restricted to quickfix gets nothing.
func TestCodeActionHonorsOnlyFilter(t *testing.T) {
	if got := codeActions(t, "file:///tmp/c.gsx", dupImportSrc, []string{"quickfix"}); len(got) != 0 {
		t.Fatalf("want no action when only=[quickfix], got %+v", got)
	}
}

// TestCodeActionOnlySourcePrefixMatches: "source" is a prefix of
// "source.organizeImports" in LSP's kind hierarchy, so it must match.
func TestCodeActionOnlySourcePrefixMatches(t *testing.T) {
	if got := codeActions(t, "file:///tmp/c.gsx", dupImportSrc, []string{"source"}); len(got) != 1 {
		t.Fatalf("only=[source] must match, got %d", len(got))
	}
}

// TestCodeActionEmptyOnlyOffersAction: an unrestricted request offers it.
func TestCodeActionEmptyOnlyOffersAction(t *testing.T) {
	if got := codeActions(t, "file:///tmp/c.gsx", dupImportSrc, nil); len(got) != 1 {
		t.Fatalf("unrestricted request must offer the action, got %d", len(got))
	}
}

// TestCodeActionParseErrorYieldsNothing: a mid-edit buffer never gets a
// destructive whole-file edit.
func TestCodeActionParseErrorYieldsNothing(t *testing.T) {
	bad := "package x\n\ncomponent C() {\n\t<p>unclosed\n"
	if got := codeActions(t, "file:///tmp/c.gsx", bad, []string{"source.organizeImports"}); len(got) != 0 {
		t.Fatalf("want no action for unparseable buffer, got %+v", got)
	}
}

// TestInitializeAdvertisesOrganizeImports: the capability names the kind so the
// client can wire editor.codeActionsOnSave.
func TestInitializeAdvertisesOrganizeImports(t *testing.T) {
	in := framed(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += framed(t, map[string]any{"jsonrpc": "2.0", "method": "exit"})
	var out bytes.Buffer
	srv := NewServer(strings.NewReader(in), &out, nilAnalyzer{})
	if err := srv.Run(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"source.organizeImports"`) {
		t.Fatalf("initialize did not advertise source.organizeImports:\n%s", out.String())
	}
}

// gofmtAnalyzer reports gofmt mode, to prove the code action ignores it and
// organizes anyway. It embeds nilAnalyzer for every other Analyzer method.
type gofmtAnalyzer struct{ nilAnalyzer }

func (gofmtAnalyzer) ImportsMode(string) gsxfmt.ImportsMode { return gsxfmt.ImportsGofmt }
