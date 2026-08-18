package lsp

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/gsxfmt"
	"github.com/gsxhq/gsx/internal/pretty"
	"github.com/gsxhq/gsx/internal/sourceintel"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// errFake is a sentinel error returned by moduleRefsAnalyzer to exercise the
// fallback path in handleReferences.
var errFake = errors.New("module error")

// moduleRefsAnalyzer is a test double that counts AnalyzeModule calls and
// returns configurable results. Analyze returns pkg when set, else an empty
// Package so s.pkgs[dir] is populated after didOpen.
type moduleRefsAnalyzer struct {
	moduleCalls int
	moduleGraph *sourceintel.SymbolGraph
	// graphsByDir answers AnalyzeModule per requested directory, standing in for
	// a workspace whose modules each have their own graph. moduleGraph answers
	// any directory it has no entry for.
	graphsByDir map[string]*sourceintel.SymbolGraph
	moduleDirs  []string
	moduleErr   error
	pkg         *Package
	overrides   []map[string][]byte
}

func (a *moduleRefsAnalyzer) ClearOverride(string) ([]string, error)       { return nil, nil }
func (a *moduleRefsAnalyzer) SetOverride(string, []byte) ([]string, error) { return nil, nil }

func (a *moduleRefsAnalyzer) Analyze(string, map[string][]byte) (*Package, error) {
	if a.pkg != nil {
		return a.pkg, nil
	}
	return &Package{}, nil
}
func (a *moduleRefsAnalyzer) AnalyzeEphemeral(string, string, []byte) (*Package, error) {
	return nil, errFake
}
func (a *moduleRefsAnalyzer) AnalyzeEphemeralNonBlocking(string, string, []byte) (*Package, bool, error) {
	return nil, true, errFake
}
func (a *moduleRefsAnalyzer) AnalyzeModule(dir string, overrides map[string][]byte) (*sourceintel.SymbolGraph, error) {
	a.moduleCalls++
	a.moduleDirs = append(a.moduleDirs, dir)
	captured := make(map[string][]byte, len(overrides))
	for path, source := range overrides {
		captured[path] = append([]byte(nil), source...)
	}
	a.overrides = append(a.overrides, captured)
	if graph, ok := a.graphsByDir[dir]; ok {
		return graph, a.moduleErr
	}
	return a.moduleGraph, a.moduleErr
}
func (a *moduleRefsAnalyzer) AnalyzeModuleParams(string, map[string][]byte) ([]ComponentParamRenameFact, error) {
	return nil, nil
}
func (a *moduleRefsAnalyzer) ModuleSymbols(string, map[string][]byte) ([]Symbol, error) {
	return nil, nil
}
func (a *moduleRefsAnalyzer) FormatSettings(string) gsxfmt.FormatSettings {
	return gsxfmt.FormatSettings{Width: 80, TabWidth: pretty.DefaultTabWidth}
}
func (a *moduleRefsAnalyzer) ImportsMode(string) gsxfmt.ImportsMode {
	return gsxfmt.ImportsGoimports
}
func (a *moduleRefsAnalyzer) ResolveImport(string, string, string) []string { return nil }
func (a *moduleRefsAnalyzer) ExportedSymbols(string, string) []ImportSymbol { return nil }
func (a *moduleRefsAnalyzer) ImportablePackages(string) []ImportablePackage { return nil }

// synthIndex type-checks files (whose contents are Go, whatever their
// extension) as one package and returns an identity-mapped sourceintel.Index
// over them: every authored byte maps to itself, so a cursor offset in the
// on-disk/open text addresses the index directly. It builds the index the LSP
// consumes without a codegen.Module open (~0.3s — see CLAUDE.md), which is
// what the handler-level tests here need: any keyable symbol will do.
func synthIndex(t *testing.T, pkgName string, files map[string]string) (*sourceintel.Index, *types.Package) {
	t.Helper()
	fset := token.NewFileSet()
	paths := slices.Sorted(maps.Keys(files))
	parsed := make([]*ast.File, 0, len(paths))
	for _, path := range paths {
		file, err := parser.ParseFile(fset, path, files[path], 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		parsed = append(parsed, file)
	}
	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Implicits:  map[ast.Node]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
		Scopes:     map[ast.Node]*types.Scope{},
	}
	typed, err := new(types.Config).Check(pkgName, fset, parsed, info)
	if err != nil {
		t.Fatalf("type-check %s: %v", pkgName, err)
	}
	mapped := make([]sourceintel.MappedFile, 0, len(parsed))
	for i, file := range parsed {
		source := files[paths[i]]
		sourceMap, err := sourceintel.IdentitySourceMap(paths[i], len(source))
		if err != nil {
			t.Fatalf("identity map %s: %v", paths[i], err)
		}
		mapped = append(mapped, sourceintel.MappedFile{
			AST:           file,
			TokenFile:     fset.File(file.Pos()),
			SourceMap:     sourceMap,
			SourceVersion: sourceintel.SourceVersion{Size: len(source), SHA256: sha256.Sum256([]byte(source))},
		})
	}
	return sourceintel.BuildIndex(info, mapped), typed
}

// synthPackage is synthIndex adapted to what the server retains per directory:
// the index plus the parsed .gsx files that mark the package as gsx-authored
// (what packageSymbolGraph gates the degraded answer on).
func synthPackage(t *testing.T, pkgName string, files map[string]string) *Package {
	t.Helper()
	index, typed := synthIndex(t, pkgName, files)
	gsxFset := token.NewFileSet()
	parsed := map[string]*gsxast.File{}
	for path, source := range files {
		if !strings.HasSuffix(path, ".gsx") {
			continue
		}
		file, err := gsxparser.ParseFile(gsxFset, path, []byte(source), 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		parsed[path] = file
	}
	return &Package{SourceIndex: index, Types: typed, GSXFset: gsxFset, Files: parsed}
}

// synthGraph is synthIndex keyed into a module-wide graph.
func synthGraph(t *testing.T, pkgName string, files map[string]string) *sourceintel.SymbolGraph {
	t.Helper()
	index, typed := synthIndex(t, pkgName, files)
	graph := sourceintel.NewSymbolGraph()
	graph.AddIndex(index, sourceintel.NewKeyer(typed))
	return graph
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
	return refsFrameDecl(id, uri, line, char, false)
}

func refsFrameDecl(id int, uri string, line, char int, includeDeclaration bool) string {
	return jsonFrame(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "textDocument/references",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"position":     map[string]any{"line": line, "character": char},
			"context":      map[string]any{"includeDeclaration": includeDeclaration},
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
	a := &moduleRefsAnalyzer{moduleGraph: nil} // nil result is valid (cached)
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

func TestReferencesAnalysisUsesCapturedRequestOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "page.gsx")
	uri := pathToURI(path)
	const captured = "package page\ncomponent Card() {}\n"
	const changed = "package page\ncomponent Changed() {}\n"
	analyzer := &moduleRefsAnalyzer{}
	server := &Server{analyzer: analyzer, docs: newDocStore()}
	server.docs.open(uri, captured, 1)
	sources := server.sourceSnapshot()

	server.docs.update(uri, changed, 2)
	server.refreshModuleGraph(sources, dir)

	if len(analyzer.overrides) != 1 {
		t.Fatalf("AnalyzeModule override calls = %d, want 1", len(analyzer.overrides))
	}
	if got := string(analyzer.overrides[0][path]); got != captured {
		t.Fatalf("AnalyzeModule override = %q, want captured request source %q", got, captured)
	}
}

// TestReferencesFallbackOnModuleError verifies that when AnalyzeModule returns
// an error, handleReferences falls back to the retained single-package index:
// the same-package reference in the sibling file is still reported.
func TestReferencesFallbackOnModuleError(t *testing.T) {
	dir := t.TempDir()
	declPath := filepath.Join(dir, "a.gsx")
	refPath := filepath.Join(dir, "other.go")
	declSource := "package x\n\nfunc Card() {}\n"
	refSource := "package x\n\nfunc use() { _ = Card }\n"
	for path, source := range map[string]string{declPath: declSource, refPath: refSource} {
		if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	a := &moduleRefsAnalyzer{
		moduleErr: errFake,
		pkg:       synthPackage(t, "x", map[string]string{declPath: declSource, refPath: refSource}),
	}
	uri := pathToURI(declPath)
	declOffset := strings.Index(declSource, "Card")
	position := positionForByteOffset(declSource, declOffset, encUTF16)

	out := drive(t, a, initFrame()+didOpenFrame(uri, declSource)+
		refsFrame(2, uri, position.Line, position.Character)+exitFrame())

	locations := referenceLocations(t, out, 2)
	refOffset := strings.Index(refSource, "_ = Card") + len("_ = ")
	want := Location{URI: pathToURI(refPath), Range: rangeForSpan(refSource, refOffset, refOffset+len("Card"), encUTF16)}
	if len(locations) != 1 || locations[0] != want {
		t.Fatalf("fallback references = %+v, want [%+v]\n%s", locations, want, out)
	}
	if a.moduleCalls == 0 {
		t.Fatal("AnalyzeModule was never attempted before the fallback")
	}
}

// TestReferencesModuleGraphIsKeyedByModule pins that the single cached module
// graph belongs to one module: a request in a second go.work module is a cache
// miss that gets its OWN graph, instead of being served the first module's
// graph (whose MatchesSource guard would fail and silently degrade the answer
// to the per-package index).
func TestReferencesModuleGraphIsKeyedByModule(t *testing.T) {
	root := t.TempDir()
	first := writeWorkspaceSymbolModule(t, filepath.Join(root, "first"))
	second := writeWorkspaceSymbolModule(t, filepath.Join(root, "second"))
	firstPath := filepath.Join(first, "page.gsx")
	secondPath := filepath.Join(second, "page.gsx")
	firstSource := "package first\n\nfunc Card() {}\n\nfunc useCard() { _ = Card }\n"
	secondSource := "package second\n\nfunc Panel() {}\n\nfunc usePanel() { _ = Panel }\n"
	a := &moduleRefsAnalyzer{graphsByDir: map[string]*sourceintel.SymbolGraph{
		first:  synthGraph(t, "first", map[string]string{firstPath: firstSource}),
		second: synthGraph(t, "second", map[string]string{secondPath: secondSource}),
	}}

	// Both buffers are opened BEFORE either request, so no mutation sits between
	// them: only the module key can make the second request a cache miss.
	firstCursor := positionForByteOffset(firstSource, strings.Index(firstSource, "Card"), encUTF16)
	secondCursor := positionForByteOffset(secondSource, strings.Index(secondSource, "Panel"), encUTF16)
	out := drive(t, a, workspaceSymbolInitializeFrame(first, second)+
		didOpenFrame(pathToURI(firstPath), firstSource)+
		didOpenFrame(pathToURI(secondPath), secondSource)+
		refsFrame(2, pathToURI(firstPath), firstCursor.Line, firstCursor.Character)+
		refsFrame(3, pathToURI(secondPath), secondCursor.Line, secondCursor.Character)+
		exitFrame())

	if a.moduleCalls != 2 {
		t.Fatalf("AnalyzeModule calls = %d (dirs %v), want one per module", a.moduleCalls, a.moduleDirs)
	}
	firstUse := strings.Index(firstSource, "_ = Card") + len("_ = ")
	wantFirst := Location{URI: pathToURI(firstPath), Range: rangeForSpan(firstSource, firstUse, firstUse+len("Card"), encUTF16)}
	if locations := referenceLocations(t, out, 2); len(locations) != 1 || locations[0] != wantFirst {
		t.Fatalf("first-module references = %+v, want [%+v]\n%s", locations, wantFirst, out)
	}
	secondUse := strings.Index(secondSource, "_ = Panel") + len("_ = ")
	wantSecond := Location{URI: pathToURI(secondPath), Range: rangeForSpan(secondSource, secondUse, secondUse+len("Panel"), encUTF16)}
	if locations := referenceLocations(t, out, 3); len(locations) != 1 || locations[0] != wantSecond {
		t.Fatalf("second-module references = %+v, want [%+v]\n%s", locations, wantSecond, out)
	}
}
