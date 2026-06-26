package gen

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// gd on the cross-package tag `components.Input` in <components.Input .../>
// resolves to `component Input(...)` in the imported package's .gsx.
func TestDefinitionCrossPkgTag(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	mk := func(p, c string) {
		t.Helper()
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	mk("ui/components/comp.gsx", "package components\n\ncomponent Input(name string) {\n\t<input value={name}/>\n}\n")
	page := "package x\n\nimport \"example.com/x/ui/components\"\n\ncomponent Page() {\n\t<components.Input name=\"a\"/>\n}\n"
	mk("page.gsx", page)

	// Generate the dependency so the importer type-checks against it (deps must be
	// importable; the decl position itself comes from the dep .gsx, not its .x.go).
	if _, err := Generate([]string{filepath.Join(dir, "ui", "components")}); err != nil {
		t.Fatalf("generate dep: %v", err)
	}

	uri := "file://" + filepath.Join(dir, "page.gsx")
	lines := strings.Split(page, "\n")
	var line, character int
	for i, l := range lines {
		if c := strings.Index(l, "components.Input"); c >= 0 {
			line, character = i, c+len("components.")+1 // a column on "Input"
			break
		}
	}

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
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	loc := definitionResult(t, out.String(), 2)
	if loc == nil {
		t.Fatalf("definition returned null; out:\n%s\nstderr:\n%s", out.String(), errBuf.String())
	}
	if !strings.HasSuffix(loc.URI, filepath.Join("ui", "components", "comp.gsx")) {
		t.Fatalf("resolved to %q, want ui/components/comp.gsx", loc.URI)
	}
	// `component Input(...)` is on line index 2 of comp.gsx; expect the `Input` name.
	if loc.Range.Start.Line != 2 {
		t.Fatalf("landed on line %d, want 2 (the Input decl)", loc.Range.Start.Line)
	}
}

// gd on the `name` attribute of <components.Input name="a"/> resolves to the
// `name` parameter of `component Input(name string)` in the imported package.
func TestDefinitionCrossPkgAttrParam(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	mk := func(p, c string) {
		t.Helper()
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	depSrc := "package components\n\ncomponent Input(name string) {\n\t<input value={name}/>\n}\n"
	mk("ui/components/comp.gsx", depSrc)
	page := "package x\n\nimport \"example.com/x/ui/components\"\n\ncomponent Page() {\n\t<components.Input name=\"a\"/>\n}\n"
	mk("page.gsx", page)
	if _, err := Generate([]string{filepath.Join(dir, "ui", "components")}); err != nil {
		t.Fatalf("generate dep: %v", err)
	}

	uri := "file://" + filepath.Join(dir, "page.gsx")
	pageLines := strings.Split(page, "\n")
	var line, character int
	for i, l := range pageLines {
		if c := strings.Index(l, "name=\""); c >= 0 {
			line, character = i, c // the 'n' of the `name` attribute
			break
		}
	}

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
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	loc := definitionResult(t, out.String(), 2)
	if loc == nil {
		t.Fatalf("definition returned null; out:\n%s\nstderr:\n%s", out.String(), errBuf.String())
	}
	if !strings.HasSuffix(loc.URI, filepath.Join("ui", "components", "comp.gsx")) {
		t.Fatalf("resolved to %q, want ui/components/comp.gsx", loc.URI)
	}
	// `component Input(name string)` is line index 2; expect the `name` PARAM.
	depLines := strings.Split(depSrc, "\n")
	wantCol := strings.Index(depLines[2], "name string")
	if loc.Range.Start.Line != 2 || loc.Range.Start.Character != wantCol {
		t.Fatalf("landed at L%d:C%d, want L2:C%d (the name param)",
			loc.Range.Start.Line, loc.Range.Start.Character, wantCol)
	}
}

// gd on the cross-package call `helpers.Thing` inside an EXPRESSION interp
// `{ helpers.Thing() }` resolves to the `component Thing()` declaration in the
// imported package's .gsx — with NO .x.go on disk for either package (the warm
// Module resolves the sibling from skeletons). This exercises the expression
// go-to-def path (exprNodeAtOffset → innermostIdent → Info.Uses → Fset.Position),
// the path NOT covered by the tag/attr cross-pkg tests above. It is a regression
// guard for the single-shared-Module-fset fix: before it, the sibling package was
// analyzed in a separate (discarded) FileSet, so resolving the dep object's
// position against the importing package's fset returned a wrong/empty location
// (a random spot in page.gsx, never helpers.gsx).
func TestDefinitionCrossPkgExprCall(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	mk := func(p, c string) {
		t.Helper()
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	// `component Thing()` decl is on line index 2 of helpers.gsx.
	mk("helpers/helpers.gsx", "package helpers\n\ncomponent Thing() {\n\t<span>thing</span>\n}\n")
	page := "package page\n\nimport \"example.com/x/helpers\"\n\ncomponent Page() {\n\t<div>{ helpers.Thing() }</div>\n}\n"
	mk("page/page.gsx", page)
	// Deliberately do NOT Generate either package: no .x.go on disk. The warm
	// Module must resolve helpers.Thing from in-memory skeletons.

	uri := "file://" + filepath.Join(dir, "page", "page.gsx")
	lines := strings.Split(page, "\n")
	var line, character int
	for i, l := range lines {
		if c := strings.Index(l, "helpers.Thing"); c >= 0 {
			line, character = i, c+len("helpers.")+1 // a column on "Thing"
			break
		}
	}

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
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	loc := definitionResult(t, out.String(), 2)
	if loc == nil {
		t.Fatalf("definition returned null; out:\n%s\nstderr:\n%s", out.String(), errBuf.String())
	}
	if !strings.HasSuffix(loc.URI, filepath.Join("helpers", "helpers.gsx")) {
		t.Fatalf("resolved to %q, want helpers/helpers.gsx (NOT a spot inside page.gsx)", loc.URI)
	}
	// `component Thing()` is on line index 2 of helpers.gsx.
	if loc.Range.Start.Line != 2 {
		t.Fatalf("landed on line %d of %s, want 2 (the Thing decl)", loc.Range.Start.Line, loc.URI)
	}
}

// TestDefinitionCrossPkgClosingTag verifies that go-to-definition works when
// the cursor sits on the closing tag of a cross-package component invocation
// (the "Input" in </components.Input>), not just on the opening tag. This is
// Bug B (cross-package): crossPkgTagDeclAt previously only checked el.Pos()
// (opening), so a cursor on the closing tag returned null.
func TestDefinitionCrossPkgClosingTag(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	mk := func(p, c string) {
		t.Helper()
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	mk("ui/components/comp.gsx", "package components\n\ncomponent Input(name string) {\n\t<input value={name}/>\n}\n")
	// Use an explicit (non-self-closing) call so the parser sets CloseNamePos on
	// the element — empty children is fine; codegen treats it like self-closing.
	page := "package x\n\nimport \"example.com/x/ui/components\"\n\ncomponent Page() {\n\t<components.Input name=\"a\"></components.Input>\n}\n"
	mk("page.gsx", page)

	// Generate the dependency so the importer type-checks against it.
	if _, err := Generate([]string{filepath.Join(dir, "ui", "components")}); err != nil {
		t.Fatalf("generate dep: %v", err)
	}

	uri := "file://" + filepath.Join(dir, "page.gsx")
	lines := strings.Split(page, "\n")
	var line, character int
	for i, l := range lines {
		if c := strings.Index(l, "</components.Input>"); c >= 0 {
			// Cursor on "Input" in the closing tag: skip "</" then "components."
			// c = column of '<', c+2 = column of 'c' in "components.Input",
			// c+2+len("components.")+1 = second character of "Input" (the 'n').
			line, character = i, c+2+len("components.")+1
			break
		}
	}

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
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	loc := definitionResult(t, out.String(), 2)
	if loc == nil {
		t.Fatalf("closing-tag definition returned null (Bug B cross-package); out:\n%s\nstderr:\n%s", out.String(), errBuf.String())
	}
	if !strings.HasSuffix(loc.URI, filepath.Join("ui", "components", "comp.gsx")) {
		t.Fatalf("resolved to %q, want ui/components/comp.gsx", loc.URI)
	}
	// `component Input(...)` is on line index 2 of comp.gsx; expect the `Input` name.
	if loc.Range.Start.Line != 2 {
		t.Fatalf("landed on line %d, want 2 (the Input decl)", loc.Range.Start.Line)
	}
}
