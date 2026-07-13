# Reserved component-body identifiers: `ctx`, `children`, `attrs`

**Date:** 2026-07-13
**Status:** approved design, pre-implementation
**Prior art:** `2026-07-13-nested-fallthrough-forwarding-design.md` (made the
`attrs` trigger uniform across positions), `checkReservedParams` /
`checkReservedRecvVar` / `checkReservedDecls` (existing reservation machinery),
usage study of `~/work/one-learning-gsx` (116 files, 891 components).

## Problem

"Reference `attrs`" — the manual-mode trigger — was never precisely defined,
and the same is true of `children` and `ctx`. The trigger is a token scan
(`attrs` IDENT not after `.`, in any body Go fragment), but Go scoping isn't
token-level, so several author-written shapes misbehave (all probe-verified):

| Shape | Today |
|---|---|
| `{{ attrs, ok := lookup() }}` (tuple `:=`) | **compiles silently** — props grow an `Attrs` field; the "local" assigns into the bag; callers can inject |
| `{ for _, attrs := range bags() { <span { attrs... }/> } }` | false rejection: `declared and not used: attrs` |
| `opt{attrs: 1}` (struct-literal key) | false rejection: `declared and not used: attrs` |
| `{{ f := func(attrs []string) … }}` (func-lit param) | false rejection |
| `{{ attrs := … }}` / `{{ const attrs = "x" }}` | raw collision errors (1 and 3 of them) |
| `component (attrs page) C()` (receiver) | two garbage errors at wrong positions |
| `{{ ctx := "hello" }}` / `{{ children := "hi" }}` | raw collision errors |

Meanwhile all three names are already reserved as **param** names with worded
errors; receivers reserve only `ctx`. The reservation exists — partially,
inconsistently, and undocumented.

**Real-world evidence** (one-learning-gsx): 106 `attrs` occurrences in 16/116
files — 85 are the plain `{ attrs... }` element spread, 9 are `Has`/`Without`/
`Class` calls (mostly binding locals in `{{ }}` blocks), zero in clauses/
pipelines/holes, and **zero declarations of any reserved name inside a
component body**. Lowercase `attrs` is however a *popular ordinary identifier*
in sibling `.go` code, so migrated Go will eventually paste these shapes into
component bodies. Making the rule explicit now costs nothing and prevents
every surprise above.

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
- **`attrs`** — the fallthrough bag. **Any value-position use** of the
  identifier in any body Go fragment declares the bag (synthesizes
  `Attrs gsx.Attrs`) and brings an `attrs gsx.Attrs` local into scope.

**Declaring any of the three is an error** — as a param (already enforced),
as a method receiver, or as any body binding: short var decl (including the
tuple form), `var`/`const`, a func-literal parameter, or a range variable.
Positioned diagnostic, code `reserved-identifier`, message naming the meaning
and the fix (e.g. `"attrs" is reserved (the implicit fallthrough bag) —
rename the variable`).

**What is never the identifier:** selector fields (`x.attrs`), struct-literal
keys (`opt{attrs: 1}`), longer identifiers (`attrsList`), string/comment
content, and anything in sibling `.go` files. Package-level declarations of
these names remain legal; inside a component body the reserved meaning wins
(pinned by `nestedforward/shadows_package_var`).

The unification is the **reservation and the documentation** — each name's
trigger semantics (placement for `children`, token for `attrs`, ambient for
`ctx`) are deliberately unchanged.

## Enforcement

New body-level check (sibling of the `_gsx` reservation pass), run during
analysis so both `generate` and the LSP surface it:

- **GoBlocks** (`{{ }}`) parse as a statement list; **control-flow clauses**
  (for/if/switch headers, cond-attr conds) parse as statement headers;
  **interp/attr expressions** parse via `ParseExpr` (the only binding form in
  an expression is a func-literal parameter list). Precedent for parsing
  isolated fragments: `isCallExpr`, `exprHasCall`, `goexprshape`. An
  **unparseable fragment is skipped** — the existing Go collision errors
  remain the backstop, so the check can only improve an error, never mask one.
- Detected binding forms per fragment: `ident :=` (any LHS position, incl.
  tuple), `var`/`const` specs, func-literal params, `for … := range` vars,
  ordinary `for` init statements.
- `checkReservedRecvVar` extends from `ctx`-only to all three names.
- Diagnostic positioned at the binding identifier.

## Trigger-scan refinement (struct keys)

`opt{attrs: 1}` stops triggering. This is a **dedicated trigger scan** used
only by `usesAttrs`/`attrsRefAttrs` — NOT a change to the shared
`valueIdents`, which feeds liveness analysis where `s[lo:hi]`'s
ident-before-colon is a genuine value use. The trigger scan skips an IDENT
immediately followed by `:` at brace level (struct keys, labels). Safety of
the skip: `gsx.Attrs` is a slice — not comparable, not hashable — so no valid
program uses the bag as a `case` value, map key, or index; an ident-before-
colon `attrs` inside braces is provably not the bag.

## Non-goals (explicit, to prevent re-litigating)

- **No `children`-as-value expansion**: `{ wrap(children) }` or
  `{ if children != nil }` without placement stays undefined. A worded
  "place {children} first" diagnostic was considered and dropped — it would
  false-positive on a legitimate package-level `children` variable used in
  value position. The asymmetry is documented instead.
- **No scope-aware shadowing**: declarations do not shadow the reserved
  meaning; they error. Zero real-world demand (study §2), and it keeps the
  one-scanner trigger model.
- **No trigger change for `children`** (`{{ x := children }}` alone does not
  synthesize the slot).

## Test cases (corpus unless noted; every row of the probe matrix pins)

Positive — new group `reserved/` unless noted:

1. `reserved/goblock_consumes_attrs` — `{{ d := attrs.Has("disabled") }}`
   feeding markup (the real-world form.gsx shape); render golden.
2. `reserved/closure_over_attrs` — `{{ f := func() string { return
   attrs.Class() } }}` + `class={ f() }`; render golden.
3. `reserved/struct_key_not_trigger` — `opt{attrs: 1}` + `o.attrs` selector in
   a component with NO other `attrs` use: generates, props struct has **no**
   `Attrs` field (generated golden pins the absence), renders.
4. `reserved/children_value_after_placement` — body places `{children}` AND
   uses `{{ n := children }}`-style value access; render golden (pins the
   slot-first positive side).

Rejections — `diagnostics.golden` captured via `-update`, never hand-written;
each must carry code `reserved-identifier`, position at the binding ident:

5. `reserved/attrs_shortvar_rejected` — `{{ attrs := gsx.Attrs{…} }}`.
6. `reserved/attrs_tuple_rejected` — `{{ attrs, ok := lookup() }}` (the
   silent-trap shape; MUST become an error).
7. `reserved/attrs_range_rejected` — `{ for _, attrs := range bags() { … } }`.
8. `reserved/attrs_funclit_param_rejected` — `{{ f := func(attrs []string)
   int { return len(attrs) } }}`.
9. `reserved/attrs_const_rejected` — `{{ const attrs = "x" }}`.
10. `reserved/attrs_receiver_rejected` — `component (attrs page) C()`.
11. `reserved/children_shortvar_rejected` — `{{ children := "hi" }}` in a
    body that also places `{children}` (the collision shape).
11b. `reserved/children_shortvar_unplaced_rejected` — same binding with NO
    `{children}` placement (pure reservation: nothing collides, the
    diagnostic still fires — this is what makes the rule uniform).
12. `reserved/ctx_shortvar_rejected` — `{{ ctx := "hello" }}`.
13. `reserved/children_value_unplaced` — `{{ x := children }}` with no
    `{children}`: pins the existing `undefined: children` (the documented
    asymmetry — an expected raw error, not a worded one).

**Plain assignment is allowed** (decided here, not punted): `{{ attrs =
attrs.Without("id") }}` is the useful bag-filtering idiom — `attrs` is an
ordinary typed local, reassignment is visible and type-checked, and it is a
*use*, so the trigger fires correctly. Pinned positive:
`reserved/attrs_reassign` (filter then spread; render golden proves the
dropped key is gone). The write-only corner (`attrs = x` as the body's ONLY
occurrence) keeps Go's raw `declared and not used` — a nonsensical program,
documented as out of scope for wording.

Unit tests (`internal/codegen`): table-driven coverage of the binding
detector (each binding form × each fragment kind × each reserved name, plus
non-bindings: plain `=` assignment, selector, struct key) and of the trigger
scan's key-skip (`opt{attrs: 1}` no-trigger; `case attrs:` skip-safety
documented; `s[lo:hi]` unaffected because `valueIdents` is untouched).

Existing cases that must stay byte-identical: `nestedforward/*` (all),
`shadows_package_var`, the fallthrough/attrsonly groups, and the two
`ds/icon/named.gsx`-shaped uses (func-literal `attrs` params in a **plain
`func`**, outside any component body — remain legal; add
`reserved/plain_func_attrs_param_ok` to pin it).

## Documentation

`docs/guide/syntax/` gains a short **Reserved variables** section (probably
in `components.md` or `props.md` — one table, three rows, the declaration
rule, one example of the diagnostic; concise per standing feedback), with
cross-references from `composition.md` (attrs) and wherever `{children}` is
documented. ROADMAP: one entry. Spec is this file.
