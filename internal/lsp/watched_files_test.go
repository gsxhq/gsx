package lsp

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gsxhq/gsx/internal/gsxfmt"
)

type watchedWorkspaceSymbolAnalyzer struct {
	*wsSymAnalyzer
	refreshCalls [][]string
}

func (a *watchedWorkspaceSymbolAnalyzer) RefreshDisk(paths []string) ([]string, error) {
	a.refreshCalls = append(a.refreshCalls, slices.Clone(paths))
	return nil, nil
}

type watchedFilesAnalyzer struct {
	nilAnalyzer
	refreshCalls [][]string
	affected     []string
	analyses     []string
	refreshErr   error
	refreshErrs  []error
	refreshIndex int
	renameFacts  []ComponentParamRenameFact
	moduleCalls  int
	symbolCalls  int
	formatCalls  int
}

func dynamicRenameInitializeFrame(prepareSupport bool) string {
	return jsonFrame(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"capabilities": map[string]any{
			"workspace": map[string]any{"didChangeWatchedFiles": map[string]any{"dynamicRegistration": true}},
			"textDocument": map[string]any{"rename": map[string]any{
				"dynamicRegistration": true,
				"prepareSupport":      prepareSupport,
			}},
		}},
	})
}

func (a *watchedFilesAnalyzer) RefreshDisk(paths []string) ([]string, error) {
	a.refreshCalls = append(a.refreshCalls, append([]string(nil), paths...))
	err := a.refreshErr
	if a.refreshIndex < len(a.refreshErrs) {
		err = a.refreshErrs[a.refreshIndex]
		a.refreshIndex++
	}
	return append([]string(nil), a.affected...), err
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

func (a *watchedFilesAnalyzer) FormatSettings(string) gsxfmt.FormatSettings {
	a.formatCalls++
	return gsxfmt.FormatSettings{Width: 80, TabWidth: 4}
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

func TestRenameRegistrationFollowsWatchedFilesAckAndPrepareCapability(t *testing.T) {
	frames := dynamicRenameInitializeFrame(false)
	frames += jsonFrame(map[string]any{"jsonrpc": "2.0", "method": "initialized"})
	frames += jsonFrame(map[string]any{"jsonrpc": "2.0", "id": watchedFilesRegistrationID, "result": nil})
	frames += jsonFrame(map[string]any{"jsonrpc": "2.0", "id": "gsx-rename", "result": nil})
	frames += exitFrame()
	out := drive(t, &watchedFilesAnalyzer{}, frames)
	messages := readFrames(t, out)
	var methods []string
	var renameOptions renameRegistrationOptions
	for _, message := range messages {
		var method string
		_ = json.Unmarshal(message["method"], &method)
		if method != "client/registerCapability" {
			continue
		}
		var params struct {
			Registrations []struct {
				Method          string                    `json:"method"`
				RegisterOptions renameRegistrationOptions `json:"registerOptions"`
			} `json:"registrations"`
		}
		if err := json.Unmarshal(message["params"], &params); err != nil {
			t.Fatal(err)
		}
		if len(params.Registrations) != 1 {
			t.Fatalf("registrations = %+v", params.Registrations)
		}
		methods = append(methods, params.Registrations[0].Method)
		if params.Registrations[0].Method == "textDocument/rename" {
			renameOptions = params.Registrations[0].RegisterOptions
		}
	}
	want := []string{"workspace/didChangeWatchedFiles", "textDocument/rename"}
	if !slices.Equal(methods, want) {
		t.Fatalf("registration sequence = %v, want %v", methods, want)
	}
	if renameOptions.PrepareProvider {
		t.Fatal("rename registered prepareProvider without client prepareSupport")
	}
	if want := []documentFilter{{Scheme: "file", Pattern: "**/*.gsx"}}; !slices.Equal(renameOptions.DocumentSelector, want) {
		t.Fatalf("rename document selector = %+v, want %+v", renameOptions.DocumentSelector, want)
	}
	var initialized initializeResult
	if err := json.Unmarshal(responseByID(t, out, 1)["result"], &initialized); err != nil {
		t.Fatal(err)
	}
	if initialized.Capabilities.RenameProvider != nil {
		t.Fatalf("rename was statically advertised: %+v", initialized.Capabilities.RenameProvider)
	}
}

func TestRenameDynamicRegistrationUnsupportedNeverRegistersRename(t *testing.T) {
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
	if strings.Contains(out, `"method":"textDocument/rename"`) {
		t.Fatalf("registered rename without client dynamic support: %s", out)
	}
	if !strings.Contains(out, "does not support dynamic rename registration") {
		t.Fatalf("missing rename capability was not surfaced: %s", out)
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
	initialize := dynamicRenameInitializeFrame(true)
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

	t.Run("before rename acknowledgement", func(t *testing.T) {
		frames := initialize + didOpenFrame(pathToURI(path), source) + jsonFrame(map[string]any{"jsonrpc": "2.0", "method": "initialized"})
		frames += jsonFrame(map[string]any{"jsonrpc": "2.0", "id": watchedFilesRegistrationID, "result": nil})
		frames += rename + exitFrame()
		out := drive(t, &watchedFilesAnalyzer{renameFacts: []ComponentParamRenameFact{fact}}, frames)
		if got := string(responseByID(t, out, 2)["error"]); !strings.Contains(got, "rename registration") {
			t.Fatalf("pre-rename-ack response = %s", got)
		}
	})

	t.Run("rename registration rejected", func(t *testing.T) {
		frames := initialize + didOpenFrame(pathToURI(path), source) + jsonFrame(map[string]any{"jsonrpc": "2.0", "method": "initialized"})
		frames += jsonFrame(map[string]any{"jsonrpc": "2.0", "id": watchedFilesRegistrationID, "result": nil})
		frames += jsonFrame(map[string]any{"jsonrpc": "2.0", "id": "gsx-rename", "error": map[string]any{"code": -32603, "message": "no rename"}})
		frames += rename + exitFrame()
		out := drive(t, &watchedFilesAnalyzer{renameFacts: []ComponentParamRenameFact{fact}}, frames)
		if got := string(responseByID(t, out, 2)["error"]); !strings.Contains(got, "rename registration") {
			t.Fatalf("rejected-rename-registration response = %s", got)
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

func TestDidChangeWatchedFilesRebuildsWorkspaceOwnershipAndRecovers(t *testing.T) {
	workspace := t.TempDir()
	first := writeWorkspaceSymbolModule(t, filepath.Join(workspace, "first"))
	second := writeWorkspaceSymbolModule(t, filepath.Join(workspace, "second"))
	third := writeWorkspaceSymbolModule(t, filepath.Join(workspace, "third"))
	goWorkPath := filepath.Join(workspace, "go.work")
	writeWork := func(uses string) {
		t.Helper()
		writeWorkspaceSymbolSource(t, goWorkPath, "go 1.26.1\n\nuse (\n"+uses+")\n")
	}
	writeWork("\t./first\n\t./second\n")
	type moduleSource struct {
		root, path, source, name string
	}
	modules := []moduleSource{
		{root: first, path: filepath.Join(first, "page.gsx"), source: "package page\n\nvar First = 1\n", name: "First"},
		{root: second, path: filepath.Join(second, "page.gsx"), source: "package page\n\nvar Second = 1\n", name: "Second"},
		{root: third, path: filepath.Join(third, "page.gsx"), source: "package page\n\nvar Third = 1\n", name: "Third"},
	}
	syms := make(map[string][]Symbol, len(modules))
	for _, module := range modules {
		writeWorkspaceSymbolSource(t, module.path, module.source)
		syms[module.root] = []Symbol{{Name: module.name, Kind: symKindVariable, Container: "page", NamePos: authoredTokenPosition(module.path, module.source, strings.Index(module.source, module.name))}}
	}
	analyzer := &watchedWorkspaceSymbolAnalyzer{wsSymAnalyzer: &wsSymAnalyzer{symsByModule: syms}}
	var output bytes.Buffer
	server := NewServer(nil, &output, analyzer)
	if err := server.setWorkspaceFolders([]workspaceFolder{{URI: pathToURI(workspace), Name: "workspace"}}); err != nil {
		t.Fatal(err)
	}
	requestSymbols := func(id int) []SymbolInformation {
		t.Helper()
		params, _ := json.Marshal(workspaceSymbolParams{})
		if err := server.handleWorkspaceSymbol(frame{ID: json.RawMessage(strconv.Itoa(id)), Params: params}); err != nil {
			t.Fatal(err)
		}
		var result []SymbolInformation
		decodeResult(t, output.String(), id, &result)
		return result
	}
	watch := func(path string) {
		t.Helper()
		params, _ := json.Marshal(didChangeWatchedFilesParams{Changes: []fileEvent{{URI: pathToURI(path), Type: fileChangeChanged}}})
		if err := server.handleDidChangeWatchedFiles(frame{Params: params}); err != nil {
			t.Fatal(err)
		}
	}
	names := func(symbols []SymbolInformation) []string {
		result := make([]string, len(symbols))
		for i, symbol := range symbols {
			result[i] = symbol.Name
		}
		return result
	}

	if got := names(requestSymbols(10)); !slices.Equal(got, []string{"First", "Second"}) {
		t.Fatalf("initial workspace symbols = %v, want First Second", got)
	}
	if err := os.WriteFile(goWorkPath, []byte("go not-a-version\nuse (\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	watch(goWorkPath)
	if got := requestSymbols(11); len(got) != 0 {
		t.Fatalf("malformed workspace symbols = %+v, want fail closed", got)
	}
	if !server.diskViewValid {
		t.Fatal("malformed workspace metadata poisoned the permanent disk view")
	}
	if server.workspaceViewValid {
		t.Fatal("malformed workspace metadata left workspace ownership valid")
	}
	if want := []string{first, second}; !slices.Equal(server.workspaceModules, want) || server.workspaceModulePaths[first] != "example.test/first" || server.workspaceModulePaths[second] != "example.test/second" {
		t.Fatalf("malformed metadata partially replaced ownership: modules=%v paths=%v", server.workspaceModules, server.workspaceModulePaths)
	}
	if len(analyzer.refreshCalls) != 0 {
		t.Fatalf("malformed ownership metadata reached analyzer RefreshDisk: %v", analyzer.refreshCalls)
	}
	watch(goWorkPath)
	if got := requestSymbols(111); len(got) != 0 || server.workspaceViewValid {
		t.Fatalf("repeated malformed event restored workspace ownership: symbols=%+v valid=%t", got, server.workspaceViewValid)
	}
	if got := strings.Count(output.String(), "refresh workspace ownership for"); got != 2 {
		t.Fatalf("malformed ownership logs = %d, want exactly one per failed event", got)
	}
	if !strings.Contains(output.String(), goWorkPath) || !strings.Contains(output.String(), "parse workspace file") {
		t.Fatalf("malformed ownership error was not surfaced actionably: %s", output.String())
	}

	writeWork("\t./first\n\t./third\n")
	watch(goWorkPath)
	if got := names(requestSymbols(12)); !slices.Equal(got, []string{"First", "Third"}) {
		t.Fatalf("recovered workspace symbols = %v, want First Third", got)
	}
	if !server.workspaceViewValid {
		t.Fatal("fixed workspace metadata did not restore ownership validity")
	}
	if want := []string{first, third}; !slices.Equal(server.workspaceModules, want) {
		t.Fatalf("recovered workspace modules = %v, want %v", server.workspaceModules, want)
	}
	if _, retained := server.moduleSyms[second]; retained {
		t.Fatalf("removed module cache retained: %+v", server.moduleSyms)
	}
	if server.workspaceModuleOwners[first] != workspace || server.workspaceModuleOwners[third] != workspace || server.workspaceModulePaths[third] != "example.test/third" {
		t.Fatalf("recovered ownership metadata = owners %v paths %v", server.workspaceModuleOwners, server.workspaceModulePaths)
	}
	if len(analyzer.refreshCalls) != 1 || !slices.Equal(analyzer.refreshCalls[0], []string{goWorkPath}) {
		t.Fatalf("recovered metadata refresh calls = %v, want fixed go.work once", analyzer.refreshCalls)
	}
	if analyzer.callsByModule[first] != 2 || analyzer.callsByModule[second] != 1 || analyzer.callsByModule[third] != 1 {
		t.Fatalf("module symbol calls = %v, want first recovery refresh, removed second once, new third once", analyzer.callsByModule)
	}
}

func TestDidChangeWatchedFilesRefreshesModuleDirectiveContainersAndRecovers(t *testing.T) {
	workspace := t.TempDir()
	first := writeWorkspaceSymbolModule(t, filepath.Join(workspace, "first"))
	second := writeWorkspaceSymbolModule(t, filepath.Join(workspace, "second"))
	firstPath := filepath.Join(first, "page.gsx")
	secondPath := filepath.Join(second, "page.gsx")
	firstSource := "package page\n\nvar SharedFirst = 1\n"
	secondSource := "package page\n\nvar SharedSecond = 1\n"
	writeWorkspaceSymbolSource(t, firstPath, firstSource)
	writeWorkspaceSymbolSource(t, secondPath, secondSource)
	analyzer := &watchedWorkspaceSymbolAnalyzer{wsSymAnalyzer: &wsSymAnalyzer{symsByModule: map[string][]Symbol{
		first:  {{Name: "SharedFirst", Kind: symKindVariable, Container: "page", NamePos: authoredTokenPosition(firstPath, firstSource, strings.Index(firstSource, "SharedFirst"))}},
		second: {{Name: "SharedSecond", Kind: symKindVariable, Container: "page", NamePos: authoredTokenPosition(secondPath, secondSource, strings.Index(secondSource, "SharedSecond"))}},
	}}}
	var output bytes.Buffer
	server := NewServer(nil, &output, analyzer)
	if err := server.setWorkspaceFolders([]workspaceFolder{{URI: pathToURI(first)}, {URI: pathToURI(second)}}); err != nil {
		t.Fatal(err)
	}
	request := func(id int) []SymbolInformation {
		params, _ := json.Marshal(workspaceSymbolParams{Query: "Shared"})
		if err := server.handleWorkspaceSymbol(frame{ID: json.RawMessage(strconv.Itoa(id)), Params: params}); err != nil {
			t.Fatal(err)
		}
		var result []SymbolInformation
		decodeResult(t, output.String(), id, &result)
		return result
	}
	watch := func() {
		params, _ := json.Marshal(didChangeWatchedFilesParams{Changes: []fileEvent{{URI: pathToURI(filepath.Join(first, "go.mod")), Type: fileChangeChanged}}})
		if err := server.handleDidChangeWatchedFiles(frame{Params: params}); err != nil {
			t.Fatal(err)
		}
	}
	if got := request(20); len(got) != 2 || got[0].ContainerName != "example.test/first" {
		t.Fatalf("initial ambiguous containers = %+v", got)
	}
	if err := os.WriteFile(filepath.Join(first, "go.mod"), []byte("module\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	watch()
	if got := request(21); len(got) != 0 {
		t.Fatalf("malformed go.mod workspace symbols = %+v, want fail closed", got)
	}
	if !server.diskViewValid || len(analyzer.refreshCalls) != 0 {
		t.Fatalf("malformed go.mod poisoned disk view or reached analyzer: valid=%t calls=%v", server.diskViewValid, analyzer.refreshCalls)
	}
	if server.workspaceViewValid {
		t.Fatal("malformed go.mod left workspace ownership valid")
	}
	if server.workspaceModulePaths[first] != "example.test/first" {
		t.Fatalf("malformed go.mod partially replaced module path: %v", server.workspaceModulePaths)
	}
	if !strings.Contains(output.String(), filepath.Join(first, "go.mod")) || !strings.Contains(output.String(), "parse module file") {
		t.Fatalf("malformed go.mod ownership error was not surfaced: %s", output.String())
	}
	writeWorkspaceSymbolSource(t, filepath.Join(first, "go.mod"), "module example.test/renamed\n\ngo 1.26.1\n")
	watch()
	got := request(22)
	if !server.workspaceViewValid {
		t.Fatal("fixed go.mod did not restore workspace ownership validity")
	}
	if len(got) != 2 || got[0].ContainerName != "example.test/renamed" {
		t.Fatalf("refreshed module directive containers = %+v, want example.test/renamed", got)
	}
	if len(analyzer.refreshCalls) != 1 || !slices.Equal(analyzer.refreshCalls[0], []string{filepath.Join(first, "go.mod")}) {
		t.Fatalf("recovered go.mod refresh calls = %v, want fixed module metadata once", analyzer.refreshCalls)
	}
	writeWorkspaceSymbolSource(t, filepath.Join(first, "go.mod"), "module example.test/renamed\n\ngo 1.26.1\n// same module identity, changed build universe\n")
	watch()
	_ = request(23)
	if analyzer.callsByModule[first] != 3 || analyzer.callsByModule[second] != 3 {
		t.Fatalf("same-identity go.mod edit reused symbol caches: calls=%v, want both modules rebuilt", analyzer.callsByModule)
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
	frames := dynamicRenameInitializeFrame(true)
	frames += didOpenFrame(pathToURI(path), source) + jsonFrame(map[string]any{"jsonrpc": "2.0", "method": "initialized"})
	frames += jsonFrame(map[string]any{"jsonrpc": "2.0", "id": watchedFilesRegistrationID, "result": nil})
	frames += jsonFrame(map[string]any{"jsonrpc": "2.0", "id": renameRegistrationID, "result": nil})
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
	if !strings.Contains(out, "remains disabled until the language server restarts") {
		t.Fatalf("failed refresh did not explain the persistent recovery boundary: %s", out)
	}
	if len(analyzer.analyses) != 1 {
		t.Fatalf("failed refresh reanalyzed stale disk state: %v", analyzer.analyses)
	}
	if analyzer.moduleCalls != 0 || analyzer.symbolCalls != 0 {
		t.Fatalf("failed refresh rebuilt semantic facts from stale disk: module=%d symbols=%d", analyzer.moduleCalls, analyzer.symbolCalls)
	}
}

func TestDiskViewFailureSurvivesUnrelatedSuccessfulWatchEvent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "page.gsx")
	source := "package page\ncomponent Card(value string) { <p>{value}</p> }\n"
	decl := strings.Index(source, "value string")
	fact := renameFact(path, source, ".Card", 0, "value", ComponentParamOrdinary, []int{decl}, nil)
	analyzer := &watchedFilesAnalyzer{
		affected:    []string{root},
		refreshErrs: []error{errors.New("unreadable saved source"), nil},
		renameFacts: []ComponentParamRenameFact{fact},
	}
	frames := dynamicRenameInitializeFrame(true)
	frames += didOpenFrame(pathToURI(path), source)
	frames += jsonFrame(map[string]any{"jsonrpc": "2.0", "method": "initialized"})
	frames += jsonFrame(map[string]any{"jsonrpc": "2.0", "id": watchedFilesRegistrationID, "result": nil})
	frames += jsonFrame(map[string]any{"jsonrpc": "2.0", "id": renameRegistrationID, "result": nil})
	frames += jsonFrame(map[string]any{
		"jsonrpc": "2.0", "method": "workspace/didChangeWatchedFiles",
		"params": map[string]any{"changes": []map[string]any{{"uri": pathToURI(path), "type": fileChangeChanged}}},
	})
	frames += jsonFrame(map[string]any{
		"jsonrpc": "2.0", "method": "workspace/didChangeWatchedFiles",
		"params": map[string]any{"changes": []map[string]any{{"uri": pathToURI(filepath.Join(root, "gsx.toml")), "type": fileChangeChanged}}},
	})
	frames += renameRequestFrame(2, "textDocument/rename", pathToURI(path), positionForByteOffset(source, decl+1, encUTF16), "label")
	frames += exitFrame()
	out := drive(t, analyzer, frames)
	if got := string(responseByID(t, out, 2)["error"]); !strings.Contains(got, "saved-source view") {
		t.Fatalf("rename after failure then unrelated success = %s", got)
	}
	if len(analyzer.analyses) != 1 {
		t.Fatalf("unrelated successful event reanalyzed invalid view: %v", analyzer.analyses)
	}
}

func TestInvalidDiskViewBlocksEditorAnalysisAndReadIntelligence(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "page.gsx")
	otherPath := filepath.Join(root, "other.gsx")
	source := "package page\ncomponent Page() { <p/> }\n"
	analyzer := &watchedFilesAnalyzer{affected: []string{root}, refreshErr: errors.New("unreadable saved source")}
	var output bytes.Buffer
	server := NewServer(nil, &output, analyzer)
	var scheduled int
	server.schedule = func(time.Duration, func()) func() {
		scheduled++
		return func() {}
	}

	openParams, _ := json.Marshal(didOpenParams{TextDocument: textDocumentItem{URI: pathToURI(path), Version: 1, Text: source}})
	if err := server.handleDidOpen(frame{Params: openParams}); err != nil {
		t.Fatal(err)
	}
	if len(analyzer.analyses) != 1 {
		t.Fatalf("initial analyses = %v, want one", analyzer.analyses)
	}
	watchParams, _ := json.Marshal(didChangeWatchedFilesParams{Changes: []fileEvent{{URI: pathToURI(path), Type: fileChangeChanged}}})
	if err := server.handleDidChangeWatchedFiles(frame{Params: watchParams}); err != nil {
		t.Fatal(err)
	}
	changeParams, _ := json.Marshal(didChangeParams{
		TextDocument:   versionedTextDocumentIdentifier{URI: pathToURI(path), Version: 2},
		ContentChanges: []contentChange{{Text: source + "// changed\n"}},
	})
	if err := server.handleDidChange(frame{Params: changeParams}); err != nil {
		t.Fatal(err)
	}
	otherOpen, _ := json.Marshal(didOpenParams{TextDocument: textDocumentItem{URI: pathToURI(otherPath), Version: 1, Text: source}})
	if err := server.handleDidOpen(frame{Params: otherOpen}); err != nil {
		t.Fatal(err)
	}
	closeParams, _ := json.Marshal(didCloseParams{TextDocument: textDocumentIdentifier{URI: pathToURI(path)}})
	if err := server.handleDidClose(frame{Params: closeParams}); err != nil {
		t.Fatal(err)
	}
	if len(analyzer.analyses) != 1 || scheduled != 0 || len(server.pkgs) != 0 {
		t.Fatalf("invalid view launched or retained analysis: analyses=%v scheduled=%d pkgs=%v", analyzer.analyses, scheduled, server.pkgs)
	}

	request := func(id int, method string, params any) {
		t.Helper()
		raw, _ := json.Marshal(params)
		if err := server.handle(frame{ID: json.RawMessage(strconv.Itoa(id)), Method: method, Params: raw}); err != nil {
			t.Fatal(err)
		}
	}
	positionParams := textDocumentPositionParams{TextDocument: textDocumentIdentifier{URI: pathToURI(otherPath)}}
	request(10, "textDocument/definition", positionParams)
	request(11, "textDocument/hover", positionParams)
	request(12, "textDocument/documentSymbol", documentSymbolParams{TextDocument: textDocumentIdentifier{URI: pathToURI(otherPath)}})
	request(13, "textDocument/codeAction", codeActionParams{TextDocument: textDocumentIdentifier{URI: pathToURI(otherPath)}})
	if analyzer.formatCalls != 0 {
		t.Fatalf("invalid view served analysis-backed code actions: format calls=%d", analyzer.formatCalls)
	}
	for _, id := range []int{10, 11} {
		if got := string(responseByID(t, output.String(), id)["result"]); got != "null" {
			t.Fatalf("invalid-view response %d = %s, want null", id, got)
		}
	}
	for _, id := range []int{12, 13} {
		if got := string(responseByID(t, output.String(), id)["result"]); got != "[]" {
			t.Fatalf("invalid-view response %d = %s, want []", id, got)
		}
	}
}
