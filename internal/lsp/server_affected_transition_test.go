package lsp

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gsxhq/gsx/internal/diag"
)

type affectedTransitionAnalyzer struct {
	nilAnalyzer
	setAffected   []string
	setErr        error
	clearAffected []string
	clearErr      error
	analyzed      []string
	pkg           *Package
	analyzeErr    error
}

func (a *affectedTransitionAnalyzer) SetOverride(string, []byte) ([]string, error) {
	return append([]string(nil), a.setAffected...), a.setErr
}

func (a *affectedTransitionAnalyzer) ClearOverride(string) ([]string, error) {
	return append([]string(nil), a.clearAffected...), a.clearErr
}

func (a *affectedTransitionAnalyzer) Analyze(dir string, _ map[string][]byte) (*Package, error) {
	a.analyzed = append(a.analyzed, dir)
	if a.pkg == nil && a.analyzeErr == nil {
		return &Package{}, nil
	}
	return a.pkg, a.analyzeErr
}

func rawParams(t *testing.T, value any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDidChangeSchedulesExactOpenAffectedDirectories(t *testing.T) {
	root := t.TempDir()
	dependencyDir := filepath.Join(root, "dependency")
	importerDir := filepath.Join(root, "importer")
	unrelatedDir := filepath.Join(root, "unrelated")
	dependencyPath := filepath.Join(dependencyDir, "dependency.gsx")
	importerPath := filepath.Join(importerDir, "importer.gsx")
	unrelatedPath := filepath.Join(unrelatedDir, "unrelated.gsx")

	analyzer := &affectedTransitionAnalyzer{setAffected: []string{
		importerDir,
		dependencyDir,
		importerDir, // normalization must not schedule duplicate work
	}}
	var out bytes.Buffer
	server := NewServer(strings.NewReader(""), &out, analyzer)
	server.docs.open(pathToURI(dependencyPath), "package dependency", 1)
	server.docs.open(pathToURI(importerPath), "package importer", 1)
	server.docs.open(pathToURI(unrelatedPath), "package unrelated", 1)
	dependencyPkg := &Package{}
	importerPkg := &Package{}
	unrelatedPkg := &Package{}
	server.pkgs[dependencyDir] = dependencyPkg
	server.pkgs[importerDir] = importerPkg
	server.pkgs[unrelatedDir] = unrelatedPkg
	server.schedule = func(_ time.Duration, _ func()) func() { return func() {} }

	err := server.handleDidChange(frame{Params: rawParams(t, map[string]any{
		"textDocument":   map[string]any{"uri": pathToURI(dependencyPath), "version": 2},
		"contentChanges": []map[string]any{{"text": "package dependency\n\ncomponent New() { <div/> }"}},
	})})
	if err != nil {
		t.Fatal(err)
	}

	gotTimers := make([]string, 0, len(server.timers))
	for dir := range server.timers {
		gotTimers = append(gotTimers, dir)
	}
	slices.Sort(gotTimers)
	wantTimers := []string{dependencyDir, importerDir}
	if !slices.Equal(gotTimers, wantTimers) {
		t.Fatalf("scheduled directories = %v, want exact affected set %v", gotTimers, wantTimers)
	}
	if server.pkgs[dependencyDir] != nil || server.pkgs[importerDir] != nil {
		t.Fatalf("affected package facts survived transition: dependency=%p importer=%p", server.pkgs[dependencyDir], server.pkgs[importerDir])
	}
	if server.pkgs[unrelatedDir] != unrelatedPkg {
		t.Fatal("unrelated retained package was evicted")
	}
	if server.gen[dependencyDir] != 1 || server.gen[importerDir] != 1 || server.gen[unrelatedDir] != 0 {
		t.Fatalf("generations = dependency:%d importer:%d unrelated:%d, want 1,1,0",
			server.gen[dependencyDir], server.gen[importerDir], server.gen[unrelatedDir])
	}
}

func TestDidCloseErrorStillReanalyzesOpenAffectedImporters(t *testing.T) {
	root := t.TempDir()
	dependencyDir := filepath.Join(root, "dependency")
	importerDir := filepath.Join(root, "importer")
	dependencyPath := filepath.Join(dependencyDir, "dependency.gsx")
	importerPath := filepath.Join(importerDir, "importer.gsx")
	analyzer := &affectedTransitionAnalyzer{
		clearAffected: []string{dependencyDir, importerDir},
		clearErr:      errors.New("saved source unreadable"),
	}
	var out bytes.Buffer
	server := NewServer(strings.NewReader(""), &out, analyzer)
	server.docs.open(pathToURI(dependencyPath), "package dependency", 1)
	server.docs.open(pathToURI(importerPath), "package importer", 1)
	server.pkgs[dependencyDir] = &Package{}
	server.pkgs[importerDir] = &Package{}

	err := server.handleDidClose(frame{Params: rawParams(t, map[string]any{
		"textDocument": map[string]any{"uri": pathToURI(dependencyPath)},
	})})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(analyzer.analyzed, []string{importerDir}) {
		t.Fatalf("analyzed directories = %v, want importer despite clear error", analyzer.analyzed)
	}
	if !strings.Contains(out.String(), "saved source unreadable") {
		t.Fatalf("clear transition error was not logged: %s", out.String())
	}
	if _, stale := server.pkgs[dependencyDir]; stale {
		t.Fatal("closed dependency package facts survived failed clear")
	}
}

func TestGoBufferTriggersAnalysisWithoutReceivingGSXDiagnostics(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "component.go")
	analyzer := &affectedTransitionAnalyzer{
		setAffected: []string{dir},
		pkg: &Package{Diags: []diag.Diagnostic{
			{Severity: diag.Error, Message: "positionless GSX diagnostic"},
			{
				Start:    token.Position{Filename: path, Line: 1, Column: 1},
				End:      token.Position{Filename: path, Line: 1, Column: 2},
				Severity: diag.Error,
				Message:  "Go-targeted diagnostic",
			},
		}},
	}
	var out bytes.Buffer
	server := NewServer(strings.NewReader(""), &out, analyzer)
	err := server.handleDidOpen(frame{Params: rawParams(t, map[string]any{
		"textDocument": map[string]any{
			"uri": pathToURI(path), "version": 1, "text": "package page",
		},
	})})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(analyzer.analyzed, []string{dir}) {
		t.Fatalf("analyzed directories = %v, want %s", analyzer.analyzed, dir)
	}
	if server.pkgs[dir] != analyzer.pkg {
		t.Fatal("Go-triggered analysis was not retained for read intelligence")
	}
	if strings.Contains(out.String(), "textDocument/publishDiagnostics") {
		t.Fatalf("GSX LSP published diagnostics to a Go-only directory: %s", out.String())
	}
}

func TestIdenticalByteChangeRepublishesRetainedPackageAtCurrentVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "page.gsx")
	uri := pathToURI(path)
	analyzer := &affectedTransitionAnalyzer{}
	var out bytes.Buffer
	server := NewServer(strings.NewReader(""), &out, analyzer)
	server.docs.open(uri, "package page", 1)
	server.lastURI[dir] = uri
	server.pkgs[dir] = &Package{Diags: []diag.Diagnostic{{
		Start:    token.Position{Filename: path, Line: 1, Column: 1},
		End:      token.Position{Filename: path, Line: 1, Column: 2},
		Severity: diag.Error,
		Message:  "retained diagnostic",
	}}}

	err := server.handleDidChange(frame{Params: rawParams(t, map[string]any{
		"textDocument":   map[string]any{"uri": uri, "version": 2},
		"contentChanges": []map[string]any{{"text": "package page"}},
	})})
	if err != nil {
		t.Fatal(err)
	}
	if len(analyzer.analyzed) != 0 {
		t.Fatalf("identical bytes triggered analysis despite retained package: %v", analyzer.analyzed)
	}
	if len(server.timers) != 0 {
		t.Fatalf("identical bytes scheduled analysis: %v", server.timers)
	}
	if !strings.Contains(out.String(), `"version":2`) || !strings.Contains(out.String(), "retained diagnostic") {
		t.Fatalf("retained diagnostics were not republished at version 2: %s", out.String())
	}
}
