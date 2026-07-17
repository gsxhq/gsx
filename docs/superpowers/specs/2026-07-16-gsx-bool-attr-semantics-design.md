# Boolean Attribute Semantics — Design

Status: **draft, awaiting review**
Supersedes: `2026-06-18-gsx-templating-design.md` §"Boolean attributes are type-driven"
Evidence: `internal/corpus/testdata/cases/attrs/bool_exact_dispatch_required.txtar`

## Problem

gsx decides presence-vs-value from the **Go type**: a `bool`-typed
`name={ expr }` becomes a boolean attribute (bare when true, omitted when
false). The 2026-06-18 spec rejected templ's `?=` on that basis — "gsx knows
the value's type at compile time, so it emits boolean-attr code directly".

The premise is wrong. The type answers *what value the author has*; it cannot
answer *whether HTML wants presence or a string*. Only the attribute **name**
answers that, and HTML has two classes with **opposite** requirements:

- **Boolean attributes** (`required`, `checked`, `disabled`, …) — presence
  alone means true and the **value is ignored**. `required="false"` is
  required. Only **absence** is false, so no string can express false.
- **Enumerated attributes** (`aria-*`, `contenteditable`, `draggable`,
  `spellcheck`, and every `data-*`) — the **string is** the value. `="true"` /
  `="false"` are the required forms; a bare name yields `=""`, which is not a
  valid token and falls back to the default.

One Go `bool`, two opposite correct renderings. gsx applies presence to both
and is right for exactly one class:

| source | renders | |
| --- | --- | --- |
| `<input required={false}/>` — static `bool` | `<input/>` | correct |
| `<Mixed[bool] req={false}/>` — `T string \| bool` | `required="false"` | **required** |
| bag `{required: Flag(false)}` — named bool | `required="false"` | **required** |
| bag `{required: anyFalse}` — `var anyFalse any = false` | `<input/>` | correct |
| `<div aria-hidden={true}/>` | `<div aria-hidden/>` | `=""` invalid → **not hidden** |
| `<div aria-hidden={strconv.FormatBool(h)}/>` | `aria-hidden="true"` | correct |
| `<div contenteditable={false}/>` | `<div/>` | **inverted** — inherits editable |
| `<img draggable={false}/>` | `<img/>` | **inverted** — still draggable |

`contenteditable` and `draggable` are inherited-or-default-on: `="false"` is
what *blocks* editing inside an editable ancestor and what *stops* an image
dragging. Omitting them means the author writes `false` and the browser does
`true`.

Two further defects share one root cause:

1. **Type-parameter divergence.** `classify(t) == catBool` gates the `BoolAttr`
   emit, so a `T string | bool` type parameter classifies as `catAnyMixed` and
   falls to `AttrAny` → `required="false"`.
2. **Named-bool bag divergence.** `attrs.go` asserts `kv.Value.(bool)` — an
   *exact* type assertion — so a `type Flag bool` falls to `toStr` → `"false"`.

**Root cause: EXACT vs UNDERLYING dispatch, not type erasure.** The control row
above proves it — a plain `bool` boxed in an `any` renders *correctly*, because
the assertion sees the dynamic type. gsx runs two classifiers that disagree:

```
static  (codegen)  classify(t) → t.Underlying() via go/types  →  Flag → catBool ✓
runtime (writer)   anyRenderString / kv.Value.(bool)          →  EXACT types only ✗
```

Only a **named** bool and a **type parameter** fall through. This is `gsx.Val`'s
documented contract, mirrored deliberately by `anyRenderString`.

## Goals

- **Correctness is the default.** Standard HTML renders correctly with no
  annotation.
- Both attribute classes expressible; either direction forceable.
- Static and dynamic (bag) paths agree by construction, for every type.
- No project-wide invisible state.

## Non-goals

- Configuration. See §Rejected alternatives.
- Truthiness. Unlike Lit, `?=` takes a `bool`, not "any value, evaluated for
  truthiness".
- Knowing which attributes are *enumerated*. Only the boolean list exists;
  everything not in it stringifies, which is correct for enumerated attributes
  and for arbitrary `data-*`.
- Changing spread syntax or the `{ if cond { attr } }` in-tag conditional form.

## Surface syntax

```
name?={ expr }    → toggle, any name (expr must be bool-typed)
name={ boolExpr } → name in the boolean list ? toggle : "true"/"false"
name={ strExpr }  → always the string
name              → bare; toggle
```

```go
<input required={ p.Req } />          // list name → <input required> / <input>
<div aria-expanded={ p.Open } />      // not listed → aria-expanded="false"
<img draggable={ false } />           // → draggable="false"
<my-toggle active?={ p.On } />        // custom element → forced toggle
```

### Forcing either direction

Both overrides already exist; only one is new syntax. **The list is consulted
only for bool-typed values**, which is what makes the second row work — a string
never reaches the list lookup.

| want | write | works because |
| --- | --- | --- |
| toggle on a non-list name | `active?={ b }` | `?=` forces it |
| string on a list name | `required={ strconv.FormatBool(b) }` | the value isn't bool |

This also handles HTML's hybrid attributes for free: `hidden={ true }` → bare
`hidden`, `hidden={ "until-found" }` → `hidden="until-found"`; `download={ true }`
→ bare `download`, `download={ "a.txt" }` → `download="a.txt"`.

### Grammar — no reservation required

`isAttrNameByte` (`parser/markup.go`) admits `A-Za-z0-9_:@.-` only; `?` has
**never** been legal in a source attribute name, so `?=` is unambiguous and
claims no namespace. tree-sitter's `attribute_name`
(`/[A-Za-z_@:][A-Za-z0-9_@:.\-]*/`) already agrees.

`validAttrName` (`attrs.go`) *does* admit `?`, but it governs **runtime bag
keys**, not source. `Toggle` places the marker in the value and leaves the key
untouched (§Runtime), so the key namespace is not claimed either.
`validAttrName` is unchanged.

Binding rule: `?` must **immediately** follow the name, after which the existing
whitespace-tolerant `=` lookahead applies unchanged.

```
required?={b}   ok        required? = {b}   ok  (existing '=' ws tolerance)
required?= {b}  ok        required ?={b}    error — '?' must abut the name
required?       error     — "'?' requires a value; did you mean bare `required`?"
```

### AST

Add `Toggle bool` to `ast.ExprAttr` rather than a new node type. Attributes are
consumed at many codegen/LSP/fmt sites; a flag on the existing node keeps every
one working and follows the `Element.IsComponent` stamp precedent. The formatter
reads it to re-emit `?=`.

## The boolean-attribute list

**Semantic, not advisory** — it decides. That is safe *because* `?=` exists: a
list gap is recoverable at the call site (`newattr?={b}`) without waiting for a
gsx release, so an incomplete list is never a dead end.

### Derivation

Ported from the WHATWG **index of attributes**, taking every entry whose type is
"Boolean attribute". Do not hand-write it from memory. Approximate size ~30:
`allowfullscreen`, `async`, `autofocus`, `autoplay`, `checked`, `controls`,
`default`, `defer`, `disabled`, `download`, `formnovalidate`, `inert`, `ismap`,
`itemscope`, `loop`, `multiple`, `muted`, `nomodule`, `novalidate`, `open`,
`playsinline`, `readonly`, `required`, `reversed`, `selected`, …

**Verify during implementation; do not trust the list above.** Two known traps:

- **`hidden`** is *enumerated* in current HTML (`until-found` / `hidden` / `""`),
  not boolean — but the empty string maps to the hidden state, so listing it
  still behaves correctly for bool values (§Forcing either direction). Decide
  deliberately and record why.
- **`contenteditable`, `draggable`, `spellcheck`** look boolean and are **not** —
  they are enumerated. They must NOT be listed; listing them reproduces the
  inverted rows in §Problem.

Record the spec URL and a refresh policy beside the list.

### Where it lives

Codegen owns it, exactly as it owns `navNames`/`imageNames`/`srcsetNames`, and
threads it into `Spread` as a `[]string` literal. One source of truth, consulted
by both the static and dynamic paths.

## Runtime: `gsx.Toggle`

```go
// Toggle marks a bag value as carrying boolean-attribute (presence) semantics
// regardless of the attribute name: Spread writes a bare ` name` when true and
// nothing when false. It is the runtime carrier of the `?=` declaration, needed
// only for names outside the boolean-attribute list — a plain bool on a listed
// name already toggles.
type Toggle bool
```

A named `bool` type, so `Toggle(expr)` is a conversion; boxing a bool into `any`
does not allocate (runtime `staticuint64s`).

### `AttrAnyToggle`

```go
// AttrAnyToggle writes one complete attribute whose name IS a boolean attribute
// (codegen resolved the list at generate time) but whose value type is only known
// at runtime. A bool-kinded value writes presence — ` name` or nothing — and any
// other value writes ` name="escaped"`. Generated code emits it for a catAnyMixed
// value on a listed name, so `<input required={req}/>` with T = string | bool is
// correct at both instantiations.
func (gw *Writer) AttrAnyToggle(name string, v any)
```

It owns the whole span — the leading space, the name, and the optional `="…"` —
which is what lets it omit a name that codegen would otherwise have baked into a
static string.

### `Spread`

`Spread` grows a `boolNames []string` parameter beside the existing name sets,
preserving the property that policy comes from the caller.

| bag value, key `required` | renders |
| --- | --- |
| `Toggle(true)` / `Toggle(false)` | ` required` / *(omitted)* |
| `false` — plain bool, listed name | *(omitted)* — **unchanged from today** |
| `Flag(false)` — named bool, listed name | *(omitted)* — divergence fixed |
| `"false"` — string | `required="false"` |
| `false` — plain bool, key `data-open` | `data-open="false"` — the fix |

**Placement.** `Toggle` is checked after the force-owned `excluded` skip but
**before** the `imageNames`/`srcsetNames`/`navNames` switch. The rule: `Toggle`
declares the attribute has no value, and an attribute with no value cannot carry
a URL, so the URL sinks are not skipped but *inapplicable*. Checking it in
`default` would route `Toggle(true)` on `href` to `URLVal` → `href="true"`,
fabricating a value declared absent.

The plain-bool list lookup lives in the `default` branch (no listed name is also
a URL name, so it cannot reach the sinks).

The key is never modified, so last-wins dedup, `Get`/`Has`, class-merge and
caller-wins precedence keep working: `required?={true}` overrides an earlier
`required={false}` because both are key `"required"`.

### Prerequisite: unify the dispatch tables

`Flag(false)` on a listed name only reaches the list lookup if the runtime can
see "underlying bool". Today four tables disagree — proven:

```
uintptr     Val="ERR"     anyRenderString="5"     toStr="5"     ← Val is the outlier
[]string    Val="ERR"     anyRenderString="ERR"   toStr="a b"   ← toStr is the outlier
```

| table | dispatch | quirk |
| --- | --- | --- |
| `classify` (codegen) | **underlying**, via go/types | correct for named types |
| `anyRenderString` | exact | has `uintptr`, no `[]string` |
| `valNode.Render` | exact | **no `uintptr`**, has `Node`/`[]Node` |
| `toStr` | exact | has `[]string`, numerics via `fmt.Sprint` |

`val.go` claims parity with the inline path ("gw.Text mirrors emitRender") but
inline `{ x }` with a `uintptr` renders `"5"` while `gsx.Val(x)` errors — the
documented invariant is already broken.

Unify onto one runtime table **before** this work, as its own PR. It is
independently valuable, it shrinks this change to a single recognition site
rather than a fifth table, and it has an obvious differential test: for every
value, `classify` / `Val` / `anyRenderString` / `toStr` must agree.

**A string return is not sufficient.** Three consumers now need the *kind*, not
just the text — `AttrAnyToggle` and `Spread` must know "is this bool-kinded" to
choose presence, and `Val` must know "is this escape-free" to keep PR #122's
`gw.S` win rather than routing numerics back through the escape pass. So the
table returns a kind alongside the string:

```go
func anyRenderVal(v any) (s string, k valKind, ok bool)   // kindString, kindBool, kindNumber, …
```

One table, four consumers: `Val` (kind → `S` vs `Text`), `TextAny`/`AttrAny`,
`AttrAnyToggle` (kindBool → presence), `Spread` (kindBool + listed name →
presence). `toStr` takes the string and ignores the kind.

**Matching must be by underlying type**, or `Flag(false)` never reaches the list
lookup at all. Per the repo's correctness-first/optimize-simple rule, the general
path is the default and the fast path must be proven equivalent to it: a
`reflect.Kind` classification is the correct general behaviour, with the existing
exact-type switch retained **as a fast path** for the predeclared types, and a
differential test asserting the two agree on every case. Reflect is then only
reached on the miss path — named types — so `bool`/`string`/`int` never pay for
it.

## Codegen lowering

| site | condition | emit |
| --- | --- | --- |
| element, `?=` | `catBool` | `gw.BoolAttr(name, bool(expr))` |
| element, `?=` | otherwise | error `toggle-requires-bool` |
| element, `=` | `catBool`, listed | `gw.BoolAttr(name, bool(expr))` |
| element, `=` | `catBool`, not listed | stringify `"true"`/`"false"` |
| element, `=` | `catAnyMixed`, listed | `gw.AttrAnyToggle(name, expr)` |
| element, `=` | `catAnyMixed`, not listed | existing ` name="` + `AttrAny` |
| element, `=` | any other non-`catBool` | existing stringify path |
| bare `name` | — | `gw.BoolAttr(name, true)` |
| component, `?=`, matched field | — | **open** (§Risks 1) |
| component, `?=`, fallthrough | `catBool` | bag entry `gsx.Toggle(expr)` |
| component, `=`, fallthrough | — | bag entry as-is; `Spread` consults the list |

The `catBool` gate moves from "→ BoolAttr" to "→ consult the list".

### Defect 1: `catAnyMixed` on a listed name

An earlier draft claimed defect 1 "dissolves" under `=`-means-stringify. It does
not — that reasoning would leave `<Mixed[bool] req={false}/>` rendering
`required="false"` (required) with no escape, since `?=` demands an all-bool
constraint. Refusing the construct with a diagnostic was also considered and
rejected: it respects neither the author's intention nor the HTML spec, merely
declining to serve them.

**Resolution: defer only the boolness, never the name.** The list lookup stays
**static** — codegen has the literal attribute name, so it resolves membership at
generate time and only the *value's* type is unknown until runtime:

```go
// name IS listed and the value is catAnyMixed:
_gsxgw.S("<input")
_gsxgw.AttrAnyToggle("required", req)   // bool at runtime → presence; else ="…"
_gsxgw.S("/>")

// name is NOT listed: unchanged, name stays baked into the static string
_gsxgw.S("<input data-x=\"")
_gsxgw.AttrAny(v)
_gsxgw.S("\"/>")
```

So `boolNames` never threads into a static attribute write, and `AttrAnyToggle`
is emitted only where it can matter. Both instantiations then render correctly
with **no annotation**:

```go
component Mixed[T string | bool](req T) { <input required={req} /> }

<Mixed[bool]   req={false} />   → <input/>              // bool → presence
<Mixed[bool]   req={true}  />   → <input required/>
<Mixed[string] req={"foo"} />   → <input required="foo"/>
```

`?=` is therefore *unnecessary* here rather than unavailable, and stays an error
on a non-all-bool constraint: `?=` means "always toggle", which a `string`
instantiation cannot honour.

`?=` on a listed name (`required?={b}`) is allowed and silent — redundant but
explicit and self-documenting, like `int64(x)` on an `int64`.

## Diagnostics

The old "did you mean `checked?=`" advisory is **gone** — `checked={b}` now just
works. Two remain:

1. `toggle-requires-bool` — `?=` on a non-bool value, including a type parameter
   whose constraint is not all-bool terms.
2. **Custom-element bool hint.** `<my-toggle active={b}/>` — a *hyphenated* tag
   (a custom element, which gsx already identifies) carrying a bool-valued
   attribute **not** in the list now renders `active="false"`, silently changing
   from today's bare `active`. gsx cannot know the element's intent, so warn:
   "custom-element attribute with a bool value renders as a string; use
   `active?={ b }` for a toggle". Scoped to hyphenated tags so it stays silent on
   `data-*`/`aria-*`, where stringifying is correct.

Diagnostics live in the analyzer, never the formatter.

## Security

No new sink. `?=` and a listed name both write a bare name through the existing
`BoolAttr`, which emits ` ` + name and never a value, so no escaping context is
entered.

`Toggle` short-circuits the URL/image/srcset sinks (§Spread), which is safe
*because* it emits no value: sanitizers police a value, and there is none. This
is a narrowing, not a bypass — the only reachable output is ` ` + a
`validAttrName`-checked name. The inverse placement would be the unsafe one.

The change **removes** a security-adjacent defect: `contenteditable={false}` and
`draggable={false}` currently render as their opposite.

## Rejected alternatives

**Pure `?=` (author declares everything; no list).** Chosen briefly, then
reversed. The list-as-semantics objection — "a missing entry ships wrong HTML" —
only holds for a list *without* an override. With `?=`, a gap is fixable at the
call site. Meanwhile pure `?=` puts a permanent annotation obligation on the most
common case, failing silently to `checked="false"` (which the browser reads as
checked) every time an author forgets. A ~30-entry closed list is a smaller
liability than a forever-obligation on every author and call site.

**Config (`gsx.toml` boolean-attribute table).** Rejected: `?=` already covers
every name a list cannot know, per call site, visible in the file you are
reading. A config table adds project-wide invisible state — a reviewer of one
file cannot see that `gsx.toml` redefined what `open` means.

**Bag key suffix (`"required?"`).** Rejected: `lastValidAttrIndexes` and `Get`
compare keys exactly, so `"required"` and `"required?"` are distinct keys in
dedup, lookup, exclusion and class-merge. A caller override would emit **both**
(`<input required="false" required>`), and browsers take the first — silently
losing the override. It also collides with real names (`?` is legal in HTML
attribute names per WHATWG) and is stringly-typed. No framework does this: Lit's
`?disabled` is a **prefix** consumed at template-parse time, templ's
`noshade?={x}` a **suffix operator** — in both, `?` never reaches a runtime key.

## Migration

Smaller than under pure `?=`, because listed names keep today's behavior.

1. **Bags with listed names — no change.** `gsx.Attrs{{Key: "disabled", Value:
   true}}` still renders bare `disabled`. The riskiest step of the pure-`?=`
   plan (a silent, compiler-invisible sweep of every hand-written bag) is gone.
2. **Non-listed names with bool values now stringify** — `data-open={b}`,
   `aria-expanded={b}`. This *is* the fix; no author action, but goldens,
   `examples/` and docs change.
3. **Custom elements using non-standard bool attribute names** need `?=`
   (`<my-toggle active?={b}/>`). Silent behavior change, caught by the
   custom-element hint (§Diagnostics 2).
4. `val.go` is unaffected: `Val(true)` renders text `"true"`, unchanged.

Sweep `../structpages` and `../one-learning` for cases 2 and 3.

## Testing

`?=` is attribute-position only; per CLAUDE.md the contexts each need a case.

**Semantic corpus** — `input.gsx` + `generated.x.go.golden` + `render.golden`:

- listed name, `=`, bool true/false → toggle
- non-listed name, `=`, bool → `="true"`/`="false"` (the reversal)
- enumerated regression: `aria-expanded`, `aria-hidden`, `contenteditable`,
  `draggable`, `spellcheck`, `data-*`
- listed name, `=`, **string** value → `="false"` (force-stringify)
- `hidden={true}` → bare; `hidden={"until-found"}` → `="until-found"`
- `?=` on a non-listed name → toggle; on a listed name → toggle, no diagnostic
- element `?=` true/false
- **`catAnyMixed` on a listed name** (`AttrAnyToggle`): `Mixed[bool] req={false}`
  → `<input/>`, `req={true}` → `<input required/>`, `Mixed[string] req={"foo"}`
  → `required="foo"` — all three from one component, no annotation
- `catAnyMixed` on a **non**-listed name → unchanged ` data-x="…"` emission
- named bool through `AttrAnyToggle` (underlying-bool match, not exact)
- component `?=` fallthrough → `Toggle` in bag → `BoolAttr`
- component `?=` on a matched field (per §Risks 1)
- bare attribute unchanged
- in-tag conditional: `<input { if r { required?={b} } }/>`
- spread override: `required={false}` then `required?={true}` → single ` required`
- bag: `Toggle(true/false)`, plain `bool` listed/non-listed, named `Flag(false)`,
  string
- `?=` on non-bool → `toggle-requires-bool`; on `T string | bool` → diagnostic
- `required?` with no value → parse error
- custom-element hint fires on `<my-toggle active={b}/>`, silent on `<div data-x={b}/>`
- **update `bool_exact_dispatch_required.txtar`** — its goldens flip; it becomes
  the regression test for this design

**Formatter corpus** — `?=` round-trips; `required? = {b}` normalizes to
`required?={b}`; idempotence.

**Runtime unit tests** — `Toggle` through `Spread` (true/false, dedup/override,
precedence against a plain-bool entry, `Toggle` on a URL key renders bare); plain
bool listed vs non-listed; named bool on a listed name.

Regenerate with `-update` (rewrites `coverage.golden`; a forgotten manifest bump
fails the suite), then verify without. `make ci` before merge.

## Docs

- `docs/guide/` — attributes page: the four-line rule, the two force directions,
  one sentence of rationale. Concise; rationale lives here, not the guide.
  Literal `{{ }}` in prose needs `::: v-pre`.
- `docs/ROADMAP.md` — record the 2026-06-18 reversal and why.

## Sibling projects

- **`../tree-sitter-gsx`** — add a `toggle_attribute` rule beside
  `expr_attribute` (`grammar.js:350`); `attribute_name` needs no change.
  Regenerate, add corpus entries, run the snippet gate.
- **`../vscode-gsx`** — `syntaxes/gsx.tmLanguage.src.yaml` → regenerate `.json`.
  Bump `package.json`, tag `vX.Y.Z` to release.
- **`../gsxhq.github.io`** — CodeMirror mode in
  `.vitepress/theme/GsxPlayground.vue`; playground WASM rebuilds with a hash
  cache-bust. `/guide` is a gitignored synced copy — edit gsx `docs/guide`.

`make lint` and `make ci` gate the gsx side.

## Process

Per CLAUDE.md: this spec → implementation plan → subagent-driven execution with
per-task reviews → one **independent adversarial reviewer** (throwaway probe
programs, not diff reading) before merge. Feature work in a git worktree.

**Sequencing:** dispatch unification PR → this change.

## Risks / open questions

1. **Bool on a component tag — the one open decision.** Covers `?=` and bare
   together. Presence is meaningless for a Go struct field, so `<Modal
   open?={b}/>` with a declared `bool` field has no coherent reading. Lean:
   **error on a matched field, `Toggle(b)` into the bag on fallthrough**. Bare is
   the harder half — `<Badge on/>` on a `gsx.Node` field silently means the text
   `true` (`gsx.Val(true)`), so bare already carries both readings. Options: (a)
   leave it, documenting the fork; (b) reject bare on a `gsx.Node` field; (c)
   reject bare on components entirely — the only option where bare means
   *presence* everywhere with no exceptions, and the largest migration. Resolve
   before planning.
2. **The bare-bool fork is unpinned** — needs its own corpus case.
3. **List accuracy is now load-bearing.** Derive from the WHATWG index, not
   memory; decide `hidden` deliberately; keep `contenteditable`/`draggable`/
   `spellcheck` out. Mitigated but not eliminated by `?=`.
4. **`?=` on an enumerated attribute** (`aria-expanded?={b}`) produces the
   invalid bare form. Detecting it needs a second (enumerated) list; deferred —
   the author explicitly asked for the override.
5. **`AttrAnyToggle` restructures a static emission site.** Resolved in design
   (§Defect 1), but it is the one place where an attribute's name leaves the
   static string and becomes a runtime argument, so the coalescer, `//line`
   directives, and the attr-hoisting paths all see a shape they have not seen
   before. Worth an explicit probe during implementation rather than trusting the
   goldens alone.
