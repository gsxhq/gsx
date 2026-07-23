package lsp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// callResolve drives one completionItem/resolve request straight at
// handleCompletionResolve (no full initialize/didOpen dance needed: the
// handler is analyzer-free, see its doc comment) and returns the decoded
// reply item.
func callResolve(t *testing.T, params any) CompletionItem {
	t.Helper()
	data, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	srv := NewServer(nil, &out, nilAnalyzer{})
	if err := srv.handleCompletionResolve(frame{ID: json.RawMessage("7"), Params: data}); err != nil {
		t.Fatalf("handleCompletionResolve: %v", err)
	}
	msgs := readFrames(t, out.String())
	if len(msgs) != 1 {
		t.Fatalf("got %d frames, want 1: %s", len(msgs), out.String())
	}
	var item CompletionItem
	if err := json.Unmarshal(msgs[0]["result"], &item); err != nil {
		t.Fatalf("decode result: %v (%s)", err, msgs[0]["result"])
	}
	return item
}

// writeGoFile writes a real .go file with a documented func and returns its
// absolute path plus the 1-based line of the func's name identifier.
func writeGoFile(t *testing.T, dir, name string) (path string, line int) {
	t.Helper()
	const content = "package dep\n\n// Greet returns a friendly hello for name.\nfunc Greet(name string) string {\n\treturn \"hello \" + name\n}\n"
	path = filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path, 4 // "func Greet(..." is line 4
}

// TestHandleCompletionResolveNoDataUnchanged: an item with no Data comes
// back byte-for-byte unchanged (no Documentation fabricated).
func TestHandleCompletionResolveNoDataUnchanged(t *testing.T) {
	item := callResolve(t, map[string]any{"label": "Foo", "detail": "func()"})
	if item.Label != "Foo" || item.Detail != "func()" {
		t.Errorf("item = %+v, want Label=Foo Detail=func() unchanged", item)
	}
	if item.Documentation != nil {
		t.Errorf("item.Documentation = %+v, want nil (no Data to resolve)", item.Documentation)
	}
}

// TestHandleCompletionResolveMalformedDataUnchanged: a "data" value that
// does not match {file,line} (here, a bare string instead of an object)
// leaves the rest of the item untouched — no crash, no fabricated doc, and
// critically no file read is attempted (there is no interpretable path).
func TestHandleCompletionResolveMalformedDataUnchanged(t *testing.T) {
	item := callResolve(t, map[string]any{"label": "Bar", "data": "not-an-object"})
	if item.Label != "Bar" {
		t.Errorf("item.Label = %q, want %q (rest of item preserved despite malformed Data)", item.Label, "Bar")
	}
	if item.Documentation != nil {
		t.Errorf("item.Documentation = %+v, want nil (malformed Data must not resolve)", item.Documentation)
	}
}

// TestHandleCompletionResolveOutsideAllowedRootsRejected pins the SECURITY
// gate (resolvablePath): a real, readable, well-formed {file,line} payload
// pointing at a sentinel .go file OUTSIDE every allowed root (GOMODCACHE,
// GOROOT, workspace module roots) must be rejected — Documentation stays
// nil, proving no file read was even attempted for a location the server
// itself could never have emitted.
func TestHandleCompletionResolveOutsideAllowedRootsRejected(t *testing.T) {
	// Point GOMODCACHE somewhere else entirely so the sentinel (under a
	// DIFFERENT temp dir) is provably outside it; resolveRoots reads the env
	// fresh on every call (no process-wide memoization), so t.Setenv is
	// sufficient and self-reverting.
	t.Setenv("GOMODCACHE", t.TempDir())
	outsideDir := t.TempDir()
	path, line := writeGoFile(t, outsideDir, "dep/dep.go")

	item := callResolve(t, map[string]any{
		"label": "Greet",
		"data":  map[string]any{"file": path, "line": line},
	})
	if item.Documentation != nil {
		t.Errorf("resolve on a path outside every allowed root returned Documentation = %+v, want nil (rejected)", item.Documentation)
	}
}

// TestHandleCompletionResolveWithinGOMODCACHERoundTrips is the positive
// counterpart: a {file,line} payload for a real, documented func sitting
// UNDER a (test-pointed) GOMODCACHE resolves successfully end to end.
func TestHandleCompletionResolveWithinGOMODCACHERoundTrips(t *testing.T) {
	modCache := t.TempDir()
	t.Setenv("GOMODCACHE", modCache)
	path, line := writeGoFile(t, modCache, "example.com/dep@v1.0.0/dep.go")

	item := callResolve(t, map[string]any{
		"label": "Greet",
		"data":  map[string]any{"file": path, "line": line},
	})
	if item.Documentation == nil {
		t.Fatal("resolve on a path under GOMODCACHE returned nil Documentation, want the func's doc comment")
	}
	if got := item.Documentation.Value; got != "Greet returns a friendly hello for name." {
		t.Errorf("Documentation.Value = %q, want the doc comment text", got)
	}
}

// TestResolvablePathRejectsNonGoSuffix pins the .go-suffix half of the gate
// independent of the root check: even a path physically under GOMODCACHE is
// rejected if it doesn't end in ".go" (e.g. a go.sum, a vendored asset).
func TestResolvablePathRejectsNonGoSuffix(t *testing.T) {
	modCache := t.TempDir()
	t.Setenv("GOMODCACHE", modCache)
	path := filepath.Join(modCache, "example.com/dep@v1.0.0/go.sum")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not go source"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	srv := NewServer(nil, &out, nilAnalyzer{})
	if srv.resolvablePath(path) {
		t.Errorf("resolvablePath(%q) = true, want false (not a .go file)", path)
	}
}

// TestResolvablePathRejectsRelativeAndEscapingPaths pins two more gate
// edge cases: a relative path (never something the server itself would
// publish — every position comes from an Fset, always absolute) and a
// "root/../../elsewhere.go" escape that Cleans outside the root.
func TestResolvablePathRejectsRelativeAndEscapingPaths(t *testing.T) {
	modCache := t.TempDir()
	t.Setenv("GOMODCACHE", modCache)
	var out bytes.Buffer
	srv := NewServer(nil, &out, nilAnalyzer{})

	if srv.resolvablePath("relative/dep.go") {
		t.Error("resolvablePath accepted a relative path, want rejected")
	}
	escaped := filepath.Join(modCache, "..", "..", "escaped.go")
	if srv.resolvablePath(escaped) {
		t.Errorf("resolvablePath(%q) = true, want false (escapes GOMODCACHE via ..)", escaped)
	}
}

// TestResolvablePathAllowsWorkspaceModuleRoot proves the third allow-list
// bucket (negotiated workspace module roots, s.workspaceModules) admits a
// real .go file under the user's own module — the same trust boundary every
// other read-intelligence feature (hover, go-to-definition) already crosses.
func TestResolvablePathAllowsWorkspaceModuleRoot(t *testing.T) {
	// GOMODCACHE pointed elsewhere so this test exercises the workspace-root
	// bucket specifically, not an accidental GOMODCACHE containment.
	t.Setenv("GOMODCACHE", t.TempDir())
	moduleRoot := t.TempDir()
	path, line := writeGoFile(t, moduleRoot, "pkg/dep.go")

	var out bytes.Buffer
	srv := NewServer(nil, &out, nilAnalyzer{})
	srv.workspaceModules = []string{moduleRoot}
	if !srv.resolvablePath(path) {
		t.Fatalf("resolvablePath(%q) = false, want true (under a workspace module root)", path)
	}

	if err := srv.handleCompletionResolve(frame{ID: json.RawMessage("1"), Params: mustMarshal(t, map[string]any{
		"label": "Greet",
		"data":  map[string]any{"file": path, "line": line},
	})}); err != nil {
		t.Fatal(err)
	}
	msgs := readFrames(t, out.String())
	if len(msgs) != 1 {
		t.Fatalf("got %d frames, want 1", len(msgs))
	}
	var item CompletionItem
	if err := json.Unmarshal(msgs[0]["result"], &item); err != nil {
		t.Fatal(err)
	}
	if item.Documentation == nil {
		t.Error("workspace-module-root resolve returned nil Documentation, want the func's doc comment")
	}
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestInitializeAdvertisesCompletionResolveProvider pins T10's protocol
// capability flag: initialize must advertise completionProvider.resolveProvider.
func TestInitializeAdvertisesCompletionResolveProvider(t *testing.T) {
	out := drive(t, nilAnalyzer{}, initFrame()+exitFrame())
	var res initializeResult
	if err := json.Unmarshal(responseByID(t, out, 1)["result"], &res); err != nil {
		t.Fatal(err)
	}
	got := res.Capabilities.CompletionProvider
	if got == nil {
		t.Fatalf("initialize result missing completionProvider:\n%s", out)
	}
	if !got.ResolveProvider {
		t.Error("completionProvider.resolveProvider = false, want true")
	}
}
