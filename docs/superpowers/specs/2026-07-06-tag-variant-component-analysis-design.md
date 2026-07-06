# Tag-variant component analysis — tolerate same-name variants

Date: 2026-07-06
Status: approved

## Problem

gsx analyzes a directory as **one Go package**: it synthesizes a Go skeleton
for every `.gsx` file (`buildSkeleton`) and type-checks them all together in a
single `go/types` unit (`checkSkeletonPackage`, `module_importer.go`). Build
constraints are handled purely as opaque text — `File.Doc` is captured as a
string and `goDirectiveLines` (`emit.go`) copies `//go:build` / `// +build`
lines verbatim into the generated `.x.go`. Nothing ever parses the boolean
expression; `go/build/constraint` is imported nowhere.

So two `.gsx` files gated by disjoint build tags that both declare
`component Icon` produce two skeleton `func Icon`s in one type-check →
`Icon redeclared in this block`. Worse, that is a `types.Error`, and
`Module.Generate` skips `generateFile` for the **whole package** when
`len(a.typeErrs) != 0` (`module.go:457`). One colliding pair therefore stops
gsx emitting **every** `.x.go` in the directory — the package cannot build at
all. The platform-variant pattern (`icon_linux.gsx` / `icon_windows.gsx`, each
`//go:build`-gated, both declaring `component Icon`) is unusable today.

The `//go:build` pass-through itself already works (see
`2026-07-02-go-directive-comments-and-gotip-lane-design.md`); this spec closes
the analysis gap that design recorded as a known limitation.

## Principle

**Generation stays build-context-independent.** gsx emits one `.x.go` per
`.gsx`, each carrying its own `//go:build` directive, regardless of host
GOOS/GOARCH/tags. `go build` filters at build time. gsx does **not** parse or
evaluate build constraints — no `constraint.Parse`, no `build.Context`, no
GOOS/GOARCH/`unix`/go-version modeling. `go build` remains the sole arbiter of
whether a same-name pair is an actual same-configuration duplicate.

The only change is to stop a cross-file name collision from being a *fatal*
analysis error, and to make the language server surface all variants of an
ambiguous name instead of guessing one.

## Why suppression is sufficient (probed)

A concern with "just suppress the `redeclared` error" is whether go/types still
resolves the *second* duplicate's body expressions — emit needs them. A probe
(two files in one package, each `func Icon(title string) string` with different
bodies, one calling a local `widthB()`) confirms it does: with the
`Icon redeclared` error reported, go/types still types **both** bodies
best-effort — `fileB`'s body, including its `widthB()` call, is fully typed
(`typed=true type=string`).

This works cleanly because:

- gsx's resolved-type map is keyed by **gsx AST node** (per file), so
  `fileA`'s and `fileB`'s `Icon` interpolations never mix. Each file's emit
  looks up its own nodes.
- The only **name-keyed** harvest is the props signature (`propFields` etc.,
  keyed by props-type name). For same-signature variants those maps are
  **identical**, so a last-writer-wins collapse is harmless — emit gets the
  same field set either way.
- Per-file body facts (does the body reference `attrs` / `children`, generic
  instantiation) are computed per file during `generateFile`, not via
  name-keyed harvest, so they never misattribute across variants.

## Design

### Detection & classification

In the existing package-wide component walk (`componentPropFieldsFor`,
`analyze.go`), when two components share a `componentKey` (`.Name`, or
`recvType.Name` for a method component), compare their **caller signature**:

- props field set — field name + normalized type text, order-independent;
- generic type parameters — names + constraint text;
- receiver — method-vs-func and receiver type text.

This is a syntactic structural comparison over the parsed component
declarations. Type-alias / dot-import edge cases (textually equal, semantically
different) fall through to `go build`; this is documented, not solved.

Classification of a same-`componentKey` pair whose two declarations live in
**different files**:

| Case | Handling |
|---|---|
| Identical signature | **Tolerated.** Recorded as a variant set. |
| Different signature | **Clean gsx error** (`duplicate-component`). |

A same-`componentKey` pair in the **same file** is a within-file redeclaration
— always a real mistake — and is left as a hard error (never a variant).

### Type-check error handling

`buildSkeleton` records, for each component, the skeleton positions of the
decls it emits for that component (the `func`, and — see optimization below —
its props `type`). After `checkSkeletonPackage`, gsx already walks
`[]types.Error`. Extend that walk:

- **Tolerated variant sets:** suppress the `redeclared` / `other declaration of`
  errors whose positions are the known duplicate-decl positions of a tolerated
  set. Matching is **by position** (robust — we generated those skeletons),
  not by parsing the error message. These errors never reach `typeErrs`, so
  `Generate` proceeds and emits all files.
- **Different-signature sets:** the collision is instead reported once as a gsx
  `error[duplicate-component]` at both declaration sites (`.gsx` positions),
  with a message naming the conflicting files and pointing at the signature
  difference. The raw `redeclared` at those positions is suppressed to control
  the message. To keep the package from emitting invalid code, this collision
  must gate emission the way `typeErrs` does — `Generate`'s emit guard
  (`module.go:457`, currently `len(a.typeErrs) == 0`) is widened to also block
  when a different-signature collision was recorded (e.g. by keeping the raw
  `redeclared` in `typeErrs` and reporting the friendly diagnostic alongside
  it, or by adding an explicit blocking flag). A bag diagnostic alone does not
  gate emission, so this wiring is required, not incidental.
- **Non-component cross-file duplicates** (top-level `func`/`type`/`const`/`var`
  declared in `.gsx` `GoChunk`s under disjoint tags — e.g. a shared
  `const iconPath` helper name): the redeclaration family for **cross-file**
  collisions is suppressed silently. gsx cannot cheaply compare arbitrary Go
  declarations, so their same-configuration correctness is deferred to
  `go build` (consistent with the principle).
- **Within-file** redeclarations are **never** suppressed.

**How cross-file vs within-file is actually distinguished (shipped, method-aware).**
go/types (Go 1.26.1) emits redeclarations in two message families:

- Func/var/const/type → a pair: `"<name> redeclared in this block"` (at the 2nd+
  decl) plus a `"other declaration of <name>"` note (at the first decl).
- Method → a single self-contained record: `"method <BaseType>.<Method> already
  declared at <file>:<line>:<col>"`. `<BaseType>` drops the pointer and generic
  type-args, so `(f *Form)` and `(f Form[T])` both report `"Form.Field"`.

The suppressor recognizes **both** families (`redeclName`) and keys them the same
way `collectRedeclFacts` keys the skeleton decls (bare name for functions/vars,
`"<BaseType>.<Method>"` for methods). A redeclaration error is dropped iff its
name is declared in ≥2 skeleton files (a candidate variant) **and never twice in
one file**. Crucially, the within-file fact comes from the **skeleton ASTs**, not
the error positions: go/types anchors *every* redeclaration against the single
globally-first decl of a name, and gsx feeds skeleton files to the checker in
nondeterministic (map) order, so a within-file duplicate living in a non-anchor
file is reported as if it were cross-file. The AST sees every declaration
regardless of order, so the within-file fact is exact and order-independent.

Optimization (not required for correctness): for a non-canonical tolerated
variant, `buildSkeleton` can emit only the `func` (needed to type-check the
body) and reuse the canonical props `type` by name, so no duplicate props-type
`redeclared` arises in the first place — shrinking the suppression surface to
just the func. The blanket position-keyed suppression is the correctness
guarantee; this only reduces noise.

### Emit

Unchanged. Each file emits its `.x.go` by real component name (`Icon`), with
its `//go:build` directive passed through. `go build` selects the active
variant by tag. Because a tolerated variant set is signature-identical, any
caller `<Icon .../>` compiles against whichever variant a given build config
activates.

### LSP — multi-valued navigation

The cross-navigation index (`compByKey`, `crossnav.go`, built at
`module_importer.go`) collapses same-key components to one entry today. Make it
multi-valued (`map[string][]*Component`):

- **`textDocument/definition`** on a `<Icon/>` tag returns **all** variant
  declaration `Location`s. The LSP protocol supports a `Location[]` result; the
  editor lets the user pick.
- **`textDocument/references`** on a component lists tag sites plus **all**
  variant declaration sites.
- **Hover** shows the shared component signature (identical across a tolerated
  set; a different-signature set is an error surfaced as a diagnostic).

### Caching

No `computeKey` change. A component's signature and the set of `.gsx` files in
a directory are derived from source content, which already keys the incremental
cache. Adding, removing, or editing a variant changes source content and
invalidates correctly.

## Conscious trade-offs

- A genuinely-accidental same-signature duplicate with **no** build tags is
  caught by `go build` (`Icon redeclared`, pointing at the generated `.x.go`
  files) rather than by gsx. We do **not** add a "both untagged → warn"
  half-measure: it would be a partial heuristic (it misses two files with the
  *same* tag), and completing it correctly requires the constraint-satisfiability
  modeling this design deliberately avoids. `go build` catches every real case
  correctly.
- gsx gives a friendly `duplicate-component` diagnostic only for **components**
  (where the signature is cheap to compare). Non-component helper/const
  collisions are deferred wholesale to `go build`.
- Cross-tag reference breaks (a file references a component that only exists
  under a different tag) are not flagged by gsx analysis; `go build -tags X`
  reports them. This matches the build-context-independent principle.
- When a within-file redeclaration **coexists** with a cross-file variant of the
  same name (e.g. `Icon` twice in `a.gsx` *and* once in `b.gsx`), gsx keeps
  **all** of that name's redeclaration errors — blocking the whole package —
  rather than surgically keeping only the within-file one. Disentangling the
  specific within-file record per-error is unreliable (go/types' global-first
  anchoring, above), whereas the name-level within-file fact from the skeleton
  AST is exact. This is strictly safe: the within-file duplicate is a real
  mistake `go build` would reject under any tag too, so blocking is correct.

## Tests / docs / siblings

Corpus (`internal/corpus/testdata/cases/`):

- **Same-name same-signature variants generate fully** — two files
  (`//go:build linux` / `//go:build windows`) both `component Icon`, plus a
  third sibling component in the same directory; assert all `.x.go` emit
  (regression on the `module.go:457` whole-package skip) and each variant's
  `.x.go` carries its `//go:build`.
- **`-tags` build probe** — a probe module where the two variants differ in
  body; `go build` under each tag compiles the right one (unit test, mirrors
  `TestBuildTagExcludesGeneratedFile`).
- **Different-signature collision** → `error[duplicate-component]` naming both
  files; no `.x.go` emitted; the raw `redeclared` does not leak.
- **Within-file redeclaration** still errors.
- **Non-component cross-file helper duplicate** under disjoint tags generates
  (deferred to `go build`).

LSP unit tests (`internal/lsp`): multi-valued go-to-definition and
find-references over a variant set; hover shows the shared signature.

Docs: extend `docs/guide/syntax.md` §Build constraints with the variant rule
(same name + same signature allowed across disjoint tags; different signature
is an error; `go build` is the arbiter). Flip the ROADMAP "Tag-aware `.gsx`
analysis" item.

Siblings: no grammar/highlighting change — `//go:build` is an ordinary comment
already handled by tree-sitter/vscode/CodeMirror.

## Out of scope

- Parsing or evaluating build constraints inside gsx (`constraint.Parse`,
  `build.Context`, GOOS/GOARCH/`unix`/go-version world modeling).
- Friendly diagnostics for non-component cross-file duplicates.
- Detecting cross-tag reference breaks at generate time.
- A "variant signature mismatch" auto-hint beyond the `duplicate-component`
  error.
