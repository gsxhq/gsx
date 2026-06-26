package lsp

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// errFake is a sentinel error returned by moduleRefsAnalyzer to exercise the
// fallback path in handleReferences.
var errFake = errors.New("module error")

// moduleRefsAnalyzer is a test double that counts AnalyzeModule calls and
// returns configurable results. Analyze returns pkg when set, else an empty
// Package so s.pkgs[dir] is populated after didOpen.
type moduleRefsAnalyzer struct {
	moduleCalls int
	moduleRefs  []CrossRef
	moduleErr   error
	pkg         *Package
}

func (a *moduleRefsAnalyzer) Analyze(string, map[string][]byte) (*Package, error) {
	if a.pkg != nil {
		return a.pkg, nil
	}
	return &Package{}, nil
}
func (a *moduleRefsAnalyzer) AnalyzeModule(string, map[string][]byte) ([]CrossRef, error) {
	a.moduleCalls++
	return a.moduleRefs, a.moduleErr
}

// drive runs the given pre-framed messages through a fresh server over the
// analyzer and returns the raw output. Helper mirrors the existing
// server_*_test harness.
func drive(t *testing.T, a Analyzer, frames string) string {
	t.Helper()
	var out bytes.Buffer
	srv := NewServer(strings.NewReader(frames), &out, a)
	if err := srv.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return out.String()
}

// jsonFrame serialises v as a Content-Length-framed JSON-RPC message.
func jsonFrame(v any) string {
	b, _ := json.Marshal(v)
	return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
}

func initFrame() string {
	return jsonFrame(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{},
	})
}

func didOpenFrame(uri, text string) string {
	return jsonFrame(map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": uri, "version": 1, "text": text},
		},
	})
}

func didChangeFrame(uri, text string) string {
	return jsonFrame(map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didChange",
		"params": map[string]any{
			"textDocument":   map[string]any{"uri": uri, "version": 2},
			"contentChanges": []map[string]any{{"text": text}},
		},
	})
}

func refsFrame(id int, uri string, line, char int) string {
	return jsonFrame(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "textDocument/references",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"position":     map[string]any{"line": line, "character": char},
			"context":      map[string]any{"includeDeclaration": false},
		},
	})
}

func exitFrame() string {
	return jsonFrame(map[string]any{"jsonrpc": "2.0", "method": "exit"})
}

// TestReferencesCacheInvalidation verifies the whole-module index is rebuilt
// after a document mutation and reused when no mutation has occurred.
func TestReferencesCacheInvalidation(t *testing.T) {
	uri := "file:///m/a.gsx"
	text := "package x\n\ncomponent Card() {\n\t<div/>\n}\n"
	// Two references with no change between → one AnalyzeModule call (cached).
	a := &moduleRefsAnalyzer{moduleRefs: nil} // nil result is valid (cached)
	frames := initFrame() + didOpenFrame(uri, text) +
		refsFrame(2, uri, 2, 10) + refsFrame(3, uri, 2, 10) + exitFrame()
	drive(t, a, frames)
	if a.moduleCalls != 1 {
		t.Fatalf("cached: want 1 AnalyzeModule call, got %d", a.moduleCalls)
	}

	// A didChange between two references → two AnalyzeModule calls (invalidated).
	a2 := &moduleRefsAnalyzer{}
	frames2 := initFrame() + didOpenFrame(uri, text) +
		refsFrame(2, uri, 2, 10) + didChangeFrame(uri, text+"\n") +
		refsFrame(3, uri, 2, 10) + exitFrame()
	drive(t, a2, frames2)
	if a2.moduleCalls != 2 {
		t.Fatalf("invalidated: want 2 AnalyzeModule calls, got %d", a2.moduleCalls)
	}
}

// TestReferencesFallbackOnModuleError verifies that when AnalyzeModule returns
// an error, handleReferences falls back to the single-package CrossIndex.
func TestReferencesFallbackOnModuleError(t *testing.T) {
	uri := "file:///m/a.gsx"
	text := "package x\n\ncomponent Card() {\n\t<div/>\n}\n"
	// component Card starts at line 3 (1-based), the name at column 11.
	decl := token.Position{Filename: "/m/a.gsx", Line: 3, Column: 11}
	ref := token.Position{Filename: "/m/other.go", Line: 5, Column: 2}
	a := &moduleRefsAnalyzer{
		moduleErr: errFake,
		pkg: &Package{CrossIndex: map[string]CrossRef{
			"Card": {Name: "Card", Decl: decl, Refs: []token.Position{ref}},
		}},
	}
	// Cursor on "Card" (0-based line 2, character 10).
	out := drive(t, a, initFrame()+didOpenFrame(uri, text)+refsFrame(2, uri, 2, 10)+exitFrame())
	if !strings.Contains(out, "other.go") {
		t.Fatalf("fallback path should return single-package ref; out:\n%s", out)
	}
}
