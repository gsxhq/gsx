package lsp

import (
	"encoding/json"
	"errors"
	"go/token"
	"go/types"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type watchedFilesAnalyzer struct {
	nilAnalyzer
	refreshCalls [][]string
	affected     []string
	analyses     []string
	refreshErr   error
	renameFacts  []ComponentParamRenameFact
	moduleCalls  int
	symbolCalls  int
}

func (a *watchedFilesAnalyzer) RefreshDisk(paths []string) ([]string, error) {
	a.refreshCalls = append(a.refreshCalls, append([]string(nil), paths...))
	return append([]string(nil), a.affected...), a.refreshErr
}

func (a *watchedFilesAnalyzer) Analyze(dir string, _ map[string][]byte) (*Package, error) {
	a.analyses = append(a.analyses, filepath.Clean(dir))
	return &Package{}, nil
}

func (a *watchedFilesAnalyzer) AnalyzeModuleParams(string, map[string][]byte) ([]ComponentParamRenameFact, error) {
	return append([]ComponentParamRenameFact(nil), a.renameFacts...), nil
}

func (a *watchedFilesAnalyzer) AnalyzeModule(string, map[string][]byte) ([]CrossRef, error) {
	a.moduleCalls++
	return nil, nil
}

func (a *watchedFilesAnalyzer) ModuleSymbols(string, map[string][]byte) ([]Symbol, error) {
	a.symbolCalls++
	return nil, nil
}

func TestInitializedRegistersExactWatchedFileSurfaceWhenSupported(t *testing.T) {
	frames := jsonFrame(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"capabilities": map[string]any{
			"workspace": map[string]any{"didChangeWatchedFiles": map[string]any{"dynamicRegistration": true}},
		}},
	})
	frames += jsonFrame(map[string]any{"jsonrpc": "2.0", "method": "initialized"})
	frames += jsonFrame(map[string]any{"jsonrpc": "2.0", "id": watchedFilesRegistrationID, "result": nil})
	frames += exitFrame()

	out := drive(t, &watchedFilesAnalyzer{}, frames)
	messages := readFrames(t, out)
	var registration map[string]json.RawMessage
	for _, message := range messages {
		var method string
		_ = json.Unmarshal(message["method"], &method)
		if method == "client/registerCapability" {
			registration = message
		}
		if string(message["id"]) == `"`+watchedFilesRegistrationID+`"` && message["error"] != nil {
			t.Fatalf("registration response was treated as a client request: %s", message["error"])
		}
	}
	if registration == nil {
		t.Fatalf("client/registerCapability request missing from %s", out)
	}
	var wire struct {
		Registrations []struct {
			Method          string                                   `json:"method"`
			RegisterOptions didChangeWatchedFilesRegistrationOptions `json:"registerOptions"`
		} `json:"registrations"`
	}
	if err := json.Unmarshal(registration["params"], &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire.Registrations) != 1 || wire.Registrations[0].Method != "workspace/didChangeWatchedFiles" {
		t.Fatalf("registrations = %+v", wire.Registrations)
	}
	got := make([]string, 0, len(wire.Registrations[0].RegisterOptions.Watchers))
	for _, watcher := range wire.Registrations[0].RegisterOptions.Watchers {
		got = append(got, watcher.GlobPattern)
	}
	want := []string{"**/*.gsx", "**/go.mod", "**/go.work", "**/gsx.toml"}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("watch globs = %v, want %v", got, want)
	}
}

func TestInitializedSurfacesMissingDynamicWatchCapability(t *testing.T) {
	out := drive(t, &watchedFilesAnalyzer{}, initFrame()+jsonFrame(map[string]any{
		"jsonrpc": "2.0", "method": "initialized",
	})+exitFrame())
	if strings.Contains(out, "client/registerCapability") {
		t.Fatalf("registered watched files without client support: %s", out)
	}
	if !strings.Contains(out, "does not support dynamic watched-file registration") {
		t.Fatalf("missing client limitation was not surfaced: %s", out)
	}
	var initialized initializeResult
	if err := json.Unmarshal(responseByID(t, out, 1)["result"], &initialized); err != nil {
		t.Fatal(err)
	}
	if initialized.Capabilities.RenameProvider != nil {
		t.Fatalf("rename advertised without a sound closed-file watch: %+v", initialized.Capabilities.RenameProvider)
	}
}

func TestRenameIsWithheldUntilWatchRegistrationAcknowledged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "page.gsx")
	source := "package page\ncomponent Card(value string) { <p>{value}</p> }\n"
	decl := strings.Index(source, "value string")
	body := strings.Index(source, "{value}") + 1
	fact := ComponentParamRenameFact{
		Key:  ComponentParamKey{PackagePath: "example.test/page", ComponentKey: ".Card", Ordinal: 0},
		Name: "value", Role: ComponentParamOrdinary,
		Origin: types.NewVar(token.NoPos, nil, "value", types.Typ[types.String]),
		Decls:  []token.Position{tokenPosition(path, source, decl)},
		Refs:   []token.Position{tokenPosition(path, source, body)},
	}
	initialize := jsonFrame(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"capabilities": map[string]any{
			"workspace": map[string]any{"didChangeWatchedFiles": map[string]any{"dynamicRegistration": true}},
		}},
	})
	rename := renameRequestFrame(2, "textDocument/rename", pathToURI(path), positionForByteOffset(source, decl+1, encUTF16), "label")

	t.Run("before acknowledgement", func(t *testing.T) {
		frames := initialize + didOpenFrame(pathToURI(path), source) + jsonFrame(map[string]any{"jsonrpc": "2.0", "method": "initialized"})
		frames += rename + exitFrame()
		out := drive(t, &watchedFilesAnalyzer{renameFacts: []ComponentParamRenameFact{fact}}, frames)
		if got := string(responseByID(t, out, 2)["error"]); !strings.Contains(got, "watched-file registration") {
			t.Fatalf("pre-ack rename response = %s", got)
		}
	})

	t.Run("registration rejected", func(t *testing.T) {
		frames := initialize + didOpenFrame(pathToURI(path), source) + jsonFrame(map[string]any{"jsonrpc": "2.0", "method": "initialized"})
		frames += jsonFrame(map[string]any{"jsonrpc": "2.0", "id": watchedFilesRegistrationID, "error": map[string]any{"code": -32603, "message": "no watcher"}})
		frames += rename + exitFrame()
		out := drive(t, &watchedFilesAnalyzer{renameFacts: []ComponentParamRenameFact{fact}}, frames)
		if got := string(responseByID(t, out, 2)["error"]); !strings.Contains(got, "watched-file registration") {
			t.Fatalf("rejected-registration rename response = %s", got)
		}
	})
}

func TestDidChangeWatchedFilesRefreshesAllEventKindsAndOpenAffectedPackages(t *testing.T) {
	root := t.TempDir()
	openPath := filepath.Join(root, "page", "page.gsx")
	openDir := filepath.Dir(openPath)
	closedDir := filepath.Join(root, "closed")
	changed := filepath.Join(closedDir, "changed.gsx")
	created := filepath.Join(closedDir, "created.gsx")
	deleted := filepath.Join(closedDir, "deleted.gsx")
	analyzer := &watchedFilesAnalyzer{affected: []string{openDir, closedDir}}
	frames := initFrame() + didOpenFrame(pathToURI(openPath), "package page\ncomponent Page() { <p/> }\n")
	frames += jsonFrame(map[string]any{
		"jsonrpc": "2.0", "method": "workspace/didChangeWatchedFiles",
		"params": map[string]any{"changes": []map[string]any{
			{"uri": pathToURI(changed), "type": fileChangeChanged},
			{"uri": pathToURI(created), "type": fileChangeCreated},
			{"uri": pathToURI(deleted), "type": fileChangeDeleted},
		}},
	})
	frames += exitFrame()
	drive(t, analyzer, frames)

	if len(analyzer.refreshCalls) != 1 {
		t.Fatalf("refresh calls = %v, want one serialized batch", analyzer.refreshCalls)
	}
	wantPaths := []string{changed, created, deleted}
	for index := range wantPaths {
		wantPaths[index] = filepath.Clean(wantPaths[index])
	}
	slices.Sort(wantPaths)
	gotPaths := analyzer.refreshCalls[0]
	slices.Sort(gotPaths)
	if !slices.Equal(gotPaths, wantPaths) {
		t.Fatalf("refreshed paths = %v, want %v", gotPaths, wantPaths)
	}
	if got := analyzer.analyses; len(got) != 2 || got[0] != openDir || got[1] != openDir {
		t.Fatalf("analyzed dirs = %v, want didOpen plus watched refresh of open affected package", got)
	}
}

func TestDidChangeWatchedFilesFailureInvalidatesFactsWithoutStaleReanalysis(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "page.gsx")
	source := "package page\ncomponent Card(value string) { <p>{value}</p> }\n"
	decl := strings.Index(source, "value string")
	fact := renameFact(path, source, ".Card", 0, "value", ComponentParamOrdinary, []int{decl}, nil)
	analyzer := &watchedFilesAnalyzer{
		affected:    []string{root},
		refreshErr:  errors.New("unreadable saved source"),
		renameFacts: []ComponentParamRenameFact{fact},
	}
	frames := jsonFrame(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"capabilities": map[string]any{
			"workspace": map[string]any{"didChangeWatchedFiles": map[string]any{"dynamicRegistration": true}},
		}},
	})
	frames += didOpenFrame(pathToURI(path), source) + jsonFrame(map[string]any{"jsonrpc": "2.0", "method": "initialized"})
	frames += jsonFrame(map[string]any{"jsonrpc": "2.0", "id": watchedFilesRegistrationID, "result": nil})
	frames += jsonFrame(map[string]any{
		"jsonrpc": "2.0", "method": "workspace/didChangeWatchedFiles",
		"params": map[string]any{"changes": []map[string]any{{"uri": pathToURI(path), "type": fileChangeChanged}}},
	})
	frames += renameRequestFrame(2, "textDocument/rename", pathToURI(path), positionForByteOffset(source, decl+1, encUTF16), "label")
	frames += jsonFrame(map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "textDocument/references",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": pathToURI(path)},
			"position":     positionForByteOffset(source, decl+1, encUTF16),
			"context":      map[string]any{"includeDeclaration": true},
		},
	})
	frames += jsonFrame(map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "workspace/symbol",
		"params": map[string]any{"query": "Card"},
	})
	frames += exitFrame()
	out := drive(t, analyzer, frames)
	if got := string(responseByID(t, out, 2)["error"]); !strings.Contains(got, "saved-source view") {
		t.Fatalf("rename after failed refresh = %s", got)
	}
	if len(analyzer.analyses) != 1 {
		t.Fatalf("failed refresh reanalyzed stale disk state: %v", analyzer.analyses)
	}
	if analyzer.moduleCalls != 0 || analyzer.symbolCalls != 0 {
		t.Fatalf("failed refresh rebuilt semantic facts from stale disk: module=%d symbols=%d", analyzer.moduleCalls, analyzer.symbolCalls)
	}
}
