# Import handling: gofmt / goimports modes in `gsx fmt` + LSP `source.organizeImports`

## Problem

`gsx fmt` today **removes** unused imports but never merges, dedups, groups, or
sorts across declarations, and the removal opt-out (`-no-imports`) is CLI-only
with no config equivalent. Imports in a `.gsx` file live verbatim inside
`ast.GoChunk` spans; the parser peels a leading run of `import` declarations
(single-line **and** grouped) into one chunk. When a file accumulates separate
import declarations, `gsx fmt` leaves them as-is — even with an exact duplicate:

```go
import "github.com/tespkg/one-learning/db"

import (
	"fmt"
	"strings"

	"github.com/gsxhq/gsx"
	"github.com/tespkg/one-learning/db"
)
```

## Design: two modes, mirroring gopls

gopls does not expose a pile of independent import knobs; it offers **gofmt**
(format only) and **goimports** (organize). `gsx fmt` adopts the same model — a
single mode selector:

- **`goimports`** (default) — remove unused imports **and** reorder: merge all
  import declarations into one block, dedup identical specs, group std vs.
  everything-else (blank-line separated), sort each group. Normalizes the example
  above to a single grouped, deduped block, exactly what `goimports` produces:

  ```go
  import (
  	"fmt"
  	"strings"

  	"github.com/gsxhq/gsx"
  	"github.com/tespkg/one-learning/db"
  )
  ```

- **`gofmt`** — gofmt only: sort within existing parenthesized groups; **no**
  removal, **no** merge/dedup, **no** std/third-party split. Duplicates and
  separate `import` declarations are left exactly as gofmt leaves them.

This deliberately drops the "remove unused but don't regroup" intermediate — a
combination gopls doesn't offer either (gofmt never removes; goimports always
groups), so its absence is faithful, not a gap. Default stays `goimports`, so the
existing unused-removal behavior is preserved and the grouping is added on top.

`goimports` uses `goimports` *default* grouping — two groups, no `-local` prefix,
no per-project configuration.

## Reuse: the real gofmt and goimports libraries — no port

Both modes are the upstream implementations, called as libraries. Nothing is
cloned, vendored, or reimplemented. Verified empirically (see below):

- **gofmt** = stdlib `go/format.Source`. It sorts imports within a single group
  but does not merge separate declarations, dedup, or do std/third-party
  grouping. The gsx printer **already** runs `go/format.Source` on every chunk
  (`internal/printer` `fmtGoChunk`). So `gofmt` mode needs **no new formatting
  code** — it simply skips the organize passes and relies on the printer's
  existing gofmt.
- **goimports** = `golang.org/x/tools/imports.Process` with
  `Options{FormatOnly: true}`. Already available (the tooling side imports
  `golang.org/x/tools/go/ast/astutil`). `FormatOnly: true` is essential: it does
  merge/dedup/group/sort and **skips** goimports' usage-based add/remove logic.
  That logic must be skipped because a gsx chunk body does not reference the
  template's imports — default goimports would wrongly strip every import as
  "unused" (and cannot *add* imports for the same reason: gsx can't resolve a
  symbol to its package from a chunk body). It needs no `go/packages` load and no
  type-check — it classifies std-ness by import path — so it is cheap and pure.

Using upstream directly keeps both behaviors correct-by-construction as the
libraries evolve, in the same spirit as the `html/template` escaping rule, taken
one step further (the actual code, not a port).

### Empirical confirmation

`go/format.Source` on a duplicate spread across a single-line + grouped decl:
leaves both declarations and the duplicate untouched (only sorts within the
group). `imports.Process(FormatOnly, Comments, TabIndent, TabWidth: 8)` on the
same input: merges the two declarations, dedups `.../one-learning/db`, and emits
the std/third-party grouping shown above. The `TabIndent: true, TabWidth: 8,
Comments: true` options are required so `FormatOnly` matches gofmt's tabbed
chunk formatting (otherwise it emits spaces).

### Interaction with recent main work (verified at HEAD f6fbd8c)

Checked because PR #51/#52 churned adjacent code. All clear:

- **`d813a1e` "tokenize for the `_gsx` reservation instead of parsing"** does
  *not* constrain this design. Its rule targets **element-bearing** Go regions,
  which codegen used to reconstruct by substituting a padded `_()` placeholder
  and parsing — and fmt's new paren-wrap (`return ( <>…</> )`) made those
  reconstructions invalid Go under automatic semicolon insertion. A `GoChunk` is
  by definition **element-free, complete top-level Go**; the shipped
  `deleteChunkImports` already parses one (wrapped in `package _gsxp`) on every
  `gsx fmt`, and `d813a1e` touched only `internal/codegen`, never
  `internal/gsxfmt`. So `reorderImports` may parse its chunk exactly as
  `deleteChunkImports` does.
- **Imports never live in a `GoWithElements`.** `parser/goexpr.go
  splitGoElements` peels a leading import run into its own plain `GoChunk`
  before building the element region. A per-`GoChunk` walk therefore misses no
  import, and skipping `GoWithElements` (as `removeImports` already does via its
  type switch) is correct.
- **`435a91c` / PR #51 / `internal/goexprshape`** all operate solely on
  element-literal positions inside `GoWithElements` (paren wrap/strip,
  placeholder gofmt round-trip). Disjoint decl types — they cannot fight the
  reorder pass. `435a91c` changed `canonGo` in `corpus_test.go`, a test
  normalizer, not the printer.
- **`fmtGoChunk` still runs `format.Source` on every chunk** (`printer.go:1250`),
  so "gofmt mode = zero new formatting code" holds, as does reorder idempotency
  (gofmt sorts within a group but never merges or regroups, so the goimports
  std/third-party split survives the printer's re-gofmt).
- **WASM cost is negligible.** `gsxfmt` is in the `playground/wasm` dep graph
  (it exposes `gsxFormat`), but linking `golang.org/x/tools/imports` adds only 4
  packages and **+1,811 bytes** to a 17.3 MB `gsx.wasm` (0.01%) — `go/packages`
  machinery is already linked via `gen`. `golang.org/x/tools v0.46.0` is already
  a module requirement, so no new dependency.

## Pipeline ordering (goimports mode)

Within one `gsx fmt` invocation: **remove-unused → reorder → normalize/print.**
Removing unused first means a duplicate that was also unused is gone before
reordering; reorder then merges/dedups/groups whatever remains. In `gofmt` mode
both passes are skipped and only normalize/print (which gofmt's each chunk) runs.

## Implementation

### Reorder pass — `internal/gsxfmt/imports.go`

Add `reorderImports(f *gsxast.File)`, run only in `goimports` mode:

- Walk `f.Decls`; for each `*gsxast.GoChunk`, skip cheaply unless its source
  contains an import declaration (avoid reformatting import-less chunks —
  typically only the peeled leading-imports chunk qualifies).
- For a qualifying chunk: prepend the synthetic `package _gsxp\n` (as
  `deleteChunkImports` already does), run `imports.Process("chunk.go", src,
  &imports.Options{FormatOnly: true, Comments: true, TabIndent: true,
  TabWidth: 8})`, strip the synthetic package line, `TrimSpace`, assign back to
  `gc.Src`.
- **Fallback:** a chunk that is not standalone-valid Go (parse error) is left
  untouched — same tolerance as `deleteChunkImports`.

The "contains an import declaration" gate is decided by the parsed AST — any
`GenDecl` with `Tok == token.IMPORT` — never a substring match on `import`
(which could hit the word inside a string or comment). Reuse the parsed file for
the process step where the byte-in/byte-out contract allows.

`gofmt` mode adds nothing here — `removeImports` and `reorderImports` are both
skipped, and the printer's existing `go/format.Source` per chunk is the gofmt
behavior.

### gsxfmt API

Keep gsxfmt mechanical and mode-agnostic; the "mode" vocabulary lives at the
config/CLI layer. Fold the growing param list into an options struct:

```go
type FormatOptions struct {
	Unused  []ImportRef // imports to remove; empty = remove nothing
	Width   int
	CSSFmt  rawfmt.Formatter
	JSFmt   rawfmt.Formatter
	Reorder bool         // run reorderImports (goimports mode)
}

func FormatWith(name string, src []byte, opts FormatOptions) ([]byte, error)
```

`goimports` mode → caller computes `Unused` and sets `Reorder: true`.
`gofmt` mode → `Unused` empty, `Reorder: false`. Keep `Format`,
`FormatRemovingImports`, `FormatRemovingImportsWith` as thin wrappers delegating
to `FormatWith`, so no caller breaks.

### Mode resolution — one helper

A single shared mapping so CLI and LSP don't duplicate it:

```go
type importsMode int
const (
	importsGoimports importsMode = iota // default
	importsGofmt
)
```

`goimports` → {compute unused, reorder=true}; `gofmt` → {no unused, reorder=false}.

### Config — `gen/configfile.go` + `gen/main.go`

`tomlFormatter` gains a string key (flat, matching gopls's flat vocabulary):

```go
type tomlFormatter struct {
	PrintWidth int    `toml:"print_width"`
	Imports    string `toml:"imports"` // "goimports" (default) | "gofmt"
}
```

Parse/validate the string into `importsMode` (reject anything other than the two
spellings, naming the path — like `parseMinifyLevel`). Empty/absent = default
`goimports`. `config` stores the resolved mode; `mergeConfig` carries it like
`printWidth`. Add `effectiveImportsMode() importsMode` (default `goimports`).
Like the rest of `[formatter]`, it never touches generated output and stays out
of `computeKey`.

```toml
[formatter]
imports = "goimports"   # "goimports" (default) | "gofmt"
```

### CLI — `gen/fmt.go`

- `-imports gofmt|goimports` — string flag, overrides config for the run
  (invalid value → error naming the two spellings).
- `-no-imports` — kept as the nice-to-have alias for `-imports gofmt`. It maps
  exactly: today `-no-imports` already means "don't remove, just gofmt the
  chunk". If both `-imports` and `-no-imports` are given, `-imports` wins (and a
  contradictory pair like `-imports goimports -no-imports` is an error).
- Resolve the effective mode: CLI flag if set, else config, else `goimports`.
  Only call `analyzeUnusedImports` in `goimports` mode. Pass the resolved
  `Unused` (empty in gofmt mode) and `Reorder` into `gsxfmt.FormatWith`.

### LSP formatting — `internal/lsp/format.go`

`textDocument/formatting` **honors the configured mode**, mirroring the CLI. No
CLI flag exists in the editor, so the mode comes from resolved config only: add
an `ImportsMode(dir)` accessor next to the existing `PrintWidth(dir)` — declared
on the `Analyzer` interface in `internal/lsp/server.go` and implemented on
`lspAnalyzer` in `gen/lsp.go` (that is where `PrintWidth` actually lives, *not*
`internal/lsp/analysis.go`). Map it through the shared mode→options helper, and
pass `Unused` (from analysis, only in goimports mode) and `Reorder` into
`FormatWith`. So in
`gofmt` mode, format-on-save is gofmt only — it stops removing/reordering
imports; the organize behavior then comes exclusively from the code action
below. This is precisely the gopls split: `textDocument/formatting` = gofmt,
import organizing = a separate action.

**Pre-existing gap, preserve don't fix:** `handleFormatting` today calls
`FormatRemovingImports` — the *non*-`With` variant — so LSP formatting does not
run the `<style>`/`<script>` css/js formatters that the CLI runs. Moving it to
`FormatWith` makes that gap explicit (`CSSFmt`/`JSFmt` become visible nil
fields). Keep current behavior: pass nil formatters, preserving today's LSP
output byte-for-byte. Closing the gap is a separate change with its own tests —
do not fold it into this effort.

### LSP `source.organizeImports` code action — `internal/lsp/codeaction.go` (new)

Add a `textDocument/codeAction` handler that offers `source.organizeImports`,
the gopls-standard action editors trigger from the lightbulb menu or
`editor.codeActionsOnSave`. This is what lets a user keep `imports = "gofmt"` for
format-on-save yet still organize imports via the action — the real gopls
workflow.

- **Capabilities:** advertise `codeActionProvider` as `CodeActionOptions{
  CodeActionKinds: ["source.organizeImports"]}` in `serverCapabilities`
  (a struct, not the bare `true`, so the client knows which source-action kinds
  we support and can wire `codeActionsOnSave`).
- **Dispatch:** `case "textDocument/codeAction"` in `server.go` → `handleCodeAction`.
- **Filter:** honor the request's `context.only` — return the action only when
  `only` is empty or contains `source.organizeImports` (or the `source` prefix).
  Non-`.gsx` files return an empty list (gopls owns `.go`).
- **Behavior:** the action **always** applies the goimports transformation
  (remove unused + reorder), independent of the configured formatter mode —
  organizing is the action's entire purpose, exactly as `source.organizeImports`
  means goimports in gopls even when formatting is plain gofmt. It computes the
  edit via `FormatWith` with `Unused` (from analysis) and `Reorder: true`.
- **Edit scope — whole document.** gsx has no partial/region formatter; its
  canonical form is produced by a whole-document parse → print. So the action
  returns a single whole-document `TextEdit` (the goimports-organized canonical
  document), wrapped in a `WorkspaceEdit` on the returned `CodeAction`. This is a
  minor, deliberate deviation from gopls's import-region-only edits: applying the
  action also yields canonical gsx formatting for the rest of the document.
  Returned inline on the `CodeAction.edit` (no `resolveProvider` round-trip).
- **No-op suppression:** when the organized document equals the current buffer,
  return an empty list (no action offered / no-op on save). On a parse failure
  (mid-edit buffer) likewise return an empty list — never a destructive edit,
  matching `handleFormatting`.

## Precedence

CLI (`-imports` / `-no-imports`) → `gsx.toml` `[formatter] imports` → default
`goimports`. Consistent with the project's option > env > config layering,
scoped to what a formatter mode needs.

## Testing

- **Corpus** (`internal/corpus/testdata/cases/**`): cases pinning `input.gsx` +
  `generated.x.go.golden` + `render.golden` for goimports mode —
  (a) merge of a single-line + grouped import declaration,
  (b) dedup of an exact duplicate across declarations,
  (c) std vs. third-party group split with blank-line separation.
  Regenerate with `-update`, then verify without.
- **gsxfmt unit tests** (`internal/gsxfmt/imports_test.go`): the reorder
  transformation directly, idempotency (format twice = stable, incl. the
  printer's re-gofmt not undoing goimports grouping), parse-error fallback
  (malformed chunk left untouched), and interaction with removal
  (unused-and-duplicate resolves cleanly).
- **Config** (`gen/configfile_test.go`): `imports = "gofmt"` and
  `imports = "goimports"` each resolve; an invalid value errors naming the two
  spellings; an unknown key is still rejected by strict decoding; absent key
  defaults to `goimports`.
- **CLI** (`gen/fmt_test.go`): `-imports gofmt` leaves a duplicate/separate decls
  untouched and keeps unused imports; default (goimports) merges/dedups/groups
  and removes unused; `-no-imports` behaves as `-imports gofmt`; contradictory
  `-imports goimports -no-imports` errors.
- **LSP formatting** (`internal/lsp/format_test.go` or existing): `gofmt` mode
  formats without removing/reordering imports; `goimports` mode does both.
- **LSP code action** (`internal/lsp/codeaction_test.go`): `textDocument/codeAction`
  with `only: ["source.organizeImports"]` on a doc with a duplicate + unused
  import returns a `source.organizeImports` action whose edit is the organized
  document — **regardless** of the configured mode (assert it organizes even
  under `imports = "gofmt"`); returns empty when already organized; returns empty
  for a non-`.gsx` file and for a mid-edit parse failure; honors `context.only`
  (no action when `only` excludes source/organizeImports). Capabilities test
  (`server_lifecycle_test.go`) asserts `codeActionProvider` advertises
  `source.organizeImports`.

## Out of scope / non-goals

- **No add** — gsx can't resolve symbols to packages from a chunk body;
  `FormatOnly: true`.
- **No `-local` grouping** — goimports default two-group split only. Could be a
  later `[formatter]` knob if a project wants its module split.
- **No gofumpt** — the two modes gopls-style; gofumpt could be a later mode.
- **No cross-chunk merging** — reorder is per-`GoChunk`.
- **No syntax change** — imports stay verbatim `GoChunk` text. No
  tree-sitter / vscode / CodeMirror grammar work. The generated `.x.go` import
  region (`emit.go writeImports`) is unaffected.

## Files touched

- `internal/gsxfmt/imports.go` — `reorderImports`, chunk-level reorder.
- `internal/gsxfmt/gsxfmt.go` — `FormatOptions` / `FormatWith`, wire wrappers.
- `gen/fmt.go` — `-imports` flag, `-no-imports` alias, per-dir mode resolution.
- `gen/configfile.go` — `Imports` key, parse/validate into mode.
- `gen/main.go` — `config` mode state + `effectiveImportsMode`, shared
  mode→options helper.
- `internal/lsp/format.go` — formatting honors config mode (`Unused`/`Reorder`).
- `internal/lsp/codeaction.go` (new) — `handleCodeAction` for
  `source.organizeImports` (always goimports transform, whole-doc edit).
- `internal/lsp/protocol.go` — `CodeActionOptions`, `codeActionProvider`
  capability, `textDocument/codeAction` param/result types.
- `internal/lsp/server.go` — dispatch `textDocument/codeAction`; advertise the
  capability; declare `ImportsMode(dir)` on the `Analyzer` interface.
- `gen/lsp.go` — implement `lspAnalyzer.ImportsMode(dir)` (next to
  `PrintWidth`, via `resolveConfigBestEffort(...).effectiveImportsMode()`).
- Corpus cases + unit/config/CLI/LSP tests as above.
- Docs: `[formatter] imports` config reference + `gsx fmt` page (in
  `gsxhq.github.io`); note editor `codeActionsOnSave: source.organizeImports`
  usage in the LSP/editor docs.

### Sibling repos

- `vscode-gsx` — the extension can document/enable
  `"editor.codeActionsOnSave": { "source.organizeImports": true }` for `.gsx`.
  No grammar change (no syntax change). Verify the extension forwards
  `textDocument/codeAction` to the server (default LSP client behavior).
