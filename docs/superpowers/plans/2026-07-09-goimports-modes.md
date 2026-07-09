# gofmt/goimports Import Modes + LSP `source.organizeImports` — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give `gsx fmt` and the gsx language server two gopls-style import modes — `gofmt` (format only) and `goimports` (remove unused + merge/dedup/group/sort) — configurable via `gsx.toml` and CLI, plus an LSP `source.organizeImports` code action.

**Architecture:** Reuse the *real* upstream implementations, never a port. `gofmt` mode = the stdlib `go/format.Source` the printer already applies to every `GoChunk` (so it needs zero new formatting code — just skip the organize passes). `goimports` mode = the existing unused-removal pass plus a new `reorderImports` pass that calls `golang.org/x/tools/imports.Process` with `Options{FormatOnly: true}` on each `GoChunk` that declares imports. `FormatOnly` is mandatory: it merges/dedups/groups but skips goimports' usage-based add/remove, which would wrongly strip every import because a gsx chunk body never references the template's imports.

**Tech Stack:** Go 1.26.1, `golang.org/x/tools v0.46.0` (already a module requirement — `imports.Process` adds no new dependency and only +1,811 bytes to `gsx.wasm`), stdlib `go/format`, `go/parser`.

## Global Constraints

- Pin Go to **1.26.1** (`GO_VERSION` in `.github/workflows/ci.yml`). A different minor re-introduces gofmt drift.
- Work in the worktree `/Users/jackieli/personal/gsxhq/gsx/.claude/worktrees/gsx-goimports` on branch `worktree-gsx-goimports`. **Never commit to `main`.**
- The runtime (root `gsx` package) is standard-library only. `internal/gsxfmt`, `gen`, and `internal/lsp` are tooling and may use `golang.org/x/tools`.
- **No "simple heuristics."** The "does this chunk declare imports?" gate is decided by the parsed AST, never a substring match on the word `import` (which would hit the word inside a string or comment).
- **Do not hand-edit generated files** (`.x.go`, `*.golden`).
- The `gsx` binary name collides with Ghostscript on PATH — always `go run ./cmd/gsx …`.
- Before merging: `make ci` (authoritative, uncached). Inner loop: `make check`. Also `make lint`.
- **Behavior preservation:** `Format`, `FormatRemovingImports`, and `FormatRemovingImportsWith` must keep their exact current semantics (`Reorder: false`). `internal/gsxfmt/imports_test.go:TestNoUnusedIsPlainFormat` asserts `FormatRemovingImports(nil unused) == Format`; it must stay green untouched.
- **Pre-existing gap — preserve, do not fix:** `internal/lsp/format.go` calls the *non-*`With` variant, so LSP formatting does not run the `<style>`/`<script>` css/js formatters that the CLI runs. When moving it to `FormatWith`, pass nil `CSSFmt`/`JSFmt` to preserve today's output byte-for-byte. Closing that gap is a separate change.

## Design Vocabulary (locked — use these exact names)

```go
// internal/gsxfmt
type ImportsMode int
const (
    ImportsUnset     ImportsMode = iota // zero value: not configured
    ImportsGofmt
    ImportsGoimports
)
func ParseImportsMode(s string) (ImportsMode, error)
func (m ImportsMode) Or(def ImportsMode) ImportsMode
func (m ImportsMode) RemoveUnused() bool  // true only for ImportsGoimports
func (m ImportsMode) Reorder() bool       // true only for ImportsGoimports
func (m ImportsMode) String() string

type FormatOptions struct {
    Unused  []ImportRef      // imports to remove; nil = remove nothing
    Width   int
    CSSFmt  rawfmt.Formatter // nil = printer default
    JSFmt   rawfmt.Formatter // nil = printer default
    Reorder bool             // run reorderImports (goimports mode)
}
func FormatWith(name string, src []byte, opts FormatOptions) ([]byte, error)
```

`ImportsMode` lives in `internal/gsxfmt` because it is the one package both `gen` and `internal/lsp` already import (`internal/lsp` cannot import `gen` — `gen` imports `internal/lsp`).

`FormatOptions.Reorder` is a plain bool, not a mode: `gsxfmt` stays mechanical and mode-agnostic, and the zero `FormatOptions` is the safe (no-reorder) one. The mode vocabulary lives at the config/CLI/LSP layer, which maps `mode.Reorder()` → `opts.Reorder` and uses `mode.RemoveUnused()` to decide whether to compute `Unused` at all.

## File Structure

| File | Responsibility |
|---|---|
| `internal/gsxfmt/mode.go` (new) | `ImportsMode` type, parsing, predicates. The shared vocabulary. |
| `internal/gsxfmt/imports.go` (modify) | Add `reorderImports` / `reorderChunkImports` / `chunkHasImports`; hoist the shared `goChunkPkg` const. |
| `internal/gsxfmt/gsxfmt.go` (modify) | `FormatOptions` + `FormatWith`; existing three funcs become thin wrappers. |
| `gen/configfile.go` (modify) | `tomlFormatter.Imports` key; parse/validate into `config.importsMode`; `mergeConfig` carry. |
| `gen/main.go` (modify) | `config.importsMode` field + `effectiveImportsMode()`. |
| `gen/fmt.go` (modify) | `-imports` / `-no-imports` flags; per-dir mode resolution; plumb into `FormatWith`. |
| `internal/lsp/server.go` (modify) | `Analyzer.ImportsMode`; `textDocument/codeAction` dispatch; advertise capability. |
| `internal/lsp/protocol.go` (modify) | `CodeActionOptions`, `codeActionParams`, `CodeAction`, `WorkspaceEdit`. |
| `internal/lsp/format.go` (modify) | Formatting honors the configured mode. |
| `internal/lsp/codeaction.go` (new) | `handleCodeAction` — always the goimports transform. |
| `gen/lsp.go` (modify) | `lspAnalyzer.ImportsMode(dir)`. |
| `docs/guide/{config,cli,editor}.md` (modify) | User-facing docs. |

**Testing lives where formatter behavior is actually pinned:** `internal/gsxfmt/*_test.go` (unit) and `gen/fmt_test.go` (CLI E2E). **The txtar corpus is NOT the vehicle here** — `internal/corpus` pins parse/codegen/render and never runs the formatter, so a corpus case would not exercise reorder at all. This is a formatter change, not a syntax/codegen change.

---

### Task 1: `ImportsMode` vocabulary

**Files:**
- Create: `internal/gsxfmt/mode.go`
- Test: `internal/gsxfmt/mode_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `gsxfmt.ImportsMode`, `ImportsUnset`, `ImportsGofmt`, `ImportsGoimports`, `ParseImportsMode(string) (ImportsMode, error)`, `(ImportsMode).Or(ImportsMode) ImportsMode`, `(ImportsMode).RemoveUnused() bool`, `(ImportsMode).Reorder() bool`, `(ImportsMode).String() string`. Every later task depends on these exact names.

- [ ] **Step 1: Write the failing test**

Create `internal/gsxfmt/mode_test.go`:

```go
package gsxfmt

import (
	"strings"
	"testing"
)

func TestParseImportsMode(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want ImportsMode
	}{
		{"gofmt", ImportsGofmt},
		{"goimports", ImportsGoimports},
	} {
		got, err := ParseImportsMode(tc.in)
		if err != nil {
			t.Fatalf("ParseImportsMode(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ParseImportsMode(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestParseImportsModeRejectsUnknown: the error names both valid spellings.
func TestParseImportsModeRejectsUnknown(t *testing.T) {
	_, err := ParseImportsMode("gofumpt")
	if err == nil {
		t.Fatal("want error for unknown mode")
	}
	for _, want := range []string{"gofumpt", "gofmt", "goimports"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

// TestImportsModeZeroIsUnset: the zero value must be ImportsUnset so an absent
// config key is distinguishable from an explicit "gofmt".
func TestImportsModeZeroIsUnset(t *testing.T) {
	var m ImportsMode
	if m != ImportsUnset {
		t.Fatalf("zero ImportsMode = %v, want ImportsUnset", m)
	}
}

// TestImportsModeOr: Or falls back to def only when unset.
func TestImportsModeOr(t *testing.T) {
	if got := ImportsUnset.Or(ImportsGoimports); got != ImportsGoimports {
		t.Fatalf("Unset.Or(goimports) = %v", got)
	}
	if got := ImportsGofmt.Or(ImportsGoimports); got != ImportsGofmt {
		t.Fatalf("Gofmt.Or(goimports) = %v, want Gofmt", got)
	}
}

// TestImportsModePredicates: only goimports removes and reorders.
func TestImportsModePredicates(t *testing.T) {
	if !ImportsGoimports.RemoveUnused() || !ImportsGoimports.Reorder() {
		t.Fatal("goimports must remove and reorder")
	}
	if ImportsGofmt.RemoveUnused() || ImportsGofmt.Reorder() {
		t.Fatal("gofmt must neither remove nor reorder")
	}
	if ImportsUnset.RemoveUnused() || ImportsUnset.Reorder() {
		t.Fatal("unset must neither remove nor reorder (callers must resolve via Or first)")
	}
}

func TestImportsModeString(t *testing.T) {
	if ImportsGofmt.String() != "gofmt" || ImportsGoimports.String() != "goimports" {
		t.Fatal("String() spellings must round-trip ParseImportsMode")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/jackieli/personal/gsxhq/gsx/.claude/worktrees/gsx-goimports && go test ./internal/gsxfmt/ -run TestImportsMode -v`
Expected: FAIL — build error, `undefined: ImportsMode`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/gsxfmt/mode.go`:

```go
package gsxfmt

import "fmt"

// ImportsMode selects how `gsx fmt` and the language server treat the import
// declarations of a .gsx file's pass-through Go chunks. It mirrors gopls, which
// offers gofmt (format only) and goimports (organize), rather than a set of
// independent knobs.
//
// The zero value is ImportsUnset so that an absent gsx.toml key is
// distinguishable from an explicit "gofmt"; callers resolve it with Or.
type ImportsMode int

const (
	// ImportsUnset means no mode was configured. Resolve it with Or before use;
	// its predicates report false so an unresolved mode never silently rewrites.
	ImportsUnset ImportsMode = iota
	// ImportsGofmt formats with gofmt only: imports are sorted within their
	// existing parenthesized group, but separate import declarations are never
	// merged, duplicates are never dropped, and no std/third-party split is made.
	ImportsGofmt
	// ImportsGoimports removes unused imports and reorders the rest: merge every
	// import declaration into one block, dedup identical specs, group standard
	// library separately from everything else, sort within each group.
	ImportsGoimports
)

// DefaultImportsMode is the mode used when gsx.toml says nothing.
const DefaultImportsMode = ImportsGoimports

// ParseImportsMode converts a gsx.toml / CLI spelling into an ImportsMode. Any
// other string is an error naming both valid spellings.
func ParseImportsMode(s string) (ImportsMode, error) {
	switch s {
	case "gofmt":
		return ImportsGofmt, nil
	case "goimports":
		return ImportsGoimports, nil
	default:
		return ImportsUnset, fmt.Errorf("invalid imports mode %q (want %q or %q)", s, "gofmt", "goimports")
	}
}

// Or returns m, or def when m is ImportsUnset. It is how a CLI flag layers over
// a config value, and a config value over the built-in default.
func (m ImportsMode) Or(def ImportsMode) ImportsMode {
	if m == ImportsUnset {
		return def
	}
	return m
}

// RemoveUnused reports whether this mode drops imports the file never uses.
// Only goimports does; gofmt never removes an import.
func (m ImportsMode) RemoveUnused() bool { return m == ImportsGoimports }

// Reorder reports whether this mode merges, dedups, groups and sorts imports.
// Only goimports does.
func (m ImportsMode) Reorder() bool { return m == ImportsGoimports }

// String returns the gsx.toml spelling; it round-trips through ParseImportsMode.
func (m ImportsMode) String() string {
	switch m {
	case ImportsGofmt:
		return "gofmt"
	case ImportsGoimports:
		return "goimports"
	default:
		return "unset"
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/gsxfmt/ -run 'TestImportsMode|TestParseImportsMode' -v`
Expected: PASS (6 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/gsxfmt/mode.go internal/gsxfmt/mode_test.go
git commit -m "feat(gsxfmt): add ImportsMode vocabulary (gofmt|goimports)"
```

---

### Task 2: `reorderImports` — the goimports pass

**Files:**
- Modify: `internal/gsxfmt/imports.go`
- Test: `internal/gsxfmt/reorder_test.go` (new)

**Interfaces:**
- Consumes: nothing from Task 1 (independent).
- Produces: unexported `reorderImports(f *gsxast.File)`, `reorderChunkImports(src string) (string, bool)`, `chunkHasImports(src string) bool`, and the package const `goChunkPkg = "package _gsxp\n"`. Task 3 calls `reorderImports`.

**Background the implementer needs:**
Imports are not AST nodes in gsx — they live verbatim inside `ast.GoChunk` spans. The parser (`parser/goexpr.go splitGoElements`) peels a leading run of `import` declarations, single-line **and** grouped, into one plain `GoChunk`. Imports therefore **never** appear in a `GoWithElements` decl, so a per-`GoChunk` walk misses nothing; skip every other decl type. A `GoChunk` is element-free, complete top-level Go, so parsing it (wrapped in a synthetic package clause) is safe — the sibling `deleteChunkImports` already does exactly this on every `gsx fmt`.

- [ ] **Step 1: Write the failing test**

Create `internal/gsxfmt/reorder_test.go`:

```go
package gsxfmt

import (
	"strings"
	"testing"
)

// reorder formats src through the goimports path (reorder on, no removal).
func reorder(t *testing.T, src string) string {
	t.Helper()
	out, err := FormatWith("x.gsx", []byte(src), FormatOptions{Width: 80, Reorder: true})
	if err != nil {
		t.Fatalf("FormatWith: %v", err)
	}
	return string(out)
}

// TestReorderMergesAndDedups: a single-line import plus a grouped one that
// repeats it collapse into one block with the duplicate gone. This is the
// motivating case: gofmt leaves both declarations and the duplicate alone.
func TestReorderMergesAndDedups(t *testing.T) {
	src := "package x\n\n" +
		"import \"strings\"\n\n" +
		"import (\n\t\"fmt\"\n\n\t\"strings\"\n)\n\n" +
		"component C() {\n\t<p>{ fmt.Sprint(strings.ToUpper(\"x\")) }</p>\n}\n"
	got := reorder(t, src)
	if n := strings.Count(got, "import"); n != 1 {
		t.Fatalf("want exactly 1 import keyword, got %d:\n%s", n, got)
	}
	if n := strings.Count(got, "\"strings\""); n != 1 {
		t.Fatalf("duplicate strings import not deduped (%d occurrences):\n%s", n, got)
	}
	if !strings.Contains(got, "\"fmt\"") {
		t.Fatalf("fmt import lost:\n%s", got)
	}
}

// TestReorderGroupsStdAndThirdParty: std and non-std land in separate
// blank-line-separated groups, goimports' default two-group split.
func TestReorderGroupsStdAndThirdParty(t *testing.T) {
	src := "package x\n\n" +
		"import (\n\t\"github.com/gsxhq/gsx\"\n\t\"fmt\"\n)\n\n" +
		"component C() {\n\t<p>hi</p>\n}\n"
	got := reorder(t, src)
	fmtAt := strings.Index(got, "\"fmt\"")
	gsxAt := strings.Index(got, "\"github.com/gsxhq/gsx\"")
	if fmtAt < 0 || gsxAt < 0 {
		t.Fatalf("imports missing:\n%s", got)
	}
	if fmtAt > gsxAt {
		t.Fatalf("std must sort before third-party:\n%s", got)
	}
	between := got[fmtAt:gsxAt]
	if !strings.Contains(between, "\n\n") {
		t.Fatalf("want blank line between std and third-party groups:\n%s", got)
	}
}

// TestReorderIdempotent: reorder output is stable, including after the printer
// re-gofmt's every chunk (gofmt sorts within a group but never regroups, so the
// std/third-party split survives).
func TestReorderIdempotent(t *testing.T) {
	src := "package x\n\n" +
		"import \"strings\"\n\nimport (\n\t\"fmt\"\n\n\t\"strings\"\n)\n\n" +
		"component C() {\n\t<p>{ fmt.Sprint(strings.ToUpper(\"x\")) }</p>\n}\n"
	once := reorder(t, src)
	twice := reorder(t, once)
	if once != twice {
		t.Fatalf("reorder not idempotent:\nonce:\n%s\ntwice:\n%s", once, twice)
	}
}

// TestReorderOffLeavesImportsAlone: with Reorder:false the duplicate and the two
// separate declarations survive — that is gofmt mode.
func TestReorderOffLeavesImportsAlone(t *testing.T) {
	src := "package x\n\n" +
		"import \"strings\"\n\nimport (\n\t\"fmt\"\n\n\t\"strings\"\n)\n\n" +
		"component C() {\n\t<p>{ fmt.Sprint(strings.ToUpper(\"x\")) }</p>\n}\n"
	out, err := FormatWith("x.gsx", []byte(src), FormatOptions{Width: 80, Reorder: false})
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if n := strings.Count(got, "\"strings\""); n != 2 {
		t.Fatalf("gofmt mode must keep the duplicate (got %d):\n%s", n, got)
	}
	if n := strings.Count(got, "import"); n != 2 {
		t.Fatalf("gofmt mode must keep both import declarations (got %d):\n%s", n, got)
	}
}

// TestReorderPreservesBlankAndAliasedImports: `_` and aliased specs survive a
// reorder (FormatOnly never drops a spec).
func TestReorderPreservesBlankAndAliasedImports(t *testing.T) {
	src := "package x\n\n" +
		"import (\n\t_ \"embed\"\n\tsx \"strings\"\n)\n\n" +
		"component C() {\n\t<p>{ sx.ToUpper(\"x\") }</p>\n}\n"
	got := reorder(t, src)
	if !strings.Contains(got, "_ \"embed\"") {
		t.Fatalf("blank import lost:\n%s", got)
	}
	if !strings.Contains(got, "sx \"strings\"") {
		t.Fatalf("aliased import lost:\n%s", got)
	}
}

// TestReorderSamePathDifferentAliasBothKept: same path under two aliases is two
// distinct imports; dedup must not collapse them.
func TestReorderSamePathDifferentAliasBothKept(t *testing.T) {
	src := "package x\n\n" +
		"import (\n\tsx \"strings\"\n\tst \"strings\"\n)\n\n" +
		"component C() {\n\t<p>{ sx.ToUpper(st.ToLower(\"x\")) }</p>\n}\n"
	got := reorder(t, src)
	if !strings.Contains(got, "sx \"strings\"") || !strings.Contains(got, "st \"strings\"") {
		t.Fatalf("distinct aliases wrongly collapsed:\n%s", got)
	}
}

// TestChunkHasImportsIgnoresWordInStringsAndComments: the gate is AST-based, so
// the word "import" inside a string or comment must not trigger a reorder.
func TestChunkHasImportsIgnoresWordInStringsAndComments(t *testing.T) {
	if chunkHasImports("// import \"strings\"\nvar x = 1\n") {
		t.Fatal("comment mentioning import must not count as an import decl")
	}
	if chunkHasImports("var s = \"import \\\"strings\\\"\"\n") {
		t.Fatal("string containing import must not count as an import decl")
	}
	if !chunkHasImports("import \"strings\"\n") {
		t.Fatal("a real import decl must be detected")
	}
}

// TestReorderChunkImportsLeavesInvalidGoUntouched: a chunk that is not
// standalone-valid Go is returned unchanged rather than mangled.
func TestReorderChunkImportsLeavesInvalidGoUntouched(t *testing.T) {
	const bad = "import \"strings\"\n\nfunc ( {\n"
	got, changed := reorderChunkImports(bad)
	if changed || got != bad {
		t.Fatalf("invalid Go must be left untouched, got changed=%v:\n%s", changed, got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/gsxfmt/ -run 'TestReorder|TestChunkHasImports' -v`
Expected: FAIL — build error, `undefined: FormatWith`, `undefined: chunkHasImports`, `undefined: reorderChunkImports`.

(`FormatWith` arrives in Task 3. To keep Task 2 independently testable, implement `FormatWith` **minimally here** as part of Step 3 — Task 3 then only adds the wrappers and `FormatOptions` fields already used. See Step 3.)

- [ ] **Step 3: Write minimal implementation**

First, in `internal/gsxfmt/imports.go`: hoist the synthetic package clause to a package-level const and reuse it in `deleteChunkImports`.

Replace the body of `deleteChunkImports`'s first two lines:

```go
func deleteChunkImports(src string, unused []ImportRef) (string, bool) {
	const pkg = "package _gsxp\n"
	fset := token.NewFileSet()
	file, err := goparser.ParseFile(fset, "", pkg+src, goparser.ParseComments)
```

with:

```go
func deleteChunkImports(src string, unused []ImportRef) (string, bool) {
	fset := token.NewFileSet()
	file, err := goparser.ParseFile(fset, "", goChunkPkg+src, goparser.ParseComments)
```

Leave the rest of `deleteChunkImports` (including its `// Drop the synthetic "package _gsxp" line we prepended.` comment) exactly as-is. The only change is that the local `const pkg` is gone in favor of the shared `goChunkPkg`.

Now append to `internal/gsxfmt/imports.go`:

```go
// goChunkPkg is the synthetic package clause prepended to a GoChunk so that the
// chunk — which carries no package declaration of its own — parses as a
// standalone Go file. It is stripped from every reprinted result.
const goChunkPkg = "package _gsxp\n"

// chunkHasImports reports whether src declares at least one import. The decision
// is made on the parsed AST, never on a substring of the text: the word `import`
// can appear inside a string literal or a comment. A chunk that is not
// standalone-valid Go reports false, so it is left untouched downstream.
//
// goparser.ImportsOnly stops the parse after the import block, which is all this
// gate needs and keeps the common (import-free) chunk cheap.
func chunkHasImports(src string) bool {
	fset := token.NewFileSet()
	file, err := goparser.ParseFile(fset, "", goChunkPkg+src, goparser.ImportsOnly)
	if err != nil {
		return false
	}
	return len(file.Imports) > 0
}

// reorderChunkImports runs goimports' formatter over one Go chunk: it merges
// every import declaration into a single block, drops duplicate specs, splits
// standard-library from third-party imports with a blank line, and sorts each
// group. Returns the rewritten chunk and whether anything changed.
//
// FormatOnly is essential. Without it goimports would also ADD and REMOVE
// imports based on what the chunk body references — and a gsx chunk body never
// references the template's imports, so plain goimports would strip every one of
// them. Unused-import removal is a separate, module-analysis-driven pass
// (removeImports); adding imports is impossible for gsx (a chunk body cannot
// tell us which package a template's identifier came from).
//
// Comments/TabIndent/TabWidth make FormatOnly's output match gofmt's tabbed
// chunk formatting; without them it emits spaces and the printer's own gofmt
// would fight it.
func reorderChunkImports(src string) (string, bool) {
	if !chunkHasImports(src) {
		return src, false
	}
	out, err := imports.Process("chunk.go", []byte(goChunkPkg+src), &imports.Options{
		FormatOnly: true,
		Comments:   true,
		TabIndent:  true,
		TabWidth:   8,
	})
	if err != nil {
		return src, false // not standalone-valid Go; leave it
	}
	s := string(out)
	// Drop the synthetic package clause we prepended.
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[nl+1:]
	}
	s = strings.TrimSpace(s)
	if s == strings.TrimSpace(src) {
		return src, false
	}
	return s, true
}

// reorderImports rewrites the imports of every GoChunk in f, in place, to
// goimports' canonical form. Non-GoChunk decls are skipped: imports never live
// in a GoWithElements region, because the parser peels a leading import run into
// its own plain GoChunk before building the element region.
//
// Unlike removeImports this can never empty a chunk (FormatOnly deletes no
// specs), so no decl is dropped here.
func reorderImports(f *gsxast.File) {
	for _, d := range f.Decls {
		gc, ok := d.(*gsxast.GoChunk)
		if !ok {
			continue
		}
		if out, changed := reorderChunkImports(gc.Src); changed {
			gc.Src = out
		}
	}
}
```

Add `"golang.org/x/tools/imports"` to the import block of `internal/gsxfmt/imports.go`.

Then, in `internal/gsxfmt/gsxfmt.go`, add `FormatOptions` and `FormatWith` (Task 3 will refactor the existing three funcs onto it; here we just add it so Task 2's tests compile):

```go
// FormatOptions carries the knobs of FormatWith. The zero value is the safe one:
// no imports removed, no reorder, printer defaults for <style>/<script>.
type FormatOptions struct {
	// Unused lists imports to delete from the file's Go chunks; nil removes none.
	Unused []ImportRef
	// Width is the printer's target line width (0 → printer default).
	Width int
	// CSSFmt/JSFmt format <style>/<script> bodies; nil uses the printer default.
	CSSFmt rawfmt.Formatter
	JSFmt  rawfmt.Formatter
	// Reorder runs the goimports pass (merge/dedup/group/sort). It is a plain
	// bool, not an ImportsMode: gsxfmt stays mechanical, and callers map
	// ImportsMode.Reorder() onto it.
	Reorder bool
}

// FormatWith is the one formatting entry point: parse → remove unused imports →
// (optionally) reorder imports → whitespace-normalize → print. A non-nil error is
// a parse or print failure; callers formatting unsaved buffers should treat that
// as "leave the buffer untouched".
func FormatWith(name string, src []byte, opts FormatOptions) ([]byte, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return nil, err
	}
	// Remove first, then reorder: an import that was both unused and duplicated is
	// gone before the merge, so reorder only canonicalizes what survives.
	removeImports(f, opts.Unused)
	if opts.Reorder {
		reorderImports(f)
	}
	wsnorm.Normalize(f)
	var b bytes.Buffer
	if opts.CSSFmt == nil && opts.JSFmt == nil {
		if err := printer.Fprint(&b, f, opts.Width); err != nil {
			return nil, err
		}
	} else {
		if err := printer.FprintWith(&b, f, opts.Width, opts.CSSFmt, opts.JSFmt); err != nil {
			return nil, err
		}
	}
	return b.Bytes(), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/gsxfmt/ -v`
Expected: PASS — the new reorder tests **and** every pre-existing test (`TestRemoveIdempotent`, `TestNoUnusedIsPlainFormat`, …) still green.

- [ ] **Step 5: Verify the wasm cost claim did not regress**

Run:
```bash
GOOS=js GOARCH=wasm go build -o /tmp/w.wasm ./playground/wasm && wc -c < /tmp/w.wasm && rm -f /tmp/w.wasm
```
Expected: ~17.3 MB (baseline 17,290,466 bytes; the goimports link adds ≈1,811 bytes). A jump of megabytes means `imports.Process` pulled in the module resolver — investigate before continuing.

- [ ] **Step 6: Commit**

```bash
git add internal/gsxfmt/imports.go internal/gsxfmt/gsxfmt.go internal/gsxfmt/reorder_test.go
git commit -m "feat(gsxfmt): reorderImports via goimports FormatOnly; add FormatWith"
```

---

### Task 3: Fold the existing wrappers onto `FormatWith`

**Files:**
- Modify: `internal/gsxfmt/gsxfmt.go`

**Interfaces:**
- Consumes: `FormatWith`, `FormatOptions` (Task 2).
- Produces: unchanged public signatures `Format`, `FormatRemovingImports`, `FormatRemovingImportsWith` — now thin delegates. No caller changes.

**Why `Reorder: false` in all three:** these are the pre-existing entry points. `internal/gsxfmt/imports_test.go:TestNoUnusedIsPlainFormat` pins `FormatRemovingImports(nil) == Format`; reordering in either would break it and would silently change every existing caller's output. Only new callers opt into reorder.

- [ ] **Step 1: Verify the guard test exists and passes today**

Run: `go test ./internal/gsxfmt/ -run TestNoUnusedIsPlainFormat -v`
Expected: PASS. This test is the contract Task 3 must not break. Do not modify it.

- [ ] **Step 2: Rewrite the three wrappers**

In `internal/gsxfmt/gsxfmt.go`, replace the bodies of `Format`, `FormatRemovingImports`, and `FormatRemovingImportsWith` with delegations (keep every doc comment, appending the noted sentence):

```go
// Format parses src (named for diagnostics), normalizes whitespace, and returns
// the canonical gsx source. A non-nil error is a parse or print failure; callers
// formatting unsaved buffers should treat that as "leave the buffer untouched"
// rather than a hard failure. Imports are left exactly as written (gofmt mode).
func Format(name string, src []byte, width int) ([]byte, error) {
	return FormatWith(name, src, FormatOptions{Width: width})
}

// FormatRemovingImports formats src exactly like Format, but first removes every
// import listed in `unused` from the file's pass-through Go chunks. With an empty
// or nil `unused` it is identical to Format. A parse error is returned unchanged
// (the caller decides whether to surface or ignore it). It never reorders.
func FormatRemovingImports(name string, src []byte, unused []ImportRef, width int) ([]byte, error) {
	return FormatWith(name, src, FormatOptions{Unused: unused, Width: width})
}

// FormatRemovingImportsWith is FormatRemovingImports with explicit CSS and JS
// formatters for <style>/<script> bodies (nil → built-in default at width). It
// never reorders.
func FormatRemovingImportsWith(name string, src []byte, unused []ImportRef, width int, cssFmt, jsFmt rawfmt.Formatter) ([]byte, error) {
	return FormatWith(name, src, FormatOptions{Unused: unused, Width: width, CSSFmt: cssFmt, JSFmt: jsFmt})
}
```

Remove now-unused imports from `gsxfmt.go` (`bytes`, `go/token`, `printer`, `wsnorm`, `parser` are still used by `FormatWith`, which lives in this file — so nothing should become unused; run the build to confirm).

- [ ] **Step 3: Run the full gsxfmt suite**

Run: `go test ./internal/gsxfmt/ -count=1 -v`
Expected: PASS, all tests, including `TestNoUnusedIsPlainFormat` and `TestFormatIdempotent`.

- [ ] **Step 4: Run every downstream consumer**

Run: `go test ./gen/... ./internal/lsp/... -count=1`
Expected: PASS. No behavior changed for existing callers.

- [ ] **Step 5: Commit**

```bash
git add internal/gsxfmt/gsxfmt.go
git commit -m "refactor(gsxfmt): Format/FormatRemovingImports* delegate to FormatWith"
```

---

### Task 4: `gsx.toml` `[formatter] imports` key

**Files:**
- Modify: `gen/configfile.go` (`tomlFormatter` ~line 42; `loadConfig` ~line 197; `mergeConfig` ~line 264)
- Modify: `gen/main.go` (`config` struct ~line 45; add `effectiveImportsMode` next to `effectivePrintWidth` ~line 54)
- Test: `gen/configfile_test.go`

**Interfaces:**
- Consumes: `gsxfmt.ImportsMode`, `gsxfmt.ParseImportsMode`, `gsxfmt.DefaultImportsMode`, `(ImportsMode).Or` (Task 1).
- Produces: `config.importsMode gsxfmt.ImportsMode` field and `func (c config) effectiveImportsMode() gsxfmt.ImportsMode`. Tasks 5 and 6 call `effectiveImportsMode()`.

- [ ] **Step 1: Write the failing test**

Append to `gen/configfile_test.go`:

```go
// TestConfigImportsMode: [formatter] imports selects the mode.
func TestConfigImportsMode(t *testing.T) {
	for _, tc := range []struct {
		toml string
		want gsxfmt.ImportsMode
	}{
		{"[formatter]\nimports = \"gofmt\"\n", gsxfmt.ImportsGofmt},
		{"[formatter]\nimports = \"goimports\"\n", gsxfmt.ImportsGoimports},
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, "gsx.toml")
		if err := os.WriteFile(path, []byte(tc.toml), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadConfig(path)
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if got := cfg.effectiveImportsMode(); got != tc.want {
			t.Fatalf("imports = %v, want %v", got, tc.want)
		}
	}
}

// TestConfigImportsModeDefault: absent key ⇒ goimports.
func TestConfigImportsModeDefault(t *testing.T) {
	var c config
	if got := c.effectiveImportsMode(); got != gsxfmt.ImportsGoimports {
		t.Fatalf("default imports mode = %v, want goimports", got)
	}
}

// TestConfigImportsModeInvalid: an unknown spelling errors, naming the key and
// both valid values.
func TestConfigImportsModeInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gsx.toml")
	if err := os.WriteFile(path, []byte("[formatter]\nimports = \"gofumpt\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadConfig(path)
	if err == nil {
		t.Fatal("want error for invalid imports mode")
	}
	for _, want := range []string{"formatter.imports", "gofmt", "goimports"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

// TestConfigImportsModeUnknownKeyRejected: strict decoding still rejects typos
// inside [formatter].
func TestConfigImportsModeUnknownKeyRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gsx.toml")
	if err := os.WriteFile(path, []byte("[formatter]\nimport = \"gofmt\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), "import") {
		t.Fatalf("loadConfig err = %v, want unknown-key error naming import", err)
	}
}
```

Ensure `gen/configfile_test.go` imports `"github.com/gsxhq/gsx/internal/gsxfmt"` and `"strings"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./gen/ -run TestConfigImports -v`
Expected: FAIL — `cfg.effectiveImportsMode undefined`.

- [ ] **Step 3: Write minimal implementation**

In `gen/configfile.go`, extend `tomlFormatter`:

```go
// tomlFormatter is the [formatter] table: knobs for `gsx fmt` and LSP
// formatting. Like [dev], it never changes generated output and is NOT folded
// into computeKey. A nil pointer (table absent) leaves the defaults
// (print_width 80, imports "goimports").
type tomlFormatter struct {
	PrintWidth int    `toml:"print_width"`
	Imports    string `toml:"imports"` // "goimports" (default) | "gofmt"
}
```

In `loadConfig`, replace the `if tc.Formatter != nil { … }` block (~line 197):

```go
	if tc.Formatter != nil {
		cfg.printWidth = tc.Formatter.PrintWidth
		if s := tc.Formatter.Imports; s != "" {
			m, err := gsxfmt.ParseImportsMode(s)
			if err != nil {
				return config{}, fmt.Errorf("%s: formatter.imports: %w", path, err)
			}
			cfg.importsMode = m
		}
	}
```

Add `"github.com/gsxhq/gsx/internal/gsxfmt"` to `gen/configfile.go`'s imports.

In `mergeConfig`, next to the `printWidth` merge (~line 264):

```go
	merged.importsMode = base.importsMode
	if opts.importsMode != gsxfmt.ImportsUnset {
		merged.importsMode = opts.importsMode
	}
```

In `gen/main.go`, add the field to `config` (keep the aligned-comment style):

```go
	printWidth     int                     // gsx.toml [formatter] print_width; 0 means "unset" → 80 at use
	importsMode    gsxfmt.ImportsMode      // gsx.toml [formatter] imports; Unset → goimports at use
```

and the accessor next to `effectivePrintWidth`:

```go
// effectiveImportsMode returns the configured import-handling mode, defaulting
// to goimports (remove unused + reorder) when unset — matching gopls, where
// organizing imports is the norm and plain gofmt is the opt-out.
func (c config) effectiveImportsMode() gsxfmt.ImportsMode {
	return c.importsMode.Or(gsxfmt.DefaultImportsMode)
}
```

Add `"github.com/gsxhq/gsx/internal/gsxfmt"` to `gen/main.go`'s imports if absent.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./gen/ -run TestConfig -count=1 -v`
Expected: PASS, including the pre-existing `TestConfigPrintWidth*` tests.

- [ ] **Step 5: Commit**

```bash
git add gen/configfile.go gen/main.go gen/configfile_test.go
git commit -m "feat(config): [formatter] imports = gofmt|goimports"
```

---

### Task 5: `gsx fmt` CLI flags and per-directory mode

**Files:**
- Modify: `gen/fmt.go` (flags ~line 47-62; `unusedByPath` ~line 73-76; format loop ~line 80-126; add `importsModeFor` next to `printWidthFor` ~line 148)
- Test: `gen/fmt_test.go`

**Interfaces:**
- Consumes: `gsxfmt.ImportsMode`, `ParseImportsMode`, `Or`, `RemoveUnused`, `Reorder` (Task 1); `gsxfmt.FormatWith`/`FormatOptions` (Task 2); `config.effectiveImportsMode()` (Task 4).
- Produces: `importsModeFor(dir string) gsxfmt.ImportsMode`. No later task consumes it.

**Why mode is resolved per directory:** `gsx.toml` is discovered by walking up from each file's directory, so two files in one `gsx fmt` run can resolve different modes. A CLI flag, when given, overrides every directory.

**Why removal files are filtered:** `analyzeUnusedImports` opens a `codegen.Module` per module — expensive. Files whose directory resolves to `gofmt` need no unused analysis at all, so they must not be passed in.

- [ ] **Step 1: Write the failing test**

Append to `gen/fmt_test.go` (follow the existing helpers in that file for building a temp module — see `TestFmtRemovesUnusedImport` at line 234 for the exact `go.mod` + `replace` recipe):

```go
// TestFmtGoimportsMergesAndDedups: the default mode merges a single-line import
// with a grouped one and drops the duplicate.
func TestFmtGoimportsMergesAndDedups(t *testing.T) {
	dir := newFmtModule(t)
	src := "package u\n\nimport \"strings\"\n\nimport (\n\t\"fmt\"\n\n\t\"strings\"\n)\n\n" +
		"component C() {\n\t<p>{ fmt.Sprint(strings.ToUpper(\"x\")) }</p>\n}\n"
	p := filepath.Join(dir, "c.gsx")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := runFmt(&out, &errb, []string{"-w", p}, nil, nil, codegen.Options{}, dir); code != 0 {
		t.Fatalf("runFmt=%d stderr=%s", code, errb.String())
	}
	got := readFile(t, p)
	if n := strings.Count(got, "\"strings\""); n != 1 {
		t.Fatalf("duplicate not deduped (%d):\n%s", n, got)
	}
	if n := strings.Count(got, "import"); n != 1 {
		t.Fatalf("declarations not merged (%d import keywords):\n%s", n, got)
	}
}

// TestFmtImportsGofmtLeavesImportsAlone: -imports gofmt keeps the duplicate, the
// two declarations, AND an unused import (gofmt never removes).
func TestFmtImportsGofmtLeavesImportsAlone(t *testing.T) {
	dir := newFmtModule(t)
	src := "package u\n\nimport \"bytes\"\n\nimport (\n\t\"fmt\"\n\n\t\"bytes\"\n)\n\n" +
		"component C() {\n\t<p>{ fmt.Sprint(1) }</p>\n}\n"
	p := filepath.Join(dir, "c.gsx")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := runFmt(&out, &errb, []string{"-w", "-imports", "gofmt", p}, nil, nil, codegen.Options{}, dir); code != 0 {
		t.Fatalf("runFmt=%d stderr=%s", code, errb.String())
	}
	got := readFile(t, p)
	if n := strings.Count(got, "\"bytes\""); n != 2 {
		t.Fatalf("gofmt mode must keep the unused duplicate (%d):\n%s", n, got)
	}
	if n := strings.Count(got, "import"); n != 2 {
		t.Fatalf("gofmt mode must keep both declarations (%d):\n%s", n, got)
	}
}

// TestFmtNoImportsIsGofmtAlias: -no-imports behaves exactly like -imports gofmt.
func TestFmtNoImportsIsGofmtAlias(t *testing.T) {
	src := "package u\n\nimport \"bytes\"\n\nimport (\n\t\"fmt\"\n\n\t\"bytes\"\n)\n\n" +
		"component C() {\n\t<p>{ fmt.Sprint(1) }</p>\n}\n"
	run := func(args ...string) string {
		dir := newFmtModule(t)
		p := filepath.Join(dir, "c.gsx")
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		var out, errb bytes.Buffer
		if code := runFmt(&out, &errb, append(args, p), nil, nil, codegen.Options{}, dir); code != 0 {
			t.Fatalf("runFmt=%d stderr=%s", code, errb.String())
		}
		return readFile(t, p)
	}
	if a, b := run("-w", "-no-imports"), run("-w", "-imports", "gofmt"); a != b {
		t.Fatalf("-no-imports != -imports gofmt:\n%s\n---\n%s", a, b)
	}
}

// TestFmtImportsFlagConflict: -imports goimports with -no-imports is a usage
// error (exit 2), not a silent winner.
func TestFmtImportsFlagConflict(t *testing.T) {
	dir := newFmtModule(t)
	var out, errb bytes.Buffer
	code := runFmt(&out, &errb, []string{"-imports", "goimports", "-no-imports", dir}, nil, nil, codegen.Options{}, dir)
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stderr=%s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "-no-imports") {
		t.Fatalf("stderr must explain the conflict: %s", errb.String())
	}
}

// TestFmtImportsFlagInvalid: an unknown -imports value is exit 2 naming both
// valid spellings.
func TestFmtImportsFlagInvalid(t *testing.T) {
	dir := newFmtModule(t)
	var out, errb bytes.Buffer
	code := runFmt(&out, &errb, []string{"-imports", "gofumpt", dir}, nil, nil, codegen.Options{}, dir)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	for _, want := range []string{"gofmt", "goimports"} {
		if !strings.Contains(errb.String(), want) {
			t.Fatalf("stderr %q must name %q", errb.String(), want)
		}
	}
}

// TestFmtConfigGofmtModeHonored: [formatter] imports = "gofmt" in gsx.toml is
// honored with no CLI flag.
func TestFmtConfigGofmtModeHonored(t *testing.T) {
	dir := newFmtModule(t)
	if err := os.WriteFile(filepath.Join(dir, "gsx.toml"), []byte("[formatter]\nimports = \"gofmt\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := "package u\n\nimport \"bytes\"\n\ncomponent C() {\n\t<p>hi</p>\n}\n"
	p := filepath.Join(dir, "c.gsx")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := runFmt(&out, &errb, []string{"-w", p}, nil, nil, codegen.Options{}, dir); code != 0 {
		t.Fatalf("runFmt=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(readFile(t, p), "\"bytes\"") {
		t.Fatal("gofmt mode from gsx.toml must keep the unused import")
	}
}
```

Add these helpers to `gen/fmt_test.go` **only if they do not already exist** (check first — `TestFmtRemovesUnusedImport` already builds a module inline; extract if convenient, otherwise add):

```go
// newFmtModule creates a temp dir containing a go.mod that replaces gsx with the
// repo under test, ready for module-resolving fmt runs.
func newFmtModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	mod := "module example.com/u\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => " + repoRoot + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./gen/ -run TestFmtImports -count=1 -v`
Expected: FAIL — `flag provided but not defined: -imports`, exit 2 for the wrong reason / assertions fail.

- [ ] **Step 3: Write minimal implementation**

In `gen/fmt.go`, extend the flag block:

```go
	var (
		write       bool
		list        bool
		diff        bool
		noImports   bool
		importsFlag string
	)
	fs.BoolVar(&write, "w", false, "write result to (source) file instead of stdout")
	fs.BoolVar(&list, "l", false, "list files whose formatting differs")
	fs.BoolVar(&diff, "d", false, "display diffs instead of rewriting files")
	fs.StringVar(&importsFlag, "imports", "", `import handling: "goimports" (default; remove unused + merge/dedup/group) or "gofmt" (format only)`)
	fs.BoolVar(&noImports, "no-imports", false, `alias for -imports gofmt`)
```

After `fs.Parse`, resolve the CLI-level mode (before `paths`/`files`):

```go
	// CLI mode: -imports wins; -no-imports is its "gofmt" alias. Asking for both
	// goimports and no-imports is contradictory, so it is a usage error rather
	// than a silent precedence rule.
	var cliMode gsxfmt.ImportsMode
	if importsFlag != "" {
		m, err := gsxfmt.ParseImportsMode(importsFlag)
		if err != nil {
			fmt.Fprintf(stderr, "gsx: -imports: %v\n", err)
			return 2
		}
		cliMode = m
	}
	if noImports {
		if cliMode == gsxfmt.ImportsGoimports {
			fmt.Fprintf(stderr, "gsx: -no-imports conflicts with -imports goimports\n")
			return 2
		}
		cliMode = gsxfmt.ImportsGofmt
	}
```

Replace the `unusedByPath` block:

```go
	// Mode is resolved per directory (gsx.toml is discovered by walking up from
	// each file), with the CLI flag — when given — overriding every directory.
	modeByDir := map[string]gsxfmt.ImportsMode{}
	modeFor := func(path string) gsxfmt.ImportsMode {
		dir := filepath.Dir(path)
		m, ok := modeByDir[dir]
		if !ok {
			m = cliMode.Or(importsModeFor(dir))
			modeByDir[dir] = m
		}
		return m
	}

	// Only files whose mode removes unused imports need the (expensive) module
	// analysis; gofmt-mode files are excluded so no codegen.Module is opened for
	// them.
	var removalFiles []string
	for _, p := range files {
		if modeFor(p).RemoveUnused() {
			removalFiles = append(removalFiles, p)
		}
	}
	var unusedByPath map[string][]gsxfmt.ImportRef
	if len(removalFiles) > 0 {
		unusedByPath = analyzeUnusedImports(removalFiles, opts)
	}
```

In the per-file loop, replace the `gsxfmt.FormatRemovingImportsWith(...)` call:

```go
		mode := modeFor(path)
		formatted, err := gsxfmt.FormatWith(path, orig, gsxfmt.FormatOptions{
			Unused:  unusedByPath[abs], // nil for gofmt-mode files
			Width:   width,
			CSSFmt:  cssFmt,
			JSFmt:   jsFmt,
			Reorder: mode.Reorder(),
		})
```

Add `importsModeFor` next to `printWidthFor`:

```go
// importsModeFor returns the effective gsx.toml [formatter] imports mode for dir
// (default goimports), best-effort: discovery/decoding failures fall back to the
// default, exactly like printWidthFor.
func importsModeFor(dir string) gsxfmt.ImportsMode {
	path, ok := discoverConfig(dir)
	if !ok {
		return gsxfmt.DefaultImportsMode
	}
	cfg, err := loadConfig(path)
	if err != nil {
		return gsxfmt.DefaultImportsMode
	}
	return cfg.effectiveImportsMode()
}
```

Update `runFmt`'s doc comment: document `-imports` and `-no-imports`, and note that the default (`goimports`) both removes unused imports and merges/dedups/groups them.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./gen/ -run TestFmt -count=1 -v`
Expected: PASS — new tests plus the pre-existing `TestFmtRemovesUnusedImport`, `TestFmtNoImportsKeepsUnused`, `TestFmtOutsideModuleFallsBack`, `TestFmtWriteIdempotent`.

Note: `TestFmtNoImportsKeepsUnused` must still pass — `-no-imports` still keeps unused imports, it just now also skips reorder.

- [ ] **Step 5: Manually drive the real CLI on the motivating example**

```bash
tmp=$(mktemp -d)
printf 'module example.com/u\n\ngo 1.26.1\n' > "$tmp/go.mod"
printf 'package u\n\nimport "strings"\n\nimport (\n\t"fmt"\n\n\t"strings"\n)\n\ncomponent C() {\n\t<p>{ fmt.Sprint(strings.ToUpper("x")) }</p>\n}\n' > "$tmp/c.gsx"
go run ./cmd/gsx fmt "$tmp/c.gsx"
echo "--- with -imports gofmt ---"
go run ./cmd/gsx fmt -imports gofmt "$tmp/c.gsx"
rm -rf "$tmp"
```
Expected: the first prints ONE merged, deduped, std/third-party-grouped import block. The second prints the two original declarations with the duplicate intact.

- [ ] **Step 6: Commit**

```bash
git add gen/fmt.go gen/fmt_test.go
git commit -m "feat(fmt): -imports gofmt|goimports flag, per-dir mode resolution"
```

---

### Task 6: LSP formatting honors the configured mode

**Files:**
- Modify: `internal/lsp/server.go` (`Analyzer` interface, ~line 34)
- Modify: `internal/lsp/format.go` (`handleFormatting`)
- Modify: `gen/lsp.go` (`lspAnalyzer.PrintWidth` is at ~line 334; add `ImportsMode` beside it)
- Modify (test stubs): `internal/lsp/documentsymbol_test.go`, `internal/lsp/references_cache_test.go`, `internal/lsp/server_async_test.go`, `internal/lsp/server_debounce_test.go`, `internal/lsp/server_lifecycle_test.go`, `internal/lsp/server_sync_test.go`, `internal/lsp/workspacesymbol_test.go`
- Test: `gen/formatting_e2e_test.go`

**Interfaces:**
- Consumes: `gsxfmt.ImportsMode` + predicates (Task 1); `gsxfmt.FormatWith` (Task 2); `config.effectiveImportsMode()` (Task 4).
- Produces: `Analyzer.ImportsMode(dir string) gsxfmt.ImportsMode`. Task 7's code action uses the same `Analyzer`, but deliberately ignores the mode.

**Heads-up:** adding a method to the `Analyzer` interface breaks **seven** test analyzers. Each needs a one-line stub returning `gsxfmt.ImportsGoimports`. Find them all with:
`grep -rln "PrintWidth" internal/lsp/*_test.go`

- [ ] **Step 1: Write the failing test**

Append to `gen/formatting_e2e_test.go` (mirror `TestFormattingRemovesUnusedImport` at line ~86 for the module + `formattingEdits` helper):

```go
// TestFormattingGoimportsModeMergesImports: with no gsx.toml (default
// goimports), textDocument/formatting merges and dedups import declarations.
func TestFormattingGoimportsModeMergesImports(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must := func(p, c string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/u\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	src := "package u\n\nimport \"strings\"\n\nimport (\n\t\"fmt\"\n\n\t\"strings\"\n)\n\n" +
		"component C() {\n\t<p>{ fmt.Sprint(strings.ToUpper(\"x\")) }</p>\n}\n"
	must("c.gsx", src)
	uri := "file://" + filepath.Join(dir, "c.gsx")

	edits := formattingEdits(t, uri, src)
	if len(edits) != 1 {
		t.Fatalf("want 1 edit, got %d", len(edits))
	}
	if n := strings.Count(edits[0].NewText, "\"strings\""); n != 1 {
		t.Fatalf("formatting did not dedup strings (%d):\n%s", n, edits[0].NewText)
	}
}

// TestFormattingGofmtModeLeavesImportsAlone: [formatter] imports = "gofmt" makes
// textDocument/formatting stop removing AND stop reordering imports.
func TestFormattingGofmtModeLeavesImportsAlone(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must := func(p, c string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/u\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("gsx.toml", "[formatter]\nimports = \"gofmt\"\n")
	// An unused import: goimports mode would drop it, gofmt mode must not.
	src := "package u\n\nimport \"strings\"\n\ncomponent C() {\n\t<p>hi</p>\n}\n"
	must("c.gsx", src)
	uri := "file://" + filepath.Join(dir, "c.gsx")

	edits := formattingEdits(t, uri, src)
	// Already canonical under gofmt mode ⇒ no edits at all.
	if len(edits) != 0 {
		t.Fatalf("gofmt mode must not touch imports, got edit:\n%s", edits[0].NewText)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./gen/ -run TestFormattingGo -count=1 -v`
Expected: FAIL — `TestFormattingGofmtModeLeavesImportsAlone` gets an edit removing `"strings"` (mode ignored).

- [ ] **Step 3: Add `ImportsMode` to the `Analyzer` interface**

In `internal/lsp/server.go`, inside the `Analyzer` interface, after `PrintWidth`:

```go
	// PrintWidth returns the gsx.toml print width for the given directory
	// (default 80). Used by textDocument/formatting.
	PrintWidth(dir string) int
	// ImportsMode returns the gsx.toml [formatter] imports mode for the given
	// directory (default goimports). Used by textDocument/formatting; the
	// source.organizeImports code action deliberately ignores it and always
	// organizes.
	ImportsMode(dir string) gsxfmt.ImportsMode
```

Add `"github.com/gsxhq/gsx/internal/gsxfmt"` to `internal/lsp/server.go`'s imports.

- [ ] **Step 4: Implement it on the real analyzer**

In `gen/lsp.go`, directly below `PrintWidth`:

```go
// ImportsMode resolves the effective gsx.toml [formatter] imports mode for dir,
// layering the programmatic optCfg over the file config exactly like PrintWidth.
// Best-effort: returns the default (goimports) on any failure.
func (a lspAnalyzer) ImportsMode(dir string) gsxfmt.ImportsMode {
	merged := resolveConfigBestEffort(dir, a.optCfg, a.warnw)
	return merged.effectiveImportsMode()
}
```

- [ ] **Step 5: Add the stub to all seven test analyzers**

For each file listed under **Files**, add one method next to its `PrintWidth`. Example for `internal/lsp/server_lifecycle_test.go`:

```go
func (nilAnalyzer) PrintWidth(string) int                     { return 80 }
func (nilAnalyzer) ImportsMode(string) gsxfmt.ImportsMode     { return gsxfmt.ImportsGoimports }
```

Repeat with the correct receiver type for `symbolFileAnalyzer`, `blockingAnalyzer`, `moduleRefsAnalyzer`, `countingAnalyzer`, and whichever types `server_sync_test.go` / `workspacesymbol_test.go` declare. Add the `gsxfmt` import to each file.

Run `go build ./... && go vet ./internal/lsp/` to find any you missed.

- [ ] **Step 6: Make `handleFormatting` honor the mode**

In `internal/lsp/format.go`, replace the body from `path := uriToPath(uri)` through the `formatted, err := …` call:

```go
	path := uriToPath(uri)
	dir := filepath.Dir(path)
	mode := s.analyzer.ImportsMode(dir)

	// Only goimports mode removes unused imports; gofmt mode leaves them.
	var unused []gsxfmt.ImportRef
	if mode.RemoveUnused() {
		if pkg := s.pkgs[dir]; pkg != nil {
			unused = pkg.UnusedImports[path] // nil when analysis is unavailable/unreliable
		}
	}
	width := s.analyzer.PrintWidth(dir)
	// CSSFmt/JSFmt stay nil: LSP formatting has never run the <style>/<script>
	// formatters that the CLI runs. Preserving that here keeps output identical;
	// closing the gap is a separate change.
	formatted, err := gsxfmt.FormatWith(path, []byte(text), gsxfmt.FormatOptions{
		Unused:  unused,
		Width:   width,
		Reorder: mode.Reorder(),
	})
```

Also update `handleFormatting`'s doc comment to say it honors `[formatter] imports`.

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/lsp/ ./gen/ -run 'TestFormatting|TestServer' -count=1 -v`
Expected: PASS, including both new tests and the pre-existing `TestFormattingRemovesUnusedImport`.

- [ ] **Step 8: Commit**

```bash
git add internal/lsp/server.go internal/lsp/format.go gen/lsp.go internal/lsp/*_test.go gen/formatting_e2e_test.go
git commit -m "feat(lsp): textDocument/formatting honors [formatter] imports mode"
```

---

### Task 7: LSP `source.organizeImports` code action

**Files:**
- Modify: `internal/lsp/protocol.go` (`serverCapabilities` ~line 51; add new types)
- Modify: `internal/lsp/server.go` (`handle` dispatch ~line 204; `handleInitialize` capabilities ~line 228)
- Create: `internal/lsp/codeaction.go`
- Test: `internal/lsp/codeaction_test.go` (new); `gen/formatting_e2e_test.go` (capability assertion)

**Interfaces:**
- Consumes: `gsxfmt.FormatWith`/`FormatOptions` (Task 2); `s.docs.text(uri)`, `s.pkgs[dir]`, `s.analyzer.PrintWidth(dir)`, `endPosition(text, s.enc)` (existing, see `internal/lsp/format.go`).
- Produces: `handleCodeAction(f frame) error`, `CodeAction`, `WorkspaceEdit`, `CodeActionOptions`.

**Semantics (do not "improve" these):**
- The action **always** applies the goimports transform — remove unused **and** reorder — **regardless** of `[formatter] imports`. `source.organizeImports` *means* goimports, exactly as in gopls, where formatting can be plain gofmt while the action still organizes. Gating it on the mode would make it a no-op in gofmt mode, defeating its purpose.
- The edit is a **whole-document** `TextEdit`. gsx has no partial/region formatter; its canonical form comes from a whole-document parse → print. This is a deliberate, documented deviation from gopls's import-region-only edits.
- Return an **empty list** when the organized document equals the buffer (no no-op edits on save), when the buffer fails to parse (mid-edit), when the file is not `.gsx`, or when `context.only` excludes the kind.

- [ ] **Step 1: Write the failing test**

Create `internal/lsp/codeaction_test.go`:

```go
package lsp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// codeActions drives one textDocument/codeAction request against a server backed
// by nilAnalyzer, and returns the decoded result.
func codeActions(t *testing.T, uri, src string, only []string) []CodeAction {
	t.Helper()
	return codeActionsWith(t, uri, src, only, nilAnalyzer{})
}

// codeActionsWith is codeActions with an explicit Analyzer, so a test can vary
// the reported [formatter] imports mode.
func codeActionsWith(t *testing.T, uri, src string, only []string, a Analyzer) []CodeAction {
	t.Helper()
	in := framed(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += framed(t, map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": "gsx", "version": 1, "text": src}},
	})
	ctx := map[string]any{"diagnostics": []any{}}
	if only != nil {
		ctx["only"] = only
	}
	in += framed(t, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "textDocument/codeAction",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"range":        map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 0}},
			"context":      ctx,
		},
	})
	in += framed(t, map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out bytes.Buffer
	srv := NewServer(strings.NewReader(in), &out, a)
	if err := srv.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, m := range readFrames(t, out.String()) {
		raw, ok := m["result"]
		if !ok {
			continue
		}
		var actions []CodeAction
		if err := json.Unmarshal(raw, &actions); err == nil && len(actions) > 0 {
			return actions
		}
		// An empty result array decodes to len 0; distinguish it from the
		// initialize result (an object, which fails to unmarshal into a slice).
		if string(raw) == "[]" {
			return nil
		}
	}
	return nil
}

const dupImportSrc = "package x\n\nimport \"strings\"\n\nimport (\n\t\"fmt\"\n\n\t\"strings\"\n)\n\n" +
	"component C() {\n\t<p>{ fmt.Sprint(strings.ToUpper(\"x\")) }</p>\n}\n"

// TestCodeActionOrganizeImports: the action is offered and its edit merges and
// dedups the imports.
func TestCodeActionOrganizeImports(t *testing.T) {
	actions := codeActions(t, "file:///tmp/c.gsx", dupImportSrc, []string{"source.organizeImports"})
	if len(actions) != 1 {
		t.Fatalf("want 1 action, got %d", len(actions))
	}
	a := actions[0]
	if a.Kind != "source.organizeImports" {
		t.Fatalf("kind = %q", a.Kind)
	}
	if a.Edit == nil || len(a.Edit.Changes["file:///tmp/c.gsx"]) != 1 {
		t.Fatalf("want one whole-document edit, got %+v", a.Edit)
	}
	got := a.Edit.Changes["file:///tmp/c.gsx"][0].NewText
	if n := strings.Count(got, "\"strings\""); n != 1 {
		t.Fatalf("duplicate not deduped (%d):\n%s", n, got)
	}
	if n := strings.Count(got, "import"); n != 1 {
		t.Fatalf("declarations not merged (%d):\n%s", n, got)
	}
}

// TestCodeActionOrganizeImportsIgnoresGofmtMode: the action organizes even when
// the configured formatter mode is gofmt — organizing is its entire purpose.
func TestCodeActionOrganizeImportsIgnoresGofmtMode(t *testing.T) {
	// gofmtAnalyzer reports ImportsMode = gofmt; the action must ignore it.
	actions := codeActionsWith(t, "file:///tmp/c.gsx", dupImportSrc, []string{"source.organizeImports"}, gofmtAnalyzer{})
	if len(actions) != 1 {
		t.Fatalf("action must be offered under gofmt mode, got %d", len(actions))
	}
	got := actions[0].Edit.Changes["file:///tmp/c.gsx"][0].NewText
	if n := strings.Count(got, "\"strings\""); n != 1 {
		t.Fatalf("action must organize under gofmt mode (%d):\n%s", n, got)
	}
}

// TestCodeActionNoOpWhenAlreadyOrganized: an already-canonical document yields
// no action, so codeActionsOnSave is a no-op.
func TestCodeActionNoOpWhenAlreadyOrganized(t *testing.T) {
	src := "package x\n\nimport (\n\t\"fmt\"\n\n\t\"strings\"\n)\n\n" +
		"component C() {\n\t<p>{ fmt.Sprint(strings.ToUpper(\"x\")) }</p>\n}\n"
	if got := codeActions(t, "file:///tmp/c.gsx", src, []string{"source.organizeImports"}); len(got) != 0 {
		t.Fatalf("want no action for canonical doc, got %+v", got)
	}
}

// TestCodeActionSkipsNonGsx: gopls owns .go files.
func TestCodeActionSkipsNonGsx(t *testing.T) {
	if got := codeActions(t, "file:///tmp/c.go", dupImportSrc, []string{"source.organizeImports"}); len(got) != 0 {
		t.Fatalf("want no action for .go, got %+v", got)
	}
}

// TestCodeActionHonorsOnlyFilter: a request restricted to quickfix gets nothing.
func TestCodeActionHonorsOnlyFilter(t *testing.T) {
	if got := codeActions(t, "file:///tmp/c.gsx", dupImportSrc, []string{"quickfix"}); len(got) != 0 {
		t.Fatalf("want no action when only=[quickfix], got %+v", got)
	}
}

// TestCodeActionOnlySourcePrefixMatches: "source" is a prefix of
// "source.organizeImports" in LSP's kind hierarchy, so it must match.
func TestCodeActionOnlySourcePrefixMatches(t *testing.T) {
	if got := codeActions(t, "file:///tmp/c.gsx", dupImportSrc, []string{"source"}); len(got) != 1 {
		t.Fatalf("only=[source] must match, got %d", len(got))
	}
}

// TestCodeActionEmptyOnlyOffersAction: an unrestricted request offers it.
func TestCodeActionEmptyOnlyOffersAction(t *testing.T) {
	if got := codeActions(t, "file:///tmp/c.gsx", dupImportSrc, nil); len(got) != 1 {
		t.Fatalf("unrestricted request must offer the action, got %d", len(got))
	}
}

// TestCodeActionParseErrorYieldsNothing: a mid-edit buffer never gets a
// destructive whole-file edit.
func TestCodeActionParseErrorYieldsNothing(t *testing.T) {
	bad := "package x\n\ncomponent C() {\n\t<p>unclosed\n"
	if got := codeActions(t, "file:///tmp/c.gsx", bad, []string{"source.organizeImports"}); len(got) != 0 {
		t.Fatalf("want no action for unparseable buffer, got %+v", got)
	}
}

// TestInitializeAdvertisesOrganizeImports: the capability names the kind so the
// client can wire editor.codeActionsOnSave.
func TestInitializeAdvertisesOrganizeImports(t *testing.T) {
	in := framed(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += framed(t, map[string]any{"jsonrpc": "2.0", "method": "exit"})
	var out bytes.Buffer
	srv := NewServer(strings.NewReader(in), &out, nilAnalyzer{})
	if err := srv.Run(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"source.organizeImports"`) {
		t.Fatalf("initialize did not advertise source.organizeImports:\n%s", out.String())
	}
}
```

Add to the bottom of `internal/lsp/codeaction_test.go` the analyzer variant used by `TestCodeActionOrganizeImportsIgnoresGofmtMode`:

```go
// gofmtAnalyzer reports gofmt mode, to prove the code action ignores it and
// organizes anyway. It embeds nilAnalyzer for every other Analyzer method.
type gofmtAnalyzer struct{ nilAnalyzer }

func (gofmtAnalyzer) ImportsMode(string) gsxfmt.ImportsMode { return gsxfmt.ImportsGofmt }
```

Import `"github.com/gsxhq/gsx/internal/gsxfmt"` in this file.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lsp/ -run TestCodeAction -count=1 -v`
Expected: FAIL — build error, `undefined: CodeAction`.

- [ ] **Step 3: Add the protocol types**

In `internal/lsp/protocol.go`, extend `serverCapabilities` and add the new types:

```go
type serverCapabilities struct {
	PositionEncoding           string             `json:"positionEncoding"`
	TextDocumentSync           int                `json:"textDocumentSync"`
	DefinitionProvider         bool               `json:"definitionProvider"`
	ReferencesProvider         bool               `json:"referencesProvider"`
	DocumentFormattingProvider bool               `json:"documentFormattingProvider"`
	HoverProvider              bool               `json:"hoverProvider"`
	DocumentSymbolProvider     bool               `json:"documentSymbolProvider"`
	WorkspaceSymbolProvider    bool               `json:"workspaceSymbolProvider"`
	CodeActionProvider         *CodeActionOptions `json:"codeActionProvider,omitempty"`
}

// CodeActionOptions advertises which code-action kinds the server produces. It
// is a struct rather than a bare `true` so clients know they can wire
// editor.codeActionsOnSave to source.organizeImports.
type CodeActionOptions struct {
	CodeActionKinds []string `json:"codeActionKinds"`
}

// organizeImportsKind is the LSP kind for the organize-imports source action.
const organizeImportsKind = "source.organizeImports"

type codeActionContext struct {
	// Only restricts the kinds the client wants. Empty means "any".
	Only []string `json:"only"`
}

type codeActionParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Range        Range                  `json:"range"`
	Context      codeActionContext      `json:"context"`
}

// WorkspaceEdit maps a document URI to the edits to apply to it.
type WorkspaceEdit struct {
	Changes map[string][]TextEdit `json:"changes"`
}

// CodeAction is one entry of the textDocument/codeAction result. Edit is carried
// inline, so the server advertises no resolveProvider.
type CodeAction struct {
	Title string         `json:"title"`
	Kind  string         `json:"kind"`
	Edit  *WorkspaceEdit `json:"edit,omitempty"`
}
```

- [ ] **Step 4: Dispatch and advertise**

In `internal/lsp/server.go` `handle`, after the `textDocument/formatting` case:

```go
	case "textDocument/codeAction":
		return s.handleCodeAction(f)
```

In `handleInitialize`, add to the `serverCapabilities` literal:

```go
		WorkspaceSymbolProvider:    true,
		CodeActionProvider:         &CodeActionOptions{CodeActionKinds: []string{organizeImportsKind}},
```

- [ ] **Step 5: Write `handleCodeAction`**

Create `internal/lsp/codeaction.go`:

```go
package lsp

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/gsxhq/gsx/internal/gsxfmt"
)

// handleCodeAction answers textDocument/codeAction for a .gsx document. The only
// action offered is source.organizeImports.
//
// The action ALWAYS applies the goimports transform — remove unused imports and
// reorder (merge/dedup/group/sort) — regardless of the configured
// [formatter] imports mode. That is the point of the action, and it mirrors
// gopls: textDocument/formatting may be plain gofmt while source.organizeImports
// still organizes. This is what lets a project set imports = "gofmt" for
// format-on-save and wire editor.codeActionsOnSave to organize separately.
//
// The edit is a single whole-document TextEdit: gsx has no partial formatter, so
// its canonical form is produced by a whole-document parse → print. Applying the
// action therefore also canonicalizes the rest of the document — a deliberate
// deviation from gopls's import-region-only edits.
//
// An empty result (no action) is returned when the document is not .gsx, when
// context.only excludes the kind, when the buffer does not parse (mid-edit), or
// when the organized document already equals the buffer — so an on-save action
// is a true no-op rather than a redundant edit.
func (s *Server) handleCodeAction(f frame) error {
	var p codeActionParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.reply(f.ID, []CodeAction{})
	}
	uri := p.TextDocument.URI
	path := uriToPath(uri)
	if !strings.HasSuffix(path, ".gsx") {
		return s.reply(f.ID, []CodeAction{}) // gopls owns .go
	}
	if !wantsKind(p.Context.Only, organizeImportsKind) {
		return s.reply(f.ID, []CodeAction{})
	}
	text, ok := s.docs.text(uri)
	if !ok {
		return s.reply(f.ID, []CodeAction{})
	}
	dir := filepath.Dir(path)
	var unused []gsxfmt.ImportRef
	if pkg := s.pkgs[dir]; pkg != nil {
		unused = pkg.UnusedImports[path] // nil when analysis is unavailable
	}
	organized, err := gsxfmt.FormatWith(path, []byte(text), gsxfmt.FormatOptions{
		Unused:  unused,
		Width:   s.analyzer.PrintWidth(dir),
		Reorder: true, // always: this action IS goimports
	})
	if err != nil || string(organized) == text {
		return s.reply(f.ID, []CodeAction{}) // unparseable mid-edit, or already organized
	}
	edit := TextEdit{
		Range:   Range{Start: Position{Line: 0, Character: 0}, End: endPosition(text, s.enc)},
		NewText: string(organized),
	}
	return s.reply(f.ID, []CodeAction{{
		Title: "Organize Imports",
		Kind:  organizeImportsKind,
		Edit:  &WorkspaceEdit{Changes: map[string][]TextEdit{uri: {edit}}},
	}})
}

// wantsKind reports whether a client asking for the kinds in `only` wants `kind`.
// An empty `only` means "any kind". LSP kinds are dot-separated hierarchies, so a
// requested "source" matches "source.organizeImports".
func wantsKind(only []string, kind string) bool {
	if len(only) == 0 {
		return true
	}
	for _, k := range only {
		if k == kind || strings.HasPrefix(kind, k+".") {
			return true
		}
	}
	return false
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/lsp/ -run 'TestCodeAction|TestInitialize' -count=1 -v`
Expected: PASS (9 tests).

- [ ] **Step 7: Run the whole LSP + gen suites**

Run: `go test ./internal/lsp/ ./gen/ -count=1`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/lsp/protocol.go internal/lsp/server.go internal/lsp/codeaction.go internal/lsp/codeaction_test.go
git commit -m "feat(lsp): source.organizeImports code action (always goimports)"
```

---

### Task 8: Documentation

**Files:**
- Modify: `docs/guide/config.md` (the `[formatter]` section — it already documents `print_width`)
- Modify: `docs/guide/cli.md` (the `gsx fmt` section — it already documents `-no-imports`)
- Modify: `docs/guide/editor.md` (add `codeActionsOnSave` usage)
- Modify: `docs/ROADMAP.md` (tick off / record the shipped feature)

**Interfaces:**
- Consumes: the final user-facing surface from Tasks 4, 5, 7.
- Produces: nothing consumed by code.

**Constraint:** literal `{{ }}` in `docs/guide/**` prose must be wrapped in a `::: v-pre` block — VitePress parses `{{ }}` as a Vue interpolation and the build fails otherwise. None of the content below contains `{{ }}`, but check any example you add.

- [ ] **Step 1: Read the existing sections you are extending**

Run:
```bash
grep -n "print_width" -B 5 -A 10 docs/guide/config.md
grep -n "no-imports" -B 10 -A 10 docs/guide/cli.md
```
Match the surrounding heading depth, table style, and voice. Do not restructure the pages.

- [ ] **Step 2: Document the config key**

In `docs/guide/config.md`, in the `[formatter]` table/section, add `imports` alongside `print_width`:

```toml
[formatter]
print_width = 80
# How `gsx fmt` and the language server treat imports.
#   "goimports" (default) — remove unused imports, then merge every import
#                           declaration into one block, drop duplicates, split
#                           standard library from third-party, sort each group.
#   "gofmt"               — format only: sort within an existing group, never
#                           remove, merge, dedup, or regroup.
imports = "goimports"
```

Explain the gopls parallel in prose: these are the same two behaviors gopls offers, and `gsx` cannot *add* missing imports (a gsx Go chunk never references the template's imports, so there is no symbol to resolve to a package).

- [ ] **Step 3: Document the CLI flags**

In `docs/guide/cli.md`, under `gsx fmt`, document:

- `-imports goimports|gofmt` — override `[formatter] imports` for this run.
- `-no-imports` — alias for `-imports gofmt`.

State the resolution order explicitly: `-imports` / `-no-imports` → `gsx.toml` `[formatter] imports` → default `goimports`. Note that `-imports goimports` together with `-no-imports` is a usage error.

Update any existing sentence claiming `-no-imports` merely "skips module analysis" — it now selects gofmt mode, which also skips reordering.

- [ ] **Step 4: Document the editor code action**

In `docs/guide/editor.md`, add a short section:

````markdown
### Organize imports on save

The gsx language server offers the standard `source.organizeImports` code
action. It always organizes — remove unused, merge, dedup, group, sort — even
when `[formatter] imports = "gofmt"`, exactly like gopls, where formatting can
be plain gofmt while the action still organizes.

```json
"[gsx]": {
  "editor.formatOnSave": true,
  "editor.codeActionsOnSave": { "source.organizeImports": "explicit" }
}
```

Because gsx has no partial formatter, the action's edit spans the whole
document: applying it also canonicalizes the rest of the file.
````

- [ ] **Step 5: Update the roadmap**

In `docs/ROADMAP.md`, record that gofmt/goimports import modes and the LSP `source.organizeImports` action shipped. Remove any entry this supersedes.

- [ ] **Step 6: Verify the docs build is not broken**

Run: `grep -rn "{{" docs/guide/config.md docs/guide/cli.md docs/guide/editor.md | grep -v "v-pre"`
Expected: no output (or only pre-existing lines already inside a `::: v-pre` block).

- [ ] **Step 7: Commit**

```bash
git add docs/guide/config.md docs/guide/cli.md docs/guide/editor.md docs/ROADMAP.md
git commit -m "docs: [formatter] imports mode, gsx fmt -imports, organize-imports action"
```

---

### Task 9: Full verification and sibling check

**Files:** none modified (unless a check fails).

- [ ] **Step 1: Run the authoritative CI-equivalent suite**

Run: `make ci`
Expected: PASS. This is uncached (`-count=1`) and mirrors `.github/workflows/ci.yml` — build/vet/test both modules, examples drift, `gofmt` + `gsx fmt`.

**If `gsx fmt` drift is reported on the repo's own `.gsx` files:** that is a *real finding*, not a nuisance — the new default (goimports) now reorders imports in checked-in `.gsx` files. Inspect the diff. If it is correct organization, commit the reformatted files as their own commit (`chore: reformat .gsx imports under goimports mode`). Do not suppress it by changing the default.

- [ ] **Step 2: Run the linter**

Run: `make lint`
Expected: PASS (golangci-lint: SA\*, QF\*, modernize).

- [ ] **Step 3: Confirm no syntax change reached the siblings**

This feature changes no gsx syntax, so `../tree-sitter-gsx`, `../vscode-gsx`, and `../gsxhq.github.io` need **no grammar work**. Verify by reasoning, not by editing: no token, no AST node, and no `.gsx` grammar production was added — only formatter behavior, a TOML key, a CLI flag, and an LSP method.

One optional sibling follow-up (do **not** do it in this branch): `vscode-gsx` may want to ship a default `editor.codeActionsOnSave` for `.gsx`. Note it for a separate PR.

- [ ] **Step 4: Verify the motivating example end-to-end one final time**

Run the Task 5 Step 5 shell snippet again. Expected: `gsx fmt` merges + dedups + groups; `gsx fmt -imports gofmt` leaves the duplicate and both declarations.

- [ ] **Step 5: Request an independent adversarial review**

Per `CLAUDE.md`, a subsystem gets **one independent adversarial reviewer** before merge — one that *builds throwaway probe programs*, not just reads the diff. Probes worth writing:
- A `.gsx` file whose `GoChunk` contains the word `import` inside a string and a comment, plus no real import → assert `reorderImports` leaves it byte-identical.
- A `.gsx` file with element literals (`GoWithElements`) plus a leading import block → assert reorder touches only the import chunk and the element region round-trips unchanged.
- A file where an import is *both* unused and duplicated → assert remove-then-reorder converges and is idempotent.
- A `gsx.toml` with `imports = "gofmt"` next to one without → assert a single `gsx fmt` run over both directories applies different modes per directory.
- Assert `gsx fmt` twice is a fixed point on every `.gsx` file in `examples/`.

---

## Self-Review

**Spec coverage:** every spec section maps to a task — modes vocabulary (T1), reorder pass + `FormatOnly` rationale + per-`GoChunk` walk + parse-error fallback (T2), `FormatWith`/wrappers (T3), `[formatter] imports` config (T4), `-imports`/`-no-imports` CLI + precedence + conflict error (T5), LSP formatting honoring mode + `ImportsMode` on the `Analyzer` interface at its **corrected** location (T6), `source.organizeImports` always-goimports whole-document action + capability + `only` filter + no-op suppression (T7), docs (T8), `make ci`/`make lint`/adversarial review (T9).

**Spec deviation, deliberate:** the spec's "Testing" section calls for txtar **corpus cases**. Verified against the tree: `internal/corpus` pins parse → codegen → render and **never runs the formatter**, so a corpus case cannot exercise reorder. Formatter behavior is pinned in `internal/gsxfmt/*_test.go` and `gen/fmt_test.go`, and that is where this plan puts it. The CLAUDE.md rule "every syntax/codegen change ships a corpus case" does not bind here: this changes neither syntax nor codegen.

**Type consistency:** `ImportsMode`, `ImportsUnset`, `ImportsGofmt`, `ImportsGoimports`, `DefaultImportsMode`, `ParseImportsMode`, `Or`, `RemoveUnused`, `Reorder`, `String`, `FormatOptions{Unused,Width,CSSFmt,JSFmt,Reorder}`, `FormatWith`, `reorderImports`, `reorderChunkImports`, `chunkHasImports`, `goChunkPkg`, `importsModeFor`, `effectiveImportsMode`, `Analyzer.ImportsMode`, `handleCodeAction`, `wantsKind`, `organizeImportsKind`, `CodeActionOptions`, `CodeAction`, `WorkspaceEdit`, `codeActionParams`, `codeActionContext` — each defined exactly once and used with the same spelling and signature throughout.

**Ordering dependency:** Task 2 introduces `FormatWith` (needed by its own tests) and Task 3 folds the legacy wrappers onto it. Task 2 must land before Task 3; Tasks 4–7 all depend on Tasks 1–3. Task 6 must land before Task 7 (the code action's test analyzer embeds `nilAnalyzer`, which only satisfies `Analyzer` once Task 6 adds `ImportsMode` to every stub).
