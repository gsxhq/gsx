package lsp

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

type closeTransitionAnalyzer struct {
	nilAnalyzer
	events        []string
	overrides     []map[string][]byte
	clearAffected []string
	clearErr      error
}

func (a *closeTransitionAnalyzer) SetOverride(path string, _ []byte) ([]string, error) {
	a.events = append(a.events, "set:"+path)
	return nil, nil
}

func (a *closeTransitionAnalyzer) Analyze(_ string, override map[string][]byte) (*Package, error) {
	a.events = append(a.events, "analyze")
	copy := make(map[string][]byte, len(override))
	for path, source := range override {
		copy[path] = append([]byte(nil), source...)
	}
	a.overrides = append(a.overrides, copy)
	return &Package{}, nil
}

func (a *closeTransitionAnalyzer) ClearOverride(path string) ([]string, error) {
	a.events = append(a.events, "clear:"+path)
	affected := a.clearAffected
	if affected == nil {
		affected = []string{filepath.Dir(path)}
	}
	return affected, a.clearErr
}

func TestDidCloseClearFailureIsLoggedAndReanalyzedFailClosed(t *testing.T) {
	dir := t.TempDir()
	closedPath := filepath.Join(dir, "closed.gsx")
	remainingPath := filepath.Join(dir, "remaining.gsx")
	closedURI := pathToURI(closedPath)
	remainingURI := pathToURI(remainingPath)
	in := framed(t, map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{
			"uri": closedURI, "text": "package page\ncomponent Closed() { <div/> }\n", "version": 1,
		}},
	})
	in += framed(t, map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{
			"uri": remainingURI, "text": "package page\ncomponent Remaining() { <div/> }\n", "version": 1,
		}},
	})
	in += framed(t, map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didClose",
		"params": map[string]any{"textDocument": map[string]any{"uri": closedURI}},
	})
	in += framed(t, map[string]any{"jsonrpc": "2.0", "method": "exit"})

	analyzer := &closeTransitionAnalyzer{clearErr: errors.New("saved source unreadable")}
	var out bytes.Buffer
	if err := NewServer(strings.NewReader(in), &out, analyzer).Run(); err != nil {
		t.Fatal(err)
	}
	wantEvents := []string{
		"set:" + closedPath, "analyze",
		"set:" + remainingPath, "analyze",
		"clear:" + closedPath, "analyze",
	}
	if strings.Join(analyzer.events, "\n") != strings.Join(wantEvents, "\n") {
		t.Fatalf("events = %v, want %v", analyzer.events, wantEvents)
	}
	if !strings.Contains(out.String(), "window/logMessage") ||
		!strings.Contains(out.String(), "clear override") ||
		!strings.Contains(out.String(), "saved source unreadable") {
		t.Fatalf("clear failure was not logged: %s", out.String())
	}
}

func TestDidCloseClearsAnalyzerOverrideBeforeReanalyzingRemainingDocuments(t *testing.T) {
	dir := t.TempDir()
	closedPath := filepath.Join(dir, "closed.gsx")
	remainingPath := filepath.Join(dir, "remaining.gsx")
	closedURI := pathToURI(closedPath)
	remainingURI := pathToURI(remainingPath)

	in := framed(t, map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{
			"uri": closedURI, "text": "package page\ncomponent Closed() { <div/> }\n", "version": 1,
		}},
	})
	in += framed(t, map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{
			"uri": remainingURI, "text": "package page\ncomponent Remaining() { <div/> }\n", "version": 1,
		}},
	})
	in += framed(t, map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didClose",
		"params": map[string]any{"textDocument": map[string]any{"uri": closedURI}},
	})
	in += framed(t, map[string]any{"jsonrpc": "2.0", "method": "exit"})

	analyzer := &closeTransitionAnalyzer{}
	var out bytes.Buffer
	if err := NewServer(strings.NewReader(in), &out, analyzer).Run(); err != nil {
		t.Fatal(err)
	}

	wantEvents := []string{
		"set:" + closedPath, "analyze",
		"set:" + remainingPath, "analyze",
		"clear:" + closedPath, "analyze",
	}
	if strings.Join(analyzer.events, "\n") != strings.Join(wantEvents, "\n") {
		t.Fatalf("events = %v, want %v", analyzer.events, wantEvents)
	}
	if len(analyzer.overrides) != 3 {
		t.Fatalf("Analyze calls = %d, want 3", len(analyzer.overrides))
	}
	final := analyzer.overrides[2]
	if _, retained := final[closedPath]; retained {
		t.Fatalf("closed override remained in final analysis: %v", final)
	}
	if source, ok := final[remainingPath]; !ok || string(source) != "package page\ncomponent Remaining() { <div/> }\n" {
		t.Fatalf("remaining override = %q, %v; want current open buffer", source, ok)
	}
}
