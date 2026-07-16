package gen

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/lsp"
)

func TestLSPRenameComponentParameterEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("real module analysis")
	}
	root := t.TempDir()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	write := func(relative, source string) string {
		t.Helper()
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("go.mod", "module example.com/rename\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	iconA := "//go:build !never\n\npackage ui\n\ncomponent Icon[T ~string](value T) { <i>{value}</i> }\n"
	iconB := "//go:build never\n\npackage ui\n\ncomponent Icon[U ~string](value U) { <b>{value}</b> }\n"
	page := "package page\n\nimport \"example.com/rename/ui\"\n\ncomponent Page() { <ui.Icon value=\"ok\"/> }\n"
	iconAPath := write("ui/icon_a.gsx", iconA)
	iconBPath := write("ui/icon_b.gsx", iconB)
	pagePath := write("page/page.gsx", page)
	pageURI := lspTestPathURI(pagePath)
	requestPosition := lsp.Position{Line: 4, Character: strings.Index(strings.Split(page, "\n")[4], "value") + 1}

	in := frameMsg(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += frameMsg(t, map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": pageURI, "version": 1, "text": page}},
	})
	in += frameMsg(t, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "textDocument/rename",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": pageURI},
			"position":     requestPosition,
			"newName":      "label",
		},
	})
	in += frameMsg(t, map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errOut bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errOut, config{}, nil); code != 0 {
		t.Fatalf("runLSP exit = %d, stderr = %s", code, errOut.String())
	}
	response := lspTestResponse(t, out.String(), 2)
	if len(response.Error) != 0 {
		t.Fatalf("rename error = %s", response.Error)
	}
	var edit lsp.WorkspaceEdit
	if err := json.Unmarshal(response.Result, &edit); err != nil {
		t.Fatal(err)
	}
	wantEdits := map[string]int{
		lspTestPathURI(iconAPath): 2,
		lspTestPathURI(iconBPath): 2,
		pageURI:                   1,
	}
	if len(edit.Changes) != len(wantEdits) {
		t.Fatalf("rename files = %+v, want both declaration variants and caller", edit.Changes)
	}
	for uri, count := range wantEdits {
		edits := edit.Changes[uri]
		if len(edits) != count {
			t.Fatalf("edits for %s = %+v, want %d", uri, edits, count)
		}
		for _, textEdit := range edits {
			if textEdit.NewText != "label" {
				t.Fatalf("edit for %s = %+v, want label", uri, textEdit)
			}
		}
	}

	sources := map[string]string{
		lspTestPathURI(iconAPath): iconA,
		lspTestPathURI(iconBPath): iconB,
		pageURI:                   page,
	}
	paths := map[string]string{
		lspTestPathURI(iconAPath): iconAPath,
		lspTestPathURI(iconBPath): iconBPath,
		pageURI:                   pagePath,
	}
	for uri, source := range sources {
		updated := applyASCIITextEdits(t, source, edit.Changes[uri])
		if strings.Contains(updated, "value") || !strings.Contains(updated, "label") {
			t.Fatalf("updated %s did not rename the complete family:\n%s", uri, updated)
		}
		if err := os.WriteFile(paths[uri], []byte(updated), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Re-analyze the edited project from disk. This proves the returned ranges
	// produce a valid new declaration contract whose two body uses and one
	// cross-package invocation are all retained under the new semantic name.
	facts, err := newLSPAnalyzer(config{}, nil).AnalyzeModuleParams(filepath.Dir(pagePath), nil)
	if err != nil {
		t.Fatalf("re-analyze renamed project: %v", err)
	}
	if len(facts) != 1 || facts[0].Name != "label" || len(facts[0].Decls) != 2 || len(facts[0].Refs) != 3 {
		t.Fatalf("renamed semantic family = %+v, want label with 2 declarations and 3 exact refs", facts)
	}
}

func TestAnalyzeModuleParamsRejectsUnsafeCallableFamilies(t *testing.T) {
	if testing.Short() {
		t.Skip("real module analysis")
	}
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	makeModule := func(t *testing.T, files map[string]string) string {
		t.Helper()
		root := t.TempDir()
		files["go.mod"] = "module example.com/unsafe\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => " + repoRoot + "\n"
		for relative, source := range files {
			path := filepath.Join(root, relative)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return root
	}

	t.Run("plain Go callable", func(t *testing.T) {
		root := makeModule(t, map[string]string{
			"card.go":  "package unsafe\n\nimport \"github.com/gsxhq/gsx\"\n\nfunc GoCard(title string) gsx.Node { return nil }\n",
			"page.gsx": "package unsafe\n\ncomponent Page() { <GoCard title=\"ok\"/> }\n",
		})
		facts, err := newLSPAnalyzer(config{}, nil).AnalyzeModuleParams(root, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(facts) != 0 {
			t.Fatalf("plain-Go parameter facts = %+v, want none", facts)
		}
	})

	t.Run("non-equivalent GSX variants", func(t *testing.T) {
		root := makeModule(t, map[string]string{
			"icon_a.gsx": "//go:build !never\n\npackage unsafe\n\ncomponent Icon(value string) { <i>{value}</i> }\n",
			"icon_b.gsx": "//go:build never\n\npackage unsafe\n\ncomponent Icon(value int) { <b>{value}</b> }\n",
			"page.gsx":   "package unsafe\n\ncomponent Page() { <Icon value=\"ok\"/> }\n",
		})
		if facts, err := newLSPAnalyzer(config{}, nil).AnalyzeModuleParams(root, nil); err == nil {
			t.Fatalf("non-equivalent variant facts = %+v, want rename rejection", facts)
		}
	})
}

type lspTestWireResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func lspTestResponse(t *testing.T, stream string, id int) lspTestWireResponse {
	t.Helper()
	for len(stream) > 0 {
		headerEnd := strings.Index(stream, "\r\n\r\n")
		if headerEnd < 0 {
			break
		}
		lengthText := strings.TrimSpace(strings.TrimPrefix(stream[:headerEnd], "Content-Length:"))
		length, err := strconv.Atoi(lengthText)
		if err != nil {
			t.Fatal(err)
		}
		bodyStart := headerEnd + 4
		if bodyStart+length > len(stream) {
			t.Fatalf("truncated LSP frame")
		}
		body := stream[bodyStart : bodyStart+length]
		stream = stream[bodyStart+length:]
		var response lspTestWireResponse
		if err := json.Unmarshal([]byte(body), &response); err != nil {
			t.Fatal(err)
		}
		var gotID int
		if err := json.Unmarshal(response.ID, &gotID); err == nil && gotID == id {
			return response
		}
	}
	t.Fatalf("response %d not found", id)
	return lspTestWireResponse{}
}

func lspTestPathURI(path string) string {
	return "file://" + filepath.ToSlash(path)
}

func applyASCIITextEdits(t *testing.T, source string, edits []lsp.TextEdit) string {
	t.Helper()
	type offsetEdit struct {
		start, end int
		text       string
	}
	offsets := make([]offsetEdit, 0, len(edits))
	for _, edit := range edits {
		offsets = append(offsets, offsetEdit{
			start: lspTestOffset(source, edit.Range.Start),
			end:   lspTestOffset(source, edit.Range.End),
			text:  edit.NewText,
		})
	}
	sort.Slice(offsets, func(i, j int) bool { return offsets[i].start > offsets[j].start })
	for _, edit := range offsets {
		if edit.start < 0 || edit.end < edit.start || edit.end > len(source) {
			t.Fatalf("invalid edit offsets %+v for source length %d", edit, len(source))
		}
		source = source[:edit.start] + edit.text + source[edit.end:]
	}
	return source
}

func lspTestOffset(source string, position lsp.Position) int {
	offset := 0
	for range position.Line {
		newline := strings.IndexByte(source[offset:], '\n')
		if newline < 0 {
			return len(source)
		}
		offset += newline + 1
	}
	return min(offset+position.Character, len(source))
}
