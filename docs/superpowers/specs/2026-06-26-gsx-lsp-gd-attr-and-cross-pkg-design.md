# gsx LSP — go-to-definition on attribute names + cross-package component tags

**Status:** approved design (brainstormed 2026-06-26), ready for an implementation plan.

## 1. Goal & non-goals

**Goal.** Close the go-to-definition (`gd`) gaps a real structpages template
(`examples/blog/blog/post.gsx`) exposes, where `gd` currently returns null:

- **A** — `gd` on a component-invocation **attribute name** → the matching
  **component parameter** (e.g. `comments` in `<CommentsList comments={…}/>` →
  the `comments` param of `component CommentsList(comments []store.Comment)`).
- **B** — `gd` on a **dotted / cross-package component tag** (`<components.Input>`,
  `<layout.PublicShell>`) → that component's declaration in the *other* package.
- **C** — `gd` on an attribute name of a **cross-package** component → its param.

Delivered in two phases: **Phase 1 = A** (same-package, self-contained);
**Phase 2 = B + C** (the cross-package resolver, then C falls out of it).

**Already resolved — not in scope (gap "D").** `gd` on pipeline filters
(`{ … |> url }`, `|> id`, `|> target`) in the blog example was the fourth gap
considered. It is **already fixed** by the merged "LSP reads `gsx.toml`
in-process" slice: structpages ships a repo-root `gsx.toml` declaring
`[filters] url/id/target`, and the LSP's `discoverConfig` walks up from the
analyzed dir to the `.git` root (across the `examples/blog` module boundary) and
finds it. Verified: a freshly built `gsx info` in `examples/blog/blog` reports
`config: …/structpages/gsx.toml` and resolves `url → structpages.URLFor` (+ctx),
`id → structpages.ID`, `target → structpages.IDTarget`. The user's editor was
running a pre-merge `~/bin/gsx`; a reinstall fixes it. No code in this slice.

**Non-goals.**
- **Custom `FieldMatcher` attr→param mapping.** Phase 1/2 use the default
  exported-field rule (`firstUpper(attr) == firstUpper(param)`). A code
  `WithFieldMatcher` is code-only and out of scope (the same accepted gap as the
  config slice).
- **Reliance on generated `.x.go`.** The LSP resolves symbols from its
  **in-memory** skeleton, never the on-disk `.x.go`, so it is robust to a
  missing/stale `.x.go`. The cross-package resolver (B) preserves this: it parses
  the imported package's `.gsx` **source** in memory, never its `.x.go`. (The
  `.x.go` `//line` directives map body statements, not `func` decl lines, to
  `.gsx` — so they cannot locate a component decl anyway.)
- **Hover parity** for these cases — a follow-up; this slice is `gd` only.

## 2. Background — the current `gd` dispatcher

`handleDefinition` (`internal/lsp/definition.go:92`) dispatches, in order:
1. `.go` file → `handleGoDefinition`.
2. **D2:** `componentTagDeclAt` — cursor on a *simple* component tag name
   (`<Card>`) → component decl. It explicitly **excludes dotted tags**
   (`definition.go:266`: `strings.Contains(tag, ".")`) and lowercase-initial tags.
3. `exprNodeAtOffset` — the gsx `Interp`/`ExprAttr` whose Go-expr span (or pipe
   stage span) covers the cursor. **Returns nil for a cursor on an attribute
   name** (an attr name is not inside `{…}`), so anything on an attr name falls
   straight to null.
4. pipe stages → `pipedTarget`; else map into the skeleton expr → `innermostIdent`
   → `Info.Uses/Defs` → real source position (refuses `.x.go` positions).

So an attribute name (A/C) hits no case, and a dotted tag (B) is excluded at D2.

**Available data** (`internal/lsp/analysis.go` `Package`): `Files map[string]*gsxast.File`
(in-memory parsed gsx AST for the analyzed package), `CrossIndex`, `Fset`,
`GSXFset`, `Info`. The gsx `ast` types: `Element{Tag, Attrs []Attr}`;
`Component{Name, NamePos, Params string, ParamsPos token.Pos}` — **params are a
raw string** (`"comments []store.Comment"`) with one `ParamsPos` (start of list),
no per-param positions. Attr nodes (`ExprAttr`, `StaticAttr`, `BoolAttr`,
`MarkupAttr`, `JSAttr`) embed `span` (so `attr.Pos()` is the attr-name's first
char) and carry `Name string` — **no dedicated attr-name position**, but the
name span is `[attr.Pos(), attr.Pos()+len(Name))`.

## 3. Phase 1 — A: attribute name → same-package component param

All in `internal/lsp` (new `definition_attr.go` + a dispatch line in
`definition.go`). No parser/`ast` change; no `internal/codegen` import.

**3.1 Dispatch.** In `handleDefinition`, after the D2 `componentTagDeclAt` check
and before `exprNodeAtOffset`, add:

```go
if dp, ok := componentAttrParamAt(pkg, path, off); ok {
    return s.reply(f.ID, s.locationForPos(dp))
}
```

**3.2 `componentAttrParamAt(pkg *Package, path string, off int) (token.Position, bool)`:**
1. Walk `pkg.Files[path]` for every `*gsxast.Element`. For each, if the tag is a
   **same-package function component** (`tag != ""`, no `.`, initial `A–Z`),
   scan its `Attrs` for a named attr whose name span covers `off`:
   `attr.Pos()` ≤ cursorPos < `attr.Pos()+len(name)` (using `GSXFset` offsets).
   Named attrs: `ExprAttr`, `StaticAttr`, `BoolAttr`, `MarkupAttr`, `JSAttr`
   (a `SpreadAttr` has no name — skip). Record `(tag, attrName)` on a hit.
2. Find the component decl: scan `pkg.Files` (all files in the package) for a
   `*gsxast.Component` whose `Name == tag` (and no receiver — a plain function
   component). Not found → `false`.
3. `paramPos, ok := paramPosFor(comp, attrName)` (§3.3). Not found → `false`.
4. Return `pkg.GSXFset.Position(paramPos), true`.

**3.3 `paramPosFor(comp *gsxast.Component, attr string) (token.Pos, bool)` —
locate the param by parsing the raw `Params` string with `go/parser`:**
- If `comp.Params == ""` or `!comp.ParamsPos.IsValid()` → `false`.
- Parse `"package p\nfunc _(" + comp.Params + "){}"` with `go/parser` into a
  `*ast.FuncDecl`; walk `decl.Type.Params.List` and, for each field, each
  `*ast.Ident` name. The synthetic-source byte offset of a param name is
  `fset.Position(name.Pos()).Offset`; subtract the synthetic offset of the first
  param (the byte just after `func _(`) to get the param name's **offset within
  `comp.Params`**.
- Match the attr to a param by the default exported-field rule:
  `firstUpper(paramName) == firstUpper(attr)` (so `comments`↔`comments`,
  `title`↔`title`). First match wins.
- Return `comp.ParamsPos + token.Pos(offsetWithinParams)`, `true`.
- A parse failure (malformed `Params`) → `false` (fall through to null; never
  panic). `go/parser` is stdlib — allowed in `internal/lsp`.

**Why parse, not reuse codegen:** `internal/lsp` must not import
`internal/codegen`, and the gsx parser stores params as an opaque string. A
localized `go/parser` parse of that string is the robust, real way to get a
param's position — and it correctly handles grouped params (`a, b string`).

**Result for the reported case:** `gd` on `comments` (post.gsx:53) →
`components.gsx:30`, the `comments` token in
`component CommentsList(comments []store.Comment)`.

## 4. Phase 2 — B: cross-package component tag → declaration

The cross-package resolver. **In-memory, `.x.go`-independent**: it parses the
imported package's `.gsx` source for the component decl position — it does not
read or depend on any generated `.x.go`.

**4.1 The mechanism.** On a cursor over a dotted component tag
`<components.Input …/>` (qualifier `components`, name `Input`):
1. **Qualifier → import path.** Read the analyzed `.gsx` file's import block
   (`pkg.Files[path]` carries the file's Go imports) and map the alias/last path
   segment `components` → its import path (`…/examples/blog/ui/components`).
2. **Import path → package directory.** The analyzer already loads the package
   with `packages.NeedDeps|NeedImports|NeedFiles` (`batch.go:258`); the dep
   package's directory is derivable from its files. Phase 2 threads an
   `importDirs map[importPath]dir` (or equivalent resolver) onto the LSP
   `Package`, populated from the go/packages load — so the LSP can map an import
   path to its on-disk directory without a fresh load.
3. **Directory → component decl.** Glob `*.gsx` in that directory, parse each
   with the gsx parser (in memory), and find the `*gsxast.Component` whose
   `Name == "Input"` → its `NamePos`. Cache the parsed per-directory decl index
   (name → `NamePos`) keyed by dir so repeated `gd`s don't re-parse.
4. Return `GSXFset`/dep-fset `Position(NamePos)` for `Input`.

**4.2 Dispatch.** Extend the D2 path: when the tag contains a `.`, route to a new
`crossPkgTagDeclAt(pkg, path, off)` instead of bailing at
`definition.go:266`. The simple-tag path is unchanged.

**Open design point for the plan to resolve:** whether to (a) parse the dep dir's
`.gsx` lazily on demand (simplest, cache per dir) or (b) have the analyzer emit a
dep component index during the main load. The spec mandates the *in-memory gsx
source* path and `.x.go`-independence; the plan picks (a) unless the load already
carries the dep syntax cheaply.

## 5. Phase 2 — C: cross-package attribute name → param

C = B's decl resolution + Phase 1's `paramPosFor`. When the cursor is on an
attribute name of a **dotted** component tag: resolve the cross-package
`*gsxast.Component` (B's §4.1 steps 1–3, returning the AST node, not just
`NamePos`), then `paramPosFor(comp, attrName)` (§3.3) against that node's
`Params`/`ParamsPos` — the position lands in the *other* package's `.gsx`.
`componentAttrParamAt` (§3.2) generalizes: for a dotted tag, resolve the decl
cross-package; otherwise same-package.

## 6. Architecture invariants

- **`.x.go`-independent, in-memory.** Every resolution (A param parse, B dep
  decl parse) works from parsed `.gsx` source held in memory; none reads a
  generated `.x.go`. A project that has never run `gsx generate`, or whose
  `.x.go` is stale, still navigates correctly.
- **`internal/lsp` ⊄ `internal/codegen`.** All new code lives in `internal/lsp`
  (and the analyzer's `gen`/`batch` for the dep-dir map, which is in
  `internal/codegen` but only adds data to the existing `Package`/result — no new
  lsp→codegen import).
- **Best-effort, never panics.** Any parse failure, missing decl, or
  unresolvable import → `false` → null `gd`, never a crash.
- **Cross-package navigation requires the dependency to be importable.**
  Phase 1 (same-package attr) works even if the package has never been
  generated — the resolver reads only in-memory `.gsx` AST. Phase 2
  (cross-package B/C) requires the *dependency* to have been generated
  (`.x.go` on disk) so the Go type-checker can import it; the declaration
  position returned still comes from the dependency's `.gsx` source, not its
  `.x.go`. Truly generation-free cross-package navigation would require the
  analyzer to overlay dependency `.gsx` skeletons as a virtual package — a
  future enhancement.

## 7. Testing (per [[gsx-syntax-change-test-coverage]])

`internal/lsp` unit tests (fast, no module load) for the position logic, plus
`gen` e2e tests (real analyzer, `testing.Short`-guarded) driving `gd` over
JSON-RPC like the existing `definition_e2e_test.go`.

**Phase 1 (A):**
- **`paramPosFor` unit** (`internal/lsp`): table of `(Params, attr)` →
  expected offset — `"comments []store.Comment"`/`comments`;
  `"title string, featured bool"`/`featured` (second param);
  grouped `"a, b string"`/`b`; case `"Title string"`/`title` (firstUpper match);
  no-match `"x int"`/`y` → false; malformed `"]["`/`x` → false (no panic).
- **`componentAttrParamAt` / e2e** (`gen`, Short-guarded): a temp module with
  `component CommentsList(comments []store.Comment)` and an invocation
  `<CommentsList comments={xs}/>` in another `.gsx`; `gd` on the `comments`
  attr-name offset → the `comments` param position (line/col of the param token),
  **not** null and **not** a `.x.go` path.
- **Cursor-not-on-attr-name** → falls through (e.g. `gd` on the `{xs}` value
  still resolves `xs` via the existing expr path; `gd` on the tag still hits D2).

**Phase 2 (B, C):**
- **B e2e** (`gen`, Short-guarded): a two-package temp module —
  `ui/components` with `component Input(name, label string)`, and a page
  importing it with `<components.Input name={n} label={l}/>`; `gd` on the
  `components.Input` tag → the `Input` decl in `ui/components/*.gsx` (a real
  `.gsx` path, not `.x.go`, not null).
- **C e2e** (`gen`, Short-guarded): same module; `gd` on the `name` attr-name on
  `<components.Input …/>` → the `name` param in `ui/components/*.gsx`.
- **`.x.go`-independence**: the B/C tests do **not** run `gsx generate`, so no
  `.x.go` exists on disk — proving resolution is from in-memory `.gsx` source.
- Full `go test ./...` green; existing `gd` e2e (D1/D2/D3, pipe-nav) unaffected.

## 8. Risks & edges

- **Attr span starts at the name.** §3.2 relies on `attr.Pos()` being the
  attr-name's first char. The Phase-1 e2e asserts the exact cursor→param mapping,
  which fails loudly if the span starts elsewhere — caught at test time.
- **Grouped params** (`a, b string`): the `go/parser` walk yields each name
  ident separately, so `b`'s offset is correct (a naive comma-split would not) —
  covered by the unit table.
- **Dotted tag that is a method/receiver, not a cross-pkg component**
  (`<p.Content/>`, an existing method-component case): B must not hijack it. The
  resolver returns `false` when the qualifier resolves to a local receiver var
  rather than an imported package, leaving today's behavior intact.
- **Import path → dir** for a dep with no `.gsx` (a pure-Go imported package): the
  decl scan finds nothing → `false` → null, no error.
