# Lowercase tags resolve to package symbols

**Date:** 2026-07-10
**Status:** Implemented 2026-07-10; semantic-eligibility revision approved 2026-07-16, implementation pending

**2026-07-16 revision:** structpages dogfooding exposed the executable-package
collision `package main` + `func main()` + `<main>`. The original rule treated
every same-named package declaration as component intent, so the mandatory Go
entrypoint captured the ordinary HTML element and then failed signature
validation. The revised rule below removes that category error generally: a
lowercase tag resolves as a component only when the matching package symbol is
actually callable as a GSX component. There is no `main` special case.

## Problem

`ast.Element.IsComponent` is the authoritative resolved component-vs-leaf
stamp. The parser never sets it. Codegen's package preprocessor sets it once,
and emission, analysis, and the LSP consume that field rather than re-deriving
component-ness from the tag string.

The current writer, `stampComponentTag`, combines two inputs: the syntactic
capitalized/dotted test from `ast.IsComponentTag`, and a package declaration-
name set for lowercase simple tags. The second input is too broad. It stamps a
lowercase tag as a component whenever any same-named package declaration
exists, without establishing that the declaration is callable as a component.
The resulting stamp is internally consistent but semantically wrong: for
example, mandatory zero-result `func main()` in `package main` makes ordinary
HTML `<main>` a component target.

The revision keeps `Element.IsComponent` as the single source of truth and
changes only how the preprocessor computes that stamp. Lowercase candidates
must be resolved against exact component-capability facts instead of declaration
existence. No parser or consumer-side component classifier is introduced.

## The rule

A tag resolves in exactly one of three ways:

1. **Capital-first or dotted tag** — stamped as component intent, unchanged.
   Exact target resolution then validates the callable's signature and allowed
   provenance. Dotted targets cover imported package functions and concrete
   bound method values; method expressions, interface dispatch, function-valued
   fields, and local function variables remain invalid component targets.
2. **Lowercase simple tag whose name resolves to a component-capable
   package-level symbol** — component. A component-capable symbol has a
   callable signature with exactly one result assignable to the imported
   runtime's canonical `gsx.Node`. This includes authored GSX component
   declarations and package function variables with valid signatures.
3. **Every other lowercase simple tag** — leaf element, rendered as-is. A
   same-named zero-result function, wrong-result function, non-callable
   variable, type, or constant does not capture the tag.

There is **no reserved HTML element table in resolution**. `<div>` is a leaf
unless `div` is a real component callable. Tags that are not valid Go
identifiers (e.g. custom elements with dashes, `<my-widget>`) can never match a
package symbol and are always leaves.

### What counts as a component-capable package symbol

The syntactic declaration-name scan remains the cheap candidate inventory, but
existence alone no longer decides component-ness. GSX builds the package's
existing declaration-only semantic surface from authored component signature
stubs plus active hand-written Go companions, type-checks that surface with the
normal module importer, and indexes only callable package objects whose exact
signature satisfies the component contract.

This is a reordering of the existing exact-target machinery, not a second
semantic system: declaration skeleton mode already emits authored GSX
signatures with inert bodies, and target discovery already needs the same
runtime identity and importer. The implementation must reuse those facts. It
must not add a `packages.Load`, Go subprocess, return-count text scan, HTML-name
allowlist, or other heuristic classifier.

- **Candidates:** package-level `func`, `var`, `type`, and `const` names.
- **Eligible:** package functions and callable package variables with exactly
  one result assignable to canonical `gsx.Node`; authored `component`
  declarations qualify through their declaration stubs.
- **Never eligible:** import names (`import "time"` must not capture `<time>`),
  names declared only in `_test.go` files, and function-local names (Go
  bodies stay opaque blobs per the Go-as-blob decision).
- **Build-tag variants:** GSX component declarations use the validated variant
  family plan; hand-written Go eligibility uses the active companion inventory
  selected by the authoritative Go build context. Inactive textual declarations
  cannot capture a tag.

If declaration analysis is unavailable or incomplete, resolution fails closed
with the underlying positioned semantic diagnostic; it must not silently stamp
an uncertain tag as either a component or a leaf. When analysis succeeds and a
same-named symbol definitively does not satisfy the component contract, the tag
is definitively a leaf.

**Scope rule:** simple lowercase tags resolve only component-capable
package-level symbols. A local `item := ...` does not make `<item>` a component;
local function variables are not valid tag targets regardless of capitalization.

## Self-exclusion

Inside the body of the declaration that declares name `x`, the tag `<x>`
is a **leaf**. This makes the wrapper pattern work with zero extra syntax:

```gsx
component div(children gsx.Node, attrs gsx.Attrs) {
	<div { attrs... }>{children}</div>   // inner <div> is the real element
}
```

- Exclusion covers exactly one name: the declaration being generated. All
  other lowercase tags in that body resolve normally — inside `div`'s body,
  `<span>` is the package's span component if it is component-capable.
- Exclusion applies to the whole declaration body, including expression-
  position element literals.
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

The current ownership boundary remains:

- **Type-arg admission loosens to any tag.** `<list[int]>` parses whether or
  not `list` resolves. Codegen errors if a tag carrying type args resolves
  to a leaf ("type arguments on HTML element `<list>`").
- `ast.IsComponentTag` is only the parser-independent syntactic seed for
  capitalized/dotted tags. It does not answer whether a parsed element is a
  component.
- The package preprocessor is the only owner that stamps
  `Element.IsComponent`. Its lowercase input changes from declaration names to
  the component-capability index.
- Codegen emission/analysis and LSP definition/hover continue consuming that
  same resolved field. They must not add fallback classification branches.

## Invalidation

A package's generated `.x.go` depends on the package's **component-capable
symbol set**, not just its own `.gsx` sources. Verified against the current code:
**this edge already exists and is already handled — no new machinery.**

- Watch mode already treats every hand-written `.go` file as a dep file
  (`gen/watch.go` `watchable`/`isDepFile`); any change sets `depDirty`,
  which `regenPending` answers with a full module reopen + regeneration
  (`gen/watchsession.go`). The dependency is not new: type-aware
  interpolation already makes generated output depend on sibling `.go`
  types. Lowercase-tag resolution rides the same trigger.
- The generate cache key already folds the package's `.gsx` + `.go` sources
  (and reachable dep dirs) into every key (`gen/cachekey.go`
  `dirSourceHash`) — a callable added to a sibling `.go` busts the cache today.

Eligibility-set fingerprinting (skipping regen on body-only `.go` edits) would be
an *optimization over the status quo*, not a requirement of this change —
explicitly out of scope so the feature is additive-only here.

## Risk gates

This feature is worth having only if it stays cheap. Abort criteria agreed
up front:

- **Perf:** no measurable regression in `gsx generate` wall time or watch
  cycle latency, no additional `packages.Load`, and no additional Go-command
  launch. Declaration eligibility must reuse the exact package semantic work
  already required by target discovery.
- **Complexity:** there is one component-capability index and one final tag
  stamp. If implementation requires parallel semantic classifiers or a
  provisional stamp that can leak into emission/LSP facts, stop and reassess.

## Compatibility

No compatibility work: gsx is pre-release, so the semantic-eligibility rule
replaces declaration-existence capture directly. Existing non-callable capture
fixtures and documentation are updated rather than deprecated or supported in
parallel. The self-reference and cycle diagnostics remain the ongoing safety
net for valid lowercase wrapper components.

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
- lowercase tag resolves to callable package variables, including a named
  function type whose result is `gsx.Node`
- zero-result function, wrong-result function, and non-callable `var`/`type`/
  `const` names do not capture and render as leaf elements
- executable regression: `package main`, `func main() {}`, and `<main>` renders
  the HTML element with no special case
- leaf fallback: undeclared lowercase tag renders as-is; dashed custom element
  never resolves
- self-exclusion wrapper: `component div` rendering inner `<div>` leaf
- wrapper composition: `div` wrapper using `<span>` resolves to the span
  component, which bottoms out at its own leaf
- import name and `_test.go`-only name do **not** capture a tag
- type args on a tag that resolves to a leaf — codegen error pinned
- self-reference diagnostic: non-HTML self-named tag warns
- cycle diagnostic: unconditional two-wrapper cycle warns; conditional edge
  does not

Unit tests: candidate-name extraction remains covered (imports/test-file/
build-tag cases); add exact component-capability indexing for functions,
callable variables, invalid result shapes, runtime identity, incomplete
semantic packages, and the package-main entrypoint regression.
Cache-key honesty on sibling `.go` eligibility changes rides the pre-existing
`dirSourceHash` (gen/cachekey.go) — already covered, no new test — and was
probe-verified end to end (leaf→call→leaf flips on sibling callable add/remove
with the cache on). No fingerprint test: eligibility-set fingerprinting (skipping
regen on body-only `.go` edits) is explicitly out of scope (see
Invalidation), so there is no non-invalidation case to pin. Runtime behavior
unchanged — no root-package changes expected. Fmt corpus untouched (layout is
orthogonal).

## Out of scope

- Function-local lowercase resolution (requires scope analysis of opaque Go
  bodies).
- An explicit raw-element escape syntax (`<html:div>`-style). Additive later
  if a real need emerges; self-exclusion covers the known use case.
- Cross-package lowercase tags — unexported names are uncallable across
  packages, so the shadowing layer cannot leak by construction.
- Dotted tags and their callable-origin rules are handled by exact component
  target resolution, not by this lowercase package-symbol eligibility index.
