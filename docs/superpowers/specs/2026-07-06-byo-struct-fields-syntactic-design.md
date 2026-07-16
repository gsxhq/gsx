# Syntactic BYO struct-field enumeration (drop the per-directory type-load)

> **Superseded:** Retained as implementation history. BYO classification and
> component struct-field enumeration are removed by
> `2026-07-14-verbatim-component-signatures-design.md`.

**Status:** design
**Date:** 2026-07-06
**Follow-up to:** `2026-07-05-fmt-syntactic-unused-imports-design.md` (PR #36)

## Problem

After PR #36 made `gsx fmt`'s unused-import detection syntactic, `fmt -l` on
`one-learning-gsx` dropped from ~16s to ~3.0s — a 5.3× win, but short of the
sub-second goal. The residual is **not** in the new detection code. It is a
single prerequisite of building the skeleton: `loadExternalStructFields`
(`internal/codegen/byo.go:337`) does a full
`packages.Load(NeedName|NeedFiles|NeedCompiledGoFiles|NeedImports|NeedDeps|NeedTypes|NeedSyntax|NeedTypesInfo, ".")`
— a complete type-check of the package **and its entire dependency graph** —
**once per directory**, whenever a package has BYO components whose props struct
is declared in a sibling `.go` file (a non-empty `wanted` set).

Measured per directory (one-learning `ds/` + `ui/`, warm caches):

| dir | time | deps loaded |
|---|---|---|
| badge | 265ms | 84 |
| button | 394ms | 202 |
| card | 297ms | 84 |
| field | 343ms | 103 |
| form | 319ms | 164 |
| icon | 237ms | 84 |
| probe | 436ms | 209 |
| ui | 1.304s | 933 |
| **total (per-dir, current)** | **3.60s** | |
| batched one load over all dirs | 1.64s | |

Batching to one load per module only reaches ~1.6s — the `ui` package's own
933-dependency type-load is 1.3s and is irreducible *if we type-check at all*.
**Sub-second requires not type-checking to enumerate struct fields.**

## Insight

`loadExternalStructFields` uses `go/types` but needs almost nothing types
provides. For each wanted struct it extracts only: the set of exported **field
names**, which fields are `gsx.Node`-typed, which are `gsx.Attrs`-typed, and
whether it has `Children`/`Attrs` special fields (`byo.go:376-397`). That is a
**syntactic** property of the struct declaration.

The codebase already reads exactly this syntactically for structs declared in
`.gsx` GoChunks:

- `gsxStructDecls` (`byo.go:233`) — parses GoChunk source, collects
  `type X struct {…}` decls.
- `fieldsFromGsxStruct` (`byo.go:273`) — enumerates a `*goast.StructType`'s
  exported field names, classifying each by its **type string**
  (`isGsxNodeType`/`isGsxAttrsType`, string match on `gsx.Node`/`gsx.Attrs`),
  and detects `Children`/`Attrs`. No type resolution.

And `packageNullaryFuncs` (`byo.go:159`) already parses a package's sibling
`.go` files cheaply (`os.ReadDir` + `parser.ParseFile`, skipping
`_test.go`/`.x.go`), syntax-only.

The fix is to combine these: enumerate sibling-`.go` BYO struct fields the same
syntactic way the `.gsx` path already does, instead of type-loading the world.

## Approach

Rewrite `loadExternalStructFields(dir, wanted)` to be **type-load-free**:

1. Parse the package's hand-written `.go` files (mirror `packageNullaryFuncs`:
   `os.ReadDir(dir)`, `parser.ParseFile`, skip directories, non-`.go`,
   `_test.go`, `.x.go`; swallow per-file parse errors).
2. Collect `type X struct {…}` declarations for every name in `wanted`
   (mirror `gsxStructDecls`'s decl walk, but over the parsed `.go` files).
3. For each found struct, enumerate fields via the **existing**
   `fieldsFromGsxStruct` reader, and assemble the same
   `(fields, nodeFields map[string]map[string]bool, structs map[string]byoStruct)`
   return shape keyed by struct name.

Signature and callers are unchanged (`componentPropFieldsFor` at `byo.go:182`
still calls `loadExternalStructFields(dir, externalWanted)`), so this is a pure
internal replacement. Because `componentPropFieldsFor` feeds **both** `fmt`'s
skeleton build **and** `generate`'s emit, this removes the per-directory
type-load from `generate`'s cold path too — a shared win, not fmt-only.

Delete the now-unused `go/types`/`go/packages` machinery in
`loadExternalStructFields` (and the `isGsxNodeNamed`/`isGsxAttrsNamed` helpers
if they become unused).

## Consistency, and the one real behavior change

The syntactic reader makes the sibling-`.go` path **identical in behavior to the
existing `.gsx` GoChunk path** (both go through `fieldsFromGsxStruct`). Two
properties follow, both already true for `.gsx`-declared BYO structs today:

1. **Embedded/promoted fields are not enumerated.** `fieldsFromGsxStruct` skips
   fields with no `Names` (embedded fields). The current type-based `.go` path,
   via `types.Struct.Fields()`, surfaces an embedded field under its type's base
   name — so a `.go` struct that **embeds** `gsx.Attrs` or `gsx.Node` (rather
   than a named `Attrs gsx.Attrs` field) would lose that classification under the
   new path. This is THE behavior change to watch. A named field
   (`Attrs gsx.Attrs`, `Children gsx.Node`, `Foo gsx.Node`) is unaffected — both
   paths handle it identically.

2. **`gsx` import alias assumption.** `isGsxNodeType`/`isGsxAttrsType` match the
   literal strings `gsx.Node`/`gsx.Attrs`. A `.go` file that imports
   `github.com/gsxhq/gsx` under a non-`gsx` alias would not match. The type-based
   path was alias-robust (it resolved package paths); the `.gsx` path never was.
   In practice `gsx` is imported as `gsx`.

**Decision (per brainstorm):** accept the consistency and gate on goldens. Ship
only if the full corpus goldens **and** `one-learning generate` output stay
byte-identical. If any golden changes, STOP and reassess — the offender is
almost certainly an embedded `gsx.Attrs`/`gsx.Node` BYO struct, at which point we
extend the syntactic reader to surface embedded fields by their type-base name
(mirroring `st.Fields()`) rather than shipping a silent output change.

## Validation gate (correctness is defined by these)

1. **Full corpus goldens byte-identical** — `go test ./internal/corpus -run
   TestCorpus` passes with NO `-update` needed. This is the authoritative proof
   that generated output is unchanged for every pinned case (including BYO
   cases). A single golden diff is a stop-and-reassess signal, not a
   regenerate-and-move-on.
2. **`one-learning generate` byte-identical** — build the binary, run
   `gsx generate` in `~/work/one-learning-gsx`, confirm `git diff` shows no
   `.x.go` changes. This exercises the real BYO structs (`ds/` + `ui/`) the unit
   corpus may not cover. Revert any changes; do not commit in that repo.
3. **`one-learning fmt -l` sub-second** — the payoff: build the binary, `time`
   `fmt -l`, confirm it drops from ~3.0s to well under 1s.

## Testing

- **Corpus:** the change alters no syntax and (by design) no output — the
  existing BYO corpus cases are the regression guard. If none exercise a
  sibling-`.go` BYO struct with `gsx.Node`/`gsx.Attrs` fields, add one corpus
  case pinning a `.go`-declared props struct with a `Children gsx.Node` field, a
  `gsx.Attrs` field, and a plain field — so the syntactic enumeration's field
  classification is golden-pinned.
- **Unit (`internal/codegen`):** a focused test that `loadExternalStructFields`
  does NO external load — build a `Module`/dir with a `.go` BYO struct and assert
  the returned field/nodeField/byoStruct sets match the expected syntactic
  enumeration, and (mirroring the fmt work's `externalLoads()==0` proof) that no
  `packages.Load` occurred. Include a struct with a named `Children gsx.Node` and
  `Attrs gsx.Attrs` field to pin the classification.
- **Equivalence spot-check (test-time oracle, optional but recommended):** for a
  couple of fixtures, assert the new syntactic result equals what the old
  type-based enumeration produced, so a future regression in the syntactic reader
  is caught against the `go/types` ground truth.

## Scope boundaries

- Only `loadExternalStructFields` (and its now-dead type helpers) changes. The
  `.gsx` GoChunk path, `packageNullaryFuncs`, and all callers are untouched.
- No new syntax, no config knob, no cache-key change.
- Embedded-field handling is deliberately left at "consistent with the `.gsx`
  path" unless the goldens gate forces the embedded-field extension.
