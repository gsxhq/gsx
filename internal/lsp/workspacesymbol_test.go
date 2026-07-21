package lsp

import (
	"encoding/json"
	"errors"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/gsxfmt"
	"github.com/gsxhq/gsx/internal/pretty"
)

func wsSymFrame(id int, query string) string {
	return jsonFrame(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "workspace/symbol",
		"params": map[string]any{"query": query},
	})
}

// wsSymAnalyzer serves a fixed symbol list and counts ModuleSymbols calls.
type wsSymAnalyzer struct {
	calls           int
	syms            []Symbol
	symsByModule    map[string][]Symbol
	callsByModule   map[string]int
	overridesByCall map[string][]map[string][]byte
}

func (a *wsSymAnalyzer) ClearOverride(string) ([]string, error)       { return nil, nil }
func (a *wsSymAnalyzer) SetOverride(string, []byte) ([]string, error) { return nil, nil }
func (a *wsSymAnalyzer) AnalyzeEphemeral(string, string, []byte) (*Package, error) {
	return nil, errors.New("not implemented")
}

func (a *wsSymAnalyzer) Analyze(string, map[string][]byte) (*Package, error) { return &Package{}, nil }
func (a *wsSymAnalyzer) AnalyzeModule(string, map[string][]byte) ([]CrossRef, error) {
	return nil, nil
}
func (a *wsSymAnalyzer) AnalyzeModuleParams(string, map[string][]byte) ([]ComponentParamRenameFact, error) {
	return nil, nil
}
func (a *wsSymAnalyzer) ModuleSymbols(module string, overrides map[string][]byte) ([]Symbol, error) {
	a.calls++
	module = filepath.Clean(module)
	if a.callsByModule == nil {
		a.callsByModule = make(map[string]int)
	}
	a.callsByModule[module]++
	if a.overridesByCall == nil {
		a.overridesByCall = make(map[string][]map[string][]byte)
	}
	captured := make(map[string][]byte, len(overrides))
	for path, source := range overrides {
		captured[path] = slices.Clone(source)
	}
	a.overridesByCall[module] = append(a.overridesByCall[module], captured)
	if a.symsByModule != nil {
		return slices.Clone(a.symsByModule[module]), nil
	}
	return a.syms, nil
}
func (a *wsSymAnalyzer) FormatSettings(string) gsxfmt.FormatSettings {
	return gsxfmt.FormatSettings{Width: 80, TabWidth: pretty.DefaultTabWidth}
}
func (a *wsSymAnalyzer) ImportsMode(string) gsxfmt.ImportsMode {
	return gsxfmt.ImportsGoimports
}
func (a *wsSymAnalyzer) ResolveImport(string, string, string) []string { return nil }

func TestWorkspaceSymbolQueryAndCache(t *testing.T) {
	module := writeWorkspaceSymbolModule(t, filepath.Join(t.TempDir(), "module"))
	path := filepath.Join(module, "a.gsx")
	uri := pathToURI(path)
	text := "package x\n\ncomponent Card() {\n\t<div/>\n}\n"
	writeWorkspaceSymbolSource(t, path, text)
	a := &wsSymAnalyzer{syms: []Symbol{
		{Name: "Card", Kind: symKindFunction, Container: "x", NamePos: authoredTokenPosition(path, text, strings.Index(text, "Card"))},
		{Name: "Button", Kind: symKindFunction, Container: "x", NamePos: authoredTokenPosition(path, text, 0)},
	}}

	// Query "car" (case-insensitive substring) → only Card. Two queries with no
	// edit between → one ModuleSymbols call (cached).
	out := drive(t, a, workspaceSymbolInitializeFrame(module)+didOpenFrame(uri, text)+
		wsSymFrame(2, "car")+wsSymFrame(3, "car")+exitFrame())
	if !strings.Contains(out, `"name":"Card"`) {
		t.Fatalf("query 'car' should match Card:\n%s", out)
	}
	if strings.Contains(out, `"name":"Button"`) {
		t.Fatalf("query 'car' should not match Button:\n%s", out)
	}
	if a.calls != 1 {
		t.Fatalf("cached: want 1 ModuleSymbols call, got %d", a.calls)
	}

	// A didChange between two queries → two calls (cache invalidated).
	a2 := &wsSymAnalyzer{syms: a.syms}
	drive(t, a2, workspaceSymbolInitializeFrame(module)+didOpenFrame(uri, text)+
		wsSymFrame(2, "")+didChangeFrame(uri, text+"\n")+wsSymFrame(3, "")+exitFrame())
	if a2.calls != 2 {
		t.Fatalf("invalidated: want 2 ModuleSymbols calls, got %d", a2.calls)
	}
}

func TestWorkspaceSymbolMergesInitializedModulesDeterministically(t *testing.T) {
	root := t.TempDir()
	first := writeWorkspaceSymbolModule(t, filepath.Join(root, "first"))
	second := writeWorkspaceSymbolModule(t, filepath.Join(root, "second"))
	firstPath := filepath.Join(first, "alpha", "page.gsx")
	firstOtherPath := filepath.Join(first, "zeta", "page.gsx")
	secondPath := filepath.Join(second, "beta", "page.gsx")
	writeWorkspaceSymbolSource(t, firstPath, "package page\n\nvar Shared = 1\n")
	writeWorkspaceSymbolSource(t, firstOtherPath, "package page\n\nvar Shared = 3\n")
	writeWorkspaceSymbolSource(t, secondPath, "package page\n\nvar Shared = 2\n")
	firstStart := strings.Index(readWorkspaceSymbolSource(t, firstPath), "Shared")
	firstOtherStart := strings.Index(readWorkspaceSymbolSource(t, firstOtherPath), "Shared")
	secondStart := strings.Index(readWorkspaceSymbolSource(t, secondPath), "Shared")
	byModule := map[string][]Symbol{
		first: {
			{Name: "Shared", Kind: symKindVariable, Container: "page", NamePos: token.Position{Filename: firstPath, Offset: firstStart}},
			{Name: "Shared", Kind: symKindVariable, Container: "page", NamePos: token.Position{Filename: firstOtherPath, Offset: firstOtherStart}},
		},
		second: {{Name: "Shared", Kind: symKindVariable, Container: "page", NamePos: token.Position{Filename: secondPath, Offset: secondStart}}},
	}

	run := func(reverse bool) []SymbolInformation {
		t.Helper()
		returned := map[string][]Symbol{
			first:  slices.Clone(byModule[first]),
			second: slices.Clone(byModule[second]),
		}
		if reverse {
			slices.Reverse(returned[first])
			slices.Reverse(returned[second])
		}
		a := &wsSymAnalyzer{symsByModule: returned}
		out := drive(t, a, workspaceSymbolInitializeFrame(first, second)+wsSymFrame(2, "SHAR")+exitFrame())
		var symbols []SymbolInformation
		decodeResult(t, out, 2, &symbols)
		if a.callsByModule[first] != 1 || a.callsByModule[second] != 1 {
			t.Fatalf("ModuleSymbols calls = %v, want each initialized module exactly once", a.callsByModule)
		}
		return symbols
	}

	forward := run(false)
	reversed := run(true)
	forwardJSON, _ := json.Marshal(forward)
	reversedJSON, _ := json.Marshal(reversed)
	if string(forwardJSON) != string(reversedJSON) {
		t.Fatalf("workspace symbols depend on analyzer/module order:\nforward=%s\nreverse=%s", forwardJSON, reversedJSON)
	}
	if len(forward) != 3 || forward[0].Location.URI != pathToURI(firstPath) || forward[1].Location.URI != pathToURI(secondPath) || forward[2].Location.URI != pathToURI(firstOtherPath) {
		t.Fatalf("workspace symbols = %+v, want alpha then beta then zeta by workspace-relative path", forward)
	}
	if forward[0].ContainerName != "example.test/first/alpha" || forward[1].ContainerName != "example.test/second/beta" || forward[2].ContainerName != "example.test/first/zeta" {
		t.Fatalf("ambiguous package containers = %q, %q, %q; want exact module-relative import paths", forward[0].ContainerName, forward[1].ContainerName, forward[2].ContainerName)
	}
}

func TestWorkspaceSymbolContainerAmbiguityIsIndependentOfQuery(t *testing.T) {
	root := t.TempDir()
	first := writeWorkspaceSymbolModule(t, filepath.Join(root, "first"))
	second := writeWorkspaceSymbolModule(t, filepath.Join(root, "second"))
	firstPath := filepath.Join(first, "page.gsx")
	secondPath := filepath.Join(second, "page.gsx")
	firstSource := "package page\n\nvar OnlyFirst = 1\n"
	secondSource := "package page\n\nvar OnlySecond = 2\n"
	writeWorkspaceSymbolSource(t, firstPath, firstSource)
	writeWorkspaceSymbolSource(t, secondPath, secondSource)
	a := &wsSymAnalyzer{symsByModule: map[string][]Symbol{
		first:  {{Name: "OnlyFirst", Kind: symKindVariable, Container: "page", NamePos: authoredTokenPosition(firstPath, firstSource, strings.Index(firstSource, "OnlyFirst"))}},
		second: {{Name: "OnlySecond", Kind: symKindVariable, Container: "page", NamePos: authoredTokenPosition(secondPath, secondSource, strings.Index(secondSource, "OnlySecond"))}},
	}}
	out := drive(t, a, workspaceSymbolInitializeFrame(first, second)+wsSymFrame(2, "OnlyFirst")+exitFrame())
	var symbols []SymbolInformation
	decodeResult(t, out, 2, &symbols)
	if len(symbols) != 1 || symbols[0].ContainerName != "example.test/first" {
		t.Fatalf("query-filtered symbols = %+v, want module import-path container despite hidden ambiguous package", symbols)
	}
}

func TestWorkspaceSymbolOmitsStaleInBoundsNameSpan(t *testing.T) {
	module := writeWorkspaceSymbolModule(t, filepath.Join(t.TempDir(), "module"))
	path := filepath.Join(module, "page.gsx")
	source := "package page\n\nvar Staled = 1\n"
	writeWorkspaceSymbolSource(t, path, source)
	a := &wsSymAnalyzer{symsByModule: map[string][]Symbol{
		module: {{Name: "Target", Kind: symKindVariable, Container: "page", NamePos: authoredTokenPosition(path, source, strings.Index(source, "Staled"))}},
	}}
	out := drive(t, a, workspaceSymbolInitializeFrame(module)+wsSymFrame(2, "Target")+exitFrame())
	var symbols []SymbolInformation
	decodeResult(t, out, 2, &symbols)
	if len(symbols) != 0 {
		t.Fatalf("stale in-bounds symbol name was published: %+v", symbols)
	}
}

func TestWorkspaceSymbolNameValidationComparesEveryUTF8Byte(t *testing.T) {
	module := writeWorkspaceSymbolModule(t, filepath.Join(t.TempDir(), "module"))
	path := filepath.Join(module, "page.gsx")
	source := "package page\n\nvar ê = 1\n"
	writeWorkspaceSymbolSource(t, path, source)
	a := &wsSymAnalyzer{symsByModule: map[string][]Symbol{
		module: {{Name: "é", Kind: symKindVariable, Container: "page", NamePos: authoredTokenPosition(path, source, strings.Index(source, "ê"))}},
	}}
	out := drive(t, a, workspaceSymbolInitializeFrame(module)+wsSymFrame(2, "")+exitFrame())
	var symbols []SymbolInformation
	decodeResult(t, out, 2, &symbols)
	if len(symbols) != 0 {
		t.Fatalf("same-length Unicode byte mismatch was published: %+v", symbols)
	}
}

func TestWorkspaceSymbolTotalOrderBreaksRequiredTupleTies(t *testing.T) {
	module := writeWorkspaceSymbolModule(t, filepath.Join(t.TempDir(), "module"))
	path := filepath.Join(module, "page.gsx")
	source := "package page\n\nvar Shared = 1\n"
	writeWorkspaceSymbolSource(t, path, source)
	position := authoredTokenPosition(path, source, strings.Index(source, "Shared"))
	base := []Symbol{
		{Name: "Shared", Kind: symKindVariable, Container: "Zulu", NamePos: position},
		{Name: "Shared", Kind: symKindVariable, Container: "Alpha", NamePos: position},
	}
	run := func(symbols []Symbol) []SymbolInformation {
		t.Helper()
		a := &wsSymAnalyzer{symsByModule: map[string][]Symbol{module: symbols}}
		out := drive(t, a, workspaceSymbolInitializeFrame(module)+wsSymFrame(2, "Shared")+exitFrame())
		var result []SymbolInformation
		decodeResult(t, out, 2, &result)
		return result
	}
	forward := run(slices.Clone(base))
	reversedInput := slices.Clone(base)
	slices.Reverse(reversedInput)
	reversed := run(reversedInput)
	forwardJSON, _ := json.Marshal(forward)
	reversedJSON, _ := json.Marshal(reversed)
	if string(forwardJSON) != string(reversedJSON) {
		t.Fatalf("tied symbols depend on analyzer order:\nforward=%s\nreverse=%s", forwardJSON, reversedJSON)
	}
}

func TestWorkspaceSymbolPartitionsOverridesByExactModuleOwnership(t *testing.T) {
	workspace := t.TempDir()
	parent := writeWorkspaceSymbolModule(t, filepath.Join(workspace, "parent"))
	nested := writeWorkspaceSymbolModule(t, filepath.Join(parent, "nested"))
	outside := writeWorkspaceSymbolModule(t, filepath.Join(filepath.Dir(workspace), filepath.Base(workspace)+"-outside"))
	writeWorkspaceSymbolSource(t, filepath.Join(workspace, "go.work"), "go 1.26.1\n\nuse (\n\t./parent\n\t./parent/nested\n\t../"+filepath.Base(outside)+"\n)\n")
	parentPath := filepath.Join(parent, "page.gsx")
	nestedPath := filepath.Join(nested, "page.gsx")
	outsidePath := filepath.Join(outside, "page.gsx")
	siblingPath := filepath.Join(workspace, "parentish", "page.gsx")
	for _, path := range []string{parentPath, nestedPath, outsidePath, siblingPath} {
		writeWorkspaceSymbolSource(t, path, "package page\n")
	}
	a := &wsSymAnalyzer{symsByModule: map[string][]Symbol{parent: nil, nested: nil, outside: nil}}
	var frames strings.Builder
	frames.WriteString(workspaceSymbolInitializeFrame(workspace))
	for _, path := range []string{parentPath, nestedPath, outsidePath, siblingPath} {
		frames.WriteString(didOpenFrame(pathToURI(path), "package page\n// unsaved\n"))
	}
	frames.WriteString(wsSymFrame(2, "") + exitFrame())
	drive(t, a, frames.String())

	for module, wantPath := range map[string]string{parent: parentPath, nested: nestedPath, outside: outsidePath} {
		calls := a.overridesByCall[module]
		if len(calls) != 1 {
			t.Fatalf("%s override calls = %d, want 1", module, len(calls))
		}
		_, hasExpected := calls[0][wantPath]
		if len(calls[0]) != 1 || !hasExpected {
			t.Fatalf("%s overrides = %v, want only %s", module, calls[0], wantPath)
		}
	}
}

func TestWorkspaceSymbolCacheInvalidatesOnlyOwningModule(t *testing.T) {
	root := t.TempDir()
	first := writeWorkspaceSymbolModule(t, filepath.Join(root, "first"))
	second := writeWorkspaceSymbolModule(t, filepath.Join(root, "second"))
	firstPath := filepath.Join(first, "page.gsx")
	secondPath := filepath.Join(second, "page.gsx")
	writeWorkspaceSymbolSource(t, firstPath, "package first\n")
	writeWorkspaceSymbolSource(t, secondPath, "package second\n")
	a := &wsSymAnalyzer{symsByModule: map[string][]Symbol{first: nil, second: nil}}
	frames := workspaceSymbolInitializeFrame(first, second) +
		didOpenFrame(pathToURI(firstPath), "package first\n") +
		didOpenFrame(pathToURI(secondPath), "package second\n") +
		wsSymFrame(2, "") +
		didChangeFrame(pathToURI(firstPath), "package first\n// changed\n") +
		wsSymFrame(3, "") +
		workspaceSymbolFoldersChangeFrame(nil, []workspaceFolder{{URI: pathToURI(second), Name: "second"}}) +
		wsSymFrame(4, "") + exitFrame()
	var output strings.Builder
	server := NewServer(strings.NewReader(frames), &output, a)
	if err := server.Run(); err != nil {
		t.Fatal(err)
	}
	if a.callsByModule[first] != 2 || a.callsByModule[second] != 1 {
		t.Fatalf("ModuleSymbols calls = %v, want first=2 second=1", a.callsByModule)
	}
	if len(server.moduleSyms) != 1 {
		t.Fatalf("module symbol cache entries = %d, want only the retained first module", len(server.moduleSyms))
	}
}

func workspaceSymbolInitializeFrame(roots ...string) string {
	folders := make([]map[string]any, len(roots))
	for i, root := range roots {
		folders[i] = map[string]any{"uri": pathToURI(root), "name": filepath.Base(root)}
	}
	return jsonFrame(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"capabilities": map[string]any{}, "workspaceFolders": folders},
	})
}

func workspaceSymbolFoldersChangeFrame(added, removed []workspaceFolder) string {
	return jsonFrame(map[string]any{
		"jsonrpc": "2.0", "method": "workspace/didChangeWorkspaceFolders",
		"params": didChangeWorkspaceFoldersParams{Event: workspaceFoldersChangeEvent{Added: added, Removed: removed}},
	})
}

func writeWorkspaceSymbolModule(t *testing.T, root string) string {
	t.Helper()
	writeWorkspaceSymbolSource(t, filepath.Join(root, "go.mod"), "module example.test/"+filepath.Base(root)+"\n\ngo 1.26.1\n")
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(abs)
}

func writeWorkspaceSymbolSource(t *testing.T, path, source string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readWorkspaceSymbolSource(t *testing.T, path string) string {
	t.Helper()
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(source)
}
