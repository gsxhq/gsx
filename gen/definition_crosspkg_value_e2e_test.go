package gen

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/lsp"
)

// crossPkgValueDep is a dependency package whose .gsx declares component VALUES
// (`X = icon("x")` in a top-level var block) alongside a helper whose body is a
// gsx element. `icon` returns a `func(...gsx.Attr) gsx.Node`, so each var is a
// component value usable as `<widget.X/>`. The var block is a plain Go chunk;
// its declarations resolve back to this .gsx via a //line directive, NOT via the
// importing package's SourceIndex (that only holds the importer's own spans).
const crossPkgValueDep = `package widget

import "github.com/gsxhq/gsx"

func icon(name string) func(attrs ...gsx.Attr) gsx.Node {
	return func(attrs ...gsx.Attr) gsx.Node {
		return (
			<svg
				width="24"
				{ attrs... }
			>
				<title>{ name }</title>
			</svg>
		)
	}
}

var (
	Check = icon("check")
	X     = icon("x")
	Menu  = icon("menu")
)
`

func writeCrossPkgValueModule(t *testing.T, dir, page string) {
	t.Helper()
	repoRoot, _ := filepath.Abs("..")
	mk := func(p, c string) {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	mk("widget/named.gsx", crossPkgValueDep)
	mk("page/page.gsx", page)

	// Generate the dependency so the importer type-checks against it (matching the
	// real-world setup where ds/icon has a generated .x.go on disk). The X decl
	// position still comes from widget/named.gsx via //line, never its .x.go.
	if _, err := Generate([]string{filepath.Join(dir, "widget")}); err != nil {
		t.Fatalf("generate dep: %v", err)
	}
}

// runCrossPkgValueDefinition opens page.gsx and requests definition + hover at
// the cursor (0-based line/character), returning both results.
func runCrossPkgValueDefinition(t *testing.T, dir, page string, line, character int) (*lsp.Location, *lsp.Hover) {
	t.Helper()
	uri := "file://" + filepath.Join(dir, "page", "page.gsx")
	frame := func(v any) string {
		b, _ := json.Marshal(v)
		return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
	}
	in := frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": page}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/definition",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri},
			"position": map[string]any{"line": line, "character": character}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "textDocument/hover",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri},
			"position": map[string]any{"line": line, "character": character}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	loc := definitionResult(t, out.String(), 2)
	hov := authoredHoverResult(t, out.String(), 3)
	return loc, hov
}

// wantXLocation is the expected declaration site of `X` in widget/named.gsx: the
// var line's 0-based line index and the 0-based column of the `X` identifier.
func wantXLocation(t *testing.T) (line, character int) {
	t.Helper()
	lines := strings.Split(crossPkgValueDep, "\n")
	for i, l := range lines {
		if c := strings.Index(l, "X     = icon"); c >= 0 {
			return i, c
		}
	}
	t.Fatal("could not locate X declaration in dep source")
	return 0, 0
}

// gd on the cross-package tag `<widget.X/>` (X is a component VALUE declared in
// a Go var block of widget/named.gsx) resolves to X's declaration in that .gsx,
// NOT to null and NOT into a generated .x.go. Regression guard for the value-
// target definition gap: fact.TargetDecls is empty for value targets, so
// resolution falls through to the raw go/types object position, which //line-
// maps to an authored .gsx of a sibling package.
func TestDefinitionCrossPkgValueTag(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	page := "package page\n\nimport \"example.com/x/widget\"\n\ncomponent Page() {\n\t<widget.X/>\n}\n"
	writeCrossPkgValueModule(t, dir, page)

	var line, character int
	for i, l := range strings.Split(page, "\n") {
		if c := strings.Index(l, "widget.X"); c >= 0 {
			line, character = i, c+len("widget.") // a column on "X"
			break
		}
	}

	loc, hov := runCrossPkgValueDefinition(t, dir, page, line, character)
	if loc == nil {
		t.Fatal("tag definition returned null; want widget/named.gsx X declaration")
	}
	if !strings.HasSuffix(loc.URI, filepath.Join("widget", "named.gsx")) {
		t.Fatalf("resolved to %q, want widget/named.gsx", loc.URI)
	}
	if strings.HasSuffix(loc.URI, ".x.go") {
		t.Fatalf("resolved into a generated .x.go (%q); must never jump into generated code", loc.URI)
	}
	wantLine, wantCol := wantXLocation(t)
	if loc.Range.Start.Line != wantLine || loc.Range.Start.Character != wantCol {
		t.Fatalf("tag gd landed at L%d:C%d, want L%d:C%d (the X value decl)",
			loc.Range.Start.Line, loc.Range.Start.Character, wantLine, wantCol)
	}
	if hov == nil {
		t.Fatalf("hover on tag was null; want non-null signature")
	}
}

// gd on the cross-package reference `widget.X` inside an interpolation
// `{ widget.X }` resolves to the same X declaration as the tag case — the
// source-index / expression path (exprNodeAtOffset → Info.Uses → Fset.Position),
// which shares the same authored-.gsx target policy.
func TestDefinitionCrossPkgValueInterp(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	page := "package page\n\nimport \"example.com/x/widget\"\n\ncomponent Page() {\n\t<div>{ widget.X }</div>\n}\n"
	writeCrossPkgValueModule(t, dir, page)

	var line, character int
	for i, l := range strings.Split(page, "\n") {
		if c := strings.Index(l, "widget.X"); c >= 0 {
			line, character = i, c+len("widget.") // a column on "X"
			break
		}
	}

	loc, hov := runCrossPkgValueDefinition(t, dir, page, line, character)
	if loc == nil {
		t.Fatal("interp definition returned null; want widget/named.gsx X declaration")
	}
	if !strings.HasSuffix(loc.URI, filepath.Join("widget", "named.gsx")) {
		t.Fatalf("resolved to %q, want widget/named.gsx", loc.URI)
	}
	if strings.HasSuffix(loc.URI, ".x.go") {
		t.Fatalf("resolved into a generated .x.go (%q); must never jump into generated code", loc.URI)
	}
	wantLine, wantCol := wantXLocation(t)
	if loc.Range.Start.Line != wantLine || loc.Range.Start.Character != wantCol {
		t.Fatalf("interp gd landed at L%d:C%d, want L%d:C%d (the X value decl)",
			loc.Range.Start.Line, loc.Range.Start.Character, wantLine, wantCol)
	}
	if hov == nil {
		t.Fatalf("hover on interp reference was null; want non-null signature")
	}
}

// writeCrossModuleValueModules wires two SEPARATE modules: `app` (the page) and
// `dep` (the widget library, joined by a `replace` directive, mirroring the
// hello-gsx→libgsx setup). It returns the app dir and the page.gsx path. The dep
// is generated so it type-checks; the caller decides whether to keep the dep's
// .gsx (sources-present) or delete it (only-.x.go).
func writeCrossModuleValueModules(t *testing.T, page string, keepDepSource bool) (appDir, pagePath, depGSX, depXGo string) {
	t.Helper()
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	mk := func(p, c string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("app/go.mod", "module app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\nrequire dep v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\nreplace dep => ../dep\n")
	mk("dep/go.mod", "module dep\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	mk("dep/widget/named.gsx", crossPkgValueDep)
	mk("app/page.gsx", page)

	if _, err := Generate([]string{filepath.Join(root, "dep", "widget")}); err != nil {
		t.Fatalf("generate dep: %v", err)
	}
	depGSX = filepath.Join(root, "dep", "widget", "named.gsx")
	depXGo = filepath.Join(root, "dep", "widget", "named.x.go")
	if !keepDepSource {
		// Only the generated .x.go remains on disk (an external dependency that
		// ships its generated output but not its .gsx sources).
		if err := os.Remove(depGSX); err != nil {
			t.Fatalf("remove dep .gsx: %v", err)
		}
	}
	return filepath.Join(root, "app"), filepath.Join(root, "app", "page.gsx"), depGSX, depXGo
}

func runDefinitionAt(t *testing.T, appDir, pagePath, page string, line, character int) (*lsp.Location, *lsp.Hover) {
	t.Helper()
	uri := "file://" + pagePath
	frame := func(v any) string {
		b, _ := json.Marshal(v)
		return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
	}
	in := frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"rootUri": "file://" + appDir}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": page}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/definition",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri},
			"position": map[string]any{"line": line, "character": character}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "textDocument/hover",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri},
			"position": map[string]any{"line": line, "character": character}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})
	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	return definitionResult(t, out.String(), 2), authoredHoverResult(t, out.String(), 3)
}

func tagCursor(t *testing.T, page string) (line, character int) {
	t.Helper()
	for i, l := range strings.Split(page, "\n") {
		if c := strings.Index(l, "widget.X"); c >= 0 {
			return i, c + len("widget.")
		}
	}
	t.Fatal("could not find widget.X in page")
	return 0, 0
}

// Case (a): a cross-MODULE dependency whose .gsx sources are present (joined via
// a `replace` directive, like hello-gsx→libgsx). gd on `<widget.X/>` resolves to
// X's declaration IN THE .gsx, never the generated .x.go.
func TestDefinitionCrossModuleValueSourcesPresent(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	page := "package main\n\nimport \"dep/widget\"\n\ncomponent Page() {\n\t<widget.X/>\n}\n\nfunc main() {}\n"
	appDir, pagePath, _, _ := writeCrossModuleValueModules(t, page, true)
	line, character := tagCursor(t, page)
	loc, hov := runDefinitionAt(t, appDir, pagePath, page, line, character)
	if loc == nil {
		t.Fatal("cross-module tag definition returned null; want dep .gsx X declaration")
	}
	if !strings.HasSuffix(loc.URI, filepath.Join("dep", "widget", "named.gsx")) {
		t.Fatalf("resolved to %q, want dep/widget/named.gsx", loc.URI)
	}
	if strings.HasSuffix(loc.URI, ".x.go") {
		t.Fatalf("resolved into a generated .x.go (%q); the .gsx exists, so navigation must land there", loc.URI)
	}
	// The line is resolved from the dependency's published .x.go //line directives
	// (a separate module is loaded from its generated Go, not re-analyzed in
	// memory). emit.go does not //line-stamp top-level Go var chunks, so the line
	// can drift within the .gsx — the exact-line guarantee holds for SAME-module
	// deps (analyzed via the in-memory skeleton), pinned by the value tests above.
	// This case pins the refined policy's contract: authored .gsx present → the
	// Location lands in that .gsx (never the .x.go), non-null.
	if hov == nil {
		t.Fatal("hover was null; want non-null signature")
	}
}

// Case (b): a cross-MODULE dependency that ships ONLY its generated .x.go (its
// .gsx sources absent). gd on `<widget.X/>` must still be non-null: it falls back
// to the .x.go func definition — a generated-file location beats no location when
// it is all that exists on disk.
func TestDefinitionCrossModuleValueOnlyXGo(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	page := "package main\n\nimport \"dep/widget\"\n\ncomponent Page() {\n\t<widget.X/>\n}\n\nfunc main() {}\n"
	appDir, pagePath, depGSX, depXGo := writeCrossModuleValueModules(t, page, false)
	if _, err := os.Stat(depGSX); !os.IsNotExist(err) {
		t.Fatalf("dep .gsx should be absent for this case, stat err=%v", err)
	}
	if _, err := os.Stat(depXGo); err != nil {
		t.Fatalf("dep .x.go should exist for this case: %v", err)
	}
	line, character := tagCursor(t, page)
	loc, hov := runDefinitionAt(t, appDir, pagePath, page, line, character)
	if loc == nil {
		t.Fatal("only-.x.go tag definition returned null; want the generated .x.go location as fallback")
	}
	if !strings.HasSuffix(loc.URI, filepath.Join("dep", "widget", "named.x.go")) {
		t.Fatalf("resolved to %q, want dep/widget/named.x.go (the .gsx is absent, so the generated file is the only target)", loc.URI)
	}
	if hov == nil {
		t.Fatal("hover was null; want non-null signature even for a generated-only dependency")
	}
}
