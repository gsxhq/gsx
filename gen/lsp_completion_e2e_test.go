package gen

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/lsp"
)

// newCompletionE2EFixture builds a temp module with a page package (User{Name
// string} in types.go, a page.gsx opened via SetOverride with a valid { user.Name
// } body) and returns the warm lspAnalyzer alongside the package dir and the
// page.gsx absolute path. It mirrors the hover e2e fixture's temp-module
// scaffolding (gen/lsp_hover_e2e_test.go) but drives the analyzer directly
// rather than through the JSON-RPC framing, since completion tasks call
// Analyzer methods straight. Grows through later completion tasks.
func newCompletionE2EFixture(t *testing.T) (a lspAnalyzer, dir, pagePath string) {
	t.Helper()
	root := t.TempDir()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) string {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	write("page/types.go", "package page\n\ntype User struct{ Name string }\n")
	pagePath = write("page/page.gsx", "package page\n\ncomponent Home(user User) {\n\t<div>{ user.Name }</div>\n}\n")

	a = newLSPAnalyzer(config{}, io.Discard)
	if _, err := a.SetOverride(pagePath, []byte("package page\n\ncomponent Home(user User) {\n\t<div>{ user.Name }</div>\n}\n")); err != nil {
		t.Fatalf("SetOverride: %v", err)
	}
	dir = filepath.Dir(pagePath)
	return a, dir, pagePath
}

func TestAnalyzeEphemeralViaAnalyzer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	a, dir, pagePath := newCompletionE2EFixture(t)
	patched := []byte("package page\n\ncomponent Home(user User) {\n\t<div>{ user._ }</div>\n}\n")
	pkg, err := a.AnalyzeEphemeral(dir, pagePath, patched)
	if err != nil {
		t.Fatalf("AnalyzeEphemeral: %v", err)
	}
	if pkg.Info == nil || len(pkg.ExprMap) == 0 {
		t.Fatal("ephemeral lsp.Package missing Info/ExprMap")
	}
	if len(pkg.Filters) == 0 {
		t.Fatal("ephemeral lsp.Package missing Filters")
	}

	generated := filepath.Join(dir, "page.x.go")
	if _, err := os.Stat(generated); !os.IsNotExist(err) {
		t.Fatalf("physical generated file exists: %s", generated)
	}
}

// TestGoCompletionE2E drives textDocument/completion through the full JSON-RPC
// server (runLSP) against a real temp module, exercising repair → classify →
// ephemeral analysis → scope bridge → enumeration end to end across all four Go
// identifier-position bridges:
//   - `{ us▮ }` (ExprMap) yields the `user` parameter (a local),
//   - `{{ ▮ }}` (CtrlMap/GoBlock) yields Go keywords plus locals,
//   - a top-level `func helper() { return Us▮ }` (GoChunk) yields package-scope
//     names plus keywords,
//   - a `component Home(user User▮)` type position (SigTypes) yields type names
//     without statement keywords.
func TestGoCompletionE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	root := t.TempDir()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) string {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	write("page/types.go", "package page\n\ntype User struct{ Name string }\n")
	// On-disk page is valid; the buffer opened below carries the mid-edit content.
	pagePath := write("page/page.gsx", "package page\n\ncomponent Home(user User) {\n\t<div>{ user.Name }</div>\n}\n")
	uri := "file://" + pagePath

	// Buffer under edit: a top-level GoChunk func body, an expression cursor after
	// `us`, and a statement-context `{{ }}` GoBlock inside a second component.
	source := "package page\n\n" +
		"func helper() User {\n\treturn Us\n}\n\n" +
		"component Home(user User) {\n\t<div>{ us }</div>\n}\n\n" +
		"component Block(item User) {\n\t{{  }}\n\t<span>{ item.Name }</span>\n}\n"

	chunkCursor := strings.Index(source, "return Us") + len("return Us")  // after `Us`
	exprCursor := strings.Index(source, "{ us }") + len("{ us")           // right after `us`
	blockCursor := strings.Index(source, "{{  }}") + len("{{ ")           // between the two spaces
	sigCursor := strings.Index(source, "(user User)") + len("(user User") // on the signature type `User`

	frame := func(value any) string {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return "Content-Length: " + strconv.Itoa(len(data)) + "\r\n\r\n" + string(data)
	}
	var input strings.Builder
	input.WriteString(frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}}))
	input.WriteString(frame(map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": source}},
	}))
	req := func(id, cursor int) {
		pos := lspUTF16PositionAt(source, cursor)
		input.WriteString(frame(map[string]any{
			"jsonrpc": "2.0", "id": id, "method": "textDocument/completion",
			"params": map[string]any{
				"textDocument": map[string]any{"uri": uri},
				"position":     map[string]any{"line": pos.Line, "character": pos.Character},
			},
		}))
	}
	req(2, exprCursor)
	req(3, blockCursor)
	req(4, chunkCursor)
	req(5, sigCursor)
	input.WriteString(frame(map[string]any{"jsonrpc": "2.0", "method": "exit"}))

	var output, stderr bytes.Buffer
	if code := runLSP(strings.NewReader(input.String()), &output, &stderr, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, stderr.String())
	}
	if strings.Contains(output.String(), ".x.go") {
		t.Fatalf("completion response exposed virtual generated Go:\n%s", output.String())
	}

	exprItems := completionLabels(t, output.String(), 2)
	if !exprItems["user"] {
		t.Errorf("expression completion missing local `user`; labels=%v", exprItems)
	}

	blockItems := completionLabels(t, output.String(), 3)
	if !blockItems["user"] && !blockItems["item"] {
		t.Errorf("statement completion missing a local (`user`/`item`); labels=%v", blockItems)
	}
	for _, kw := range []string{"return", "if", "for"} {
		if !blockItems[kw] {
			t.Errorf("statement completion missing keyword %q; labels=%v", kw, blockItems)
		}
	}

	// GoChunk: inside a top-level func body, package-scope names (the sibling
	// component `Home`, the func `helper`, the type `User`) are visible.
	chunkItems := completionLabels(t, output.String(), 4)
	for _, name := range []string{"User", "helper", "Home"} {
		if !chunkItems[name] {
			t.Errorf("GoChunk completion missing package-scope %q; labels=%v", name, chunkItems)
		}
	}
	// Statement context → keywords are offered here too.
	if !chunkItems["return"] {
		t.Errorf("GoChunk completion missing keyword `return`; labels=%v", chunkItems)
	}

	// Signature type: a cursor on a component parameter type enumerates visible
	// type names (the package type `User`). Type positions are not statement
	// contexts, so Go statement keywords are NOT offered.
	sigItems := completionLabels(t, output.String(), 5)
	if !sigItems["User"] {
		t.Errorf("signature-type completion missing type `User`; labels=%v", sigItems)
	}
	if sigItems["return"] {
		t.Errorf("signature-type completion should not offer statement keywords; labels=%v", sigItems)
	}
}

// TestGoMemberCompletionE2E drives textDocument/completion after a `.` end to
// end: the trailing-dot phantom skeleton repair (`{ user.▮ }`), a prefixed
// member (`{ user.N▮ }`) with its token-scoped edit, imported-package members
// (`{ strings.▮ }`), and the Task 9 scope gap that an imported package name
// (`strings`) is offered at a plain-ident cursor with the tierImported sort
// prefix. Each scenario opens its own tailored buffer so exactly one broken
// selector exists per request (the phantom heals it; the rest stays valid).
func TestGoMemberCompletionE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}

	// run opens source as a buffer, sends one completion at cursor, returns items.
	run := func(t *testing.T, source string, cursor int) []lsp.CompletionItem {
		t.Helper()
		root := t.TempDir()
		write := func(name, content string) string {
			path := filepath.Join(root, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			return path
		}
		write("go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
		write("page/types.go", "package page\n\ntype User struct {\n\tName string\n\tAge  int\n}\n")
		pagePath := write("page/page.gsx", "package page\n\ncomponent Home(user User) {\n\t<div>{ user.Name }</div>\n}\n")
		uri := "file://" + pagePath

		frame := func(value any) string {
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			return "Content-Length: " + strconv.Itoa(len(data)) + "\r\n\r\n" + string(data)
		}
		var input strings.Builder
		input.WriteString(frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}}))
		input.WriteString(frame(map[string]any{
			"jsonrpc": "2.0", "method": "textDocument/didOpen",
			"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": source}},
		}))
		pos := lspUTF16PositionAt(source, cursor)
		input.WriteString(frame(map[string]any{
			"jsonrpc": "2.0", "id": 2, "method": "textDocument/completion",
			"params": map[string]any{
				"textDocument": map[string]any{"uri": uri},
				"position":     map[string]any{"line": pos.Line, "character": pos.Character},
			},
		}))
		input.WriteString(frame(map[string]any{"jsonrpc": "2.0", "method": "exit"}))

		var output, stderr bytes.Buffer
		if code := runLSP(strings.NewReader(input.String()), &output, &stderr, config{}, nil); code != 0 {
			t.Fatalf("runLSP=%d stderr=%s", code, stderr.String())
		}
		if strings.Contains(output.String(), ".x.go") {
			t.Fatalf("completion response exposed virtual generated Go:\n%s", output.String())
		}
		return completionItems(t, output.String(), 2)
	}

	labelsOf := func(items []lsp.CompletionItem) map[string]bool {
		m := map[string]bool{}
		for _, it := range items {
			m[it.Label] = true
		}
		return m
	}

	// Scenario 1: trailing-dot phantom. `{ user. }` parses as gsx but the skeleton
	// selector is broken; the phantom heals it to `user._` and enumerates fields.
	t.Run("trailing-dot", func(t *testing.T) {
		source := "package page\n\ncomponent Home(user User) {\n\t<div>{ user. }</div>\n}\n"
		cursor := strings.Index(source, "user.") + len("user.") // right after the dot
		got := labelsOf(run(t, source, cursor))
		for _, name := range []string{"Name", "Age"} {
			if !got[name] {
				t.Errorf("trailing-dot member %q missing; labels=%v", name, got)
			}
		}
		if got["user"] {
			t.Errorf("member position must not offer scope locals; got `user`: %v", got)
		}
	})

	// Scenario 2: prefixed member `{ user.N }`. Name is offered and its edit
	// replaces only the `N` token (not the receiver, not the dot).
	t.Run("prefixed-member", func(t *testing.T) {
		source := "package page\n\ncomponent Home(user User) {\n\t<div>{ user.N }</div>\n}\n"
		nOff := strings.Index(source, "user.N") + len("user.")
		cursor := nOff + 1 // right after `N`
		items := run(t, source, cursor)
		var nameItem *lsp.CompletionItem
		for i := range items {
			if items[i].Label == "Name" {
				nameItem = &items[i]
			}
		}
		if nameItem == nil {
			t.Fatalf("prefixed member `Name` missing; labels=%v", labelsOf(items))
		}
		if nameItem.TextEdit == nil {
			t.Fatal("Name item has no TextEdit")
		}
		wantStart := lspUTF16PositionAt(source, nOff) // start of `N`
		wantEnd := lspUTF16PositionAt(source, nOff+1) // after `N`
		if nameItem.TextEdit.Range.Start != wantStart || nameItem.TextEdit.Range.End != wantEnd {
			t.Errorf("Name edit range = %+v, want [%+v,%+v) (the `N` token only)",
				nameItem.TextEdit.Range, wantStart, wantEnd)
		}
	})

	// Scenario 3: imported-package members. `strings` is imported (and used in a
	// sibling interp so it is not an unused import) and `{ strings. }` enumerates
	// its exported names.
	t.Run("package-member", func(t *testing.T) {
		source := "package page\n\nimport \"strings\"\n\ncomponent Home(user User) {\n\t<div>{ strings. }</div>\n\t<span>{ strings.ToUpper(user.Name) }</span>\n}\n"
		cursor := strings.Index(source, "{ strings. }") + len("{ strings.")
		got := labelsOf(run(t, source, cursor))
		for _, name := range []string{"ToUpper", "ToLower", "Contains"} {
			if !got[name] {
				t.Errorf("imported-package member %q missing; labels=%v", name, got)
			}
		}
	})

	// Scenario 4 (Task 9 scope gap): an imported package name completes at a plain
	// identifier cursor and carries the tierImported (40) sort prefix.
	t.Run("imported-name-in-scope", func(t *testing.T) {
		source := "package page\n\nimport \"strings\"\n\ncomponent Home(user User) {\n\t<div>{ str }</div>\n\t<span>{ strings.ToUpper(user.Name) }</span>\n}\n"
		cursor := strings.Index(source, "{ str }") + len("{ str")
		var stringsItem *lsp.CompletionItem
		items := run(t, source, cursor)
		for i := range items {
			if items[i].Label == "strings" {
				stringsItem = &items[i]
			}
		}
		if stringsItem == nil {
			t.Fatalf("imported name `strings` missing from scope completion; labels=%v", labelsOf(items))
		}
		if !strings.HasPrefix(stringsItem.SortText, "40") {
			t.Errorf("`strings` SortText = %q, want tierImported prefix \"40\"", stringsItem.SortText)
		}
	})
}

// TestPipeStageCompletionE2E drives textDocument/completion for a cursor after
// `|>` inside a pipeline end to end: `{ user.Name |> u }` (cursor right after
// `u`) offers the resolved filter table — here, std's built-in filters, which
// config{} registers by default (see TestAnalyzeEphemeralViaAnalyzer above),
// so `upper` and `urlquery` are both present.
func TestPipeStageCompletionE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	root := t.TempDir()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) string {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	write("page/types.go", "package page\n\ntype User struct{ Name string }\n")
	source := "package page\n\ncomponent Home(user User) {\n\t<div>{ user.Name |> u }</div>\n}\n"
	pagePath := write("page/page.gsx", source)
	uri := "file://" + pagePath

	cursor := strings.Index(source, "|> u") + len("|> u") // right after the `u`

	frame := func(value any) string {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return "Content-Length: " + strconv.Itoa(len(data)) + "\r\n\r\n" + string(data)
	}
	var input strings.Builder
	input.WriteString(frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}}))
	input.WriteString(frame(map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": source}},
	}))
	pos := lspUTF16PositionAt(source, cursor)
	input.WriteString(frame(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "textDocument/completion",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"position":     map[string]any{"line": pos.Line, "character": pos.Character},
		},
	}))
	input.WriteString(frame(map[string]any{"jsonrpc": "2.0", "method": "exit"}))

	var output, stderr bytes.Buffer
	if code := runLSP(strings.NewReader(input.String()), &output, &stderr, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, stderr.String())
	}

	got := completionLabels(t, output.String(), 2)
	for _, name := range []string{"upper", "urlquery"} {
		if !got[name] {
			t.Errorf("pipe-stage completion missing filter %q; labels=%v", name, got)
		}
	}
}

// TestTagCompletionE2E drives textDocument/completion for a ctxTag cursor end
// to end: a bare `<Ot▮` cursor offers the sibling local component ("Other"),
// and a qualified `<ui.▮` cursor offers the imported gsx package's component
// ("Button"). Each subtest builds its own temp module (the shared
// newCompletionE2EFixture has no second local component or ui package, and
// growing it is not cheap enough to be worth sharing across unrelated
// scenarios in this file).
func TestTagCompletionE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}

	frame := func(t *testing.T, value any) string {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return "Content-Length: " + strconv.Itoa(len(data)) + "\r\n\r\n" + string(data)
	}

	// runRaw opens source as a buffer, sends one completion at cursor, returns
	// the raw LSP output, and asserts the root files exist under a fresh temp
	// module.
	runRaw := func(t *testing.T, files map[string]string, gsxPath, source string, cursor int) string {
		t.Helper()
		root := t.TempDir()
		write := func(name, content string) string {
			path := filepath.Join(root, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			return path
		}
		write("go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
		var pagePath string
		for name, content := range files {
			p := write(name, content)
			if name == gsxPath {
				pagePath = p
			}
		}
		uri := "file://" + pagePath

		var input strings.Builder
		input.WriteString(frame(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}}))
		input.WriteString(frame(t, map[string]any{
			"jsonrpc": "2.0", "method": "textDocument/didOpen",
			"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": source}},
		}))
		pos := lspUTF16PositionAt(source, cursor)
		input.WriteString(frame(t, map[string]any{
			"jsonrpc": "2.0", "id": 2, "method": "textDocument/completion",
			"params": map[string]any{
				"textDocument": map[string]any{"uri": uri},
				"position":     map[string]any{"line": pos.Line, "character": pos.Character},
			},
		}))
		input.WriteString(frame(t, map[string]any{"jsonrpc": "2.0", "method": "exit"}))

		var output, stderr bytes.Buffer
		if code := runLSP(strings.NewReader(input.String()), &output, &stderr, config{}, nil); code != 0 {
			t.Fatalf("runLSP=%d stderr=%s", code, stderr.String())
		}
		if strings.Contains(output.String(), ".x.go") {
			t.Fatalf("completion response exposed virtual generated Go:\n%s", output.String())
		}
		return output.String()
	}

	// run is runRaw plus label extraction, for subtests that only need the
	// label set.
	run := func(t *testing.T, files map[string]string, gsxPath, source string, cursor int) map[string]bool {
		t.Helper()
		return completionLabels(t, runRaw(t, files, gsxPath, source, cursor), 2)
	}

	t.Run("local component", func(t *testing.T) {
		source := "package page\n\ncomponent Other() {\n\t<div/>\n}\n\ncomponent Home() {\n\t<div><Ot</div>\n}\n"
		cursor := strings.Index(source, "<Ot") + len("<Ot")
		got := run(t, map[string]string{"page/page.gsx": source}, "page/page.gsx", source, cursor)
		if !got["Other"] {
			t.Errorf("tag completion missing local component `Other`; labels=%v", got)
		}
	})

	t.Run("qualified import", func(t *testing.T) {
		uiSource := "package ui\n\ncomponent Button(label string) {\n\t<button>{label}</button>\n}\n"
		source := "package page\n\nimport \"example.com/app/ui\"\n\ncomponent Home() {\n\t<ui.Button label=\"hi\"/>\n\t<ui./>\n}\n"
		cursor := strings.LastIndex(source, "<ui./>") + len("<ui.")
		got := run(t, map[string]string{
			"page/page.gsx": source,
			"ui/ui.gsx":     uiSource,
		}, "page/page.gsx", source, cursor)
		if !got["Button"] {
			t.Errorf("qualified tag completion missing imported component `Button`; labels=%v", got)
		}
	})

	// aliased import guards the qualifier-resolution bug fixed alongside this
	// test: resolving a `<qualifier.` cursor via pkg.Types.Imports()[i].Name()
	// (the package's DECLARED name) rather than the file-scope *types.PkgName
	// object (the LOCAL name, alias-aware) meant `<myui.` returned zero
	// completions and the bare-`<` qualifier item inserted the invalid "ui."
	// instead of "myui.".
	t.Run("aliased import", func(t *testing.T) {
		uiSource := "package ui\n\ncomponent Button(label string) {\n\t<button>{label}</button>\n}\n"
		source := "package page\n\nimport myui \"example.com/app/ui\"\n\ncomponent Home() {\n\t<myui.Button label=\"hi\"/>\n\t<myui./>\n}\n"
		files := map[string]string{
			"page/page.gsx": source,
			"ui/ui.gsx":     uiSource,
		}

		qualifiedCursor := strings.LastIndex(source, "<myui./>") + len("<myui.")
		got := run(t, files, "page/page.gsx", source, qualifiedCursor)
		if !got["Button"] {
			t.Errorf("aliased tag completion missing imported component `Button`; labels=%v", got)
		}

		bareCursor := strings.Index(source, "<myui.Button") + len("<")
		items := completionItems(t, runRaw(t, files, "page/page.gsx", source, bareCursor), 2)
		const lspCompletionItemKindModule = 9 // ciKindModule in internal/lsp (unexported)
		var qualItem *lsp.CompletionItem
		for i := range items {
			if items[i].Kind == lspCompletionItemKindModule {
				qualItem = &items[i]
			}
		}
		if qualItem == nil {
			t.Fatalf("bare-cursor items = %+v, want a qualifier item for the aliased import", items)
		}
		if qualItem.Label != "myui" {
			t.Errorf("qualifier item Label = %q, want %q (the alias, not the declared name)", qualItem.Label, "myui")
		}
		if qualItem.TextEdit == nil || qualItem.TextEdit.NewText != "myui." {
			t.Errorf("qualifier item TextEdit = %+v, want NewText %q", qualItem.TextEdit, "myui.")
		}
	})
}

// TestComponentAttrCompletionE2E drives textDocument/completion for a
// ctxAttrName cursor on a planned component call end to end. Other declares
// an `attrs gsx.Attrs` catch-all so an in-progress, not-yet-matching typed
// attribute (e.g. a bare "t") is absorbed as an attrs-bag contributor instead
// of failing the whole call's plan with a component-missing-attrs
// diagnostic — codegen only publishes a ComponentCalls fact for a
// SUCCESSFULLY planned call (see internal/codegen/results.go), so any
// diagnostic on the call drops it from the map entirely; the attrs catch-all
// is what keeps the fact alive while the user is mid-word.
//
// Subtest 1: `<Other t▮/>` offers both unbound params ("open", "count") —
// drawn from Other's real signature via codegen's ComponentCalls fact,
// bridged from the handler's own unstamped classification element into the
// ephemeral analysis's element by elementAtTagOffset.
//
// Subtest 2: the cursor sits ON a half-typed name that already equals a real,
// bound param. This needs a BOOL-typed param ("open"): a bare `<Other open`
// parses as a BoolAttr, whose implied `true` value only type-checks (and
// therefore only survives into a published ComponentCalls fact) against a
// bool parameter — the same bare-word form against a STRING param (e.g.
// "title") fails validateComponentOperands's component-prop-type check and
// drops the whole call, so that combination can never reach this code path
// and is not a scenario this test can exercise. With "open" bound, the
// exclusion rule must not hide it while its own name is still being typed.
func TestComponentAttrCompletionE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}

	frame := func(t *testing.T, value any) string {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return "Content-Length: " + strconv.Itoa(len(data)) + "\r\n\r\n" + string(data)
	}

	run := func(t *testing.T, source string, cursor int) map[string]bool {
		t.Helper()
		root := t.TempDir()
		write := func(name, content string) string {
			path := filepath.Join(root, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			return path
		}
		write("go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
		pagePath := write("page/page.gsx", source)
		uri := "file://" + pagePath

		var input strings.Builder
		input.WriteString(frame(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}}))
		input.WriteString(frame(t, map[string]any{
			"jsonrpc": "2.0", "method": "textDocument/didOpen",
			"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": source}},
		}))
		pos := lspUTF16PositionAt(source, cursor)
		input.WriteString(frame(t, map[string]any{
			"jsonrpc": "2.0", "id": 2, "method": "textDocument/completion",
			"params": map[string]any{
				"textDocument": map[string]any{"uri": uri},
				"position":     map[string]any{"line": pos.Line, "character": pos.Character},
			},
		}))
		input.WriteString(frame(t, map[string]any{"jsonrpc": "2.0", "method": "exit"}))

		var output, stderr bytes.Buffer
		if code := runLSP(strings.NewReader(input.String()), &output, &stderr, config{}, nil); code != 0 {
			t.Fatalf("runLSP=%d stderr=%s", code, stderr.String())
		}
		if strings.Contains(output.String(), ".x.go") {
			t.Fatalf("completion response exposed virtual generated Go:\n%s", output.String())
		}
		return completionLabels(t, output.String(), 2)
	}

	t.Run("unbound param offered", func(t *testing.T) {
		source := "package page\n\nimport \"github.com/gsxhq/gsx\"\n\ncomponent Other(open bool, count int, attrs gsx.Attrs) {\n\t<div>{ count }</div>\n}\n\ncomponent Home() {\n\t<Other c/>\n}\n"
		cursor := strings.LastIndex(source, "<Other c") + len("<Other c")
		got := run(t, source, cursor)
		if !got["open"] {
			t.Errorf("component-attr completion missing unbound param `open`; labels=%v", got)
		}
		if !got["count"] {
			t.Errorf("component-attr completion missing unbound param `count`; labels=%v", got)
		}
	})

	t.Run("cursor on bound attr stays offered", func(t *testing.T) {
		// "open" is typed in FULL as a bare bool-attribute (no value — its
		// implied value is `true`) — the planner binds it to the real "open"
		// bool parameter as soon as the name matches and type-checks. The
		// cursor sits right after the "n" in "open", i.e. ON the attribute's
		// own token, not past it. The exclusion rule must not hide "open" here.
		source := "package page\n\nimport \"github.com/gsxhq/gsx\"\n\ncomponent Other(open bool, count int, attrs gsx.Attrs) {\n\t<div>{ count }</div>\n}\n\ncomponent Home() {\n\t<Other open/>\n}\n"
		cursor := strings.LastIndex(source, "<Other open") + len("<Other open")
		got := run(t, source, cursor)
		if !got["open"] {
			t.Errorf("component-attr completion must keep `open` offered while its own name is mid-typed; labels=%v", got)
		}
		if !got["count"] {
			t.Errorf("component-attr completion missing unbound param `count`; labels=%v", got)
		}
	})
}

// completionItems extracts the full CompletionItem slice from the completion
// response with the given id.
func completionItems(t *testing.T, output string, id int) []lsp.CompletionItem {
	t.Helper()
	for part := range strings.SplitSeq(output, "Content-Length:") {
		_, body, ok := strings.Cut(part, "\r\n\r\n")
		if !ok {
			continue
		}
		var response struct {
			ID     int                 `json:"id"`
			Result *lsp.CompletionList `json:"result"`
		}
		if err := json.Unmarshal([]byte(body), &response); err != nil || response.ID != id {
			continue
		}
		if response.Result == nil {
			t.Fatalf("completion response id %d has null result:\n%s", id, output)
		}
		return response.Result.Items
	}
	t.Fatalf("no completion response for id %d in:\n%s", id, output)
	return nil
}

// completionLabels extracts the set of item labels from the completion response
// with the given id.
func completionLabels(t *testing.T, output string, id int) map[string]bool {
	t.Helper()
	for part := range strings.SplitSeq(output, "Content-Length:") {
		_, body, ok := strings.Cut(part, "\r\n\r\n")
		if !ok {
			continue
		}
		var response struct {
			ID     int                 `json:"id"`
			Result *lsp.CompletionList `json:"result"`
		}
		if err := json.Unmarshal([]byte(body), &response); err != nil || response.ID != id {
			continue
		}
		if response.Result == nil {
			t.Fatalf("completion response id %d has null result:\n%s", id, output)
		}
		labels := map[string]bool{}
		for _, item := range response.Result.Items {
			labels[item.Label] = true
		}
		return labels
	}
	t.Fatalf("no completion response for id %d in:\n%s", id, output)
	return nil
}
