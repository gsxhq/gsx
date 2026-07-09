package lsp

import (
	"encoding/json"
	"go/token"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/gsxfmt"
	"github.com/gsxhq/gsx/internal/pretty"
)

// countingAnalyzer behaves like fakeAnalyzer (one diag for its file) but counts
// how many times it ran, so a test can prove a burst of edits coalesced into a
// single analysis.
type countingAnalyzer struct {
	file string
	mu   sync.Mutex
	n    int
}

func (a *countingAnalyzer) AnalyzeModule(string, map[string][]byte) ([]CrossRef, error) {
	return nil, nil
}

func (a *countingAnalyzer) ModuleSymbols(string, map[string][]byte) ([]Symbol, error) {
	return nil, nil
}

func (a *countingAnalyzer) Analyze(_ string, override map[string][]byte) (*Package, error) {
	a.mu.Lock()
	a.n++
	a.mu.Unlock()
	if _, ok := override[a.file]; !ok {
		return &Package{}, nil
	}
	return &Package{Diags: []diag.Diagnostic{{
		Start:    token.Position{Filename: a.file, Line: 1, Column: 3},
		End:      token.Position{Filename: a.file, Line: 1, Column: 6},
		Severity: diag.Error,
		Message:  "undefined: foo",
	}}}, nil
}

func (a *countingAnalyzer) FormatSettings(string) (int, int) { return 80, pretty.DefaultTabWidth }
func (a *countingAnalyzer) ImportsMode(string) gsxfmt.ImportsMode {
	return gsxfmt.ImportsGoimports
}

func (a *countingAnalyzer) calls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.n
}

// TestDidChangeDebouncesAndTagsVersion drives the debounce timer manually so the
// test is deterministic, not timing-dependent: a burst of four didChange edits
// must produce zero analyses until the timer fires, then exactly one analysis of
// the settled text, published with the latest document version.
func TestDidChangeDebouncesAndTagsVersion(t *testing.T) {
	file := filepath.Join(t.TempDir(), "page.gsx")
	uri := pathToURI(file)

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	an := &countingAnalyzer{file: file}
	srv := NewServer(inR, outW, an)

	// Replace the real timer with a capture: record the latest scheduled callback
	// so the test decides exactly when the debounce "elapses".
	var mu sync.Mutex
	var pending func()
	srv.schedule = func(_ time.Duration, f func()) func() {
		mu.Lock()
		pending = f
		mu.Unlock()
		return func() {} // cancel is a no-op; only the captured latest f ever fires
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

	// didOpen publishes immediately (not debounced); read it and confirm version 1.
	write(framed(t, map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "text": "ab foo cd", "version": 1}},
	}))
	if p := readPublish(t, rc, uri); p.Version == nil || *p.Version != 1 || len(p.Diagnostics) != 1 {
		t.Fatalf("open publish = %+v (version want 1, diags want 1)", p)
	}
	openCalls := an.calls() // exactly the one analysis from didOpen

	// Burst of edits: none may analyze (all debounced), only the timer is reset.
	write(didChange(2, "ab foo c"))
	write(didChange(3, "ab foo |"))
	write(didChange(4, "ab foo |>"))
	write(didChange(5, "ab foo |> upper"))

	// Synchronise: a shutdown request replies in-order, so once we see the reply
	// every preceding didChange has been handled and `pending` holds the last one.
	write(framed(t, map[string]any{"jsonrpc": "2.0", "id": 99, "method": "shutdown"}))
	readReply(t, rc, 99)

	if got := an.calls() - openCalls; got != 0 {
		t.Fatalf("burst triggered %d analyses before debounce fired, want 0", got)
	}

	// Fire the (single, latest) debounce callback.
	mu.Lock()
	fire := pending
	mu.Unlock()
	if fire == nil {
		t.Fatal("no debounce timer was scheduled for the burst")
	}
	fire()

	// The coalesced analysis publishes once, tagged with the settled version 5.
	if p := readPublish(t, rc, uri); p.Version == nil || *p.Version != 5 {
		t.Fatalf("coalesced publish = %+v (version want 5)", p)
	}
	if got := an.calls() - openCalls; got != 1 {
		t.Fatalf("burst produced %d analyses, want exactly 1 (coalesced)", got)
	}

	write(framed(t, map[string]any{"jsonrpc": "2.0", "method": "exit"}))
	_ = inW.Close()
	if err := <-runErr; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// readPublish reads frames until a publishDiagnostics for uri arrives.
func readPublish(t *testing.T, rc *conn, uri string) publishDiagnosticsParams {
	t.Helper()
	for {
		f, err := rc.read()
		if err != nil {
			t.Fatalf("read publish: %v", err)
		}
		if f.Method != "textDocument/publishDiagnostics" {
			continue
		}
		var p publishDiagnosticsParams
		if err := json.Unmarshal(f.Params, &p); err != nil {
			t.Fatalf("decode publish: %v", err)
		}
		if p.URI == uri {
			return p
		}
	}
}

// readReply reads frames until the response with the given id arrives.
func readReply(t *testing.T, rc *conn, id int) {
	t.Helper()
	want := strings.TrimSpace(string(mustJSON(t, id)))
	for {
		f, err := rc.read()
		if err != nil {
			t.Fatalf("read reply %d: %v", id, err)
		}
		if f.Method == "" && len(f.ID) > 0 && strings.TrimSpace(string(f.ID)) == want {
			return
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
