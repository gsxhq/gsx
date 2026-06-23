# Diagnostics Foundation (`internal/diag`) — Design

**Date:** 2026-06-23
**Status:** Design (approved for plan)
**Slice:** 1 of 2 toward the LSP. **This spec:** the diagnostic data model, position rendering (CLI + `--json`), a diagnostic collector, and **semantic-layer error recovery** (report most errors like `go build`, not just the first). **Slice 2 (separate spec):** parser-layer error recovery (resync at structural boundaries) + rebaseline of parser error goldens.
**Motivation:** the gsx LSP (next project) needs structured diagnostics with ranges, severities, and codes, and it must report all of a file's diagnostics at once. This foundation provides that model and the semantic recovery, consumed first by `gsx generate` and later by the LSP server.
**Related:** `2026-06-23-attr-classification-extensions-design.md` (the toolserver seam the LSP will use; the deferred "jsx errors surface a raw `token.Pos` offset" follow-up that this spec fixes).

## 1. Problem

gsx currently has no diagnostic abstraction — every error is a plain `error`:

- **Positions are inconsistent.** Parser errors carry `line:col` (`fmt.Errorf("%d:%d: …")`); codegen errors are *positionless strings* even though the offending `ast.Node` (with `Pos()/End()`) is in hand at the error site; jsx errors print a **raw `token.Pos` offset** (`"jsx: <script> at 12970: …"`), so volatile the corpus harness scrubs it with a `normalizeDiag` regex (`\bat \d+\b` → `at N`).
- **Fail-fast.** Parser, type resolution, and codegen all stop at the **first** error. `go/types` (under `go/packages`) actually collects *many* type errors, but gsx surfaces only the first and discards the rest.
- **No structure for tools.** Everything is `error` text. The coming LSP needs `{range, severity, code, message, source}` per diagnostic, and all diagnostics for a file — neither of which a single `error` string provides.

**Goal:** a structured `internal/diag` foundation that (a) models diagnostics in an LSP-shaped way, (b) renders them for humans and as `--json`, (c) **collects and reports most errors in a run** by recovering across the semantic layer, and (d) migrates today's parser/codegen/jsx errors onto it — giving codegen errors real positions and fixing the jsx raw-offset gap.

## 2. Scope

**In (Slice 1):**
- `internal/diag` package: `Diagnostic`, `Severity`, `Bag` (collector), and a renderer (CLI text + JSON).
- Semantic-layer recovery: surface **all** `go/types` errors; make codegen **accumulate and continue** per component/node instead of returning on the first error.
- Migrate existing codegen + jsx errors to emit structured diagnostics (codegen gains positions; jsx gains `file:line:col`); convert the single parser error into a diagnostic.
- `--json` output and exit-code semantics for `gsx generate`.
- Rebaseline affected `diagnostics.golden` corpus cases; delete the `normalizeDiag` "at N" hack.

**Out (deferred):**
- **Parser error recovery** (Slice 2) — the parser still returns a single error; it is wrapped into one diagnostic.
- **The LSP server** itself (next project).
- **Lint-check producers** (escape-hatch audit, unused props, …) — `gsx vet` content, not this foundation.
- **0-based / UTF-16 column conversion** — the LSP wire format's concern; this foundation keeps positions as `token.Pos` and renders 1-based line:col.
- **Systematic code assignment** for every existing error — codes are introduced and applied where cheap; exhaustive coding can follow.

## 3. Data model (`internal/diag`)

```go
package diag

import "go/token"

// Severity mirrors the four LSP DiagnosticSeverity levels so the LSP layer maps
// 1:1. Slice 1 only PRODUCES Error; the rest exist so the model is LSP-shaped.
type Severity int

const (
    Error   Severity = iota // a problem that fails the run
    Warning
    Info
    Hint
)

// Diagnostic is one structured problem. It is pure data: positions are raw
// token.Pos resolved against a *token.FileSet at render time (mirrors how
// go/types.Error pairs a Pos with the checker's Fset). Pos..End is a RANGE; for
// a point diagnostic End may equal Pos.
type Diagnostic struct {
    Pos, End token.Pos
    Severity Severity
    Code     string // stable machine code, e.g. "reserved-param" (may be "" early)
    Message  string
    Source   string // origin: "parser" | "types" | "codegen" | "jsx"
}
```

Rationale for raw `token.Pos` + FileSet-at-render (not pre-resolved line:col): the `*token.FileSet` is already threaded through parser and codegen, so resolution is free at the edge; keeping `Diagnostic` FileSet-free makes it trivially serializable and lets the future LSP layer derive 0-based/UTF-16 columns from source without a lossy round-trip through byte columns.

## 4. The collector (`Bag`)

```go
// Bag accumulates diagnostics during a run. One Bag spans a package's
// resolve+codegen pass so a single run reports all of that package's problems.
type Bag struct {
    fset  *token.FileSet
    diags []Diagnostic
}

func NewBag(fset *token.FileSet) *Bag
func (b *Bag) Add(d Diagnostic)
// Errorf is the common case: an Error-severity diagnostic at an AST node's range.
func (b *Bag) Errorf(pos, end token.Pos, code, format string, args ...any)
func (b *Bag) HasErrors() bool          // any Error-severity diagnostic present
func (b *Bag) Sorted() []Diagnostic     // stable order: by filename, then Pos
func (b *Bag) FileSet() *token.FileSet
```

A `*Bag` is threaded through type resolution and codegen, replacing the `return err` sites with `b.Errorf(node.Pos(), node.End(), code, …)` + `continue`. Sorting is by file then position so output (and goldens) are deterministic regardless of recovery order.

## 5. Semantic-layer recovery (the `go build` behaviour)

The principle: **report most errors in one run.** The two layers differ:

- **Type errors (free win):** `go/packages` already collects every `go/types` error in `pkg.Errors`/`pkg.TypeErrors`, each with a real `token.Pos`. Slice 1 maps **all** of them into `Diagnostic`s (`Source:"types"`) instead of returning the first.
- **Codegen checks (accumulate + continue):** `emit.go`/`analyze.go` validation `Add`s to the `Bag` using the offending **AST node's `Pos()/End()`** and continues to the next component/node, rather than returning eagerly. A file therefore reports many codegen errors at once, each now *positioned*. The implementer migrates whichever validation sites actually `return fmt.Errorf("codegen: …")` in the current tree (the attr-merge merge recently changed some, e.g. CSS-context is no longer fail-closed) — the migration is mechanical per error site, not tied to a fixed list here.
- **Isolation unit:** the **component** is the recovery boundary within codegen — a failed component records its diagnostic(s) and is skipped; sibling components still emit. (Node-level continue within a component where safe; component-level is the guaranteed boundary.)

**Write safety is preserved — and stays all-or-nothing per package.** If a package collected any `Error`-severity diagnostic, it writes **no** `.x.go` for that package (never emit partial/broken output — unchanged from today), but it first reports **all** of that package's diagnostics; other packages are processed and written independently (also unchanged). The only behavior change is *exhaustive reporting*, not *partial emission*.

## 6. Migrating existing errors

- **Codegen** (`internal/codegen/emit.go`, `analyze.go`, …): every `return fmt.Errorf("codegen: …")` becomes a `Bag.Errorf` with the node's range; message text preserved (so human output reads the same, now prefixed with `file:line:col:`). Apply a small principled `Code` scheme for these (e.g. `reserved-param`, `reserved-recv`, `unsafe-js-context`, `unresolved-pipeline`) — codes drawn from the error sites that actually exist in the current tree.
- **jsx** (`internal/jsx/jsx.go`): the three `"jsx: … at %d …"` sites emit a `Diagnostic` with the real range from `el.Pos()/interp.Pos()`, rendered as `file:line:col`. This removes the volatility, so the corpus harness's `normalizeDiag` ("at N") scrub is **deleted**.
- **Parser** (`parser/…`): already mostly `line:col`-formatted. Slice 1 converts the single returned `error` into one `Diagnostic` (`Source:"parser"`). Parser sites that lack a position today (e.g. some `attrs.go` "unterminated…" cases) get one where the cursor position is available; true multi-error parser recovery is Slice 2. Parser/types diagnostics may start with `Code:""`.

## 7. Rendering, `--json`, exit codes

- **Human (default):** `file:line:col: message`, one per line, in `Bag.Sorted()` order — matching the parser's existing `3:24:`-style. Non-error severities gain a `warning:`/`info:`/`hint:` infix (no producer in Slice 1, but the renderer supports it). The current `gsx: <dir>: <err>` double-prefix is **dropped** — the diagnostic already carries the file path; a leading `gsx:` program prefix is only used for non-diagnostic operational errors (e.g. "cannot read dir").
- **`--json`:** a JSON array of
  ```json
  {"file":"views.gsx","range":{"start":{"line":3,"col":24},"end":{"line":3,"col":31}},
   "severity":"error","code":"reserved-param","message":"…","source":"codegen"}
  ```
  Line/col are **1-based** (the LSP layer converts to 0-based/UTF-16). Severity serializes as a lowercase string. The flag is added to `gsx generate`; rendering lives in `internal/diag` so future commands (`fmt`, `vet`, `lsp`) reuse it.
- **Exit code:** `gsx generate` exits non-zero iff the `Bag` `HasErrors()`. Warnings/info alone do not fail the build.

## 8. CLI wiring

`gsx generate` parses a `--json` flag (small `flag.FlagSet`, like other subcommands). The generate path (`gen/cache.go`, `gen/main.go`) collects the per-package `Bag`s, merges them, and hands the diagnostics to the `internal/diag` renderer (text or JSON per the flag) on `stdout`/`stderr`. The current `res.Errs`/`errors.Join` + `gsx: %v` sink is replaced by the diagnostic renderer; non-diagnostic operational failures (I/O, bad args) keep the plain `gsx:`-prefixed path.

## 9. Corpus impact + rebaseline strategy

- **Codegen `diagnostics.golden`:** gain `file:line:col:` prefixes and may list **multiple** diagnostics (recovery). Rebaselined via the runner's `--update`, reviewed in the diff to confirm positions and ordering are correct.
- **jsx `diagnostics.golden`:** `"at N"` becomes real `file:line:col`; delete `normalizeDiag` and its regex.
- **Parser `diagnostics.golden`:** largely unchanged (single error, same `line:col:` format); a few positionless cases gain a position.
- **New cases:** add multi-error fixtures (a file with ≥2 independent codegen/type errors) proving exhaustive reporting, and a `--json` golden proving the JSON shape.
- The `diagnostics.golden` section stays **always-enforced** (empty = expect no diagnostics), so a regression that drops or reorders diagnostics fails loudly.

## 10. Testing strategy

- **`internal/diag` unit:** `Bag.Add/Errorf/HasErrors/Sorted` (stable file-then-pos ordering); severity enum; CLI render of a multi-diagnostic Bag; JSON render shape (1-based, lowercase severity, range start/end). Render is FileSet-driven — test with a synthetic FileSet.
- **Semantic recovery:** a package with multiple `go/types` errors reports all of them; a file with multiple codegen errors reports all, each positioned; a failed component does not suppress a sibling component's diagnostics; a package with any error writes no `.x.go` while a clean sibling package still writes.
- **Migration fidelity:** existing single-error codegen/jsx cases produce the same message text, now positioned; parser cases unchanged.
- **Corpus:** the rebaselined goldens pass; `normalizeDiag` removed; `--json` golden round-trips.
- **Exit codes:** `gsx generate` returns non-zero with errors, zero when clean.

## 11. LSP-readiness checklist (why these choices)

- Range (`Pos..End`) → LSP `Diagnostic.range`. ✓
- `Severity` enum (4 levels) → LSP `DiagnosticSeverity`. ✓
- `Code` → LSP `Diagnostic.code` (filtering, future code actions). ✓
- `Source` → LSP `Diagnostic.source`. ✓
- `[]Diagnostic` + collector → LSP "all diagnostics for a document". ✓ (semantic now; parser in Slice 2)
- Raw `token.Pos` + FileSet → LSP layer derives 0-based/UTF-16 from source. ✓
