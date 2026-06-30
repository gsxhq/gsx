# Value-form `if` / `switch` in `class` / `style` blocks

**Status:** Design · 2026-06-29
**Origin:** `one-learning` templ→gsx migration. `ds/badge/badge.gsx` carries an
unwrappable 6-entry class map whose default arm is a fragile negation
(`variant != Green && variant != Yellow && … && variant != Purple`). The
migration flattened the original templ `switch variant { … }` into gsx's
*additive* class map, producing both the negation default and a line the
formatter cannot break.

## Problem

gsx's `class={…}` / `style={…}` value is a **composed contribution list**: each
part is `value` or `"value": cond`, and the semantics are **additive** — every
part whose guard is true is included, and multiple may fire at once.

A badge wants the opposite: **exclusive selection** — pick *exactly one* class
string out of N based on a single discriminant value. Expressing exclusive
selection inside an additive construct is what forces:

1. the negation default (`x != A && x != B && …`) — verbose and fragile (adding
   a variant means editing the negation), and
2. an unwrappable single line (a secondary formatter gap, see below).

This is a semantic impedance mismatch, not mere verbosity. The original templ
expressed it correctly with a Go `switch`; gsx has no in-place equivalent.

## Feature

Add a **value-producing** form of `if` and `switch` — an expression that
evaluates to a single value via Go control flow — usable **only inside the
composed-list blocks `class={…}` and `style={…}`**.

```gsx
class={
	"inline-flex items-center rounded-md px-2 py-1 text-xs font-medium ring-1 ring-inset",
	switch variant {
		case Green:  "bg-green-50 text-green-700 ring-green-600/20 dark:bg-green-500/10 dark:text-green-400 dark:ring-green-500/30"
		case Yellow: "bg-yellow-50 text-yellow-700 ring-yellow-600/20 dark:bg-yellow-500/10 dark:text-yellow-400 dark:ring-yellow-500/30"
		case Red:    "bg-red-50 text-red-700 ring-red-600/20 dark:bg-red-500/10 dark:text-red-400 dark:ring-red-500/30"
		default:     "bg-gray-50 text-gray-700 ring-gray-600/20 dark:bg-gray-500/10 dark:text-gray-400 dark:ring-gray-500/30"
	},
}
```

```gsx
class={ "btn", if open { "btn-open" } else { "btn-closed" } }
```

### Surface syntax

Value-form `switch` follows GSX's existing markup-switch case-body shape:
the expression after `case V:` / `default:` is unbraced and ends at the next
top-level label or the switch's closing brace. Nested Go delimiters are scanned,
so composite literals and function literals remain part of the expression.
Value-form `if` retains `{ … }` branch delimiters because they separate its
condition from its value without introducing Go's `if cond *foo` ambiguity.

- `switch`: `case V:` arms, `case A, B:` multi-value arms (Go parity), optional
  `default:`. An optional tag expression (`switch x { … }`); a tag-less
  `switch { case cond: … }` follows Go and is permitted.
- `if`: `else if …` chains and a final `else { … }`.

### Semantics

- **Exclusive**: exactly one arm's value is selected and contributed to the
  list. The `!= && != && …` default disappears — use `default:` / `else`.
- **No match, no `default`/`else` → the zero value** (empty string in
  class/style context = nothing contributed). Consequently `if cond { "x" }`
  *without* `else` is exactly equivalent to today's additive `"x": cond`. The
  value-form is a strict superset of the guard form, not a special case.
- Arms in `class`/`style` are **strings** (the contribution type). All arms must
  be strings; a non-string arm is a compile-time diagnostic.

### Out of scope (deliberate)

- **General attribute values** (`data-x={ if … }`): already covered by the
  existing cond-attr `{ if cond { data-x="xx" } else {…} }` (whole-attribute
  toggle, with `else`/`else if`). No value-form there.
- **Markup children** (`<span>{ if … }</span>`): already dispatch `if`/`switch`
  to markup control-flow. No value-form there.
- **A guard on a value-form part** (`switch x {…}: cond`): disallowed — the
  value-form *is* the selection; a trailing guard is redundant and confusing.
- **Pipe stages on the result** (`switch x {…} |> upper`): deferred (YAGNI).
  Cheap to add later; no current need.

## Parsing

`class`/`style` route through `parseComposedAttr` → `splitComposed`
(`parser/attrs.go`), which today splits parts on top-level commas and the `:`
guard. A `switch` arm contains its **own** commas (`case A, B:`) and colons
(`case X:`), so `splitComposed` must become **brace/keyword-aware**:

- When a part begins with the keyword `if` or `switch`, consume the entire
  braced construct (balanced `{…}`) as one part instead of splitting on its
  internal commas/colons.
- Otherwise behave exactly as today (`value` / `"value": cond`).

The value-form is represented as a new `ClassPart` shape (or a dedicated AST
node referenced from `ClassPart`) holding the parsed `if`/`switch` tree with
string-expression arms. Reuse `scanToBlockBrace` / the markup `if`/`switch`
sub-parsers where practical so condition and arm bodies parse identically to
their markup counterparts.

## Codegen — alloc-free temp hoist

Go `if`/`switch` are statements, not expressions, so the value-form lowers to a
**hoisted temp** assigned by a generated Go `if`/`switch`, then referenced where
the contribution is built:

```go
var _cls0 string
switch variant {
case Green:
	_cls0 = "bg-green-50 …"
case Yellow:
	_cls0 = "bg-yellow-50 …"
default:
	_cls0 = "bg-gray-50 …"
}
// _cls0 then flows into the normal class-composition / merge / escape path
```

- **Not an IIFE** — a closure call per render would allocate in the hot path,
  against gsx's perf posture. The hoisted temp matches gsx's existing
  temp-hoisting (`interpTemp`).
- Temp type is `string` (class/style contribution type) — no general type
  inference needed in v1.
- The temp feeds the **existing** class-composition, `ClassMerger`, and escaping
  machinery unchanged; the value-form only changes how one contribution's string
  is computed.

### Evaluation order

Composed `class`/`style` contributions evaluate strictly left-to-right, including
plain expressions, guarded values and guards, tuple-returning expressions, and
value-form conditions/arms. Hoisting a value-form or tuple must not move it ahead
of an earlier contribution. For example, in
`class={ setState(), if state { "on" } else { "off" } }`, `setState()` runs
before the `if` condition is evaluated.

Whenever any contribution requires statement-level lowering, codegen hoists all
effectful contributions in source order and passes their temps to the existing
composition call. This applies consistently to element class/style attributes,
root class/style fallthrough merging, and class values passed to child
components. Literal and other provably effect-free values may remain inline.

## Tuple `(T, error)` auto-unwrap — coordinated with `uniform-tuple-unwrap`

A class/style contribution is a value position, so per the project invariant
(*"`(T, error)` auto-unwrap is accepted anywhere an expression is allowed"*) a
contribution whose expression returns `(string, error)` must unwrap — emitting
`tmp, _gsxerr := <expr>; if _gsxerr != nil { return _gsxerr }` and using `tmp`,
with the error propagating out of the enclosing `Render` closure exactly as in
text interpolation. Today it does **not**: `gsx.Class(s string)` takes a single
string, so `class={ f() }` (with `f() (string,error)`) is a hard Go
*multiple-value in single-value context* error. The `uniform-tuple-unwrap`
design lists composed class/style parts as a deliberate non-goal; **this spec
closes that exclusion** for both shapes below, consistently:

1. **Plain part** `class={ f() }` / `style={ f() }` — structurally identical to
   that worktree's child-prop fix: the part value currently inlines into
   `gsx.Class(<expr>)` / `gsx.ClassIf(<expr>, cond)` (emit.go ~713/769). When any
   part value in the list is a tuple, hoist **all** of the list's value
   expressions to temps in source order (tuples via the standard
   `tmp,_gsxerr:=…;if _gsxerr!=nil{return _gsxerr}`, non-tuples via `tmp:=expr`)
   before the `_gsxgw.Class(…)` / `StyleString(…)` call, and pass the temps. The
   `ClassIf` guard `cond` is a bool and is **not** unwrapped.
2. **Value-form arm** `switch x { case A: f() }` / `if c { f() } else { g() }` —
   the value-form already hoists a `var _clsN string` assigned by a generated Go
   `switch`/`if` (above), so each arm is a statement-level assignment site: drop
   the standard hoist in *before* `_clsN = tmp`. Easier than the inlined-literal
   positions because no composite-literal tolerance is needed at emit time.

**Reuse, don't duplicate.** Both paths reuse `uniform-tuple-unwrap`'s shared
helpers, now **merged to main** (`internal/codegen/emit.go`):
`tupleUnwrapType(t types.Type) (types.Type, bool)` (reports T) and
`hoistTuple(b *bytes.Buffer, expr string, interpTemp *int) string` (emits
`tmp, _gsxerr := expr; if _gsxerr != nil { return _gsxerr }`, returns the
`_gsxv%d` temp). Plus its type-check **skeleton tolerance**: arm/part values are
wrapped in the
`_gsxunwrap[T any](v T, _ ...error) T` skeleton helper (so go/types accepts both
tuple and plain values while still field-checking `T`), with tuple-ness detected
via the raw `_gsxuse(<rawexpr>)` probe. Non-`(T, error)` tuples (e.g.
`(int,string)`) yield the existing pointed `invalid-tuple` diagnostic at the
arm/part position, not a raw Go error.

**Dependency.** `uniform-tuple-unwrap` is **merged to main** (commit `40b3d50`);
its shared `hoistTuple`/`tupleUnwrapType`/`_gsxunwrap` machinery is available.
This feature consumes those directly rather than re-implementing the hoist. The
remaining work here extends them to two positions that worktree deliberately
left out (plain class/style parts; value-form arms).

**Note on its non-goals.** The merged `uniform-tuple-unwrap` spec still lists
composed class/style parts (and "`if`/`switch` clauses") as non-goals. This spec
supersedes that for class/style: plain parts and value-form **arms** are value
positions that unwrap; the control-flow **clause/condition** (`if cond`,
`switch tag`) remains a non-value position with no unwrap.

## Formatter (folded in)

Two related printer changes in `internal/printer/printer.go`:

1. **Class-map comma wrapping (existing latent bug).** The `ClassAttr` case
   joins parts with a hard `pretty.Text(", ")` (≈ line 267), which is never a
   break point — so an overflowing composed list dumps every entry onto one
   indented line (exactly `ds/badge/badge.gsx:9`). Replace with a breakable
   separator (`Concat(Text(","), Line)`) inside a `Group` so an overflowing list
   puts one entry per line and a short list still collapses to one line. This
   helps every component that keeps the additive map rather than converting to
   `switch`.

2. **Value-form arm layout.** Print the value-form with canonical unbraced
   switch values one `case`/`default` (or `if`/`else`) per line when broken,
   collapsing to one line when it fits — consistent with how markup `if`/`switch`
   already print. Braced switch-arm blocks remain accepted input and format back
   to the canonical unbraced shape.

## Testing — corpus is canonical

Per CLAUDE.md, every syntax/codegen change ships a corpus case
(`internal/corpus/testdata/cases/**/*.txtar`) pinning `input.gsx` +
`generated.x.go.golden` + `render.golden`, and new syntax valid in multiple
contexts needs a case **per context**.

- **Per context**: a `class={…}` case and a `style={…}` case, each covering
  `switch` (with `default`, with `case A, B:` multi-value, and tag-less) and
  `if`/`else if`/`else`.
- **`if` without `else`** → asserts equivalence to the additive guard form
  (empty contribution when false).
- **No-match-no-default** → empty contribution.
- **Negative cases**: non-string arm (diagnostic); a guard on a value-form part
  (diagnostic); value-form used outside class/style (rejected / parsed as
  existing construct, per context).
- **Tuple `(T, error)` auto-unwrap** (per the coordinated section above):
  - plain class part `class={ f() }` and plain style part `style={ f() }` with
    `f() (string,error)` → unwraps and renders (these are NEW — a hard Go error
    before).
  - value-form arm returning a tuple — `class={ switch x { case A: g() } }` and
    the `style`/`if` variants — unwraps per arm.
  - multiple tuple parts in one list → hoist-all, source order preserved.
  - pipeline into an arm/part returning `(R,error)` → unwraps at the host.
  - rejection: non-`(T,error)` tuple (e.g. `(int,string)`) in a part/arm →
    pointed `invalid-tuple` diagnostic at the correct position.
  - error-propagation: an arm/part whose `f()` returns non-nil error → `Render`
    returns it (runtime unit test in the root `gsx`/codegen package).
- **Formatter**: golden(s) for (a) a long additive class map now wrapping one
  entry per line, and (b) a value-form breaking arms one per line and collapsing
  when short. Regenerate via `-update`, then verify without it.
- **Runtime**: any runtime behavior gets unit tests in the root `gsx` package.

## Downstream (required by CLAUDE.md "syntax change" line)

- **`../tree-sitter-gsx`**: grammar rules for the value-form `if`/`switch` inside
  the composed-list value position; corresponding test corpus.
- **`../vscode-gsx`**: TextMate scopes / highlighting for the new arms.
- **`docs/guide/`**: document expression `if`/`switch` in `class`/`style`,
  including the "`if` without `else` == additive guard" equivalence and the
  exclusive-vs-additive distinction.

## Real-world validation

Rewrite `ds/badge/badge.gsx` (in the `one-learning-gsx` worktree) to use
`switch variant { … }`. This eliminates the negation default *and* the
unwrappable long line — validating the feature end-to-end on the case that
motivated it. `StatusBadgeLarge` (and the templ `statusBadge`/`eventTypeBadge`
shapes) are secondary candidates exhibiting the same pattern.

## Dependencies & sequencing

- **`uniform-tuple-unwrap` Phase 0** (shared `hoistTuple`/`tupleUnwrapType` +
  `_gsxunwrap` skeleton tolerance) is a prerequisite for the tuple-unwrap
  section. Build on top of it; reconcile to the shared helper before merge if
  they land independently. The cross-worktree amendments listed in the
  tuple-unwrap section must be applied to that worktree's spec/matrix.

## Open questions

Deferred items (pipe stages on the value-form result; value-form beyond
class/style) are explicitly out of scope for v1 and can be revisited if a
concrete need appears.
