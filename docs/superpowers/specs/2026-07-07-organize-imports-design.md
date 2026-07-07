# Organize imports in `gsx fmt`

## Problem

`gsx fmt` today **removes** unused imports but never merges, dedups, groups, or
sorts them, and the removal opt-out (`-no-imports`) is CLI-only with no config
equivalent. Imports in a `.gsx` file live verbatim inside `ast.GoChunk` spans,
and the parser peels a leading run of `import` declarations (single-line **and**
grouped) into one chunk. When a file accumulates separate import declarations,
`gsx fmt` leaves them as-is — even when they contain an exact duplicate:

```go
import "github.com/tespkg/one-learning/db"

import (
	"fmt"
	"strings"

	"github.com/gsxhq/gsx"
	"github.com/tespkg/one-learning/db"
)
```

`gofmt` leaves this untouched (it sorts *within* a single parenthesized group
but never merges separate import declarations or dedups across them). The result
is duplicated and unmerged imports that a `goimports` run would clean up.

## Concept: "organize imports"

Reframe import handling under one umbrella, **organize imports**, with two
independent sub-behaviors — both default **on**, both configurable in
`gsx.toml` and on the CLI:

- **remove_unused** — drop imports the file declares but never uses. This is the
  existing pass; it needs module analysis (skeleton, no type-check) and silently
  no-ops when the module can't be loaded.
- **reorder** — merge all import declarations into one block, dedup identical
  specs, group std vs. everything-else (blank-line separated), sort each group.
  Purely syntactic; needs no analysis.

Note on scope: goimports also *adds* missing imports. **gsx cannot** — adding
requires resolving a symbol in the code to its providing package, and a gsx
chunk body does not reference the template's imports (the same reason `reorder`
must use `FormatOnly`). So organize = remove + reorder only, never add.

### Desired result

The example above normalizes (both sub-behaviors on) to a single grouped,
deduped, std/third-party block — exactly what `goimports` produces:

```go
import (
	"fmt"
	"strings"

	"github.com/gsxhq/gsx"
	"github.com/tespkg/one-learning/db"
)
```

`reorder` uses `goimports` *default* grouping — two groups, no `-local` prefix,
no per-project configuration.

## Approach (reorder)

`golang.org/x/tools/imports.Process` with `Options{FormatOnly: true}` does the
reorder work (merge / dedup / group / sort) and nothing else. `FormatOnly: true`
is essential: it skips goimports' usage-based add/remove logic. That logic must
be skipped because a gsx chunk body does not reference the template's imports —
default goimports would wrongly strip every import as "unused". Unused-import
removal stays in its own pass (`internal/codegen` module analysis feeding
`removeImports`).

`FormatOnly` needs no type-checking and no `go/packages` load, so it is cheap and
purely syntactic — safe to run on every `gsx fmt` invocation.

Verified against the example above: `imports.Process` with
`{FormatOnly: true, Comments: true, TabIndent: true, TabWidth: 8}` merged the two
declarations, deduped `.../one-learning/db`, and produced the std/third-party
grouping shown above.

### Why reorder is safe under the existing pipeline

- **Idempotent.** `imports.Process(FormatOnly)` is stable on its own output, and
  the gsx printer re-`gofmt`s each chunk on the way out; `gofmt` preserves
  blank-line-separated import groups and the within-group sort, so a second
  `gsx fmt` is a no-op.
- **Per-chunk only.** Reorder runs inside each `GoChunk`; it never relocates
  imports across chunks. Cross-chunk relocation would move Go text across
  positioned spans (e.g. an import chunk hoisted away from an embedded-element
  region) and is out of scope. In the common case the parser already peels *all*
  leading imports — single and grouped — into one chunk, so the example is one
  chunk and merges naturally.

## Pipeline ordering

Within one `gsx fmt` invocation: **remove-unused → reorder → normalize/print.**
Removing unused first means a duplicate that was also unused is gone before
reordering; reorder then merges/dedups/groups whatever remains.

## Design

### Reorder pass — `internal/gsxfmt/imports.go`

Add `reorderImports(f *gsxast.File)`:

- Walk `f.Decls`; for each `*gsxast.GoChunk`, skip cheaply unless its source
  contains an import declaration (avoid reformatting import-less chunks —
  typically only the peeled leading-imports chunk qualifies).
- For a qualifying chunk: prepend the synthetic `package _gsxp\n` (as
  `deleteChunkImports` already does), run `imports.Process("chunk.go", src,
  &imports.Options{FormatOnly: true, Comments: true, TabIndent: true,
  TabWidth: 8})`, strip the synthetic package line, `TrimSpace`, assign back to
  `gc.Src`.
- **Fallback:** a chunk that is not standalone-valid Go (parse error from
  `imports.Process`) is left untouched — same tolerance as `deleteChunkImports`.

The "contains an import declaration" gate is decided by the parsed AST — any
`GenDecl` with `Tok == token.IMPORT` — never a substring match on `import` (which
could hit the word inside a string or comment). Reuse the parsed file for the
process step where the byte-in/byte-out contract allows.

### gsxfmt API

The `FormatRemovingImportsWith` parameter list is already long
(path/orig/unused/width/cssFmt/jsFmt). Introduce a small options struct to carry
the format knobs, adding `Reorder bool`:

```go
type FormatOptions struct {
	Unused   []ImportRef        // imports to remove; empty = remove nothing
	Width    int
	CSSFmt   rawfmt.Formatter
	JSFmt    rawfmt.Formatter
	Reorder  bool               // run reorderImports
}

func FormatWith(name string, src []byte, opts FormatOptions) ([]byte, error)
```

`remove_unused` is expressed entirely through `Unused`: when removal is off, the
caller passes an empty slice (and skips the analysis that computes it). `Reorder`
gates the new pass. Keep the existing exported functions (`Format`,
`FormatRemovingImports`, `FormatRemovingImportsWith`) as thin wrappers delegating
to `FormatWith`, so no caller breaks.

### Config — `gen/configfile.go` + `gen/main.go`

`tomlFormatter` gains a nested table so `organize_imports` literally contains
both sub-behaviors:

```go
type tomlFormatter struct {
	PrintWidth      int                  `toml:"print_width"`
	OrganizeImports *tomlOrganizeImports `toml:"organize_imports"`
}

type tomlOrganizeImports struct {
	RemoveUnused *bool `toml:"remove_unused"`
	Reorder      *bool `toml:"reorder"`
}
```

Both `*bool` so *absent* = default-on; only an explicit `= false` opts out.
Strict decoding (`Undecoded`) still rejects typos. Like the rest of
`[formatter]`, this never touches generated output and stays out of `computeKey`.

`config` gains state following the `printWidth` "zero means unset → default at
use" idiom, inverted so the zero value is the default-on case: store
`disableRemoveUnusedImports bool` and `disableReorderImports bool` (both false =
default = on), set from `*tc.Formatter.OrganizeImports.RemoveUnused == false`
etc. `mergeConfig` carries them like `printWidth`. Add
`effectiveRemoveUnusedImports() bool` and `effectiveReorderImports() bool`
returning the negation.

`gsx.toml`:

```toml
[formatter.organize_imports]
remove_unused = true   # unused-import removal (needs module analysis)
reorder       = true   # merge / dedup / group / sort (goimports FormatOnly)
```

### CLI — `gen/fmt.go`

Two opt-out flags, mapping 1:1 to the config sub-toggles:

- `-no-imports` (existing, kept) → force `remove_unused` off for the run.
- `-no-reorder-imports` (new) → force `reorder` off for the run.

Resolve each effective value per directory: config default, then the CLI flag
forces it off if present (opt-out only; config is where project-wide policy
lives):

```
removeUnused := organizeCfg(dir).removeUnused && !noImportsFlag
reorder      := organizeCfg(dir).reorder      && !noReorderFlag
```

Only call `analyzeUnusedImports` when `removeUnused` is true (skipping it is the
current `-no-imports` behavior). Pass `Unused` (empty when removal off) and
`Reorder` into `gsxfmt.FormatWith`.

### LSP — `internal/lsp/format.go`

Editor "format document" has no CLI, so it reads the resolved config only: pass
`Unused` from analysis only when `effectiveRemoveUnusedImports()`, and
`Reorder: merged.effectiveReorderImports()`, into `FormatWith`.

## Precedence

Per sub-behavior: CLI opt-out flag → `gsx.toml` config → default **on**.
Consistent with the project's option > env > config layering, scoped to what a
formatter opt-out needs.

## Testing

- **Corpus** (`internal/corpus/testdata/cases/**`): new cases pinning
  `input.gsx` + `generated.x.go.golden` + `render.golden` for
  (a) merge of a single-line + grouped import declaration,
  (b) dedup of an exact duplicate across declarations,
  (c) std vs. third-party group split with blank-line separation.
  Regenerate with `-update`, then verify without.
- **gsxfmt unit tests** (`internal/gsxfmt/imports_test.go`): the reorder
  transformation directly, idempotency (format twice = stable), parse-error
  fallback (malformed chunk left untouched), and interaction with removal
  (unused-and-duplicate resolves cleanly).
- **Config** (`gen/configfile_test.go`): `[formatter.organize_imports]` with
  `remove_unused = false` / `reorder = false` each flips the matching
  `effective…()`; an unknown key is still rejected by strict decoding; absent
  table leaves both defaults on.
- **CLI** (`gen/fmt_test.go`): `-no-reorder-imports` leaves imports unreordered
  but still removes unused; `-no-imports` still reorders but keeps unused;
  both flags together leave imports fully untouched; default run does both.

## Out of scope / non-goals

- **No add** — gsx can't resolve symbols to packages from a chunk body;
  `FormatOnly: true`.
- **No `-local` grouping** — goimports default two-group split only. Could be a
  later `[formatter.organize_imports]` knob if a project wants its module split.
- **No cross-chunk merging** — reorder is per-`GoChunk`.
- **No syntax change** — imports stay verbatim `GoChunk` text. No
  tree-sitter / vscode / CodeMirror grammar work. The generated `.x.go` import
  region (`emit.go writeImports`) is unaffected.

## Files touched

- `internal/gsxfmt/imports.go` — `reorderImports`, chunk-level reorder.
- `internal/gsxfmt/gsxfmt.go` — `FormatOptions` / `FormatWith`, wire wrappers.
- `gen/fmt.go` — `-no-reorder-imports` flag, per-dir organize resolution, plumb.
- `gen/configfile.go` — `tomlOrganizeImports`, decode into config.
- `gen/main.go` — `config` toggles + `effectiveRemoveUnusedImports` /
  `effectiveReorderImports`.
- `internal/lsp/format.go` — pass `Unused`/`Reorder` from resolved config.
- Corpus cases + unit/config/CLI tests as above.
- Docs: `[formatter.organize_imports]` config reference + `gsx fmt` page (in
  `gsxhq.github.io`).
