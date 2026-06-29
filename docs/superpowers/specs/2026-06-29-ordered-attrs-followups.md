# Ordered Attributes — Follow-ups

- **Date:** 2026-06-29
- **Status:** Backlog (ordered-attrs feature merged to main at `99cd099`)
- **Context:** The `{{ }}` ordered-attrs literal + `gsx.OrderedAttrs` + whitespace-around-`=` shipped. These are deferred follow-ups identified during build + adversarial review + user direction.

## 1. `(T, error)` auto-unwrap anywhere an expression is allowed (P1)

**Principle (user):** the `(T, error)` auto-unwrap should be accepted **anywhere an
expression is allowed**, not only in direct attribute / interpolation positions.

**Current reality:** gsx auto-unwraps `(T, error)` for values rendered directly to
the writer (interpolation, plain element attribute values — `attr_error_autounwrap.txtar`:
`href={ buildHref(id) }` → `_gsxv0, _gsxerr := buildHref(id); if _gsxerr != nil { return _gsxerr }`).
But it does **not** unwrap in **child-prop value position** — values inlined into a
`CompProps{...}` composite literal — which falls to a raw Go error:
- `child_prop_tuple_error.txtar`: `<Card title={lookup(t)}/>` → `multiple-value lookup(t) … in single-value context`.
- ordered-attrs `{{ }}` pair values are in the same position: `{{ "data-signals": sig(t) }}` → same Go error.

**Scope:** make auto-unwrap uniform across **all** expression positions, including
child-prop `prop={f()}` and `{{ }}` pair values. Mechanism: hoist
`v, err := <expr>; if err != nil { return err }` **before** the component call and
reference `v` in the props literal (instead of inlining the call). For `{{ }}`,
hoist per tuple-typed pair value, then reference the temp in
`gsx.OrderedAttrs{{Key: …, Value: vN}}`. This requires type resolution for the
hoisted expressions (child-prop ExprAttr values are already resolved; `{{ }}` pair
values are currently raw strings and would need to be included in the type-check
skeleton so their tuple-ness is known).

**Tests:** flip `child_prop_tuple_error.txtar` from error → unwrap; add an
ordered-attrs `{{ }}` tuple-value case (per-context). Cover nested/threaded cases.

## 2. Editor/grammar parity for the new syntax (P1 — required by CLAUDE.md)

CLAUDE.md now mandates: *"Any syntax change should be accompanied by rigorous
tests and also documentation, ../tree-sitter-gsx and ../vscode-gsx changes."*
The `{{ }}` ordered-attrs literal and whitespace-around-`=` are syntax changes
that shipped without the sibling-repo updates. Required:

- **`../tree-sitter-gsx`** (grammar): add a rule for the `{{ "key": expr, … }}`
  ordered-attrs literal in attribute-value position (distinct from the body
  `{{ stmt }}` GoBlock), quoted-string keys + Go-expr values; and accept optional
  whitespace around `=` in attribute values. Update grammar tests / corpus.
- **`../vscode-gsx`** (extension): syntax highlighting for the `{{ }}` literal
  (keys as strings, values as Go) and any snippet/bracket-pair config; verify the
  ts grammar bump is consumed. Tag-gated release (bump `package.json` → push `vX.Y.Z`).

## 3. BYO-Props struct with a `gsx.OrderedAttrs` field (P3, minor)

`orderedProps` is keyed by the synthesized `<Name>Props` type name, but BYO
components key off the author's struct name, so `orderedOut[structName]` is never
populated (`analyze.go` ~161-174; acknowledged at `emit.go` ~248-250). Spreading a
BYO struct's `gsx.OrderedAttrs` field emits the sorted `Spread` → a confusing
`cannot use … as gsx.Attrs` compile error. Fix: either populate `orderedOut` for
BYO struct names (so dispatch + lowering work), or emit a pointed diagnostic that
the combination is unsupported. Rare; fails safe today.

## Deferred minors (final-review-triaged ACCEPTABLE; fix opportunistically)

- `analyze.go` `isOrderedAttrsType` matches three type-string forms; two
  (`_gsxrt.OrderedAttrs`, bare `OrderedAttrs`) are dead in practice — trim to match
  `isGsxNodeType` (one form).
- `ast/ast.go` `Inspect` leaf comment lists `GoBlock, ClassAttr` but not the
  structurally-identical `OrderedAttrsAttr`.
- `orderedattrs.go` — no unit test for `SpreadOrdered`'s `gw.err != nil` early-exit
  (line-identical to the tested `Spread`).
