# gsx — Unused-Import Removal on Format

## 1. Goal & non-goals

**Goal.** When formatting a `.gsx` file, drop imports (declared in a pass-through
Go chunk) that the file does not use — both through the language server's
`textDocument/formatting` and through `gsx fmt`. The authoritative signal is the
Go type-checker, not a syntactic scan.

**Non-goals.**
- **Not** `goimports`. We only *remove* unused imports. We do not add missing
  imports, sort/group import blocks, or rewrite import grouping.
- No new import organization, no alias rewriting.
- `.go` files are untouched (gopls/goimports own those).

## 2. Background — what already exists

- `gsx fmt` and the LSP `textDocument/formatting` share one engine,
  `internal/gsxfmt.Format(name, src) ([]byte, error)` = parse → `wsnorm.Normalize`
  → `printer.Fprint`. It is purely syntactic (no module load).
- The codegen analysis type-checks an in-memory skeleton per `.gsx` file.
  `internal/codegen`'s `importSpec` already carries each hoisted import's
  `name`, `path`, `srcOff` (offset within the chunk) and `pos` (the resolved
  `.gsx` `token.Pos`, set in `buildSkeleton`). The skeleton emits a `//line`
  ahead of each hoisted import, so go/types' "imported and not used" error
  resolves to the import's `.gsx` line (`import_diag_test.go`).
- The gsx printer formats every `GoChunk` through `format.Source` (gofmt), so a
  chunk whose text we rewrite is re-gofmt'd on print — empty `import ()` blocks
  and stray whitespace clean up for free.

So **detection and source-mapping already exist**. This slice adds (a) exposing
the unused set as structured data, (b) the AST edit that removes them, and (c)
wiring both formatting surfaces to it.

## 3. Architecture & data flow

```
codegen analysis ──► PackageResult.UnusedImports  (per .gsx file: []UnusedImport{Name,Path})
        │
        ├──► gen/lsp.go maps to ──► lsp.Package.UnusedImports
        │                                   │
        │                                   └──► LSP handleFormatting: gsxfmt.FormatRemovingImports(src, unused)
        │
        └──► gen `gsx fmt` (default): analyze dir, look up file's unused set,
                                       gsxfmt.FormatRemovingImports(src, unused)
```

`internal/gsxfmt` stays analysis-free: the caller computes the unused set and
passes it in. The removal is one new exported function plus an AST transform.

### 3.1 Unit boundaries

- **`internal/codegen` — detection.** After type-checking each file's skeleton,
  build the unused set for that file: an `importSpec` is unused iff a type error
  resolves (via the import's `//line`) to that spec's mapped position — exact
  position correlation, no error-message string matching. The skeleton already
  emits one `//line` per hoisted import, so each unused-import error lands on its
  import's own `.gsx` line. Expose it as
  `PackageResult.UnusedImports map[string][]UnusedImport` keyed by the `.gsx`
  file path, where `UnusedImport{Name, Path string}` is codegen's own type (the
  internal `importSpec.pos` drives detection but is not exposed). codegen does
  **not** import `gsxfmt` — `gen` converts. **Reliability gate:** populate the
  set only when the file is safe to edit — no parse error, and no type errors
  *other than* the unused-import errors themselves. Otherwise leave it empty
  (format-only).
- **`internal/gsxfmt` — the edit.** Defines `ImportRef{Name, Path string}` (the
  edit's input type, which `lsp` references too) and a new
  `FormatRemovingImports(name string, src []byte, unused []ImportRef) ([]byte, error)`:
  parse → remove the named imports from `GoChunk`s → `wsnorm` → print. The
  removal walks the parsed file's `GoChunk` nodes; for each unused `(name,
  path)`, parse the chunk's Go (`package _gsxp\n`+Src), call
  `astutil.DeleteNamedImport(fset, file, name, path)`, reprint via
  `format.Node`, strip the synthetic package clause, and write back `GoChunk.Src`.
  `Format` (no removal) stays for the syntactic path.
- **`internal/lsp` — transport.** `lsp.Package` gains
  `UnusedImports map[string][]gsxfmt.ImportRef`, populated by `gen/lsp.go`
  (converting `codegen.UnusedImport`), the same way it already converts
  `CrossRef`/`NavRef`. `lsp` already imports `gsxfmt` (the formatting handler).
- **`gen` — CLI + LSP wiring.** `gsx fmt` analyzes the target's package(s),
  converts each file's `codegen.UnusedImport`s to `gsxfmt.ImportRef`, and calls
  `gsxfmt.FormatRemovingImports`. The LSP formatting handler reads
  `pkg.UnusedImports[path]` and does the same.

## 4. The removal mechanism

For each `GoChunk` containing one or more unused imports:

1. Parse `"package _gsxp\n" + chunk.Src` with `go/parser` (ParseComments).
2. For each unused `(name, path)`: `astutil.DeleteNamedImport(fset, f, name, path)`
   (handles empty-block removal and comment association; `name==""` for a
   default import).
3. Reprint with `format.Node`, drop the leading `package _gsxp` line, trim, and
   assign back to `chunk.Src`.
4. The outer `printer.Fprint` re-gofmt's the chunk on output (idempotent).

**Why astutil, not byte-span deletion.** Deleting the raw source span of each
import line is the fragile heuristic to avoid: it mishandles `import (…)` blocks
(trailing commas, the empty block left behind), aliased specs, and comment
association. `astutil.DeleteNamedImport` is the canonical, correct primitive and
is already an available dependency (`golang.org/x/tools`).

**Blank/dot imports.** `go/types` never reports `_ "x"` or `. "x"` as unused, so
they never enter the unused set and are always preserved — no special-casing.

## 5. Safety & graceful degradation

`gsx fmt` removes imports **by default**, which means it loads the module. It
must never regress below today's behavior:

- **Module won't load** (no `go.mod`, broken deps, dir not in a module): the
  analysis returns an error/empty result → fall back to `gsxfmt.Format`
  (syntactic, no removal). Formatting still succeeds.
- **Package analyzes but has unrelated errors:** the per-file reliability gate
  (§3.1) yields an empty unused set for that file → format-only. This prevents
  dropping an import whose only use sits in code that failed to parse/type-check
  (that code may be absent from the skeleton, falsely marking the import unused).
- **Idempotent:** running format twice is a no-op on the second run.
- **`-no-imports` flag:** skips analysis entirely for the fast syntactic path
  (useful in a broken module, or to match gofmt exactly).

The LSP mirrors this: it removes only when `pkg.UnusedImports[path]` is present
(analysis ran and the file passed the gate); otherwise the existing syntactic
format. The analyzed package is already in `s.pkgs[dir]` from didOpen/didChange,
so no extra load.

## 6. CLI surface

- `gsx fmt [paths]` — format **and** remove unused imports (loads the module).
  `-l`, `-w`, `-d` semantics unchanged; the diff/-l "needs formatting" signal
  now also fires when an unused import would be removed.
- `gsx fmt -no-imports [paths]` — syntactic format only (today's behavior), no
  module load.
- Per-file failure isolation is unchanged: a file whose package can't be
  analyzed is formatted syntactically; other files proceed.

## 7. Testing

- **`internal/gsxfmt` (transform, unit):** single `import "x"` removed; one of
  several specs in an `import (…)` block removed (block survives, gofmt-clean);
  all specs removed (empty block gone); aliased import removed by `(alias,
  path)`; blank/dot imports preserved; a used import kept; idempotency
  (`FormatRemovingImports` twice).
- **`internal/codegen` (detection, unit):** a `.gsx` with two unused imports →
  `UnusedImports` lists both with correct `.gsx` `Pos`; a used import is absent;
  the reliability gate — a file with an unrelated type error yields an **empty**
  unused set (no removal) even though the unused-import error is present.
- **`gen` CLI e2e:** `gsx fmt -w` on a file with an unused import rewrites it
  without that import; `-no-imports` leaves it; a file outside a module is
  formatted (syntactically) and the unused import is **kept** (graceful
  fallback), exit code still success.
- **`gen` LSP e2e:** `textDocument/formatting` on an open `.gsx` with an unused
  import returns an edit whose text drops the import; when the package can't be
  analyzed, formatting still returns the syntactic result (import kept).

## 8. Risks

- **Falsely flagging a used import as unused** when its only reference is in code
  that failed to parse/type-check. Mitigated by the per-file reliability gate
  (§3.1): no removal unless the only errors are the unused-import errors.
- **Chunk reprint drift** — `format.Node` output for a chunk differing from the
  printer's `format.Source`. Both are gofmt; the outer print re-gofmt's, so the
  final output is stable. Idempotency tests guard this.
- **Performance** — `gsx fmt` now loads the module by default. Acceptable: it is
  the same load codegen already does; `-no-imports` is the escape hatch.

## 9. What ships

Formatting a `.gsx` — on save in the editor, or via `gsx fmt` — removes imports
the file no longer uses, using the type-checker as the source of truth, while
never removing under uncertainty and never failing where plain formatting would
have succeeded.
