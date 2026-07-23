package gen

import (
	"bytes"
	"encoding/json"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode"

	gsxparser "github.com/gsxhq/gsx/parser"

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

// BenchmarkAnalyzeEphemeralWarm measures the per-keystroke completion latency
// floor: one warm AnalyzeEphemeral call over the `{ user._ }` phantom-repaired
// buffer, against a fixture whose Module cache was already primed by one
// Analyze call (the same warm per-root codegen.Module — see lspAnalyzer.module
// — that a live LSP session's completion handler reuses across requests). Step
// 3 of the Task 16 brief: there is no pass/fail bar here, only the measured
// baseline the spec wants recorded before any tuning discussion.
func BenchmarkAnalyzeEphemeralWarm(b *testing.B) {
	root := b.TempDir()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		b.Fatal(err)
	}
	write := func(name, content string) string {
		b.Helper()
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			b.Fatal(err)
		}
		return path
	}
	write("go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	write("page/types.go", "package page\n\ntype User struct{ Name string }\n")
	live := []byte("package page\n\ncomponent Home(user User) {\n\t<div>{ user.Name }</div>\n}\n")
	pagePath := write("page/page.gsx", string(live))
	dir := filepath.Dir(pagePath)

	a := newLSPAnalyzer(config{}, io.Discard)
	if _, err := a.SetOverride(pagePath, live); err != nil {
		b.Fatalf("SetOverride: %v", err)
	}
	// Pre-warm: one Package() call via the analyzer's own Analyze method, same
	// as the LSP server does on first open — this is what fills the warm
	// per-root Module's type cache that AnalyzeEphemeral below reuses.
	if _, err := a.Analyze(dir, nil); err != nil {
		b.Fatalf("warm Analyze: %v", err)
	}

	patched := []byte("package page\n\ncomponent Home(user User) {\n\t<div>{ user._ }</div>\n}\n")
	b.ReportAllocs()
	for b.Loop() {
		pkg, err := a.AnalyzeEphemeral(dir, pagePath, patched)
		if err != nil {
			b.Fatal(err)
		}
		if pkg == nil || pkg.Info == nil {
			b.Fatal("AnalyzeEphemeral returned no Info")
		}
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

	// Buffer under edit: a top-level GoChunk with two adjacent funcs (a bare
	// cursor sits in the blank line BETWEEN them), an expression cursor after
	// `us`, and a statement-context `{{ }}` GoBlock inside a second component.
	source := "package page\n\n" +
		"import \"strings\"\n\n" +
		"func helper() User {\n\treturn Us\n}\n\n" +
		"func greet() string {\n\treturn strings.ToUpper(\"x\")\n}\n\n" +
		"component Home(user User) {\n\t<div>{ us }</div>\n}\n\n" +
		"component Block(item User) {\n\t{{  }}\n\t<span>{ item.Name }</span>\n}\n"

	chunkCursor := strings.Index(source, "return Us") + len("return Us")  // after `Us`
	exprCursor := strings.Index(source, "{ us }") + len("{ us")           // right after `us`
	blockCursor := strings.Index(source, "{{  }}") + len("{{ ")           // between the two spaces
	sigCursor := strings.Index(source, "(user User)") + len("(user User") // on the signature type `User`
	// A bare GoChunk cursor in the blank line between the two top-level funcs
	// (helper's `}` and `func greet`): no enclosing func body, only the file
	// scope — where imported package names live.
	betweenCursor := strings.Index(source, "}\n\nfunc greet") + len("}\n")

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
	req(6, betweenCursor)
	input.WriteString(frame(map[string]any{"jsonrpc": "2.0", "method": "exit"}))

	var output, stderr bytes.Buffer
	if code := runLSP(strings.NewReader(input.String()), &output, &stderr, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, stderr.String())
	}
	if strings.Contains(output.String(), ".x.go") {
		t.Fatalf("completion response exposed virtual generated Go:\n%s", output.String())
	}
	assertNoGsxInternalLeak(t, output.String())

	exprItems := completionLabels(t, output.String(), 2)
	if !exprItems["user"] {
		t.Errorf("expression completion missing local `user`; labels=%v", exprItems)
	}
	// Case 3 (Task 16 brief): the package-scope sibling component `Block` is
	// ALSO offered at the same plain-ident cursor (package scope is
	// order-independent — scopeCandidates never applies the declared-after
	// filter to it), and locals sort ahead of package scope: tierLocal's "05"
	// SortText prefix orders before tierPackage's "30" (completion_items.go).
	exprFullItems := completionItems(t, output.String(), 2)
	var userItem, blockItem *lsp.CompletionItem
	for i := range exprFullItems {
		switch exprFullItems[i].Label {
		case "user":
			userItem = &exprFullItems[i]
		case "Block":
			blockItem = &exprFullItems[i]
		}
	}
	if userItem == nil {
		t.Fatalf("expression completion missing local `user` item; labels=%v", exprItems)
	}
	if blockItem == nil {
		t.Fatalf("expression completion missing package-scope component `Block`; labels=%v", exprItems)
	}
	if !strings.HasPrefix(userItem.SortText, "05") {
		t.Errorf("`user` SortText = %q, want tierLocal prefix \"05\"", userItem.SortText)
	}
	if !strings.HasPrefix(blockItem.SortText, "30") {
		t.Errorf("`Block` SortText = %q, want tierPackage prefix \"30\"", blockItem.SortText)
	}
	if userItem.SortText >= blockItem.SortText {
		t.Errorf("local `user` (SortText %q) must sort before package-scope `Block` (SortText %q)", userItem.SortText, blockItem.SortText)
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

	// Bare GoChunk position between two top-level funcs (T4): the enclosing file
	// scope resolves, so the imported package name `strings` completes
	// (tierImported), alongside package-scope decls and statement keywords.
	betweenItems := completionLabels(t, output.String(), 6)
	if !betweenItems["strings"] {
		t.Errorf("bare GoChunk completion missing imported package `strings`; labels=%v", betweenItems)
	}
	for _, name := range []string{"helper", "greet", "Home"} {
		if !betweenItems[name] {
			t.Errorf("bare GoChunk completion missing package-scope %q; labels=%v", name, betweenItems)
		}
	}
	if !betweenItems["func"] {
		t.Errorf("bare GoChunk completion missing keyword `func`; labels=%v", betweenItems)
	}
	// The `strings` item carries the tierImported (40) sort prefix.
	for _, it := range completionItems(t, output.String(), 6) {
		if it.Label == "strings" && !strings.HasPrefix(it.SortText, "40") {
			t.Errorf("imported `strings` SortText = %q, want tierImported prefix \"40\"", it.SortText)
		}
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
		assertNoGsxInternalLeak(t, output.String())
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

	// Scenario 2b (Task 16 brief case 15): a UTF-16 surrogate-pair rune (an
	// astral-plane character, U+1D518, needing TWO UTF-16 code units) sits in
	// the HTML text content earlier on the SAME line as a prefixed member
	// cursor. The response's TextEdit range must be expressed in UTF-16
	// code-unit coordinates that account for the surrogate pair — comparing
	// against lspUTF16PositionAt (itself unicode/utf16-based, same as the
	// "prefixed-member" scenario above) pins that the production position
	// conversion counts code units, not runes or bytes.
	t.Run("utf16-surrogate-pair", func(t *testing.T) {
		source := "package page\n\ncomponent Home(user User) {\n\t<div>𝔘{ user.N }</div>\n}\n"
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
			t.Fatalf("utf16 prefixed member `Name` missing; labels=%v", labelsOf(items))
		}
		if nameItem.TextEdit == nil {
			t.Fatal("Name item has no TextEdit")
		}
		wantStart := lspUTF16PositionAt(source, nOff)
		wantEnd := lspUTF16PositionAt(source, nOff+1)
		if nameItem.TextEdit.Range.Start != wantStart || nameItem.TextEdit.Range.End != wantEnd {
			t.Errorf("Name edit range = %+v, want [%+v,%+v) accounting for the preceding UTF-16 surrogate pair",
				nameItem.TextEdit.Range, wantStart, wantEnd)
		}
	})

	// Scenario 3: imported-package members. `strings` is imported (and used in a
	// sibling interp so it is not an unused import) and `{ strings. }` enumerates
	// its exported names.
	t.Run("package-member", func(t *testing.T) {
		source := "package page\n\nimport \"strings\"\n\ncomponent Home(user User) {\n\t<div>{ strings. }</div>\n\t<span>{ strings.ToUpper(user.Name) }</span>\n}\n"
		cursor := strings.Index(source, "{ strings. }") + len("{ strings.")
		items := run(t, source, cursor)
		got := labelsOf(items)
		for _, name := range []string{"ToUpper", "ToLower", "Contains"} {
			if !got[name] {
				t.Errorf("imported-package member %q missing; labels=%v", name, got)
			}
		}
		// Case 4 (Task 16 brief): unexported members are never offered.
		// memberCompletionItems' package-member branch filters on
		// obj.Exported(), so every candidate name here must start uppercase.
		for _, it := range items {
			if it.Label == "" {
				continue
			}
			if r := []rune(it.Label)[0]; unicode.IsLower(r) {
				t.Errorf("imported-package member completion leaked unexported name %q", it.Label)
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

// TestPipeStageEmptyCompletionE2E drives textDocument/completion for a cursor
// at an EMPTY pipe stage end to end — case 6 of the Task 16 brief:
// `{ user.Name |>  }` (cursor between the two spaces after `|>`, nothing typed
// yet) still offers the full filter table. Task 7's phantom-stage repair heals
// the empty stage into a valid selector; completionTokenSpan then computes a
// zero-width span at the cursor over the ORIGINAL text (see
// pipeStageCompletion's doc comment), so nothing is typed to filter on and
// nothing leaks into the edit.
func TestPipeStageEmptyCompletionE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	extra := map[string]string{"page/types.go": "package page\n\ntype User struct{ Name string }\n"}
	source := "package page\n\ncomponent Home(user User) {\n\t<div>{ user.Name |>  }</div>\n}\n"
	cursor := strings.Index(source, "|> ") + len("|> ")
	items := runHTMLCompletionE2E(t, extra, source, cursor)
	if len(items) == 0 {
		t.Fatal("empty pipe-stage completion returned zero items")
	}
	labels := map[string]bool{}
	for _, it := range items {
		labels[it.Label] = true
	}
	for _, name := range []string{"upper", "urlquery"} {
		if !labels[name] {
			t.Errorf("empty pipe-stage completion missing filter %q; labels=%v", name, labels)
		}
	}
}

// TestPipeStageTypedNarrowingE2E drives the typed pipe-filter compatibility
// filtering end to end: at `{ user.Age |> ▮ }` the incoming type is int, so
// string-subject filters (upper) are withheld while the any-subject printf and
// generic default are offered. A companion `{ user.Name |> lower }` pipe imports
// std into the skeleton universe so the candidate signatures resolve (a filter
// whose package is not imported fails open — offered — per the design's
// per-candidate fail-open rule).
func TestPipeStageTypedNarrowingE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	extra := map[string]string{"page/types.go": "package page\n\ntype User struct{ Name string; Age int }\n"}
	source := "package page\n\ncomponent Home(user User) {\n\t<div>{ user.Name |> lower }{ user.Age |> u }</div>\n}\n"
	cursor := strings.Index(source, "|> u") + len("|> u")
	items := runHTMLCompletionE2E(t, extra, source, cursor)
	labels := map[string]bool{}
	for _, it := range items {
		labels[it.Label] = true
	}
	for _, name := range []string{"printf", "default"} {
		if !labels[name] {
			t.Errorf("int-seed pipe completion missing any/generic filter %q; labels=%v", name, labels)
		}
	}
	if labels["upper"] {
		t.Errorf("int-seed pipe completion offered string-subject filter %q; labels=%v", "upper", labels)
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

	t.Run("local plus qualifier", func(t *testing.T) {
		// Case 7 (Task 16 brief): at a PREFIXED bare cursor `<Ot`, both the
		// matching local sibling component (`Other`) and an unrelated imported
		// package's qualifier item (`ui`) are offered together — the server
		// returns the full candidate set unfiltered by the typed prefix
		// (componentTagItems' doc comment: "the client matches against
		// label/filterText as the user types").
		uiSource := "package ui\n\ncomponent Button(label string) {\n\t<button>{label}</button>\n}\n"
		// `<ui.Button/>` keeps the import used (an unused import is a compile
		// error that would sink the whole package to a shell — same gotcha the
		// "qualified import"/"aliased import" scenarios below guard against).
		source := "package page\n\nimport \"example.com/app/ui\"\n\ncomponent Other() {\n\t<div/>\n}\n\ncomponent Home() {\n\t<ui.Button label=\"hi\"/>\n\t<div><Ot</div>\n}\n"
		cursor := strings.LastIndex(source, "<Ot") + len("<Ot")
		files := map[string]string{
			"page/page.gsx": source,
			"ui/ui.gsx":     uiSource,
		}
		items := completionItems(t, runRaw(t, files, "page/page.gsx", source, cursor), 2)
		got := map[string]bool{}
		var qualItem *lsp.CompletionItem
		for i := range items {
			got[items[i].Label] = true
			if items[i].Label == "ui" {
				qualItem = &items[i]
			}
		}
		if !got["Other"] {
			t.Errorf("tag completion missing local component `Other`; labels=%v", got)
		}
		if qualItem == nil {
			t.Fatalf("tag completion missing `ui` qualifier item alongside local component; labels=%v", got)
		}
		if qualItem.TextEdit == nil || qualItem.TextEdit.NewText != "ui." {
			t.Errorf("`ui` qualifier item TextEdit = %+v, want NewText \"ui.\"", qualItem.TextEdit)
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

	// bare cursor mid-edit: a completely untyped `<` with nothing after it —
	// not even a self-close — fails to parse on its own; repairAtCursor's
	// last-tried "_/>" phantom patch heals it (a placeholder tag name plus a
	// self-close). The merged tag list must still surface all three
	// candidate families at once: the local sibling component, a vendored
	// HTML tag, and the imported package's qualifier item. The local
	// component's Kind is asserted ciKindClass (not Function): these items
	// are offered in TAG position, and editors (blink.cmp observed)
	// auto-append "()" on accepting a Function/Method-kind item, which is
	// wrong for a tag.
	t.Run("bare cursor mid-edit", func(t *testing.T) {
		uiSource := "package ui\n\ncomponent Button(label string) {\n\t<button>{label}</button>\n}\n"
		source := "package page\n\nimport \"example.com/app/ui\"\n\ncomponent Other() {\n\t<div/>\n}\n\ncomponent Home() {\n\t<ui.Button label=\"hi\"/>\n\t<\n}\n"
		cursor := strings.LastIndex(source, "<") + len("<")
		files := map[string]string{
			"page/page.gsx": source,
			"ui/ui.gsx":     uiSource,
		}
		items := completionItems(t, runRaw(t, files, "page/page.gsx", source, cursor), 2)
		const lspCompletionItemKindClass = 7 // ciKindClass in internal/lsp (unexported)
		var other, div, qual *lsp.CompletionItem
		for i := range items {
			switch items[i].Label {
			case "Other":
				other = &items[i]
			case "div":
				div = &items[i]
			case "ui":
				qual = &items[i]
			}
		}
		if other == nil {
			t.Fatalf("bare-cursor tag completion missing local component `Other`; items=%+v", items)
		}
		if other.Kind != lspCompletionItemKindClass {
			t.Errorf("`Other` Kind = %d, want ciKindClass (%d) — tag position must not trigger editor auto-brackets", other.Kind, lspCompletionItemKindClass)
		}
		if div == nil {
			t.Fatalf("bare-cursor tag completion missing HTML tag `div`; items=%+v", items)
		}
		if qual == nil {
			t.Fatalf("bare-cursor tag completion missing import qualifier item `ui`; items=%+v", items)
		}
	})

	// qualified trailing dot mid-edit: `<ui.` with nothing typed after the dot
	// and no self-close. Unlike a bare `<`, the parser accepts a qualified tag
	// with a trailing dot and no member token (`<ui./>` parses clean — see
	// TestRepairAtCursor/qualified_tag_trailing_dot in internal/lsp), so the
	// plain `/>` patch heals it before the phantom `_/>` patch is ever tried.
	// The qualifier "ui" extracted from the healed Tag ("ui.") still resolves
	// the import and lists every one of its components.
	t.Run("qualified trailing dot mid-edit", func(t *testing.T) {
		uiSource := "package ui\n\ncomponent Button(label string) {\n\t<button>{label}</button>\n}\n"
		source := "package page\n\nimport \"example.com/app/ui\"\n\ncomponent Home() {\n\t<ui.Button label=\"hi\"/>\n\t<ui.\n}\n"
		cursor := strings.LastIndex(source, "<ui.") + len("<ui.")
		got := run(t, map[string]string{
			"page/page.gsx": source,
			"ui/ui.gsx":     uiSource,
		}, "page/page.gsx", source, cursor)
		if !got["Button"] {
			t.Errorf("qualified trailing-dot tag completion missing imported component `Button`; labels=%v", got)
		}
	})

	// method component: a `<recv.▮` cursor where `recv` is the enclosing method
	// component's RECEIVER var (not an import) resolves the receiver type's method
	// components. `Page` is a method on UsersPage; its body invokes a sibling
	// method component `<p.Row/>` on the same receiver `p`. A trailing-dot cursor
	// `<p.` heals through the same `/>` repair as the qualified-import case and
	// classifies with qualifier "p"; receiverVarComponentItems resolves `p` to the
	// UsersPage receiver var and offers its methods (Row, Page), never HTML tags
	// (a qualified cursor is component-only) or the plain sibling component.
	t.Run("method component receiver var", func(t *testing.T) {
		source := "package page\n\ntype UsersPage struct {\n\tTitle string\n}\n\ncomponent (p UsersPage) Row(x string) {\n\t<span>{x}-{p.Title}</span>\n}\n\ncomponent (p UsersPage) Page() {\n\t<div><p.Row x=\"a\"/>\n\t<p.\n\t</div>\n}\n"
		cursor := strings.LastIndex(source, "<p.") + len("<p.")
		got := run(t, map[string]string{"page/page.gsx": source}, "page/page.gsx", source, cursor)
		if !got["Row"] {
			t.Errorf("method-component tag completion missing receiver method `Row`; labels=%v", got)
		}
		if !got["Page"] {
			t.Errorf("method-component tag completion missing receiver method `Page`; labels=%v", got)
		}
		if got["div"] {
			t.Errorf("qualified `<p.` cursor offered an HTML tag `div`; labels=%v", got)
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

	t.Run("brief fixture: title offered, ctx/children absent", func(t *testing.T) {
		// Case 9 of the Task 16 brief, using the brief's own Other(title string)
		// shape: `title` is offered, and the reserved component-parameter names
		// `ctx`/`children` (reservedComponentAttrName in completion_gsx.go) are
		// never candidates. Other needs the same `attrs gsx.Attrs` catch-all as
		// the scenarios above — without it, the in-progress bare "t" attribute
		// (matching no real param yet) fails the whole call's plan and
		// componentAttrItems has no ComponentCalls fact to read.
		source := "package page\n\nimport \"github.com/gsxhq/gsx\"\n\ncomponent Other(title string, attrs gsx.Attrs) {\n\t<div>{ title }</div>\n}\n\ncomponent Home() {\n\t<Other t/>\n}\n"
		cursor := strings.LastIndex(source, "<Other t") + len("<Other t")
		got := run(t, source, cursor)
		if !got["title"] {
			t.Errorf("component-attr completion missing `title`; labels=%v", got)
		}
		if got["ctx"] {
			t.Errorf("component-attr completion must not offer reserved `ctx`; labels=%v", got)
		}
		if got["children"] {
			t.Errorf("component-attr completion must not offer reserved `children`; labels=%v", got)
		}
	})
}

// TestComponentAttrForwardedGlobalsE2E drives the new forwarded-attrs-globals
// rule end to end: a component whose signature declares the reserved "attrs"
// catch-all forwards arbitrary attributes to whatever element it renders, so
// an attr-name cursor on its call site now also offers the HTML GLOBAL
// attribute set (in addition to any named params) — icon.Bell's real shape,
// `func(attrs ...gsx.Attr) gsx.Node`, is a component VALUE (a plain package-
// scope Go func, no `component`-keyword decl) in a sibling package, so this
// also exercises the componentValueNameItems/tag-callable-value resolution
// path, not just a `component`-keyword decl. A sibling component with no
// attrs catch-all must NOT offer the HTML globals — an unknown attribute
// there would be rejected by the planner.
func TestComponentAttrForwardedGlobalsE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	labelsOf := func(items []lsp.CompletionItem) map[string]bool {
		m := map[string]bool{}
		for _, it := range items {
			m[it.Label] = true
		}
		return m
	}
	itemOf := func(items []lsp.CompletionItem, label string) *lsp.CompletionItem {
		for i := range items {
			if items[i].Label == label {
				return &items[i]
			}
		}
		return nil
	}
	iconFile := "package icon\n\nimport \"github.com/gsxhq/gsx\"\n\nfunc Bell(attrs ...gsx.Attr) gsx.Node {\n\treturn nil\n}\n"

	t.Run("value-component with attrs catch-all offers HTML globals", func(t *testing.T) {
		source := "package page\n\nimport \"example.com/app/icon\"\n\ncomponent Home() {\n\t<icon.Bell />\n}\n"
		cursor := strings.Index(source, "<icon.Bell ") + len("<icon.Bell ")
		items := runHTMLCompletionE2E(t, map[string]string{"icon/icon.go": iconFile}, source, cursor)
		labels := labelsOf(items)
		if !labels["hidden"] {
			t.Fatalf("forwarded-globals completion missing boolean global `hidden`; labels=%v", labels)
		}
		if !labels["class"] {
			t.Fatalf("forwarded-globals completion missing value global `class`; labels=%v", labels)
		}
		if hidden := itemOf(items, "hidden"); hidden == nil || hidden.TextEdit == nil || hidden.TextEdit.NewText != "hidden" {
			t.Errorf("`hidden` must insert the bare name; got %+v", hidden)
		}
		if class := itemOf(items, "class"); class == nil || class.TextEdit == nil || class.TextEdit.NewText != `class=""` {
			t.Errorf("`class` must insert class=\"\"; got %+v", class)
		}
		if labels["attrs"] {
			t.Errorf("forwarded-globals completion must not offer the reserved `attrs` name itself; labels=%v", labels)
		}
	})

	t.Run("component-keyword decl without attrs catch-all offers no HTML globals", func(t *testing.T) {
		source := "package page\n\ncomponent Plain(title string) {\n\t<div>{ title }</div>\n}\n\ncomponent Home() {\n\t<Plain />\n}\n"
		cursor := strings.Index(source, "<Plain ") + len("<Plain ")
		items := runHTMLCompletionE2E(t, nil, source, cursor)
		labels := labelsOf(items)
		if !labels["title"] {
			t.Fatalf("component-attr completion missing named param `title`; labels=%v", labels)
		}
		for _, global := range []string{"class", "hidden", "id", "style"} {
			if labels[global] {
				t.Errorf("component without attrs catch-all must NOT offer HTML global %q; labels=%v", global, labels)
			}
		}
	})
}

// runHTMLCompletionE2E writes a temp module (with the given extra files, e.g. a
// gsx.toml), opens gsxPath's source as a buffer, sends one completion at cursor,
// and returns the response items. Mirrors the other e2e runners but parameterizes
// the extra files so an htmx gsx.toml fixture can drive the preset path.
func runHTMLCompletionE2E(t *testing.T, extra map[string]string, source string, cursor int) []lsp.CompletionItem {
	t.Helper()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
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
	for name, content := range extra {
		write(name, content)
	}
	pagePath := write("page/page.gsx", source)
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

// runHTMLCompletionE2ESnippet mirrors runHTMLCompletionE2E exactly, except the
// initialize params advertise textDocument.completion.completionItem
// .snippetSupport=true, so the response is expected to carry `$1`-tabstop
// snippet inserts for value attributes instead of the plain `name=""` form.
// Kept as a separate helper (rather than a parameter added to
// runHTMLCompletionE2E) so every existing call site — and therefore every
// existing e2e assertion pinning the no-capability behavior — stays untouched.
func runHTMLCompletionE2ESnippet(t *testing.T, source string, cursor int) []lsp.CompletionItem {
	t.Helper()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
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

	frame := func(value any) string {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return "Content-Length: " + strconv.Itoa(len(data)) + "\r\n\r\n" + string(data)
	}
	var input strings.Builder
	input.WriteString(frame(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"capabilities": map[string]any{
			"textDocument": map[string]any{"completion": map[string]any{
				"completionItem": map[string]any{"snippetSupport": true},
			}},
		}},
	}))
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

// TestHTMLCompletionE2E drives the HTML tag/attr/value completion paths through
// the full JSON-RPC server against a real temp module: a `<di▮` tag cursor
// offers `div` (kind Property, doc non-empty); a `<div ▮>` attr-name cursor
// offers `class` (value insert) and `hidden` (bare boolean); an
// `<input type="▮"/>` value cursor offers the enumerated `submit`.
func TestHTMLCompletionE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	labelsOf := func(items []lsp.CompletionItem) map[string]bool {
		m := map[string]bool{}
		for _, it := range items {
			m[it.Label] = true
		}
		return m
	}
	itemOf := func(items []lsp.CompletionItem, label string) *lsp.CompletionItem {
		for i := range items {
			if items[i].Label == label {
				return &items[i]
			}
		}
		return nil
	}

	t.Run("html tag", func(t *testing.T) {
		source := "package page\n\ncomponent Home() {\n\t<div><di</div>\n}\n"
		cursor := strings.LastIndex(source, "<di") + len("<di")
		items := runHTMLCompletionE2E(t, nil, source, cursor)
		div := itemOf(items, "div")
		if div == nil {
			t.Fatalf("HTML tag completion missing `div`; labels=%v", labelsOf(items))
		}
		if div.Documentation == nil || div.Documentation.Value == "" {
			t.Errorf("`div` tag has empty documentation: %+v", div)
		}
	})

	t.Run("html attr", func(t *testing.T) {
		source := "package page\n\ncomponent Home() {\n\t<div ></div>\n}\n"
		cursor := strings.Index(source, "<div ") + len("<div ")
		items := runHTMLCompletionE2E(t, nil, source, cursor)
		labels := labelsOf(items)
		if !labels["class"] || !labels["hidden"] {
			t.Fatalf("HTML attr completion labels missing class/hidden: %v", labels)
		}
		if hidden := itemOf(items, "hidden"); hidden == nil || hidden.TextEdit == nil || hidden.TextEdit.NewText != "hidden" {
			t.Errorf("`hidden` must insert the bare name; got %+v", hidden)
		}
		if class := itemOf(items, "class"); class == nil || class.TextEdit == nil || class.TextEdit.NewText != `class=""` {
			t.Errorf("`class` must insert class=\"\"; got %+v", class)
		}
	})

	t.Run("html attr cursor on own exact-match token", func(t *testing.T) {
		// `<div class▮` — the cursor sits right after the already-parsed
		// `class` attribute's own name, mid-typing it. This must stay offered
		// (parity with the component-attr path's cursor-on-bound-attr
		// carve-out), not vanish as "already present".
		source := "package page\n\ncomponent Home() {\n\t<div class></div>\n}\n"
		cursor := strings.Index(source, "<div class") + len("<div class")
		items := runHTMLCompletionE2E(t, nil, source, cursor)
		class := itemOf(items, "class")
		if class == nil {
			t.Fatalf("HTML attr completion excludes `class` at exact-name cursor; labels=%v", labelsOf(items))
		}
		if class.TextEdit == nil || class.TextEdit.NewText != `class=""` {
			t.Errorf("`class` must insert class=\"\"; got %+v", class)
		}
	})

	t.Run("attr value", func(t *testing.T) {
		source := "package page\n\ncomponent Home() {\n\t<input type=\"\"/>\n}\n"
		cursor := strings.Index(source, `type="`) + len(`type="`)
		items := runHTMLCompletionE2E(t, nil, source, cursor)
		if !labelsOf(items)["submit"] {
			t.Fatalf("attr-value completion missing `submit`; labels=%v", labelsOf(items))
		}
	})

	t.Run("html attr snippet capability", func(t *testing.T) {
		// Same cursor as "html attr" above, but the client advertises
		// snippetSupport this time: `class` must insert `class="$1"` with
		// insertTextFormat=2 (Snippet) so the cursor lands INSIDE the quotes,
		// while `hidden` — no quotes to place a tabstop inside — is unaffected.
		source := "package page\n\ncomponent Home() {\n\t<div ></div>\n}\n"
		cursor := strings.Index(source, "<div ") + len("<div ")
		items := runHTMLCompletionE2ESnippet(t, source, cursor)

		class := itemOf(items, "class")
		if class == nil || class.TextEdit == nil || class.TextEdit.NewText != `class="$1"` {
			t.Fatalf("`class` must insert `class=\"$1\"` under snippetSupport; got %+v", class)
		}
		if class.InsertTextFormat != 2 {
			t.Errorf("class.InsertTextFormat = %d, want 2 (Snippet)", class.InsertTextFormat)
		}

		hidden := itemOf(items, "hidden")
		if hidden == nil || hidden.TextEdit == nil || hidden.TextEdit.NewText != "hidden" {
			t.Errorf("`hidden` must still insert the bare name under snippetSupport; got %+v", hidden)
		}
		if hidden.InsertTextFormat != 0 {
			t.Errorf("hidden.InsertTextFormat = %d, want 0 (no quotes to tabstop into)", hidden.InsertTextFormat)
		}
	})
}

// TestHTMXAttrCompletionE2E drives HTML attribute-name completion with the htmx
// url preset enabled via a gsx.toml (`url_presets = ["htmx"]`) — the same
// effective config the classifier consults — and checks the hx-* attributes join
// the candidate list only then. A control run with no gsx.toml must NOT offer
// them, proving the gate is the retained preset name, not a hardcoded list.
func TestHTMXAttrCompletionE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	labelsOf := func(items []lsp.CompletionItem) map[string]bool {
		m := map[string]bool{}
		for _, it := range items {
			m[it.Label] = true
		}
		return m
	}
	source := "package page\n\ncomponent Home() {\n\t<div ></div>\n}\n"
	cursor := strings.Index(source, "<div ") + len("<div ")

	off := runHTMLCompletionE2E(t, nil, source, cursor)
	if labelsOf(off)["hx-get"] {
		t.Errorf("hx-get offered without the htmx preset; labels=%v", labelsOf(off))
	}

	on := runHTMLCompletionE2E(t, map[string]string{"gsx.toml": "url_presets = [\"htmx\"]\n"}, source, cursor)
	if !labelsOf(on)["hx-get"] {
		t.Fatalf("hx-get NOT offered with url_presets=[\"htmx\"]; labels=%v", labelsOf(on))
	}
}

// TestCompletionFailSoftE2E drives textDocument/completion end to end for two
// source-state failure modes that must yield an ordinary EMPTY completion
// reply — never a JSON-RPC error (handleCompletion's doc comment: "completion
// is advisory and must fail soft") — cases 13 and 14 of the Task 16 brief.
func TestCompletionFailSoftE2E(t *testing.T) {
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

	// run writes go.mod plus the given files, opens gsxPath's own content as a
	// live buffer via didOpen, sends one completion at cursor, and returns the
	// raw LSP output (not just the parsed items — assertEmptyCompletionNoError
	// needs the full JSON-RPC envelope to confirm no "error" field rode along).
	run := func(t *testing.T, files map[string]string, gsxPath, source string, cursor int) string {
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
		assertNoGsxInternalLeak(t, output.String())
		return output.String()
	}

	t.Run("other-file broken", func(t *testing.T) {
		// page/other.gsx is structurally broken (an unclosed tag); page.gsx
		// carries a clean `{ user. }` member cursor. Both live in the SAME
		// package, so the whole-package compile AnalyzeEphemeral runs for
		// page.gsx's buffer comes back a diagnostics-only shell (Info nil) — see
		// TestAnalyzeEphemeralShellOnBrokenElsewhere in
		// internal/codegen/module_ephemeral_test.go for the same mechanism
		// pinned at the analyzer layer. Completion must still answer empty, not
		// error.
		source := "package page\n\ncomponent Home(user User) {\n\t<div>{ user. }</div>\n}\n"
		cursor := strings.Index(source, "user.") + len("user.")
		files := map[string]string{
			"page/types.go":  "package page\n\ntype User struct{ Name string }\n",
			"page/other.gsx": "package page\n\ncomponent Other(title string) {\n\t<div\n\t<span>{ title }</span>\n}\n",
			"page/page.gsx":  source,
		}
		output := run(t, files, "page/page.gsx", source, cursor)
		assertEmptyCompletionNoError(t, output, 2)
	})

	t.Run("package-clause mismatch", func(t *testing.T) {
		// page.gsx declares `package pag` while its sibling types.go declares
		// `package page` — a genuine multi-file package-name conflict ("found
		// packages ... in DIR"). Completion must answer empty, not error.
		source := "package pag\n\ncomponent Home(user User) {\n\t<div>{ user. }</div>\n}\n"
		cursor := strings.Index(source, "user.") + len("user.")
		files := map[string]string{
			"page/types.go": "package page\n\ntype User struct{ Name string }\n",
			"page/page.gsx": source,
		}
		output := run(t, files, "page/page.gsx", source, cursor)
		assertEmptyCompletionNoError(t, output, 2)
	})
}

// TestGoFileCompletionE2E drives textDocument/completion on a `.go` URI end to
// end — case 16 of the Task 16 brief: gopls, not gsx, owns Go-file completion
// (handleCompletion's doc comment), so the server must reply with a literal
// JSON null result — never gsx's own item list, and never an error.
func TestGoFileCompletionE2E(t *testing.T) {
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
	goPath := write("page/types.go", "package page\n\ntype User struct{ Name string }\n")
	uri := "file://" + goPath

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
		"jsonrpc": "2.0", "id": 2, "method": "textDocument/completion",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"position":     map[string]any{"line": 2, "character": 5},
		},
	}))
	input.WriteString(frame(map[string]any{"jsonrpc": "2.0", "method": "exit"}))

	var output, stderr bytes.Buffer
	if code := runLSP(strings.NewReader(input.String()), &output, &stderr, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, stderr.String())
	}
	result, hasErr := completionResponseRaw(t, output.String(), 2)
	if hasErr {
		t.Fatalf("completion on a .go URI returned a JSON-RPC error; want a null reply:\n%s", output.String())
	}
	if string(result) != "null" {
		t.Errorf("completion on a .go URI = %s, want JSON null (gopls owns .go completion)", result)
	}
}

// TestGoBlockDeclaredAfterCursorE2E pins Task 9's declared-after-cursor
// exclusion (scopeCandidates in completion_go.go) specifically at the `{{ }}`
// GoBlock bridge's coordinate mapping — case 17 of the Task 16 brief. A local
// declared BEFORE the cursor inside the same block is offered; one declared
// AFTER is not (Go's declaration-order rule for function-local scopes),
// distinct from package/import scope which stays order-independent (case 3
// above).
func TestGoBlockDeclaredAfterCursorE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	extra := map[string]string{"page/types.go": "package page\n\ntype User struct{ Name string }\n"}
	source := "package page\n\ncomponent Block(item User) {\n\t{{\n\t\tearly := item.Name\n\t\t_ = early\n\t\t_ = ea\n\t\tlate := item.Name\n\t\t_ = late\n\t}}\n}\n"
	cursor := strings.LastIndex(source, "_ = ea") + len("_ = ea")
	items := runHTMLCompletionE2E(t, extra, source, cursor)
	labels := map[string]bool{}
	for _, it := range items {
		labels[it.Label] = true
	}
	if !labels["early"] {
		t.Errorf("GoBlock completion missing local `early` declared before the cursor; labels=%v", labels)
	}
	if labels["late"] {
		t.Errorf("GoBlock completion must exclude `late`, declared AFTER the cursor in the same block; labels=%v", labels)
	}
}

// TestGoMemberStatementCompletionE2E drives textDocument/completion end to end
// at a member (`.`) cursor sitting in a GoBlock/GoChunk STATEMENT position —
// the gap statementMemberItems closes (internal/lsp/completion_go.go). Unlike
// the ExprMap-bridged member cursors TestGoMemberCompletionE2E covers, these
// bridges (CtrlMap/GoBlock and the bare GoChunk) have no skeleton selector for
// the original member path to walk; statementMemberItems resolves the receiver
// directly from AUTHORED text via pkg.SourceIndex.At instead. Each subtest
// hits the SAME phantom trailing-dot repair (goContextCompletion) that heals
// `{ user. }`'s broken skeleton selector, since GoBlock/GoChunk both classify
// as ctxGoExpr (completion_context.go) exactly like the ExprMap bridge does.
func TestGoMemberStatementCompletionE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	labelsOf := func(items []lsp.CompletionItem) map[string]bool {
		m := map[string]bool{}
		for _, it := range items {
			m[it.Label] = true
		}
		return m
	}

	// GoBlock member: `{{ user. }}` — a CtrlMap-bridged statement cursor with a
	// value (struct) receiver.
	t.Run("goblock value receiver", func(t *testing.T) {
		extra := map[string]string{"page/types.go": "package page\n\ntype User struct {\n\tName string\n\tAge  int\n}\n"}
		source := "package page\n\ncomponent Home(user User) {\n\t{{ user. }}\n}\n"
		cursor := strings.Index(source, "user.") + len("user.")
		got := labelsOf(runHTMLCompletionE2E(t, extra, source, cursor))
		for _, name := range []string{"Name", "Age"} {
			if !got[name] {
				t.Errorf("GoBlock statement member %q missing; labels=%v", name, got)
			}
		}
		if got["user"] {
			t.Errorf("member position must not offer scope locals; got `user`: %v", got)
		}
	})

	// GoChunk member: `return u.` inside a top-level func body — a bare verbatim
	// Go span with no ExprMap/CtrlMap entry at all.
	t.Run("gochunk value receiver", func(t *testing.T) {
		source := "package page\n\ntype User struct {\n\tName string\n\tAge  int\n}\n\n" +
			"func greet(u User) string {\n\treturn u.\n}\n\n" +
			"component Home() {\n\t<div></div>\n}\n"
		cursor := strings.Index(source, "return u.") + len("return u.")
		got := labelsOf(runHTMLCompletionE2E(t, nil, source, cursor))
		for _, name := range []string{"Name", "Age"} {
			if !got[name] {
				t.Errorf("GoChunk statement member %q missing; labels=%v", name, got)
			}
		}
		if got["greet"] || got["u"] {
			t.Errorf("member position must not offer scope names; labels=%v", got)
		}
	})

	// GoBlock package receiver: `{{ strings. }}` — the *types.PkgName branch of
	// statementMemberItems, exercised at a statement (not expression) cursor.
	t.Run("goblock package receiver", func(t *testing.T) {
		source := "package page\n\nimport \"strings\"\n\ncomponent Home() {\n\t{{ x := strings.\n\t_ = x }}\n\t<div></div>\n}\n"
		cursor := strings.Index(source, "strings.") + len("strings.")
		got := labelsOf(runHTMLCompletionE2E(t, nil, source, cursor))
		for _, name := range []string{"ToUpper", "ToLower", "Contains"} {
			if !got[name] {
				t.Errorf("GoBlock package-receiver member %q missing; labels=%v", name, got)
			}
		}
	})
}

// assertNoGsxInternalLeak fails if any completion item, in ANY completion
// response carried by output, has a label with the reserved `_gsx` prefix — the
// generated-code internals (_gsxuse/_gsxcompsig/_gsxrt/_gsxbody/...) the
// skeleton declares in package/file/body scopes. Accepting one would insert a
// reserved identifier that poisons the file. Applied inside the Go-completion
// runners so every scenario inherits it, mirroring the `.x.go` output guard.
func assertNoGsxInternalLeak(t *testing.T, output string) {
	t.Helper()
	for part := range strings.SplitSeq(output, "Content-Length:") {
		_, body, ok := strings.Cut(part, "\r\n\r\n")
		if !ok {
			continue
		}
		var response struct {
			Result *lsp.CompletionList `json:"result"`
		}
		if err := json.Unmarshal([]byte(body), &response); err != nil || response.Result == nil {
			continue
		}
		for _, item := range response.Result.Items {
			if strings.HasPrefix(item.Label, "_gsx") {
				t.Fatalf("completion response leaked reserved internal label %q:\n%s", item.Label, output)
			}
		}
	}
}

// TestExpectedTypeRankingE2E drives expected-type ranking end to end: in a
// component attr value hole `<Card title={ s }/>` whose parameter is `title
// string`, the string-typed local `s` sorts ahead of the int-typed local `n`
// (both stay in tierLocal — ranking never filters, only refines within a tier).
func TestExpectedTypeRankingE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
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

	source := "package page\n\n" +
		"component Card(title string) {\n\t<div>{ title }</div>\n}\n\n" +
		"component Home(s string, n int) {\n\t<Card title={ s }/>\n}\n"
	pagePath := write("page/page.gsx", source)
	uri := "file://" + pagePath

	frame := func(value any) string {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return "Content-Length: " + strconv.Itoa(len(data)) + "\r\n\r\n" + string(data)
	}
	// Cursor right after `s` inside the component attr value hole.
	cursor := strings.Index(source, "title={ s") + len("title={ s")
	pos := lspUTF16PositionAt(source, cursor)

	var input strings.Builder
	input.WriteString(frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}}))
	input.WriteString(frame(map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": source}},
	}))
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

	var sItem, nItem *lsp.CompletionItem
	items := completionItems(t, output.String(), 2)
	for i := range items {
		switch items[i].Label {
		case "s":
			sItem = &items[i]
		case "n":
			nItem = &items[i]
		}
	}
	if sItem == nil || nItem == nil {
		t.Fatalf("component-attr value hole missing locals (s=%v n=%v); items=%v", sItem, nItem, items)
	}
	// Ranking never filters: both stay in tierLocal ("05"). The string-typed `s`
	// matches the `title string` parameter and sorts ahead of the int-typed `n`.
	if !strings.HasPrefix(sItem.SortText, "05") {
		t.Errorf("`s` SortText = %q, want tierLocal prefix \"05\"", sItem.SortText)
	}
	if !strings.HasPrefix(nItem.SortText, "05") {
		t.Errorf("`n` SortText = %q, want tierLocal prefix \"05\" (mismatch is ranked, never filtered)", nItem.SortText)
	}
	if sItem.SortText >= nItem.SortText {
		t.Errorf("type-matching `s` (SortText %q) must sort before mismatching `n` (SortText %q)", sItem.SortText, nItem.SortText)
	}
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

// completionResponseRaw extracts the raw "result" bytes and whether an
// "error" field is present in the JSON-RPC response with the given id. Used
// by the fail-soft (empty-list) and .go-file (null) assertions, which must
// distinguish "answered normally with an empty/null result" from "the server
// replied with a protocol-level error" — a distinction completionItems/
// completionLabels intentionally collapse (both treat a null result as a test
// failure) but that these callers need to keep apart.
func completionResponseRaw(t *testing.T, output string, id int) (result json.RawMessage, hasError bool) {
	t.Helper()
	for part := range strings.SplitSeq(output, "Content-Length:") {
		_, body, ok := strings.Cut(part, "\r\n\r\n")
		if !ok {
			continue
		}
		var response struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal([]byte(body), &response); err != nil || response.ID != id {
			continue
		}
		return response.Result, len(response.Error) != 0
	}
	t.Fatalf("no completion response for id %d in:\n%s", id, output)
	return nil, false
}

// assertEmptyCompletionNoError asserts the completion response with the given
// id is a normal (non-error) reply carrying zero items — the fail-soft
// contract for source-state problems (mid-edit breakage elsewhere in the
// package, package-clause mismatch): completion must never surface these as a
// JSON-RPC error (handleCompletion's doc comment).
func assertEmptyCompletionNoError(t *testing.T, output string, id int) {
	t.Helper()
	result, hasErr := completionResponseRaw(t, output, id)
	if hasErr {
		t.Fatalf("completion id %d returned a JSON-RPC error; want a normal empty reply:\n%s", id, output)
	}
	var list lsp.CompletionList
	if err := json.Unmarshal(result, &list); err != nil {
		t.Fatalf("completion id %d result did not decode as CompletionList: %v\nraw=%s", id, err, result)
	}
	if len(list.Items) != 0 {
		t.Fatalf("completion id %d = %d items, want 0 (fail-soft empty): %+v", id, len(list.Items), list.Items)
	}
}

// posToByteOffsetASCII converts an LSP (line, character) to a byte offset for an
// ASCII buffer (the auto-import fixtures are ASCII, so a UTF-16 code unit is one
// byte). Used to apply completion edits and reparse.
func posToByteOffsetASCII(src string, line, character int) int {
	off := 0
	for range line {
		nl := strings.IndexByte(src[off:], '\n')
		if nl < 0 {
			return len(src)
		}
		off += nl + 1
	}
	return off + character
}

// applyLSPEdits applies non-overlapping LSP TextEdits to src (rightmost first).
func applyLSPEdits(src string, edits []lsp.TextEdit) string {
	type off struct {
		start, end int
		text       string
	}
	os := make([]off, len(edits))
	for i, e := range edits {
		os[i] = off{
			start: posToByteOffsetASCII(src, e.Range.Start.Line, e.Range.Start.Character),
			end:   posToByteOffsetASCII(src, e.Range.End.Line, e.Range.End.Character),
			text:  e.NewText,
		}
	}
	for i := 1; i < len(os); i++ {
		for j := i; j > 0 && os[j].start > os[j-1].start; j-- {
			os[j], os[j-1] = os[j-1], os[j]
		}
	}
	out := src
	for _, o := range os {
		out = out[:o.start] + o.text + out[o.end:]
	}
	return out
}

// TestAutoImportCompletionE2E drives auto-import completion end to end through
// the JSON-RPC server: an unimported qualifier's symbols (Option 1) and
// unimported package names (Option 2), each carrying an import
// additionalTextEdit that — applied together with the main edit — yields a
// document that reparses cleanly with the import present.
func TestAutoImportCompletionE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}

	// Option 1: `{ strings. }` where strings is unimported → ToUpper offered with
	// an import edit.
	t.Run("unimported qualifier symbols", func(t *testing.T) {
		source := "package page\n\ncomponent Home() {\n\t<div>{ strings. }</div>\n}\n"
		cursor := strings.Index(source, "strings.") + len("strings.")
		items := runHTMLCompletionE2E(t, nil, source, cursor)
		var up *lsp.CompletionItem
		for i := range items {
			if items[i].Label == "ToUpper" {
				up = &items[i]
			}
		}
		if up == nil {
			labels := map[string]bool{}
			for _, it := range items {
				labels[it.Label] = true
			}
			t.Fatalf("unimported `strings.` did not offer ToUpper; labels=%v", labels)
		}
		if len(up.AdditionalTextEdits) != 1 {
			t.Fatalf("ToUpper AdditionalTextEdits = %d, want 1 (the import)", len(up.AdditionalTextEdits))
		}
		// Apply the symbol edit + import edit and reparse: the import must land and
		// the whole document must parse.
		all := append([]lsp.TextEdit{*up.TextEdit}, up.AdditionalTextEdits...)
		got := applyLSPEdits(source, all)
		if !strings.Contains(got, "import \"strings\"") {
			t.Errorf("applied doc missing import \"strings\":\n%s", got)
		}
		if !strings.Contains(got, "strings.ToUpper") {
			t.Errorf("applied doc missing strings.ToUpper:\n%s", got)
		}
		fset := token.NewFileSet()
		if f, err := gsxparser.ParseFile(fset, "page.gsx", got, 0); f == nil || err != nil {
			t.Errorf("applied doc does not reparse: err=%v\n%s", err, got)
		}
		// Without labelDetails capability (runHTMLCompletionE2E negotiates none),
		// the import path is carried in the detail string.
		if up.Detail != "strings" {
			t.Errorf("ToUpper detail = %q, want import path \"strings\" (labelDetails fallback)", up.Detail)
		}
	})

	// Option 1, statement position: `{{ strings. }}` (a GoBlock, not an Interp)
	// where strings is unimported → ToUpper offered with an import edit, exactly
	// as the expression-position `{ strings. }` case above. Auto-import's
	// text-level qualifier/edit machinery is node-kind-agnostic, but the
	// classifier routes GoBlock through a different rule (nodeNavSpans, not the
	// Interp rule) — exercise it directly rather than relying on the expression
	// case as a stand-in for every Go-expr context.
	t.Run("statement position unimported qualifier", func(t *testing.T) {
		source := "package page\n\ncomponent Home() {\n\t{{ strings. }}\n}\n"
		cursor := strings.Index(source, "strings.") + len("strings.")
		items := runHTMLCompletionE2E(t, nil, source, cursor)
		var up *lsp.CompletionItem
		for i := range items {
			if items[i].Label == "ToUpper" {
				up = &items[i]
			}
		}
		if up == nil {
			labels := map[string]bool{}
			for _, it := range items {
				labels[it.Label] = true
			}
			t.Fatalf("unimported statement-position `strings.` did not offer ToUpper; labels=%v", labels)
		}
		if len(up.AdditionalTextEdits) != 1 {
			t.Fatalf("ToUpper AdditionalTextEdits = %d, want 1 (the import)", len(up.AdditionalTextEdits))
		}
		all := append([]lsp.TextEdit{*up.TextEdit}, up.AdditionalTextEdits...)
		got := applyLSPEdits(source, all)
		if !strings.Contains(got, "import \"strings\"") {
			t.Errorf("applied doc missing import \"strings\":\n%s", got)
		}
		if !strings.Contains(got, "strings.ToUpper") {
			t.Errorf("applied doc missing strings.ToUpper:\n%s", got)
		}
		fset := token.NewFileSet()
		if f, err := gsxparser.ParseFile(fset, "page.gsx", got, 0); f == nil || err != nil {
			t.Errorf("applied doc does not reparse: err=%v\n%s", err, got)
		}
	})

	// Option 2: bare `{ fm }` offers the package name `fmt` at the bottom tier
	// with an import edit.
	t.Run("unimported package name", func(t *testing.T) {
		source := "package page\n\ncomponent Home() {\n\t<div>{ fm }</div>\n}\n"
		cursor := strings.Index(source, "{ fm }") + len("{ fm")
		items := runHTMLCompletionE2E(t, nil, source, cursor)
		var fm *lsp.CompletionItem
		for i := range items {
			if items[i].Label == "fmt" {
				fm = &items[i]
			}
		}
		if fm == nil {
			t.Fatalf("bare `fm` did not offer package name fmt; got %d items", len(items))
		}
		if fm.Kind != 9 { // ciKindModule
			t.Errorf("fmt kind = %d, want Module(9)", fm.Kind)
		}
		if !strings.HasPrefix(fm.SortText, "70") {
			t.Errorf("fmt SortText = %q, want tierUnimported prefix \"70\"", fm.SortText)
		}
		if len(fm.AdditionalTextEdits) != 1 {
			t.Fatalf("fmt AdditionalTextEdits = %d, want 1", len(fm.AdditionalTextEdits))
		}
		got := applyLSPEdits(source, append([]lsp.TextEdit{*fm.TextEdit}, fm.AdditionalTextEdits...))
		if !strings.Contains(got, "import \"fmt\"") || !strings.Contains(got, "{ fmt }") {
			t.Errorf("applied package-name doc wrong:\n%s", got)
		}
		fset := token.NewFileSet()
		if f, err := gsxparser.ParseFile(fset, "page.gsx", got, 0); f == nil || err != nil {
			t.Errorf("applied doc does not reparse: err=%v\n%s", err, got)
		}
	})

	// Precedence: an already-imported package is unaffected — `strings.` offers
	// real members with NO import edit (the import qualifier resolves; auto-import
	// never fires).
	t.Run("imported package unaffected", func(t *testing.T) {
		source := "package page\n\nimport \"strings\"\n\ncomponent Home() {\n\t<div>{ strings. }</div>\n\t<span>{ strings.ToLower(\"x\") }</span>\n}\n"
		cursor := strings.Index(source, "{ strings. }") + len("{ strings.")
		items := runHTMLCompletionE2E(t, nil, source, cursor)
		var up *lsp.CompletionItem
		for i := range items {
			if items[i].Label == "ToUpper" {
				up = &items[i]
			}
		}
		if up == nil {
			t.Fatalf("imported `strings.` did not offer ToUpper")
		}
		if len(up.AdditionalTextEdits) != 0 {
			t.Errorf("imported member ToUpper carries an import edit %v, want none (already imported)", up.AdditionalTextEdits)
		}
	})
}
