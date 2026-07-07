# Organize imports in `gsx fmt`

## Problem

`gsx fmt` today **removes** unused imports but never merges, dedups, groups, or
sorts them. Imports in a `.gsx` file live verbatim inside `ast.GoChunk` spans,
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
is duplicated and unmerged imports that a `goimports` run would clean up. We want
`gsx fmt` to do the same normalization.

## Desired behavior

`gsx fmt` should normalize the imports of the example above to a single grouped,
deduped, std/third-party-grouped block — exactly what `goimports` produces:

```go
import (
	"fmt"
	"strings"

	"github.com/gsxhq/gsx"
	"github.com/tespkg/one-learning/db"
)
```

Specifically, per Go chunk:

1. **Merge** all top-level `import` declarations into one block.
2. **Dedup** identical `(alias, path)` specs. (Same path, different alias = two
   distinct imports, both kept.)
3. **Group** into standard-library vs. everything-else, blank-line separated.
4. **Sort** within each group.

This is `goimports` *default* grouping — two groups, no `-local` prefix, no
per-project configuration.

## Approach

`golang.org/x/tools/imports.Process` with `Options{FormatOnly: true}` does
exactly steps 1–4 and nothing else. `FormatOnly: true` is essential: it skips
goimports' usage-based add/remove logic. That logic must be skipped because a
gsx chunk body does not reference the template's imports — default goimports
would wrongly strip every import as "unused". Unused-import removal stays where
it is today (`internal/codegen` module analysis feeding `removeImports`).

`FormatOnly` needs no type-checking and no `go/packages` load, so it is cheap and
purely syntactic — safe to run on every `gsx fmt` invocation.

Verified against the example above: `imports.Process` with
`{FormatOnly: true, Comments: true, TabIndent: true, TabWidth: 8}` merged the two
declarations, deduped `.../one-learning/db`, and produced the std/third-party
grouping shown under *Desired behavior*.

### Why this is safe under the existing pipeline

- **Idempotent.** `imports.Process(FormatOnly)` is stable on its own output, and
  the gsx printer re-`gofmt`s each chunk on the way out; `gofmt` preserves
  blank-line-separated import groups and the within-group sort, so a second
  `gsx fmt` is a no-op.
- **Per-chunk only.** Organizing runs inside each `GoChunk`; it never relocates
  imports across chunks. Cross-chunk relocation would move Go text across
  positioned spans (e.g. an import chunk hoisted away from an embedded-element
  region) and is out of scope. In the common case the parser already peels *all*
  leading imports — single and grouped — into one chunk, so the example above is
  one chunk and merges naturally.

## Design

### Organize pass — `internal/gsxfmt/imports.go`

Add `organizeImports(f *gsxast.File)`:

- Walk `f.Decls`; for each `*gsxast.GoChunk`, skip cheaply unless its source
  contains an import declaration (avoid reformatting import-less chunks —
  typically only the peeled leading-imports chunk qualifies).
- For a qualifying chunk: prepend the synthetic `package _gsxp\n` (as
  `deleteChunkImports` already does), run `imports.Process("chunk.go", src,
  &imports.Options{FormatOnly: true, Comments: true, TabIndent: true,
  TabWidth: 8})`, strip the synthetic package line, `TrimSpace`, and assign back
  to `gc.Src`.
- **Fallback:** a chunk that is not standalone-valid Go (parse error from
  `imports.Process`) is left untouched — same tolerance as `deleteChunkImports`.

The cheap "contains an import declaration" gate: parse the chunk once (synthetic
package) and check for any `GenDecl` with `Tok == token.IMPORT`; reuse that
parsed file for the process step rather than parsing twice where practical. (If
reuse complicates the `imports.Process` byte-in/byte-out contract, a
`strings`-level pre-check for a top-level `import` token is acceptable — but the
authoritative decision is the parsed AST, never a substring heuristic that could
match `import` inside a string/comment.)

Ordering in the format pipeline: **remove-unused → organize → normalize/print.**
Removing unused first means a duplicate that was also unused is gone before
organizing; organizing then merges/dedups/groups whatever remains.

### gsxfmt API

The `FormatRemovingImportsWith` parameter list is already long
(path/orig/unused/width/cssFmt/jsFmt). Introduce a small options struct to carry
the format knobs, adding `Organize bool`:

```go
type FormatOptions struct {
	Unused    []ImportRef
	Width     int
	CSSFmt    rawfmt.Formatter
	JSFmt     rawfmt.Formatter
	Organize  bool
}

func FormatWith(name string, src []byte, opts FormatOptions) ([]byte, error)
```

Keep the existing exported functions (`Format`, `FormatRemovingImports`,
`FormatRemovingImportsWith`) as thin wrappers so no caller breaks; internally
they delegate to `FormatWith`. `organizeImports(f)` runs when `opts.Organize` is
true.

### CLI — `gen/fmt.go`

- Add `-no-organize-imports` (bool, default false) to the `gsx fmt` flag set.
- Resolve the effective organize setting per directory from config (mirroring
  `printWidthFor`): a new `organizeImportsFor(dir) bool`.
- Effective value: `organize := organizeImportsFor(dir) && !noOrganizeImports`.
  The CLI flag is opt-out only (forces off); config is the place to set
  project-wide policy.
- Pass `Organize: organize` through to `gsxfmt.FormatWith`.

### Config — `gen/configfile.go` + `gen/main.go`

- `tomlFormatter` gains `OrganizeImports *bool `toml:"organize_imports"``. A
  pointer so *absent* = default-on; only an explicit `organize_imports = false`
  opts out. Strict decoding (`Undecoded`) still rejects typos.
- `config` gains `organizeImports` state. Follow the `printWidth` "zero means
  unset → default at use" idiom, inverted so the zero value is the default-on
  case: store `disableOrganizeImports bool` (false = default = organize on), and
  set it from `*tc.Formatter.OrganizeImports == false`. `mergeConfig` carries it
  like `printWidth`.
- `effectiveOrganizeImports() bool` returns `!c.disableOrganizeImports`.

### LSP — `internal/lsp/format.go`

Editor "format document" calls `gsxfmt` and should organize by default too. It
has no CLI, so it reads the resolved config only: pass
`Organize: merged.effectiveOrganizeImports()` into `FormatWith` alongside the
already-resolved unused set and print width.

## Precedence

`-no-organize-imports` (CLI) → `organize_imports` (config) → default **on**.

Consistent with the layering the project already uses (option > env > config),
scoped to what a formatter opt-out needs: a per-run CLI override on top of a
project-wide config default.

## Testing

- **Corpus** (`internal/corpus/testdata/cases/**`): new cases pinning
  `input.gsx` + `generated.x.go.golden` + `render.golden` for
  (a) merge of a single-line + grouped import declaration,
  (b) dedup of an exact duplicate across declarations,
  (c) std vs. third-party group split with blank-line separation.
  Regenerate with `-update`, then verify without.
- **gsxfmt unit tests** (`internal/gsxfmt/imports_test.go`): the organize
  transformation directly, idempotency (format twice = stable), parse-error
  fallback (malformed chunk left untouched), and interaction with unused-removal
  (unused-and-duplicate resolves cleanly).
- **Config** (`gen/configfile_test.go`): `organize_imports = false` decodes and
  flips `effectiveOrganizeImports()`; an unknown key is still rejected by strict
  decoding; absent table leaves the default on.
- **CLI** (`gen/fmt_test.go`): `-no-organize-imports` leaves imports unorganized;
  default run organizes; `-no-organize-imports` + `-no-imports` together leave
  imports fully untouched.

## Out of scope / non-goals

- **No `-local` grouping** — goimports default two-group split only. Could be a
  later `[formatter]` knob if a project wants its own module split out.
- **No cross-chunk merging** — organizing is per-`GoChunk`.
- **No usage-based add/remove** — `FormatOnly: true`; unused removal stays in the
  existing module-analysis path.
- **No syntax change** — imports are still verbatim `GoChunk` text. No
  tree-sitter / vscode / CodeMirror grammar work. The generated `.x.go` import
  region (`emit.go writeImports`) is unaffected.

## Files touched

- `internal/gsxfmt/imports.go` — `organizeImports`, chunk-level organize.
- `internal/gsxfmt/gsxfmt.go` — `FormatOptions` / `FormatWith`, wire wrappers.
- `gen/fmt.go` — `-no-organize-imports` flag, `organizeImportsFor`, plumb.
- `gen/configfile.go` — `OrganizeImports *bool`, decode into config.
- `gen/main.go` — `config.disableOrganizeImports`, `effectiveOrganizeImports`.
- `internal/lsp/format.go` — pass `Organize` from resolved config.
- Corpus cases + unit/config/CLI tests as above.
- Docs: `[formatter]` config reference + `gsx fmt` page (in `gsxhq.github.io`).
