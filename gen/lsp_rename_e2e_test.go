package gen

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
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

func TestLSPRenameComponentParameterRejectsSemanticNamespaceCollisions(t *testing.T) {
	if testing.Short() {
		t.Skip("real module analysis")
	}
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		files      map[string]string
		requestRel string
		needle     string
		newName    string
	}{
		{
			name: "import qualifier used by equivalent variant body",
			files: map[string]string{
				"card_a.gsx": "//go:build !never\n\npackage unsafe\n\ncomponent Card(value string) { <i>{value}</i> }\n",
				"card_b.gsx": "//go:build never\n\npackage unsafe\n\nimport \"strings\"\n\ncomponent Card(value string) { <b>{strings.ToUpper(value)}</b> }\n",
			},
			requestRel: "card_a.gsx", needle: "value string", newName: "strings",
		},
		{
			name: "dot imported object used by equivalent variant body",
			files: map[string]string{
				"card_a.gsx": "//go:build !never\n\npackage unsafe\n\ncomponent Card(value string) { <i>{value}</i> }\n",
				"card_b.gsx": "//go:build never\n\npackage unsafe\n\nimport . \"strings\"\n\ncomponent Card(value string) { <b>{ToUpper(value)}</b> }\n",
			},
			requestRel: "card_a.gsx", needle: "value string", newName: "ToUpper",
		},
		{
			name: "receiver in equivalent method variant",
			files: map[string]string{
				"card_a.gsx": "//go:build !never\n\npackage unsafe\n\ntype Panel struct{}\n\ncomponent (p Panel) Card(value string) { <i>{value}</i> }\n",
				"card_b.gsx": "//go:build never\n\npackage unsafe\n\ncomponent (q Panel) Card(value string) { <b>{value}</b> }\n",
			},
			requestRel: "card_a.gsx", needle: "value string", newName: "q",
		},
		{
			name: "body local in equivalent variant",
			files: map[string]string{
				"card_a.gsx": "//go:build !never\n\npackage unsafe\n\ncomponent Card(value string) { <i>{value}</i> }\n",
				"card_b.gsx": "//go:build never\n\npackage unsafe\n\ncomponent Card(value string) { {{ label := \"local\" }}<b>{value}{label}</b> }\n",
			},
			requestRel: "card_a.gsx", needle: "value string", newName: "label",
		},
		{
			name: "type switch implicit case variable in equivalent variant",
			files: map[string]string{
				"card_a.gsx": "//go:build !never\n\npackage unsafe\n\ncomponent Card(value string) { <i>{value}</i> }\n",
				"card_b.gsx": "//go:build never\n\npackage unsafe\n\ncomponent Card(value string) { <b>{func() string { switch label := any(value).(type) { case string: _ = label; return value; default: return \"\" } }()}</b> }\n",
			},
			requestRel: "card_a.gsx", needle: "value string", newName: "label",
		},
		{
			name: "type parameter in equivalent generic variant",
			files: map[string]string{
				"card_a.gsx": "//go:build !never\n\npackage unsafe\n\ncomponent Card[T ~string](value T) { <i>{value}</i> }\n",
				"card_b.gsx": "//go:build never\n\npackage unsafe\n\ncomponent Card[U ~string](value U) { <b>{value}</b> }\n",
			},
			requestRel: "card_a.gsx", needle: "value T", newName: "U",
		},
		{
			name: "fallthrough attribute at target call",
			files: map[string]string{
				"page.gsx": "package unsafe\n\nimport \"github.com/gsxhq/gsx\"\n\ncomponent Card(value string, attrs gsx.Attrs) { <div {attrs...}>{value}</div> }\ncomponent Page() { <Card value=\"ok\" { if true { label=\"fallthrough\" } }/> }\n",
			},
			requestRel: "page.gsx", needle: "value=\"ok\"", newName: "label",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeLSPRenameModule(t, root, repoRoot, test.files)
			path := filepath.Join(root, test.requestRel)
			source := test.files[test.requestRel]
			offset := strings.Index(source, test.needle)
			if offset < 0 {
				t.Fatalf("request needle %q not found", test.needle)
			}
			response := runLSPRename(t, path, source, offset+1, test.newName)
			if len(response.Error) == 0 {
				t.Fatalf("rename to %q returned WorkspaceEdit %s, want semantic collision rejection", test.newName, response.Result)
			}
			if !strings.Contains(string(response.Error), "conflicts with an existing declaration or call-site attribute") {
				t.Fatalf("rename error = %s, want semantic collision rejection", response.Error)
			}
		})
	}
}

func TestLSPRenameComponentParameterAllowsQualifiedSelectorNames(t *testing.T) {
	if testing.Short() {
		t.Skip("real module analysis")
	}
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		source  string
		newName string
	}{
		{
			name: "package selector member",
			source: "package safe\n\nimport \"strings\"\n\n" +
				"component Card(value string) { <b>{strings.ToUpper(value)}</b> }\n",
			newName: "ToUpper",
		},
		{
			name: "field selector member",
			source: "package safe\n\ntype Item struct { Label string }\n\n" +
				"component Card(value string, item Item) { <b>{item.Label}{value}</b> }\n",
			newName: "Label",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeLSPRenameModule(t, root, repoRoot, map[string]string{"card.gsx": test.source})
			path := filepath.Join(root, "card.gsx")
			offset := strings.Index(test.source, "value string")
			response := runLSPRename(t, path, test.source, offset+1, test.newName)
			if len(response.Error) != 0 {
				t.Fatalf("safe rename to %q rejected: %s", test.newName, response.Error)
			}
			var edit lsp.WorkspaceEdit
			if err := json.Unmarshal(response.Result, &edit); err != nil {
				t.Fatal(err)
			}
			updated := applyASCIITextEdits(t, test.source, edit.Changes[lspTestPathURI(path)])
			if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
				t.Fatal(err)
			}
			facts, err := newLSPAnalyzer(config{}, nil).AnalyzeModuleParams(root, nil)
			if err != nil {
				t.Fatalf("re-analyze safe rename: %v\n%s", err, updated)
			}
			if !slices.ContainsFunc(facts, func(fact lsp.ComponentParamRenameFact) bool {
				return fact.Name == test.newName && fact.Key.ComponentKey == ".Card" && fact.Key.Ordinal == 0
			}) {
				t.Fatalf("renamed fact missing from %+v", facts)
			}
		})
	}
}

func writeLSPRenameModule(t *testing.T, root, repoRoot string, files map[string]string) {
	t.Helper()
	all := make(map[string]string, len(files)+1)
	maps.Copy(all, files)
	all["go.mod"] = "module example.com/unsafe\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => " + repoRoot + "\n"
	for relative, source := range all {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func runLSPRename(t *testing.T, path, source string, offset int, newName string) lspTestWireResponse {
	t.Helper()
	uri := lspTestPathURI(path)
	position := lsp.Position{Line: strings.Count(source[:offset], "\n")}
	lineStart := strings.LastIndex(source[:offset], "\n") + 1
	position.Character = offset - lineStart
	in := frameMsg(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += frameMsg(t, map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": source}},
	})
	in += frameMsg(t, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "textDocument/rename",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"position":     position,
			"newName":      newName,
		},
	})
	in += frameMsg(t, map[string]any{"jsonrpc": "2.0", "method": "exit"})
	var out, errOut bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errOut, config{}, nil); code != 0 {
		t.Fatalf("runLSP exit = %d, stderr = %s", code, errOut.String())
	}
	return lspTestResponse(t, out.String(), 2)
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
