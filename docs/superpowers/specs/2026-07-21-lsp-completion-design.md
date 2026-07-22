# LSP completion (textDocument/completion) design

Date: 2026-07-21
Status: approved design, pre-implementation
Prior work: `2026-07-19-lsp-authored-source-intelligence-design.md` (reserved the
`sourceintel.Completion` capability bit and named the three problems this design
solves: cursor-context classification, cursor-time invalid syntax, candidate
enumeration/ranking/edits).

## Goal

Full-surface completion for `.gsx` files, served by the gsx LSP from its own
warm analysis core:

- Go expression positions: in-scope identifiers and members after `.`
- Pipe stages after `|>`: filter names
- Component tags and component attributes
- HTML tags, attributes, and enumerated attribute values

## Approach rejected: proxying gopls (templ's architecture)

templ's LSP spawns gopls and forwards completion against the generated Go file,
remapping positions both ways. We explicitly do not take this approach. Observed
consequences in templ:

- Completion is nil anywhere the sourcemap has no entry — exactly the
  half-typed positions where completion matters most (templ #1102).
- Inverse range remapping produces out-of-bounds edits (templ #816).
- Auto-import edits target the generated file and must be heuristically
  re-synthesized against the DSL source — a persistent bug tail (#775, #879).
- Coupling to unversioned gopls output formats breaks across gopls releases
  (#830).

gsx is in the opposite position: the warm module core already type-checks every
package in memory (`.x.go`-free), holds `types.Info`/`types.Package` per
package, and computes all edits directly in authored coordinates. Own
completion, no proxy, no reverse remapping of client-visible ranges.

## Decisions (settled during brainstorming)

1. **Scope: full surface in v1** — Go expressions, pipes, component tags,
   component attrs, HTML tags/attrs/values. Each domain has an independent
   candidate source; they phase cleanly inside one design.
2. **Freshness: synchronous fresh analysis** per completion request (gopls's
   model), with a deterministic repair step for the broken token at the
   cursor. No stale-snapshot fast path in v1; one may be added later if proven
   equivalent (correct general behaviour first, fast paths as measured
   optimisations).
3. **Auto-import completion: deferred.** V1 completes what is in scope and
   already imported. The existing missing-import quickfix covers the gap. A
   follow-up design owns unimported-symbol completion (export-data latency,
   import-edit synthesis).
4. **Ranking: full candidate set, client filters.** Return every candidate
   valid in the context with `isIncomplete: false`; `sortText` encodes
   locality/kind tiers; the client's fuzzy matcher does the filtering. No
   server-side matcher in v1.
5. **HTML data: vendor `@vscode/web-custom-data`** (`browsers.html-data.json`,
   MIT) — the dataset behind VS Code's own HTML completion — converted to a Go
   table at `go:generate` time.

## Empirical foundation (probed against the warm core)

| Mid-edit state | `Module.Package()` behaviour today |
|---|---|
| `{ user.Na }` (half-typed member) | Full result; `Info.Types` resolves `user`; type errors are non-fatal |
| `{ user. }` (trailing dot) | Diagnostics-only shell: skeleton go/parse error is fatal; go/parser's recovered `SelectorExpr{X: user, Sel: "_"}` is discarded (`module_importer.go` skeleton-parse gate) |
| `<Ca` / `<div cl` (half-typed tag/attr) | Shell: gsx parse error; parser recovery is destructive (enclosing component collapses); one broken file drops the whole dir's facts |
| `{{ x := }}` | Accidentally fine (scanner absorbs next skeleton line); full Info |
| `package pag` (mid-typing clause) | Hard `(nil, err)` — package-clause mismatch is a plain error |

Consequences baked into the design:

- `user.Na` already yields everything member completion needs, so repair only
  has to convert `user.` into that shape (phantom identifier).
- Tag/attr contexts must be classified and repaired *before* the gsx parse,
  because the broken parse is unusable.
- `Info.Selections` is never allocated by `checkSkeletonPackage`; member
  enumeration uses `types.NewMethodSet` + an embedded-field walk instead.
- **Bug found, fixed as part of this work:** `ComponentDecls` empties on any
  type error in the package (syntactic facts gated behind type success). Tag
  completion — and existing tag hover/definition — must survive type errors;
  the harvest gate moves to syntax-level.
- Incidental (not fixed here, noted): `{{ x := }}` leaks the internal helper
  name in a user-facing diagnostic (`_gsxuse(...) (no value) used as value`).

## Architecture

### Placement and flow

- `internal/lsp/completion.go`: `case "textDocument/completion"` in the
  `handle` switch; `handleCompletion` mirrors `handleHover` (reject `.go`
  files, fetch live buffer, `byteOffsetForPosition` with the negotiated
  encoding, then the completion pipeline).
- Capability: `CompletionProvider` on `serverCapabilities` with trigger
  characters `.`, `<`, `|`, `"`. No `resolveProvider` in v1.
- `Analyzer` interface grows `AnalyzeEphemeral(dir, path string, content
  []byte) (*lsp.Package, error)`, implemented by `lspAnalyzer` over a new
  `Module` entry point.
- Enumeration and item construction live in `internal/lsp` (protocol-shaped
  work, same bridge arithmetic as hover); `internal/codegen` supplies the
  fresh analysis result.

Request flow:

1. Stage-1 lexical repair scan chooses a deterministic repair (or none).
2. Parse the repaired buffer with the gsx parser; classify the cursor context
   from the AST.
3. gsx-native contexts (pipes, tags, attrs, HTML) enumerate from warm facts —
   no typecheck needed.
4. Go contexts run the synchronous ephemeral analysis and enumerate from
   `types` at the bridged skeleton position.
5. Items return as a full set, `sortText`-ordered, with `TextEdit`s computed
   against the original buffer.

### Threading and concurrency

The handler runs on the Run goroutine like every request. The ephemeral
analysis acquires `analysisMu` (serialized with `Package()`/`Generate()`,
non-reentrant — the documented Module contract). A completion arriving while a
debounced background analysis is in flight waits for it; that is the
serialization contract working as designed. If measurement shows it matters,
the fix is a scheduling change later, not a v1 workaround.

## Cursor-context classification (two-stage)

Chicken-and-egg: precise classification wants a parsed AST; a half-typed tag
destroys the parse. Resolution:

### Stage 1 — lexical repair scan

A scanner-based pass around the cursor (using the existing consolidated
scanner machinery — tokenization, not a parallel grammar) chooses from a
closed set of deterministic repairs applied to an ephemeral copy of the
buffer:

- cursor immediately after `.` in Go text → insert a phantom identifier
- cursor inside an unclosed open tag (`<Ca`, `<div cl`) → insert `/>` at cursor
- cursor after `|>` with no stage name → insert a phantom filter name
- otherwise → no repair (identifier prefixes like `user.Na` analyze as-is)

The scan only decides whether to patch bytes at the cursor; it never chooses
candidates. All repairs insert *at* the cursor, so buffer bytes before the
cursor — and every offset the client sees — are unchanged.

### Stage 2 — AST classification on the repaired parse

Locate the innermost node containing the cursor via `inspectWithEmbedded`;
map node → context using existing position fields:

| Cursor in | Context → candidate class |
|---|---|
| `Interp.Expr`, `ExprAttr`, `SpreadAttr`, `ClassPart.Expr`, `OrderedPair` value, value-form conds/arms, control-flow clauses, `GoBlock.Code`, `GoChunk`, `@{}` holes in js/css/f-literals | Go expression → skeleton bridge |
| `PipeStage.NamePos` region after `\|>` | Pipe filter → warm filter table |
| Tag-name region after `<` (`Element.TagPos`) | Tag → components + HTML tags (merged; lowercase tags can be components per the lowercase-tag resolution rule; components sort first when the prefix is capitalized) |
| Open tag, attribute-name position | Attr name → component params or per-tag + global HTML attrs |
| Inside a `StaticAttr` string value | Attr value → enumerated values from vendored data (expr values are Go contexts) |
| Component signature/params region | Go type position → skeleton bridge via `SigTypes` |
| Markup text; js/css literal bodies outside `@{}` holes | No completion in v1 |
| Import path strings | No completion in v1 (pairs with deferred auto-import) |

Top-level `GoChunk`s are Go contexts — authored Go, emitted verbatim, already
stamped with the `Completion` capability. Classification never consults the
retained (possibly stale) `Package`; it works on the live buffer's own parse.

## Go-context completion

### Ephemeral analysis

`(*Module).AnalyzeEphemeral(dir, path, src)` (name may be refined in the
plan), under `analysisMu`:

- Same pipeline as `Package()`, with the repaired buffer layered as a
  read-time overlay **on top of** the regular override map. Persistent
  overrides, dirty tracking, and the per-dir cache are untouched: the stale
  cached result stays valid for hover/definition; no invalidation, no
  reverse-dependant re-analysis.
- Only the cursor's package re-typechecks; dependency packages and the
  external importer stay warm. Result adapted like `adaptPackageResult`, used
  once, dropped.
- Fset growth from ephemeral analyses is bounded by the existing
  `maybeRebuildFset` mechanism.

### Bridging and enumeration

The fresh result's `ExprMap`/`CtrlMap`/`SigTypes` are built from the repaired
buffer, byte-identical to the live buffer up to the cursor — so the cursor
bridges by the existing relative-offset recipe; with a phantom, the bridged
position is the phantom identifier.

Two shapes:

1. **Identifier position** (no receiver): walk the scope chain outward from
   `types.Scope.Innermost` at the bridged position — locals → params → file
   scope (imported package names) → package scope → universe. Plus Go keywords
   in statement contexts (`{{ }}`, `GoChunk`) at low priority. No deep
   (`a.b.c`) completion in v1.
2. **Member position** (after `.`): the bridged `SelectorExpr`'s `X` has its
   type in `Info.Types` (probe-verified for the phantom case). `X` a
   `*types.PkgName` → exported names of that package scope. Otherwise →
   `types.NewMethodSet(T)` ∪ `NewMethodSet(*T)` plus an embedded-field BFS
   with promotion (the standard field-accessibility algorithm). Unexported
   members only for same-package types.

### Ordering

`sortText` encodes locality tiers: locals < params < receiver members by
embedding depth < file/package scope < imported-package members < universe /
keywords. No expected-type inference in v1 (listed follow-up); the client's
fuzzy matcher handles filtering.

### Edits

`TextEdit` replacing `[tokenStart, cursor)` in the original buffer, token
start found by scanning identifier bytes backward from the cursor. All ranges
and text are authored-buffer coordinates; the phantom never leaks.

### Fail-soft ladder

- Repaired analysis still yields a diagnostics-only shell (breakage elsewhere
  in the package) → fall back to gsx-native candidates if the context has
  them, else empty list.
- Hard `(nil, err)` (package-clause mismatch) → empty list.
- Never an error response to the client for source-state reasons.

## gsx-native candidate sources

### Pipe filters

From the warm filter tables the Module already holds (the data behind
`ResolveFilters` / `gsx info`), surfaced as a `Filters` field on
`lsp.Package` at adapt time. Item: label = template name, detail =
`pkg.Func` signature, shadowed std filters marked. All filters offered at
every stage in v1 (typed subject-compatibility filtering is a follow-up;
existing type diagnostics catch mismatches). Renderers are type-keyed, not
name-invoked — absent from pipe completion.

### Component tags

- Current package: `ComponentDecls` (includes receiver components).
- Imported gsx packages: exported `ComponentDecls` from their warm dep
  results; after bare `<`, imported gsx-package qualifiers are offered so
  `<ui.` is reachable; after `<pkg.`, that package's components.
- Merged with the HTML tag list (both always offered; components first when
  the prefix is capitalized).

**Includes the `ComponentDecls`-survive-type-errors fix** (syntactic facts
must not be gated on type success) — required so tag completion works
mid-edit, and it improves existing tag hover/definition resilience.

### Attribute names

- **Component tag:** the repaired parse (`<Card ` → `<Card/>`) produces a
  real planned call, so `ComponentCallFact.Params` gives the exact param set;
  already-present attrs excluded. Fallback: the component's `ComponentDecls`
  param list when call planning didn't happen.
- **HTML element:** per-tag + global attributes from the vendored table;
  boolean attrs annotated via `gsx.IsBooleanAttr` (bare-name insertion);
  `hx-*` attributes offered when the module's htmx preset is enabled.
- Present attributes excluded in all cases.

### Attribute values

Enumerated values from the vendored data (`type="submit"`, `rel="noopener"`).
No dynamic values in v1.

### HTML data pipeline

Vendor `browsers.html-data.json` from `@vscode/web-custom-data` (MIT, license
preserved) under `internal/htmldata/`; a `go:generate` converter emits a Go
table: tags → attributes → enumerated values, with the dataset's markdown
documentation and MDN links. Completion items carry that documentation.
Upstream sync = drop in new JSON, regenerate.

## Protocol details

- `CompletionOptions{TriggerCharacters: [".", "<", "|", "\""]}`.
- Response always `CompletionList{isIncomplete: false, items}`.
- Plain `TextEdit`s only — no snippet syntax in v1 (no `snippetSupport`
  dependency). Attribute insertion is `name=""` for value-taking attrs, bare
  `name` for booleans. Snippet placeholders (cursor-inside-quotes) are a
  follow-up gated on client capability.

Item mapping:

| Candidate | kind | detail | documentation |
|---|---|---|---|
| Go var/param/const/func/type/field/method | matching LSP kinds | type via `qualifierFor(pkg)` | — (Go doc comments deferred; skeleton doesn't retain them) |
| Imported package name | Module | import path | — |
| Keyword | Keyword | — | — |
| Filter | Function | `pkg.Func` signature | shadow note |
| Component | Function | receiver + param list | — |
| Component param | Field | param type | — |
| HTML tag / attr / value | Property / Enum kinds | — | vendored markdown + MDN link |

`filterText`/`insertText` set wherever label ≠ inserted text.

## Testing

- **`internal/lsp` unit tests:** repair-chooser table tests (buffer + cursor →
  chosen repair); AST classification over synthetic parses; enumeration and
  `sortText` ordering against hand-built `*Package` fixtures (the
  `definition_test.go` pattern); edit ranges including UTF-16 multibyte
  before the cursor.
- **`gen/` e2e tests** (`lsp_completion_e2e_test.go`, real `lspAnalyzer`):
  member completion on a struct; trailing-dot phantom repair;
  package-qualifier members; half-typed component tag; pipe-stage names;
  component attr names with present-attr exclusion; HTML tag/attr/value;
  fail-soft with unrelated breakage elsewhere in the package;
  package-clause-mismatch → empty; cross-package component tags.
- **Codegen test** for the `ComponentDecls`-survive-type-errors fix.
- **Benchmark:** `AnalyzeEphemeral` warm latency on a realistic package —
  measured before any tuning.

## Docs

`docs/guide/editor.md` and `docs/guide/status.md` updated; ROADMAP completion
item ticked with a pointer here.

## Follow-ups (recorded, not in v1)

- Unimported-symbol completion with auto-import edits (own design; export-data
  latency + import-edit synthesis in `.gsx` coordinates).
- Expected-type ranking; server-side fuzzy scoring if client filtering proves
  insufficient.
- Snippet placeholders gated on `snippetSupport`.
- Typed pipe-filter compatibility filtering.
- `completionItem/resolve` for lazy documentation.
- Stale-snapshot fast path, only if benchmarked latency demands it and proven
  equivalent to the fresh-analysis path.
- Go doc comments on Go candidates (requires comment retention through the
  skeleton).
- `_gsxuse` name leak in the `{{ x := }}` diagnostic (unrelated to
  completion; surfaced by the probe).
