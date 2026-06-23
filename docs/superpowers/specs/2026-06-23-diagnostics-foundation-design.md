# Diagnostics Foundation (`internal/diag`) — Design

**Date:** 2026-06-23
**Status:** Design (approved for plan)
**Slice:** 1 of 2 toward the LSP. **This spec:** the diagnostic data model, position rendering (CLI + `--json`), a diagnostic collector, and **semantic-layer error recovery** (report most errors like `go build`, not just the first). **Slice 2 (separate spec):** parser-layer error recovery (resync at structural boundaries) + rebaseline of parser error goldens.
**Motivation:** the gsx LSP (next project) needs structured diagnostics with ranges, severities, and codes, and it must report all of a file's diagnostics at once. This foundation provides that model and the semantic recovery, consumed first by `gsx generate` and later by the LSP server.
**Related:** `2026-06-23-attr-classification-extensions-design.md` (the toolserver seam the LSP will use; the deferred "jsx errors surface a raw `token.Pos` offset" follow-up that this spec fixes).

**Prior art considered:**
- `codegen-diagnostic-position-audit.md` — a checked-in inventory of **all 55 positionless codegen diagnostic sites**, each classified and mapped to the AST node whose `Pos()` should supply the position. This is the authoritative migration worklist for §6; the plan reuses it directly instead of re-deriving the sites.
- `2026-06-22-testing-architecture-review.md` §R3 / `2026-06-22-testing-foundation-p0-design.md` §4 — the "position-annotated diagnostics" intention (the audit was its deferred deliverable). R3 floated two test styles: `line:col` in `diagnostics.golden` vs rustc-style inline `//~ ERROR <substr>` annotations. This spec keeps the existing golden approach (190 cases already use it) and notes inline annotations as a possible later refinement (§9).

## 1. Problem

gsx currently has no diagnostic abstraction — every error is a plain `error`:

- **Positions are inconsistent.** Parser errors carry `line:col` (`fmt.Errorf("%d:%d: …")`); codegen errors are *positionless strings* even though the offending `ast.Node` (with `Pos()/End()`) is in hand at the error site; jsx errors print a **raw `token.Pos` offset** (`"jsx: <script> at 12970: …"`), so volatile the corpus harness scrubs it with a `normalizeDiag` regex (`\bat \d+\b` → `at N`).
- **Fail-fast.** Parser, type resolution, and codegen all stop at the **first** error. `go/types` (under `go/packages`) actually collects *many* type errors, but gsx surfaces only the first and discards the rest.
- **No structure for tools.** Everything is `error` text. The coming LSP needs `{range, severity, code, message, source}` per diagnostic, and all diagnostics for a file — neither of which a single `error` string provides.

**Goal:** a structured `internal/diag` foundation that (a) models diagnostics in an LSP-shaped way, (b) renders them for humans and as `--json`, (c) **collects and reports most errors in a run** by recovering across the semantic layer, and (d) migrates today's parser/codegen/jsx errors onto it — giving codegen errors real positions and fixing the jsx raw-offset gap.

## 2. Scope

**In (Slice 1):**
- `internal/diag` package: `Diagnostic` (incl. optional `Help`), `Severity`, `Bag` (collector), and three renderers — **rich** (source snippet + caret underline + `help:`), **compact** (one-line), and **JSON**.
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
- **Structured suggested-fix edits** (a range + replacement, applied by `--fix` / surfaced as LSP code actions) — `Help` is free text only in Slice 1; the edit model + applier is a later increment.

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

// Diagnostic is one structured problem. It carries already-RESOLVED positions
// (token.Position: Filename, Line 1-based, Column 1-based byte) rather than raw
// token.Pos. Rationale: diagnostics originate from TWO different token.FileSets —
// the gsx parser's fset (parser/codegen/jsx) and go/packages' own fset (type
// errors, whose positions map back to .gsx through the generated `//line`
// directives). Storing resolved Positions makes Diagnostic fileset-agnostic and
// self-contained; the LSP layer still derives 0-based/UTF-16 columns from the
// source line (via SourceProvider) when it needs them. Start..End is a RANGE;
// for a point diagnostic End may equal Start.
type Diagnostic struct {
    Start, End token.Position
    Severity   Severity
    Code       string // stable machine code, e.g. "reserved-param" (may be "" early)
    Message    string // the primary one-line problem statement
    Help       string // optional secondary guidance, e.g. "rename the parameter" ("" = none)
    Source     string // origin: "parser" | "types" | "codegen" | "jsx"
}
```

`Help` is the one "rich" field in the model — a single secondary guidance line that
serves both audiences: humans read it under the caret, agents read it from JSON.
A *structured* suggested-fix (a range + replacement text, mapping to an LSP code
action / a future `gsx … --fix`) is deliberately **not** modeled in Slice 1 (see
§2 Out); `Help` stays free text. The richness in Slice 1 is in *rendering*, not
in an edit-application engine.

Rationale for raw `token.Pos` + FileSet-at-render (not pre-resolved line:col): the `*token.FileSet` is already threaded through parser and codegen, so resolution is free at the edge; keeping `Diagnostic` FileSet-free makes it trivially serializable and lets the future LSP layer derive 0-based/UTF-16 columns from source without a lossy round-trip through byte columns.

## 4. The collector (`Bag`)

```go
// Bag accumulates diagnostics during a run. One Bag spans a package's
// resolve+codegen pass so a single run reports all of that package's problems.
// It holds the gsx parser fset purely as a convenience for resolving AST-node
// token.Pos in Errorf; diagnostics from other filesets (type errors) are added
// pre-resolved via Add.
type Bag struct {
    fset  *token.FileSet
    diags []Diagnostic
}

func NewBag(fset *token.FileSet) *Bag
// Add appends an already-resolved Diagnostic (used for go/types errors, whose
// Position comes from go/packages' fset).
func (b *Bag) Add(d Diagnostic)
// Errorf is the common case for gsx-fset diagnostics: an Error-severity
// diagnostic at an AST node's range. It resolves pos/end via the Bag's fset.
func (b *Bag) Errorf(pos, end token.Pos, code, format string, args ...any)
func (b *Bag) HasErrors() bool          // any Error-severity diagnostic present
func (b *Bag) Sorted() []Diagnostic     // stable order: by filename, then line, then column
```

A `*Bag` is threaded through type resolution and codegen, replacing the `return err` sites with `b.Errorf(node.Pos(), node.End(), code, …)`. Sorting is by filename, then line, then column, so output (and goldens) are deterministic regardless of recovery order.

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

## 7. Rendering (rich · compact · JSON), `--json`, exit codes

Three renderings live in `internal/diag`, all driven by `[]Diagnostic` (positions
already resolved), so every command (`generate`, `fmt`, `vet`, `lsp`) reuses them.
Positions are 1-based `line:col` for all human/JSON output; the LSP layer converts
to 0-based/UTF-16 separately.

```go
// SourceProvider yields a file's bytes for snippet rendering. The CLI reads disk;
// the future LSP supplies the in-memory (possibly unsaved) buffer. nil → no snippet.
type SourceProvider func(filename string) ([]byte, bool)

func RenderRich(w io.Writer, diags []Diagnostic, src SourceProvider)
func RenderCompact(w io.Writer, diags []Diagnostic)
func RenderJSON(w io.Writer, diags []Diagnostic) error
```

- **Rich (default for an interactive `gsx generate`)** — rustc/Go-flavoured, human-first:
  ```
  error[reserved-param]: param name "ctx" is reserved (ambient context)
    --> views.gsx:3:13
     |
   3 | component X(ctx string) {
     |             ^^^ reserved name
     |
     = help: rename the parameter; `ctx` is the ambient context
  ```
  Header is `severity[code]: message` (code omitted when `""`); `-->` locates the
  primary position; the source line (via `SourceProvider`) carries a caret
  underline spanning `Start..End`; `Help` renders as the `= help:` line. With no
  `SourceProvider` (or the file is unreadable) it degrades gracefully to the
  compact line. Diagnostics print in `Bag.Sorted()` order.

- **Compact (goldens, CI, pipes, `--quiet`)** — one deterministic line per
  diagnostic: `file:line:col: severity[code]: message` (a minimal evolution of
  today's `line:col: message`). No source snippet → stable and grep-friendly.
  This is the form the corpus `diagnostics.golden` asserts (§9).

- **JSON (`--json`, agent-first)** — array of:
  ```json
  {"file":"views.gsx","range":{"start":{"line":3,"col":13},"end":{"line":3,"col":16}},
   "severity":"error","code":"reserved-param","message":"…","help":"rename the parameter; …","source":"codegen"}
  ```
  1-based line/col; severity as a lowercase string; `help` omitted when empty.

**Selection:** `gsx generate` defaults to **rich** on a TTY and **compact** when
stderr is not a terminal (so CI logs and pipes stay one-line and stable),
overridable by `--json`. The current `gsx: <dir>: <err>` double-prefix is
**dropped** — the diagnostic carries the file path; a leading `gsx:` program
prefix remains only for non-diagnostic operational errors (e.g. "cannot read dir").

- **Exit code:** `gsx generate` exits non-zero iff the `Bag` `HasErrors()`. Warnings/info alone do not fail the build.

## 8. CLI wiring

`gsx generate` parses a `--json` flag (small `flag.FlagSet`, like other subcommands). The generate path (`gen/cache.go`, `gen/main.go`) collects the per-package `Bag`s, merges them, and hands the diagnostics to the `internal/diag` renderer (text or JSON per the flag) on `stdout`/`stderr`. The current `res.Errs`/`errors.Join` + `gsx: %v` sink is replaced by the diagnostic renderer; non-diagnostic operational failures (I/O, bad args) keep the plain `gsx:`-prefixed path.

## 9. Corpus impact + rebaseline strategy

To keep churn proportionate, the 190-case `diagnostics.golden` corpus keeps its
existing **`line:col: message`** projection (one line per diagnostic, in
`Bag.Sorted()` order) — a small harness formatter over the diagnostic list, *not*
the rich/compact CLI renderers. Severity is always `Error` in Slice 1 (no value
pinning it per case) and `Code`/`Help`/`range`-end are pinned separately (below),
so the big corpus changes minimally:

- **Codegen `diagnostics.golden`:** gain a `line:col:` prefix (per the
  `codegen-diagnostic-position-audit.md` node mapping) and may list **multiple**
  lines (recovery). Rebaselined via the runner's `--update`, reviewed in the diff.
- **jsx `diagnostics.golden`:** `"at N"` becomes a real `line:col`; **delete**
  `normalizeDiag` and its regex.
- **Parser `diagnostics.golden`:** unchanged (already `line:col: message`); a few
  positionless cases gain a position.
- **Structured-shape goldens (new, small):** 2–3 dedicated cases assert the **JSON**
  renderer output — pinning `range.start`/`range.end`, `severity`, `code`, `help`,
  `source` — so the rich model is tested without reformatting every case.
- **Multi-error fixture (new):** a file with ≥2 independent codegen/type errors,
  proving exhaustive reporting (semantic recovery).
- `diagnostics.golden` stays **always-enforced** (empty = expect none), so a
  regression that drops or reorders diagnostics fails loudly.
- **Deferred test refinement:** the R3 rustc-style inline `//~ ERROR <substr>`
  annotation harness remains a possible later convenience; not adopted here.

## 10. Testing strategy

- **`internal/diag` unit:** `Bag.Add` (pre-resolved), `Bag.Errorf` (resolves AST `token.Pos` via the bag's fset into `token.Position`), `HasErrors`, `Sorted` (stable filename→line→column ordering); severity enum. Renderers (driven by `[]Diagnostic`, tested with hand-built `token.Position` values — no fset needed): **compact** one-line form; **JSON** shape (1-based, lowercase severity, `range.start`/`range.end`, `code`/`help` omitted-when-empty); **rich** snippet with the caret spanning `Start..End` and the `= help:` line, plus the **graceful degradation** to compact when the `SourceProvider` returns nothing.
- **Semantic recovery:** a package with multiple `go/types` errors reports all of them; a file with multiple codegen errors reports all, each positioned; a failed component does not suppress a sibling component's diagnostics; a package with any error writes no `.x.go` while a clean sibling package still writes.
- **Migration fidelity:** existing single-error codegen/jsx cases produce the same message text, now positioned; parser cases unchanged.
- **Corpus:** the rebaselined goldens pass; `normalizeDiag` removed; `--json` golden round-trips.
- **Exit codes:** `gsx generate` returns non-zero with errors, zero when clean.

## 11. LSP-readiness checklist (why these choices)

- Range (`Start..End` as `token.Position`) → LSP `Diagnostic.range`. ✓
- `Severity` enum (4 levels) → LSP `DiagnosticSeverity`. ✓
- `Code` → LSP `Diagnostic.code` (filtering, future code actions). ✓
- `Source` → LSP `Diagnostic.source`. ✓
- `Help` → LSP diagnostic message detail / `relatedInformation`. ✓
- `[]Diagnostic` + collector → LSP "all diagnostics for a document". ✓ (semantic now; parser in Slice 2)
- Resolved `token.Position` (fileset-agnostic) + `SourceProvider` → LSP layer derives 0-based/UTF-16 from the (possibly unsaved) buffer line. ✓
- Structured suggested-fix edits → LSP code actions / `textEdit`. ✗ deferred (§2 Out) — `Help` free text for now.
