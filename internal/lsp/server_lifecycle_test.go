package lsp

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/gsxfmt"
	"github.com/gsxhq/gsx/internal/pretty"
)

// nilAnalyzer satisfies Analyzer and returns nothing.
type nilAnalyzer struct{}

func (nilAnalyzer) SetOverride(string, []byte) ([]string, error)        { return nil, nil }
func (nilAnalyzer) Analyze(string, map[string][]byte) (*Package, error) { return &Package{}, nil }
func (nilAnalyzer) AnalyzeEphemeral(string, string, []byte) (*Package, error) {
	return nil, errors.New("not implemented")
}
func (nilAnalyzer) AnalyzeEphemeralNonBlocking(string, string, []byte) (*Package, bool, error) {
	return nil, true, errors.New("not implemented")
}
func (nilAnalyzer) ClearOverride(string) ([]string, error) { return nil, nil }
func (nilAnalyzer) AnalyzeModule(string, map[string][]byte) ([]CrossRef, error) {
	return nil, nil
}
func (nilAnalyzer) AnalyzeModuleParams(string, map[string][]byte) ([]ComponentParamRenameFact, error) {
	return nil, nil
}
func (nilAnalyzer) ModuleSymbols(string, map[string][]byte) ([]Symbol, error) { return nil, nil }
func (nilAnalyzer) FormatSettings(string) gsxfmt.FormatSettings {
	return gsxfmt.FormatSettings{Width: 80, TabWidth: pretty.DefaultTabWidth}
}
func (nilAnalyzer) ImportsMode(string) gsxfmt.ImportsMode         { return gsxfmt.ImportsGoimports }
func (nilAnalyzer) ResolveImport(string, string, string) []string { return nil }
func (nilAnalyzer) ExportedSymbols(string, string) []ImportSymbol { return nil }
func (nilAnalyzer) ImportablePackages(string) []ImportablePackage { return nil }

// framed wraps a JSON-RPC body in Content-Length framing.
func framed(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
}

// readFrames splits a server's output stream into decoded frames (by method or,
// for responses, raw bodies).
func readFrames(t *testing.T, out string) []map[string]json.RawMessage {
	t.Helper()
	var msgs []map[string]json.RawMessage
	rest := out
	for {
		i := strings.Index(rest, "Content-Length: ")
		if i < 0 {
			break
		}
		rest = rest[i+len("Content-Length: "):]
		j := strings.Index(rest, "\r\n\r\n")
		if j < 0 {
			break
		}
		n, _ := strconv.Atoi(strings.TrimSpace(rest[:j]))
		body := rest[j+4 : j+4+n]
		rest = rest[j+4+n:]
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			t.Fatalf("decode frame: %v", err)
		}
		msgs = append(msgs, m)
	}
	return msgs
}

func TestServerInitializeNegotiatesUTF8(t *testing.T) {
	in := framed(t, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"capabilities": map[string]any{"general": map[string]any{"positionEncodings": []string{"utf-8", "utf-16"}}}},
	})
	in += framed(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "shutdown"})
	in += framed(t, map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out bytes.Buffer
	srv := NewServer(strings.NewReader(in), &out, nilAnalyzer{})
	if err := srv.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if srv.enc != encUTF8 {
		t.Fatalf("enc = %v, want encUTF8", srv.enc)
	}
	msgs := readFrames(t, out.String())
	// First response is the initialize result; assert positionEncoding.
	var res struct {
		Capabilities serverCapabilities `json:"capabilities"`
	}
	if err := json.Unmarshal(msgs[0]["result"], &res); err != nil {
		t.Fatalf("init result: %v", err)
	}
	if res.Capabilities.PositionEncoding != "utf-8" || res.Capabilities.TextDocumentSync != 1 {
		t.Fatalf("caps = %+v", res.Capabilities)
	}
}

func TestServerInitializeDefaultsUTF16(t *testing.T) {
	in := framed(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += framed(t, map[string]any{"jsonrpc": "2.0", "method": "exit"})
	var out bytes.Buffer
	srv := NewServer(strings.NewReader(in), &out, nilAnalyzer{})
	if err := srv.Run(); err != nil {
		t.Fatal(err)
	}
	if srv.enc != encUTF16 {
		t.Fatalf("enc = %v, want encUTF16", srv.enc)
	}
}

func TestInitializeAdvertisesSymbolProviders(t *testing.T) {
	out := drive(t, nilAnalyzer{}, initFrame()+exitFrame())
	for _, want := range []string{`"documentSymbolProvider":true`, `"workspaceSymbolProvider":true`} {
		if !strings.Contains(out, want) {
			t.Errorf("initialize result missing %s:\n%s", want, out)
		}
	}
}

func TestInitializeAdvertisesWorkspaceFolderLifecycle(t *testing.T) {
	out := drive(t, nilAnalyzer{}, initFrame()+exitFrame())
	var initialized initializeResult
	if err := json.Unmarshal(responseByID(t, out, 1)["result"], &initialized); err != nil {
		t.Fatal(err)
	}
	got := initialized.Capabilities.Workspace.WorkspaceFolders
	if !got.Supported || !got.ChangeNotifications {
		t.Fatalf("workspace folder capabilities = %+v, want supported change notifications", got)
	}
}

func TestInitializeWorkspaceOwnershipPrecedence(t *testing.T) {
	workspace := writeTestModule(t, filepath.Join(t.TempDir(), "workspace"), "example.test/workspace")
	fallback := writeTestModule(t, filepath.Join(t.TempDir(), "fallback"), "example.test/fallback")

	tests := []struct {
		name   string
		params map[string]any
		want   []string
	}{
		{
			name: "workspace folders precede root URI",
			params: map[string]any{
				"capabilities":     map[string]any{},
				"rootUri":          pathToURI(fallback),
				"workspaceFolders": []map[string]any{{"uri": pathToURI(workspace), "name": "workspace"}},
			},
			want: []string{workspace},
		},
		{
			name: "explicit empty workspace folders do not fall back",
			params: map[string]any{
				"capabilities":     map[string]any{},
				"rootUri":          pathToURI(fallback),
				"workspaceFolders": []map[string]any{},
			},
			want: []string{},
		},
		{
			name: "root URI is used when folders are absent",
			params: map[string]any{
				"capabilities": map[string]any{},
				"rootUri":      pathToURI(fallback),
			},
			want: []string{fallback},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frames := framed(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": tt.params})
			frames += framed(t, map[string]any{"jsonrpc": "2.0", "method": "exit"})
			var output bytes.Buffer
			server := NewServer(strings.NewReader(frames), &output, nilAnalyzer{})
			if err := server.Run(); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(server.workspaceModules, tt.want) {
				t.Fatalf("workspace modules = %v, want %v", server.workspaceModules, tt.want)
			}
			if !server.workspaceViewValid {
				t.Fatal("successful initialize did not mark workspace ownership valid")
			}
		})
	}
}

func TestInitializeFallsBackToProcessWorkingDirectory(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want, err := discoverWorkspaceModules([]string{wd})
	if err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	server := NewServer(strings.NewReader(initFrame()+exitFrame()), &output, nilAnalyzer{})
	if err := server.Run(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(server.workspaceModules, want) {
		t.Fatalf("workspace modules = %v, want cwd-owned %v", server.workspaceModules, want)
	}
}

func TestInitializeExplicitWorkspaceErrorDoesNotFallBack(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	frames := framed(t, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"rootUri":      pathToURI(missing),
			"capabilities": map[string]any{},
		},
	}) + exitFrame()
	var output bytes.Buffer
	server := NewServer(strings.NewReader(frames), &output, nilAnalyzer{})
	if err := server.Run(); err != nil {
		t.Fatal(err)
	}
	response := responseByID(t, output.String(), 1)
	if !strings.Contains(string(response["error"]), missing) {
		t.Fatalf("initialize error = %s, want explicit root %s", response["error"], missing)
	}
	if len(server.workspaceModules) != 0 {
		t.Fatalf("workspace modules = %v after rejected explicit root", server.workspaceModules)
	}
}

func TestInitializeRejectsMalformedExplicitWorkspaceIdentityWithoutCWDFallback(t *testing.T) {
	tests := []struct {
		name     string
		identity map[string]any
	}{
		{name: "root URI has the wrong type", identity: map[string]any{"rootUri": 7}},
		{name: "workspace folders have the wrong type", identity: map[string]any{"workspaceFolders": map[string]any{"uri": "file:///wrong"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cwdModule := writeTestModule(t, filepath.Join(t.TempDir(), "cwd-module"), "example.test/cwd")
			t.Chdir(cwdModule)
			params := map[string]any{"capabilities": map[string]any{}}
			maps.Copy(params, tt.identity)
			frames := framed(t, map[string]any{
				"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": params,
			}) + exitFrame()
			var output bytes.Buffer
			server := NewServer(strings.NewReader(frames), &output, nilAnalyzer{})
			if err := server.Run(); err != nil {
				t.Fatal(err)
			}
			response := responseByID(t, output.String(), 1)
			assertInvalidParams(t, response)
			if _, repliedSuccess := response["result"]; repliedSuccess {
				t.Fatalf("malformed initialize also replied success: %s", output.String())
			}
			if !strings.Contains(string(response["error"]), "initialize params") {
				t.Fatalf("initialize error is not actionable: %s", response["error"])
			}
			if server.workspaceFolders != nil || server.workspaceRoots != nil || server.workspaceModules != nil || server.workspaceViewValid {
				t.Fatalf("malformed initialize fell back to cwd: folders=%v roots=%v modules=%v", server.workspaceFolders, server.workspaceRoots, server.workspaceModules)
			}
		})
	}
}

func TestWorkspaceFolderLifecycleIsTransactionalAndKeepsPackageAnalyses(t *testing.T) {
	first := writeTestModule(t, filepath.Join(t.TempDir(), "first"), "example.test/first")
	second := writeTestModule(t, filepath.Join(t.TempDir(), "second"), "example.test/second")
	var output bytes.Buffer
	server := NewServer(strings.NewReader(""), &output, nilAnalyzer{})
	if err := server.setWorkspaceFolders([]workspaceFolder{{URI: pathToURI(first), Name: "first"}}); err != nil {
		t.Fatal(err)
	}
	pkg := &Package{}
	server.pkgs[first] = pkg
	primeModuleCaches := func() {
		server.moduleRefs = []CrossRef{{Name: "ref"}}
		server.moduleRefsValid = true
		server.moduleParams = []ComponentParamRenameFact{{Name: "param"}}
		server.moduleParamsValid = true
		server.moduleParamsDir = first
		server.moduleSyms = make(map[string]moduleSymbolCache, len(server.workspaceModules))
		for _, module := range server.workspaceModules {
			server.moduleSyms[module] = moduleSymbolCache{symbols: []Symbol{{Name: "symbol", NamePos: token.Position{Filename: filepath.Join(module, "page.gsx")}}}, valid: true}
		}
	}
	assertOnlyNonSymbolCachesInvalidated := func(t *testing.T, wantSymbolModules ...string) {
		t.Helper()
		if server.moduleRefs != nil || server.moduleRefsValid || server.moduleParams != nil || server.moduleParamsValid || server.moduleParamsDir != "" {
			t.Fatalf("whole-module caches retained: refs=%v/%v params=%v/%v/%q", server.moduleRefs, server.moduleRefsValid, server.moduleParams, server.moduleParamsValid, server.moduleParamsDir)
		}
		if len(server.moduleSyms) != len(wantSymbolModules) {
			t.Fatalf("symbol cache modules = %v, want %v", server.moduleSyms, wantSymbolModules)
		}
		for _, module := range wantSymbolModules {
			if !server.moduleSyms[module].valid {
				t.Fatalf("symbol cache for retained module %s was invalidated", module)
			}
		}
		if server.pkgs[first] != pkg {
			t.Fatalf("open package analysis was invalidated: got %p, want %p", server.pkgs[first], pkg)
		}
	}
	handleChange := func(added, removed []workspaceFolder) error {
		params, err := json.Marshal(didChangeWorkspaceFoldersParams{Event: workspaceFoldersChangeEvent{Added: added, Removed: removed}})
		if err != nil {
			return err
		}
		return server.handle(frame{Method: "workspace/didChangeWorkspaceFolders", Params: params})
	}

	primeModuleCaches()
	if err := handleChange([]workspaceFolder{{URI: pathToURI(second), Name: "second"}}, nil); err != nil {
		t.Fatal(err)
	}
	wantBoth := []string{first, second}
	slices.Sort(wantBoth)
	if !slices.Equal(server.workspaceModules, wantBoth) {
		t.Fatalf("modules after add = %v, want %v", server.workspaceModules, wantBoth)
	}
	assertOnlyNonSymbolCachesInvalidated(t, first)

	primeModuleCaches()
	if err := handleChange(nil, []workspaceFolder{{URI: pathToURI(first) + "/.", Name: "ignored"}}); err != nil {
		t.Fatal(err)
	}
	if want := []string{second}; !slices.Equal(server.workspaceModules, want) {
		t.Fatalf("modules after normalized removal = %v, want %v", server.workspaceModules, want)
	}
	assertOnlyNonSymbolCachesInvalidated(t, second)

	primeModuleCaches()
	beforeFolders := append([]workspaceFolder(nil), server.workspaceFolders...)
	beforeModules := append([]string(nil), server.workspaceModules...)
	missing := filepath.Join(t.TempDir(), "missing")
	if err := handleChange([]workspaceFolder{{URI: pathToURI(missing), Name: "missing"}}, nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(server.workspaceFolders, beforeFolders) || !slices.Equal(server.workspaceModules, beforeModules) {
		t.Fatalf("rejected update partially applied: folders=%v modules=%v", server.workspaceFolders, server.workspaceModules)
	}
	if !server.workspaceViewValid || !server.moduleRefsValid || !server.moduleParamsValid || len(server.moduleSyms) != 1 || !server.moduleSyms[second].valid {
		t.Fatal("rejected workspace update invalidated whole-module caches")
	}
	if server.pkgs[first] != pkg {
		t.Fatal("rejected workspace update invalidated open package analysis")
	}
	if !strings.Contains(output.String(), "workspace folder change rejected") || !strings.Contains(output.String(), missing) {
		t.Fatalf("notification failure was not surfaced: %s", output.String())
	}
}

func TestServerUnknownRequestMethodNotFound(t *testing.T) {
	in := framed(t, map[string]any{"jsonrpc": "2.0", "id": 9, "method": "textDocument/unknownMethod", "params": map[string]any{}})
	in += framed(t, map[string]any{"jsonrpc": "2.0", "method": "exit"})
	var out bytes.Buffer
	srv := NewServer(strings.NewReader(in), &out, nilAnalyzer{})
	if err := srv.Run(); err != nil {
		t.Fatal(err)
	}
	msgs := readFrames(t, out.String())
	if len(msgs) == 0 || msgs[0]["error"] == nil {
		t.Fatalf("expected an error response, got %v", msgs)
	}
}
