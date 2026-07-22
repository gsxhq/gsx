package lsp

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/gsxfmt"
	"github.com/gsxhq/gsx/internal/pretty"
)

// fakeAnalyzer returns one error diagnostic for the file it is told about.
type fakeAnalyzer struct {
	file string // abs .gsx path to attach the diagnostic to
}

func (a fakeAnalyzer) ClearOverride(string) ([]string, error)       { return nil, nil }
func (a fakeAnalyzer) SetOverride(string, []byte) ([]string, error) { return nil, nil }

func (a fakeAnalyzer) AnalyzeModule(string, map[string][]byte) ([]CrossRef, error) { return nil, nil }
func (a fakeAnalyzer) AnalyzeModuleParams(string, map[string][]byte) ([]ComponentParamRenameFact, error) {
	return nil, nil
}

func (a fakeAnalyzer) ModuleSymbols(string, map[string][]byte) ([]Symbol, error) { return nil, nil }

func (a fakeAnalyzer) Analyze(dir string, override map[string][]byte) (*Package, error) {
	if _, ok := override[a.file]; !ok {
		return &Package{}, nil // the open buffer must reach the analyzer
	}
	return &Package{Diags: []diag.Diagnostic{{
		Start:    token.Position{Filename: a.file, Line: 1, Column: 3},
		End:      token.Position{Filename: a.file, Line: 1, Column: 6},
		Severity: diag.Error,
		Code:     "type-error",
		Source:   "types",
		Message:  "undefined: foo",
	}}}, nil
}

func (a fakeAnalyzer) AnalyzeEphemeral(string, string, []byte) (*Package, error) {
	return nil, errors.New("not implemented")
}

func (a fakeAnalyzer) AnalyzeEphemeralNonBlocking(string, string, []byte) (*Package, bool, error) {
	return nil, true, errors.New("not implemented")
}

func (a fakeAnalyzer) FormatSettings(string) gsxfmt.FormatSettings {
	return gsxfmt.FormatSettings{Width: 80, TabWidth: pretty.DefaultTabWidth}
}
func (a fakeAnalyzer) ImportsMode(string) gsxfmt.ImportsMode {
	return gsxfmt.ImportsGoimports
}
func (a fakeAnalyzer) ResolveImport(string, string, string) []string { return nil }

func TestDidOpenPublishesDiagnostics(t *testing.T) {
	file := filepath.Join(t.TempDir(), "page.gsx")
	uri := pathToURI(file)
	in := framed(t, map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "text": "ab foo cd", "version": 1}},
	})
	in += framed(t, map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out bytes.Buffer
	srv := NewServer(strings.NewReader(in), &out, fakeAnalyzer{file: file})
	if err := srv.Run(); err != nil {
		t.Fatal(err)
	}
	msgs := readFrames(t, out.String())
	var found bool
	for _, m := range msgs {
		if string(m["method"]) != `"textDocument/publishDiagnostics"` {
			continue
		}
		var p publishDiagnosticsParams
		if err := json.Unmarshal(m["params"], &p); err != nil {
			t.Fatal(err)
		}
		if p.URI != uri {
			continue
		}
		if len(p.Diagnostics) != 1 {
			t.Fatalf("diagnostics = %d, want 1", len(p.Diagnostics))
		}
		d := p.Diagnostics[0]
		if d.Range.Start != (Position{Line: 0, Character: 2}) || d.Severity != 1 || d.Message != "undefined: foo" {
			t.Fatalf("converted diag = %+v", d)
		}
		found = true
	}
	if !found {
		t.Fatalf("no publishDiagnostics for %s in %q", uri, out.String())
	}
}

func TestDidCloseClearsDiagnostics(t *testing.T) {
	file := filepath.Join(t.TempDir(), "page.gsx")
	uri := pathToURI(file)
	in := framed(t, map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "text": "ab foo cd", "version": 1}},
	})
	in += framed(t, map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didClose",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri}},
	})
	in += framed(t, map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out bytes.Buffer
	srv := NewServer(strings.NewReader(in), &out, fakeAnalyzer{file: file})
	if err := srv.Run(); err != nil {
		t.Fatal(err)
	}
	msgs := readFrames(t, out.String())
	// The LAST publishDiagnostics for uri must be empty.
	var last *publishDiagnosticsParams
	for _, m := range msgs {
		if string(m["method"]) != `"textDocument/publishDiagnostics"` {
			continue
		}
		var p publishDiagnosticsParams
		_ = json.Unmarshal(m["params"], &p)
		if p.URI == uri {
			cp := p
			last = &cp
		}
	}
	if last == nil || len(last.Diagnostics) != 0 {
		t.Fatalf("expected final empty publish for %s, got %+v", uri, last)
	}
}
