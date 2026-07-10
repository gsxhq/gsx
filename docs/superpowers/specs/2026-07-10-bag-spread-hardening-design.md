# Bag hardening — resolve everything at the leaf

**Status:** design
**Date:** 2026-07-10
**Follow-up to:** `2026-06-23-gsx-attr-fallthrough-caller-wins-design.md`,
`2026-07-07-attrs-only-component-values-design.md` (PR #72).

**Principle: a bag is a dumb, ordered, duplicate-tolerant carrier. Everything
resolves once, at the leaf element that renders it — precedence, class/style
merge, and URL sanitization. Nothing happens at component-call level.**

## Problems

1. **Precedence.** The forwarding machinery (`!attrs.Has(k)` default guards,
   forced-after, `ClassMerged`/`StyleMerged`) is keyed on the literal token
   `attrs`. A byo `{ p.Attrs... }` gets a bare inline `Spread` instead:
   `<span a="b" { p.Attrs... }>` + caller `a="c"` renders
   `<span a="b" a="c">` — invalid HTML, first-wins, contradicting
   `composition.md` §Precedence. (Probe-verified 2026-07-10.)
2. **URL sinks.** `<a href={evil}>` scheme-sanitizes; the same value landing
   on the same `<a>` through a bag does not — no level of the chain checks.
   The caller-wins design kept *root* attrs out of the bag because `Spread`
   lacked context escaping; it never blessed unchecked *caller* values. PR
   #72 put bags on a mainstream path.
3. **Redundant eager merging.** Call sites emit `.Merge()` chains (one
   alloc + dedup scan per link) whose only effect the leaf reproduces anyway:
   render is last-wins on scalars, aggregating for class/style, and `Get`/
   `Has` are last-wins by contract.

Prior art (checked 2026-07-10): html/template classifies escaping by attr
name wherever a name is visible (`attrType`) — its `data-`-strip and `on*`→JS
heuristics are deliberately **not** ported (they would break Datastar
`data-on-*` and whole-value handlers). templ's `RenderAttributes` does not
sanitize spreads at all — gsx leads here, and the one-learning migration
loses nothing.

## Design

### B — declared bags get the forwarding machinery

The forwarding classification extends from the token `attrs` to two more
declared-bag spellings, each with its own fact source: a byo component's
`p.Attrs` field (classified via `byoStruct.hasAttrs`, which gates on that
exact field name — `analyze.go`'s byo path) and a **generated** component's
own named `gsx.Attrs` param(s) (classified via the parsed param list with
`isGsxAttrsType`, at the same point genComponent already builds the props
struct). Both get derived forms too (`.Without(…)`, `.Merge(…)`). Classified
elements get the existing machinery unchanged: `Has`-guarded defaults before
the spread, forced statics after, position-exempt `ClassMerged`/
`StyleMerged`, one forwarding spread per element (second is the existing
generate-time error). Emit ≡ probe: probe and emitter share the classifier.
Follow-ups (not implemented here): local `gsx.Attrs` variables stay
inline-emitted (needs body-local type tracking); a byo struct's **second**
`gsx.Attrs` field alongside `Attrs` (e.g. `Extra gsx.Attrs`) also stays
inline-emitted — `attrsProps` is never populated for byo structs
(`analyze.go:168-176` short-circuits to the byo branch before `genProps` runs,
so only the `Attrs` field is classified, via `byoStruct.hasAttrs`); recognizing
a second field needs new analysis facts, not just a wider `bagBases` scan.

### A — URL sanitization at the leaf, in generated code

At each forwarding element, codegen emits `Get`-extraction for every
URL-classified attribute name, through the **same tag-aware sinks static
attrs use** (`gw.URL` nav vs `gw.URLImage` image resources; failure →
`about:invalid#gsx`):

```go
if v, ok := attrs.Get("href"); ok {
	_gsxgw.S(` href="`); _gsxgw.URL(_gsxrt.URLVal(v)); _gsxgw.S(`"`)
}
_gsxgw.Spread(ctx, attrs.Without("class", "style", "href", …))
```

- The name set is resolved **at generate time** from the same three-layer
  policy elements use: `[[urlAttrs]]` built-ins + `gsx.toml` rules +
  programmatic `gen` Options (option > env > config). No runtime config, no
  `Spread` signature change; hand-written `gw.Spread` calls keep today's
  documented contract (manual writer use owns its sinks).
- Prefix rules (`prefix = "data-url-"`) cannot be enumerated into `Get`s;
  when (and only when) a project configures prefixes, the residual spread
  consults a small generated matcher. Name matching is case-insensitive,
  matching element classification.
- `_gsxrt.URLVal(any)` unwraps: `gsx.RawURL` passes verbatim (still
  attribute-escaped), everything else stringifies for the sink.
- Extracted URL attrs render at the guard block, not at their bag position.
  Bag order is otherwise preserved; `data-*` (Datastar) is unaffected unless
  a project's own rule classifies it.
- CSS/JS stay literal-opt-in (`` css`…` ``/`` js`…` `` → `RawCSS`/`RawJS`),
  exactly like elements — no name classification, no `on*` heuristic.
  Call-site literal trust-marking considered and deferred: the allow-list
  passes conventional URLs; exotic literal schemes use `gsx.RawURL`.

### C — call sites concatenate; the leaf resolves

Generated call-site bag assembly stops emitting `.Merge()` chains and
concatenates instead — base entries, spreads, conditional bags in source
order, the `attrs={{ … }}` literal appended last (preserving merge-last) —
one allocation via a small runtime concat helper. Semantics are identical by
the type's documented contract (last-wins scalars, aggregating class/style,
last-wins `Get`/`Has`); the only observable change is that a component
iterating its bag sees duplicates, which the contract already permits.
`Attrs.Merge` remains for userland. Corpus pins the equivalence (a collision
case rendering identically before/after) and the duplicate-visible iteration
behavior.

## Testing

- **Corpus B:** byo `p.Attrs` with statics before/after + collisions
  (defaults/forced pinned via `generated.x.go.golden` — render compare is
  attr-order-insensitive); class/style merge through a declared bag; derived
  form; two-spread error; local-bag keeps inline behavior (pin).
- **Corpus A:** `javascript:` href via bag onto `<a>` → `about:invalid#gsx`;
  `data:image/*` via bag onto `<img src>` (passes) vs `<a href>` (rejected);
  project exact + prefix `[[urlAttrs]]` rules respected in a bag (per-case
  `gsx.toml` plumbing exists at `loader.go:100`; the harness's `caseToml`
  needs a `urlAttrs` field); `RawURL` passthrough; htmx `hx-get="/x"`
  untouched; case-variant key (`HREF`).
- **Corpus C:** merge-order case re-pinned on the concat emission;
  duplicate-visible iteration pin.
- Runtime unit tests for `URLVal` and the concat helper; benchmark gate
  (existing attr-write benches — concat should *reduce* allocs).
- One-learning revalidation; adversarial probe review before merge
  (multi-hop bags, conflicting URL keys across concatenated segments,
  prefix rules vs Datastar ordering, duplicate/case-variant smuggling).

## Docs

`gsx.Attrs`/`Spread` godoc (the "NOT URL-sanitized" note becomes the
leaf-rule + `RawURL` opt-out); `attributes.md` + `props.md` PR #72 security
notes shrink accordingly; `composition.md` §Precedence covers declared bags;
ROADMAP follow-up entries (local bags; literal trust-marking). No surface
syntax changes — tree-sitter/vscode/CodeMirror/fmt unaffected.
