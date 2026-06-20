# Plan: Codegen composable `class` + spread + conditional attributes (Phase 2)

**Date:** 2026-06-20
**Branch:** `feat/codegen-composable-attrs` off `main`
**Design:** `specs/2026-06-18-gsx-templating-design.md` §3 (composable class/style),
`specs/2026-06-19-gsx-codegen-design.md` (Attributes lowering).
**Status:** ready for SDD

## Goal

Emit the three deferred attribute kinds that currently hard-error in `emitAttr`
(internal/codegen/emit.go): composable **`class`**, **spread** `{...attrs}` (on
elements), and **conditional** `{ if cond { attr } else { attr } }`. Runtime
helpers already exist (`gw.Class`, `gw.Spread`, `gsx.Class`/`gsx.ClassIf`).

## Scope

**IN:**
- Composable `class={ "a", "b": cond, override }` → `gw.Class(...)` with
  `gsx.Class(expr)` / `gsx.ClassIf(expr, cond)` parts (string contributions).
- Spread on ELEMENTS `<div {...attrs}>` → `gw.Spread(ctx, attrs)` (attrs is a
  `gsx.Attrs`; the Go compiler type-checks the arg).
- Conditional attrs `{ if cond { attrs } [else if…] [else { attrs }] }` →
  `if cond { …emit Then attrs… } else { …emit Else attrs… }`.

**OUT (deferred — clear errors, not silent):**
- **`style` composition stays FAIL-CLOSED** (CSS grammar can't be entity-secured;
  consistent with the reviewed attributes-security decision). Keep the existing
  ctxCSS error in `emitAttr`'s ClassAttr branch. Re-enable only when `|> css`
  safe-type filters exist.
- **`[]string` class contributions** — runtime `ClassPart` only takes `string`.
  A `[]string` part is a Go compile error for now (acceptable; note it). A future
  runtime `Classes([]string)` constructor lifts this.
- **Spread / fallthrough on COMPONENTS** (`<Card {...attrs}>`, auto-fallthrough to
  a single root) — that's Phase 3 (child props) / Phase 7. This phase is
  element-spread only; spread on a component-tag element still errors.
- Pipelines (`|>`) inside class parts / cond / spread exprs — not parsed there;
  nested ExprAttrs inside a CondAttr already route through `emitExprAttr` which
  handles their stages.

## Key facts (from the codebase map)

- AST: `ClassAttr{Name, Parts []ClassPart}`, `ClassPart{Expr, Cond string}` (Cond
  "" = unconditional; plain value, not a Node). `SpreadAttr{Expr string}` (Expr is
  the post-`...` text). `CondAttr{Cond string, Then []Attr, Else []Attr}` (else-if
  = `Else == []Attr{<*CondAttr>}`).
- Runtime ref in generated code: package `gsx` (plain import), writer local
  `_gsxgw`, `ctx` ambient. `gsx`/`ctx` are reserved param names already.
- `emitAttr` is called per-attr in `genNode`'s Element open-tag loop
  (`resolved, table, imports` order). Attr emission is just writer calls between
  `<tag` and `>`, so wrapping in `if {…}` (CondAttr) is valid.
- **Order invariant** (load-bearing): `collectExprs`, `emitProbes`, and `harvest`
  must traverse a component body identically. Today they collect `*Interp` and
  top-level `*ExprAttr`. A CondAttr nests `*ExprAttr`s that need type resolution
  for context-aware escaping — those must be collected/probed/harvested **in
  source order**, at the CondAttr's position among the element's attrs.
- `usedParams` binds component params referenced in interp/attr exprs + control
  clauses + (now) pipeline args. The new kinds add ident sources: ClassPart
  Expr+Cond, SpreadAttr Expr, CondAttr Cond (+ its nested attrs recursively).

## Tasks

### Task 1: Composable `class` + element spread

`emitAttr` (emit.go) — replace the deferred errors for the `class` ClassAttr and
SpreadAttr cases:
- **ClassAttr, Name=="class"**: emit
  ```
  _gsxgw.S(` class="`)
  _gsxgw.Class(<parts>)
  _gsxgw.S(`"`)
  ```
  where each `ClassPart` lowers to `gsx.Class(<Expr>)` if `Cond == ""`, else
  `gsx.ClassIf(<Expr>, <Cond>)`. Join parts with `, `.
- **ClassAttr, Name=="style"**: keep the existing ctxCSS fail-closed error (do NOT
  enable). (`attrContext("style") == ctxCSS`.)
- **SpreadAttr**: if the enclosing tag is a component, error (deferred to Phase 3);
  else emit `_gsxgw.Spread(ctx, <Expr>)`. (emitAttr doesn't know the tag — pass a
  flag or check in genNode before calling; simplest: handle SpreadAttr in genNode's
  Element case where the tag is known, OR thread an `isComponent bool` into
  emitAttr. Pick the cleaner one and keep it consistent.)
- **usedParams**: bind idents referenced in ClassPart Expr+Cond and SpreadAttr Expr
  (extend the `usedParams` harvest, mirroring the pipeline-args fix — scan each
  ClassAttr's parts and each SpreadAttr's Expr via `valueIdents`).

Tests (e2e): static+composable class with a conditional part and a param override
(`class={ "btn", "btn-active": on, extra }`); element spread
(`<div {...attrs}>` with `attrs gsx.Attrs`) rendering sorted keys + bool attrs;
class value escaping (a token with `"`); `style={...}` composition still errors
with a "context" message; spread on a component tag errors (deferred).

Commit: `codegen: emit composable class + element spread`.

### Task 2: Conditional attributes

The order-invariant-sensitive task: a `CondAttr` nests `*ExprAttr`s (in Then/Else)
that need type resolution.

- **Traversal** (analyze.go): extend `collectExprs` and `emitProbes` to recurse
  into a `*CondAttr`'s `Then` then `Else` attr lists, collecting/probing nested
  `*ExprAttr`s (and nested `*CondAttr`s recursively) IN SOURCE ORDER, at the
  CondAttr's position in the element's attr sequence. `collectExprs` and
  `emitProbes` must walk identically (so `harvest`'s k-th `_gsxuse` maps to the
  k-th node). A nested ClassAttr/SpreadAttr inside Then/Else contributes no probe
  (same as top level) but its idents still need binding.
  - For the probe body, a CondAttr's nested attrs are emitted as bare
    `_gsxuse(expr)` calls in order (they don't need the real `if` wrapper for type
    resolution — the exprs type-check regardless of the branch; keep it flat and
    aligned, mirroring how attr-exprs are already probed flatly). Confirm this
    keeps the k-th alignment.
- **usedParams**: bind idents in CondAttr `Cond` and recurse into Then/Else
  (their ExprAttr exprs, nested ClassParts, nested CondAttr conds).
- **Emit** (emit.go `emitAttr`): `*CondAttr` →
  ```
  if <Cond> {
      <emitAttr for each Then attr>
  } else {            // only if len(Else) > 0
      <emitAttr for each Else attr>
  }
  ```
  Recurse through `emitAttr` for nested attrs (handles else-if = a `*CondAttr` in
  Else, and nested class/expr/bool/spread). Emit a `//line` directive for the cond
  if the existing attr emission does (match surrounding style).

Tests: `{ if featured { class="badge" } }`; `{ if a { data-x={x} } else { data-y={y} } }`
(both branches' expr values resolve + escape correctly — the order-invariant
check); else-if chain; a conditional whose attr references a param only used there
(usedParams binding); nested conditional.

Commit: `codegen: emit conditional attributes ({ if cond { attr } })`.

## After tasks
- Final whole-feature review (adversarial: order invariant with CondAttr-nested
  exprs interleaved among plain attrs of different types; class escaping; spread
  key-name validation is runtime's job; style still fail-closed).
- Independent adversarial review with live probing (merge gate).
- Merge `--no-ff`; update ROADMAP (phase-2 #3 attribute kinds: class/spread/cond
  done; style + component-spread + fallthrough still pending).

## Risks
- **Order invariant with CondAttr.** The nested-ExprAttr collection is the one
  thing that, if `collectExprs`/`emitProbes` diverge, silently maps a wrong type to
  a wrong node. Task 2's tests must include a CondAttr with a typed expr value
  interleaved with other typed attrs/interps so a misalignment fails loudly.
- **Spread arg typing.** `gw.Spread(ctx, expr)` requires `expr` to be `gsx.Attrs`;
  a wrong type is a clean Go compile error via `//line` maps — acceptable.
