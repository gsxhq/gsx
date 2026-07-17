package lsp

import (
	"go/token"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/gsxfmt"
	"github.com/gsxhq/gsx/internal/pretty"
)

// blockingAnalyzer parks inside Analyze until the test releases that specific
// call, so a test can hold an analysis "in flight" and observe what the server
// does meanwhile. Each call sends its own release channel over calls; closing
// that channel lets exactly that call return.
type blockingAnalyzer struct {
	file  string
	calls chan chan struct{}
}

type overrideLifetimeAnalyzer struct {
	mu       sync.Mutex
	sources  map[string][]byte
	analyses int
	calls    chan chan struct{}
}

func (a *overrideLifetimeAnalyzer) SetOverride(path string, source []byte) ([]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sources[path] = append([]byte(nil), source...)
	// The edited directory's package view is invalidated by the transition; the
	// Analyzer contract requires returning it so the server reanalyzes/schedules
	// rather than republishing a retained package (a nil set models an identical-
	// byte no-op).
	return []string{filepath.Dir(path)}, nil
}

func (a *overrideLifetimeAnalyzer) ClearOverride(path string) ([]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sources, path)
	return nil, nil
}

func (a *overrideLifetimeAnalyzer) Analyze(string, map[string][]byte) (*Package, error) {
	a.mu.Lock()
	a.analyses++
	call := a.analyses
	a.mu.Unlock()
	if call == 1 {
		return &Package{}, nil
	}
	release := make(chan struct{})
	a.calls <- release
	<-release
	return &Package{}, nil
}

func (a *overrideLifetimeAnalyzer) AnalyzeModule(string, map[string][]byte) ([]CrossRef, error) {
	return nil, nil
}
func (a *overrideLifetimeAnalyzer) AnalyzeModuleParams(string, map[string][]byte) ([]ComponentParamRenameFact, error) {
	return nil, nil
}

func (a *overrideLifetimeAnalyzer) ModuleSymbols(string, map[string][]byte) ([]Symbol, error) {
	return nil, nil
}

func (a *overrideLifetimeAnalyzer) FormatSettings(string) gsxfmt.FormatSettings {
	return gsxfmt.FormatSettings{Width: 80, TabWidth: pretty.DefaultTabWidth}
}

func (a *overrideLifetimeAnalyzer) ImportsMode(string) gsxfmt.ImportsMode {
	return gsxfmt.ImportsGoimports
}

func (a *overrideLifetimeAnalyzer) ResolveImport(string, string, string) []string { return nil }

func (a *overrideLifetimeAnalyzer) source(path string) ([]byte, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	source, ok := a.sources[path]
	return append([]byte(nil), source...), ok
}

func (a *blockingAnalyzer) ClearOverride(string) ([]string, error)       { return nil, nil }
func (a *blockingAnalyzer) SetOverride(string, []byte) ([]string, error) { return nil, nil }

func (a *blockingAnalyzer) AnalyzeModule(string, map[string][]byte) ([]CrossRef, error) {
	return nil, nil
}
func (a *blockingAnalyzer) AnalyzeModuleParams(string, map[string][]byte) ([]ComponentParamRenameFact, error) {
	return nil, nil
}

func (a *blockingAnalyzer) ModuleSymbols(string, map[string][]byte) ([]Symbol, error) {
	return nil, nil
}

func (a *blockingAnalyzer) Analyze(_ string, override map[string][]byte) (*Package, error) {
	release := make(chan struct{})
	a.calls <- release
	<-release
	if _, ok := override[a.file]; !ok {
		return &Package{}, nil
	}
	return &Package{Diags: []diag.Diagnostic{{
		Start:    token.Position{Filename: a.file, Line: 1, Column: 1},
		End:      token.Position{Filename: a.file, Line: 1, Column: 2},
		Severity: diag.Error,
		Message:  "boom",
	}}}, nil
}

func (a *blockingAnalyzer) FormatSettings(string) gsxfmt.FormatSettings {
	return gsxfmt.FormatSettings{Width: 80, TabWidth: pretty.DefaultTabWidth}
}
func (a *blockingAnalyzer) ImportsMode(string) gsxfmt.ImportsMode {
	return gsxfmt.ImportsGoimports
}
func (a *blockingAnalyzer) ResolveImport(string, string, string) []string { return nil }

// TestAnalysisIsAsyncAndSupersededResultsDiscarded proves two Phase-2 properties
// deterministically (no sleeps): (1) the Run loop answers requests while an
// analysis is parked in a worker — so a heavy type-check never blocks hover; and
// (2) when a newer edit launches a second analysis, the first (now stale) result
// is discarded rather than published.
func TestAnalysisIsAsyncAndSupersededResultsDiscarded(t *testing.T) {
	file := filepath.Join(t.TempDir(), "page.gsx")
	uri := pathToURI(file)

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	an := &blockingAnalyzer{file: file, calls: make(chan chan struct{})}
	srv := NewServer(inR, outW, an)

	var mu sync.Mutex
	var pending func()
	srv.schedule = func(_ time.Duration, f func()) func() {
		mu.Lock()
		pending = f
		mu.Unlock()
		return func() {}
	}
	fireDebounce := func() {
		mu.Lock()
		f := pending
		mu.Unlock()
		if f == nil {
			t.Fatal("no debounce timer scheduled")
		}
		f()
	}

	runErr := make(chan error, 1)
	go func() { runErr <- srv.Run() }()
	rc := newConn(outR, io.Discard)

	write := func(s string) {
		if _, err := io.WriteString(inW, s); err != nil {
			t.Errorf("write: %v", err)
		}
	}
	didChange := func(ver int, text string) string {
		return framed(t, map[string]any{
			"jsonrpc": "2.0", "method": "textDocument/didChange",
			"params": map[string]any{
				"textDocument":   map[string]any{"uri": uri, "version": ver},
				"contentChanges": []map[string]any{{"text": text}},
			},
		})
	}
	// syncPoint issues a shutdown request and waits for its reply: because frames
	// are handled in order, the reply proves every preceding message was processed.
	syncPoint := func(id int) {
		write(framed(t, map[string]any{"jsonrpc": "2.0", "id": id, "method": "shutdown"}))
		readReply(t, rc, id)
	}

	// First edit, then fire its debounce → analysis gen 1 starts on a worker.
	write(didChange(1, "x"))
	syncPoint(1)
	fireDebounce()
	release1 := <-an.calls // worker gen 1 is now parked inside Analyze

	// The loop must still answer requests while that analysis is in flight.
	syncPoint(2)

	// Second edit supersedes the first; fire its debounce → analysis gen 2 starts.
	write(didChange(2, "xy"))
	syncPoint(3)
	fireDebounce()
	release2 := <-an.calls // worker gen 2 parked

	// Release gen 1: its result returns but is stale (gen 2 is current) → discarded.
	// Release gen 2: its result is current → it publishes, tagged version 2.
	close(release1)
	close(release2)

	// The only publish that reaches the editor is the gen-2 result at version 2;
	// the gen-1 result never appears (it would have carried version 1).
	if p := readPublish(t, rc, uri); p.Version == nil || *p.Version != 2 {
		t.Fatalf("first publish = %+v, want the gen-2 result at version 2 (gen-1 must be discarded)", p)
	}

	write(framed(t, map[string]any{"jsonrpc": "2.0", "method": "exit"}))
	_ = inW.Close()
	if err := <-runErr; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestDidCloseWinsOverSupersededWorkerSnapshot(t *testing.T) {
	file := filepath.Join(t.TempDir(), "page.gsx")
	uri := pathToURI(file)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	an := &overrideLifetimeAnalyzer{sources: map[string][]byte{}, calls: make(chan chan struct{})}
	srv := NewServer(inR, outW, an)

	var timerMu sync.Mutex
	var pending func()
	srv.schedule = func(_ time.Duration, f func()) func() {
		timerMu.Lock()
		pending = f
		timerMu.Unlock()
		return func() {}
	}

	runErr := make(chan error, 1)
	go func() { runErr <- srv.Run() }()
	rc := newConn(outR, io.Discard)
	write := func(message string) {
		t.Helper()
		if _, err := io.WriteString(inW, message); err != nil {
			t.Fatal(err)
		}
	}
	syncPoint := func(id int) {
		write(framed(t, map[string]any{"jsonrpc": "2.0", "id": id, "method": "shutdown"}))
		readReply(t, rc, id)
	}

	write(framed(t, map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{
			"uri": uri, "version": 1, "text": "package page\ncomponent Page() { <div>one</div> }\n",
		}},
	}))
	_ = readPublish(t, rc, uri)

	write(framed(t, map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didChange",
		"params": map[string]any{
			"textDocument":   map[string]any{"uri": uri, "version": 2},
			"contentChanges": []map[string]any{{"text": "package page\ncomponent Page() { <div>two</div> }\n"}},
		},
	}))
	syncPoint(1)
	timerMu.Lock()
	fire := pending
	timerMu.Unlock()
	if fire == nil {
		t.Fatal("didChange did not schedule analysis")
	}
	fire()
	releaseStale := <-an.calls

	write(framed(t, map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didClose",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri}},
	}))
	// The empty diagnostic publish proves didClose, including ClearOverride,
	// completed while the superseded worker is still parked in Analyze.
	_ = readPublish(t, rc, uri)
	if source, ok := an.source(file); ok {
		t.Fatalf("closed source remains authoritative before stale worker returns: %q", source)
	}

	close(releaseStale)
	syncPoint(2)
	if source, ok := an.source(file); ok {
		t.Fatalf("superseded worker resurrected closed source: %q", source)
	}

	write(framed(t, map[string]any{"jsonrpc": "2.0", "method": "exit"}))
	_ = inW.Close()
	if err := <-runErr; err != nil {
		t.Fatalf("Run: %v", err)
	}
}
