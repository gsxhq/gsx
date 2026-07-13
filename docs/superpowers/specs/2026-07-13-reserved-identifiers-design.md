# Reserved component-body identifiers: `ctx`, `children`, `attrs`

**Date:** 2026-07-13 (revised same day: two-stage trigger + body-scope
reservation, per review)
**Status:** approved design, pre-implementation
**Prior art:** `2026-07-13-nested-fallthrough-forwarding-design.md` (made the
`attrs` trigger uniform across positions), `checkReservedParams` /
`checkReservedRecvVar` / `checkReservedDecls` (existing reservation machinery),
usage study of `~/work/one-learning-gsx` (116 files, 891 components).

## Design principle (governs every decision below)

**Soundness over completeness.** A *false rejection* — correct code that gsx
refuses to build — is the bug class this spec eliminates; it blocks users
outright. *Incomplete diagnostics* are acceptable: gsx words the common wrong
shapes, and any exotic shape the checks miss falls through to the Go
compiler's own errors (the backstop). We do not chase every case; we
guarantee we never block a correct program.

## Problem

"Reference `attrs`" — the manual-mode trigger — was never precisely defined,
and the same is true of `children` and `ctx`. The trigger is a token scan
(`attrs` IDENT not after `.`, in any body Go fragment), but Go scoping isn't
token-level. Probe-verified today:

| Shape | Today |
|---|---|
| `{{ attrs, ok := lookup() }}` (tuple `:=`) | **compiles silently** — props grow an `Attrs` field; the "local" assigns into the bag; callers can inject |
| `{ for _, attrs := range bags() { <span { attrs... }/> } }` | **false rejection**: `declared and not used: attrs` |
| `opt{attrs: 1}` (struct-literal key) | **false rejection**: same |
| `{{ f := func(attrs []string) … }}` (func-lit param) | **false rejection**: same |
| `{{ attrs := … }}` / `{{ const attrs = "x" }}` | raw collision errors (1 and 3 of them) |
| `component (attrs page) C()` (receiver) | two garbage errors at wrong positions |
| `{{ ctx := "hello" }}` / `{{ children := "hi" }}` | raw collision errors |

All three names are already reserved as **param** names with worded errors;
receivers reserve only `ctx`. The reservation exists — partially,
inconsistently, undocumented.

**Real-world evidence** (one-learning-gsx): 106 `attrs` occurrences in 16/116
files — 85 are the plain `{ attrs... }` element spread, 9 are `Has`/`Without`/
`Class` calls (mostly binding locals in `{{ }}` blocks), zero in clauses/
pipelines/holes, **zero declarations of any reserved name inside a component
body**. Lowercase `attrs` is however a *popular ordinary identifier* in
sibling `.go` code (params, locals, variadics across 6+ files), so migrated
Go will paste these shapes into component bodies eventually.

## The model (docs-facing rule)

A component body has three reserved identifiers, all provided by gsx:

- **`ctx`** — the render `context.Context`. Always in scope, in every Go
  fragment. No synthesis.
- **`children`** — the caller's child markup. **Placing `{children}`** (the
  exact interpolation, or a bare `@{children}` hole) declares the slot
  (synthesizes `Children gsx.Node`) and brings a `children` local into scope —
  only then is `children` also usable as an ordinary value in other fragments.
  A value-position use without placement stays `undefined: children`
  (slot-first; documented, not "fixed" — see Non-goals).
- **`attrs`** — the fallthrough bag. **Any free value-position use** of the
  identifier in any body Go fragment declares the bag (synthesizes
  `Attrs gsx.Attrs`) and brings an `attrs gsx.Attrs` local into scope.
  "Free" = not bound by a fragment-local nested scope.

**Body-scope declarations of the three are errors; nested-scope shadows are
legal Go.** GoBlock top-level statements share the render closure's scope, so
`:=` (including the tuple form — the silent trap), `var`, or `const` of a
reserved name there is a positioned `reserved-identifier` diagnostic, as are
params (already enforced) and method receivers (extended from `ctx`-only).
Inside a *nested* scope — a func-literal parameter list, a `for`/`range`
variable, an inner block — the name shadows like any Go variable, the
shadowed occurrences are not free, and **no prop is synthesized** for them:

```gsx
{ for _, attrs := range bags() {   // legal: loop-scoped shadow,
    <span { attrs... }>i</span>    // spreads the LOOP var,
} }                                // component gets NO Attrs prop
```

**What is never the identifier:** selector fields (`x.attrs`), struct-literal
keys (`opt{attrs: 1}`), longer identifiers (`attrsList`), string/comment
content, anything in sibling `.go` files. Package-level declarations of these
names remain legal; inside a component body the reserved meaning wins for
free uses (pinned by `nestedforward/shadows_package_var`).

**Reassignment is allowed**: `{{ attrs = attrs.Without("id") }}` is the
bag-filtering idiom — a typed, visible *use* (the trigger fires correctly).
The write-only corner (`attrs = x` as the body's only occurrence) keeps Go's
raw `declared and not used`; nonsensical program, backstop suffices.

Each name's trigger semantics (ambient `ctx`, placement-declared `children`,
use-declared `attrs`) are deliberately unchanged — the unification is the
reservation, the free-use precision, and the documentation.

## Mechanics

### Two-stage trigger (`usesAttrs`)

1. **Token pre-filter** (existing scan, kept): no `attrs` token anywhere →
   no trigger, zero new cost. This is ~90% of components in the study.
2. **Free-use confirmation** (new, only on a token hit): parse the hit
   fragments (GoBlocks as statement lists, clauses as statement headers,
   interp/attr values via `ParseExpr` — precedent: `isCallExpr`,
   `exprHasCall`, `goexprshape`) and walk with a syntactic scope stack (func
   literals, blocks, `:=`, range/for clauses). Trigger iff ≥1 occurrence is
   free. The walk is exact on parseable fragments — no heuristic; valid Go
   parses, so **neither over-fire (`declared and not used`) nor under-fire
   (`undefined: attrs`) can reject a valid program**.

   **Scope spans markup, not just fragments.** A markup control-flow
   clause's bindings scope over its markup *subtree*'s fragments: in
   `{ for _, attrs := range bags() { <span { attrs... }/> } }` the spread is
   a different fragment than the clause that binds it. The walk therefore
   threads a bound-names environment through the existing markup recursion
   (`usesAttrs` already recurses exactly this tree): a `ForMarkup`/`IfMarkup`
   clause's declared names are added to the environment for its body (and
   else) subtree; a GoBlock's top-level declarations extend the environment
   for subsequent sibling fragments (this also keeps a reserved-name
   body-scope binding that the reservation check happened to miss from
   double-erroring — the name is simply treated as bound thereafter, and
   Go's collision error is the single backstop).
3. **Fallback:** an unparseable fragment falls back to the token answer for
   that fragment. A component with an unparseable fragment does not compile
   anyway (fragments emit verbatim), so the fallback cannot reject a correct
   program — it only preserves today's behavior mid-edit.

The struct-literal-key exclusion comes free with parsing (a key is not a
value ident in the AST); the token pre-filter alone never rejects (it only
escalates to stage 2). `valueIdents` — which feeds liveness analysis where
`s[lo:hi]`'s ident-before-colon IS a value use — is untouched.

### Reservation check (best-effort wording, Go backstop)

A body-level pass (sibling of the `_gsx` reservation pass, shared by
`generate` and the LSP) reports `reserved-identifier` for the **common
body-scope binding shapes**: GoBlock top-level `:=` (any LHS position, incl.
tuple), `var`/`const` specs, plus params and receivers. Positioned at the
binding identifier; message names the meaning and the fix (`"attrs" is
reserved (the implicit fallthrough bag) — rename the variable`). Shapes the
pass misses (or fragments that don't parse) fall through to Go's collision
errors — acceptable per the design principle; the check upgrades wording,
never gates validity of correct code.

`usesAttrs`'s five consumers (emit, analyze ×3, variantcollide) share the
predicate as today; emit ≡ probe is unaffected because both passes consume
the same answer.

## Non-goals (explicit, to prevent re-litigating)

- **No `children`-as-value expansion**: `{ wrap(children) }` or
  `{ if children != nil }` without placement stays undefined. A worded
  "place {children} first" diagnostic would false-positive on a legitimate
  package-level `children` variable; the asymmetry is documented instead.
- **No trigger change for `children`** (`{{ x := children }}` alone does not
  synthesize the slot).
- **No completeness chase in the reservation check** — the Go compiler is
  the backstop for exotic binding shapes.

## Test cases

Positive — new corpus group `reserved/`; hand-written render goldens that
must survive `-update` byte-identical:

1. `reserved/goblock_consumes_attrs` — `{{ d := attrs.Has("disabled") }}`
   feeding markup (the real-world form.gsx shape).
2. `reserved/closure_over_attrs` — `{{ f := func() string { return
   attrs.Class() } }}` + `class={ f() }` (free use inside a func literal —
   closure over the bag still triggers).
3. `reserved/struct_key_not_trigger` — `opt{attrs: 1}` + `o.attrs` selector,
   no other `attrs` use: generates, props struct has **no** `Attrs` field
   (generated golden pins the absence), renders.
4. `reserved/range_shadow_ok` — `{ for _, attrs := range bags() {
   <span { attrs... }/> } }`: renders each bag; props struct has **no**
   `Attrs` field.
5. `reserved/funclit_param_shadow_ok` — `{{ f := func(attrs []string) int {
   return len(attrs) } }}` + `data-n={ f(nil) }`: works, no `Attrs` field.
6. `reserved/shadow_and_free_mixed` — a body with BOTH a range shadow and a
   separate free use (`{ attrs... }` on the root): prop synthesized once,
   loop var independent; render proves both.
7. `reserved/attrs_reassign` — `{{ attrs = attrs.Without("id") }}` then
   spread; render proves the dropped key is gone.
8. `reserved/children_value_after_placement` — `{children}` placed AND
   `{{ n := children }}`-style value access.
9. `reserved/plain_func_attrs_param_ok` — a plain `.gsx` `func(attrs
   ...gsx.Attr) gsx.Node` helper (the one-learning `ds/icon/named.gsx`
   shape) — outside any component body, untouched by reservation.

Rejections — `diagnostics.golden` captured via `-update`, never hand-written;
each carries code `reserved-identifier` positioned at the binding ident:

10. `reserved/attrs_shortvar_rejected` — `{{ attrs := gsx.Attrs{…} }}`.
11. `reserved/attrs_tuple_rejected` — `{{ attrs, ok := lookup() }}` (the
    silent trap — MUST become an error).
12. `reserved/attrs_const_rejected` — `{{ const attrs = "x" }}`.
13. `reserved/attrs_receiver_rejected` — `component (attrs page) C()`.
14. `reserved/children_shortvar_rejected` — `{{ children := "hi" }}` with
    `{children}` placed (collision shape).
15. `reserved/children_shortvar_unplaced_rejected` — same binding, NO
    placement (pure reservation — what makes the rule uniform).
16. `reserved/ctx_shortvar_rejected` — `{{ ctx := "hello" }}`.
17. `reserved/children_value_unplaced` — `{{ x := children }}`, no
    `{children}`: pins the existing `undefined: children` (documented
    asymmetry; an expected raw error, not a worded one — also pins that the
    backstop path still fires when the reservation check has nothing to say).

Unit tests (`internal/codegen`), table-driven:

- Free-use walker: each binding form × each fragment kind × each reserved
  name; shadow-only → not free; mixed shadow+free → free; struct key /
  selector / string / comment → not occurrences; unparseable fragment →
  token fallback.
- Reservation detector: the common shapes flag; a deliberately exotic shape
  (e.g. `var attrs, other = …` multi-name spec if unhandled) documents the
  Go-backstop expectation rather than a gsx diagnostic.
- `valueIdents` untouched: `s[lo:hi]` liveness regression pin.

Byte-stability guards: all `nestedforward/*`, `fallthrough/*`, `attrsonly/*`
goldens unchanged (the two-stage trigger must answer identically to the token
scan for every existing corpus case — every case either has no `attrs` token
or uses it free).

## Documentation

`docs/guide/syntax/` gains a short **Reserved variables** section (one table,
three rows; the body-scope-declaration rule; one line on nested shadowing
being ordinary Go; one diagnostic example — concise per standing feedback),
cross-referenced from `composition.md` (attrs) and the `{children}` docs.
ROADMAP: one entry. Perf note for the plan: stage 2 runs only on token hits
(~10% of components in the study), on fragments already in memory.
