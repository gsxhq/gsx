# Lowercase tags resolve to package symbols

**Date:** 2026-07-10
**Status:** Approved design, pre-implementation

## Problem

Today `ast.IsComponentTag` (ast/ast.go) is purely syntactic: dotted or
ASCII-uppercase-first tags are components; every other tag is a leaf HTML
element rendered as-is. The rule is shared by the parser (type-arg
admission), codegen (call lowering vs. HTML emission), and the LSP.

The consequence is that a component must be capitalized — which in Go means
exported — and the capital/lowercase split feels arbitrary from the Go side,
where lowercase names are ordinary package symbols. We relax the rule:
lowercase tags participate in symbol resolution.

Verified by probe: lowercase `component card()` declarations already parse
and generate (`func card(cardProps) gsx.Node`) — they are merely unreachable
by tag syntax. This change is therefore **resolution-only**; component
declarations need no parser change.

## The rule

A tag resolves in exactly one of three ways:

1. **Capital-first or dotted tag** — component, unchanged. Codegen emits the
   call and `go build` resolves the name (including function-local names,
   which is why capital tags work for element-literal locals today). This
   already covers **lowercase struct methods**: `<p.content/>` on the
   enclosing receiver generates `p.content()` today, unexported type and
   method included (verified by probe). Methods therefore need no symbol
   resolution at all — they ride the dotted rule.
2. **Lowercase simple tag whose name matches a package-level declaration**
   — component. Codegen lowers it as a component invocation of that name.
3. **Lowercase simple tag with no matching declaration** — leaf element,
   rendered as-is.

There is **no reserved HTML element table in resolution**. `<div>` is a leaf
only because nobody declared `div`. Tags that are not valid Go identifiers
(e.g. custom elements with dashes, `<my-widget>`) can never match a
declaration and are always leaves.

### What counts as a package-level declaration

The name set is gathered **syntactically** from all `.gsx` and `.go` files in
the package directory — skeleton scan via the existing `FileSymbols`-style
machinery (internal/lsp/symbols.go, surfaced to gen through
`Analyzer.ModuleSymbols`). No `packages.Load`, no type checking — and none
is needed: a package scope's bare names are exactly its declared names, so
the syntactic scan is *complete* for simple-tag resolution, not an
approximation. go/types would only add method sets and locals; methods are
covered by the dotted rule and locals are out of scope. (Type-aware
interpolation's existing load happens downstream of this decision and stays
untouched — resolution feeding the skeleton that the load consumes is
exactly why resolution must not depend on the load.)

- **Counted:** every package-level `func`, `var`, `type`, and `const` name.
- **Not counted:** import names (`import "time"` must not capture `<time>`),
  names declared only in `_test.go` files, and function-local names (Go
  bodies stay opaque blobs per the Go-as-blob decision).
- **Build-tag variants:** syntactic presence anywhere in the package counts,
  regardless of build tags — consistent with the tag-variant stance (PR #43):
  gsx never evaluates build tags; presence is textual.

If the matched declaration is not invocable as a component (`var data int`,
`type data string`), codegen still lowers the tag as a component invocation
and **`go build` is the arbiter** — a loud compile error directs the author
to rename. Resolution never consults type information, so there is no silent
fallback to leaf.

**Documented asymmetry:** capital tags resolve function-local names (via
`go build`); lowercase tags resolve only package-level declarations. A local
`item := ...` does not make `<item>` a component.

## Self-exclusion

Inside the body of the declaration that declares name `x`, the tag `<x>`
is a **leaf**. This makes the wrapper pattern work with zero extra syntax:

```gsx
component div() {
	<div { attrs... }>{children}</div>   // inner <div> is the real element
}
```

- Exclusion covers exactly one name: the declaration being generated. All
  other lowercase tags in that body resolve normally — inside `div`'s body,
  `<span>` is the package's span component if one is declared.
- Exclusion applies to the whole declaration body, including expression-
  position element literals and the initializer of a package-level `var`
  component value (inside `var card = ...`, tag `<card>` is a leaf).
- Recursion for a lowercase component uses the Go call form in a hole
  (`{item(...)}`) or a capital name. Mutual recursion via tags resolves to
  components and may loop — see the cycle diagnostic below.
- Exclusion is keyed by the enclosing declaration's **name**, methods
  included: inside `component (p page) div()`, tag `<div>` is a leaf even if
  a package-level `div` is also declared — least surprise; the package
  component remains reachable via the call form. Dotted self-invocation
  (`<p.div>`) is unaffected (dotted is always a component).

**Self-reference diagnostic (warning):** a self-named tag whose name is
*not* a spec HTML element name almost certainly intended recursion — warn:
"`<item>` inside `component item` renders as a leaf element; for recursion
call `item(...)`." The WHATWG element-name table lives **only** in this
diagnostic, never in resolution.

## Wrapper-cycle diagnostic

Self-exclusion breaks direct self-loops only. If `div`'s body renders
`<span>` and `span`'s body renders `<div>`, the two wrappers call each other
forever — it compiles clean and dies at render with a stack overflow.

Analyze builds, per package, the directed graph *component → lowercase tags
in its body that resolve to package components*, and reports a **warning**
(not an error) on each cycle whose edges are all **unconditional** — a tag
under `if`/`for`/pipe-conditional lowering legitimately breaks a static
cycle, so a cycle containing any conditional edge is not reported. Message
names the cycle:
"wrapper cycle `div → span → div` will recurse infinitely at render."

This is a diagnostic on the existing analyze walk (tags per component are
already visited); no new pass over source.

## Parser / codegen split

The parser is per-file and syntactic — it cannot know the package symbol
set. Therefore:

- **Type-arg admission loosens to any tag.** `<list[int]>` parses whether or
  not `list` resolves. Codegen errors if a tag carrying type args resolves
  to a leaf ("type arguments on HTML element `<list>`").
- `ast.IsComponentTag` stops being the single source of truth for
  component-ness. It remains the rule for capital/dotted tags; the lowercase
  decision moves to codegen/analyze, where the package name set is in hand.
  Every current caller (codegen emit/analyze/attrsonly, LSP definition/
  hover) is audited to take the resolved answer, not the syntactic guess.

## Invalidation

A package's generated `.x.go` now depends on the package's **declared-name
set**, not just its own `.gsx` sources. New dependency edge:

- Watch mode regenerates a package's `.gsx` files when a sibling `.go`
  file's top-level declaration set changes. Fingerprint the decl-name set so
  body-only edits to `.go` files do not trigger regeneration.
- Non-watch `gsx generate` already scans the package; no change.

## Compatibility

**Technically breaking, practically pre-release.** Any package where a
lowercase tag in use collides with an existing package-level name flips from
leaf to component invocation — usually a loud build error (non-invocable
symbol), occasionally a silent semantic change (symbol happens to be
renderable). gsx has made no release yet, so no migration tooling is
warranted; ship with:

- A changelog note stating the rule and the common collision names
  (`data`, `time`, `form`, `header`, `section`, ...).
- The self-reference and cycle diagnostics above, which catch the two
  runtime-surprise shapes.

**Tooling:** surface syntax is unchanged, so tree-sitter / vscode /
CodeMirror grammars need no structural changes — but static highlighters
cannot resolve symbols, so lowercase component tags highlight as plain
elements. The LSP corrects this in-editor via semantic tokens (follow-up if
semantic tokens are not yet wired for tags). Sibling repos get README/docs
notes, not grammar rewrites.

**Docs:** guide section on the resolution rule, the wrapper pattern, the
self-exclusion rule, and the recursion caveat. Update the syntax reference
where the capital rule is stated.

## Testing

Corpus cases (semantic corpus, per context where applicable):

- lowercase tag resolves to a package `func` component (same file and
  cross-file within the package)
- lowercase tag resolves to a package `var` component value
- non-invocable capture (`var data int` + `<data>`) — build error pinned
- leaf fallback: undeclared lowercase tag renders as-is; dashed custom
  element never resolves
- self-exclusion wrapper: `component div` rendering inner `<div>` leaf
- wrapper composition: `div` wrapper using `<span>` resolves to the span
  component, which bottoms out at its own leaf
- import name and `_test.go`-only name do **not** capture a tag
- type args on a tag that resolves to a leaf — codegen error pinned
- self-reference diagnostic: non-HTML self-named tag warns
- cycle diagnostic: unconditional two-wrapper cycle warns; conditional edge
  does not

Unit tests: decl-name-set extraction (imports/test-file/build-tag cases),
watch invalidation on sibling `.go` decl-set change (and non-invalidation on
body-only edit). Runtime behavior unchanged — no root-package changes
expected. Fmt corpus untouched (layout is orthogonal).

## Out of scope

- Function-local lowercase resolution (requires scope analysis of opaque Go
  bodies).
- An explicit raw-element escape syntax (`<html:div>`-style). Additive later
  if a real need emerges; self-exclusion covers the known use case.
- Cross-package lowercase tags — unexported names are uncallable across
  packages, so the shadowing layer cannot leak by construction.
- **Pre-existing gap found while probing (not introduced here):** a dotted
  tag whose qualifier is an ordinary local/param rather than the enclosing
  receiver (`component List(p page) { <p.Item/> }`) is mislowered as a
  package-qualified component (`p.ItemProps{}` — "p.ItemProps is not a
  type"). Method invocation currently only recognizes the enclosing
  receiver's name. Tracked separately; candidate for ROADMAP.
