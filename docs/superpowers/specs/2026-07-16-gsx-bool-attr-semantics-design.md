# Boolean Attribute Semantics — Design

Status: **draft, awaiting review**
Supersedes: `2026-06-18-gsx-templating-design.md` §"Boolean attributes are type-driven"
Evidence: `internal/corpus/testdata/cases/attrs/bool_exact_dispatch_required.txtar`

**This is not a syntax change.** No new operator, no grammar change, no parser or
AST change, no formatter work, and no sibling-repo updates. The fix is a
name-keyed policy table plus one exported runtime type.

## Problem

gsx decides presence-vs-value from the **Go type**: a `bool`-typed
`name={ expr }` becomes a boolean attribute (bare when true, omitted when
false). The 2026-06-18 spec rejected templ's `?=` on that basis — "gsx knows
the value's type at compile time, so it emits boolean-attr code directly".

The premise is wrong. The type answers *what value the author has*; it cannot
answer *whether HTML wants presence or a string*. Only the attribute **name**
answers that, and HTML has two classes with **opposite** requirements:

- **Presence-only attributes** (`required`, `checked`, `disabled`, …) — presence
  alone means true and the **value is ignored**. Only **absence** is false.
- **Value attributes** (`aria-*`, `contenteditable`, `draggable`, `spellcheck`,
  every `data-*`) — the **string is** the value. `="true"`/`="false"` are the
  required forms; a bare name yields `=""`, usually invalid.

One Go `bool`, two opposite correct renderings. gsx applies presence to both and
is right for exactly one class:

| source | renders | |
| --- | --- | --- |
| `<input required={false}/>` — static `bool` | `<input/>` | correct |
| `<Mixed[bool] req={false}/>` — `T string \| bool` | `required="false"` | **required** |
| bag `{required: Flag(false)}` — named bool | `required="false"` | **required** |
| bag `{required: anyFalse}` — `var anyFalse any = false` | `<input/>` | correct |
| `<div aria-hidden={true}/>` | `<div aria-hidden/>` | `=""` invalid → **not hidden** |
| `<div contenteditable={false}/>` | `<div/>` | **inverted** — inherits editable |
| `<img draggable={false}/>` | `<img/>` | **inverted** — still draggable |

`contenteditable` and `draggable` are inherited-or-default-on: `="false"` is what
*blocks* editing inside an editable ancestor and what *stops* an image dragging.
Omitting them means the author writes `false` and the browser does `true`.

**Root cause: EXACT vs UNDERLYING dispatch, not type erasure.** The control row
proves it — a plain `bool` boxed in an `any` renders *correctly*, because the
assertion sees the dynamic type. gsx runs two classifiers that disagree:

```
static  (codegen)  classify(t) → t.Underlying() via go/types  →  Flag → catBool ✓
runtime (writer)   anyRenderString / kv.Value.(bool)          →  EXACT types only ✗
```

Only a **named** bool and a **type parameter** fall through. This is `gsx.Val`'s
documented contract, mirrored deliberately by `anyRenderString`.

## Goals

- **Correct HTML by default**, with no annotation.
- **Never block the author.** `required="foo"` stays expressible — it is a valid
  CSS selector target (`[required="foo"]`), and gsx is not the HTML police.
- One override mechanism that **travels**: element, component prop, and bag.
- Static and dynamic paths agree by construction, for every type.

## Non-goals

- New syntax. See §Rejected alternatives.
- Configuration. See §Rejected alternatives.
- Knowing which attributes are *enumerated*. Only the presence-only list exists;
  everything else stringifies, which is correct for enumerated and `data-*`.
- Truthiness. `Toggle` takes a `bool`, not "any value, evaluated for truthiness".

## The rule

```
name={ boolExpr }        → listed name ? presence : "true"/"false"
name={ strExpr }         → always the string          (the list only sees bools)
name={ gsx.Toggle(b) }   → presence, any name         (the override)
name                     → bare; presence
```

```go
<input required={ p.Req } />               // listed → <input required> / <input>
<div aria-expanded={ p.Open } />           // not listed → aria-expanded="false"
<input required={ "foo" } />               // string → required="foo"  (CSS selector)
<my-toggle active={ gsx.Toggle(b) } />     // custom element → forced presence
```

## The presence-only list

**Semantic** — it decides. Safe because `Toggle` recovers any gap at the call
site, without waiting for a gsx release.

### Membership: is there a string that means false?

The WHATWG "Value" column is only a **proxy for the real question, and it is
wrong in both directions.** The criterion is:

> **Is there a string that means false?**
> No → only absence can express it → **list it**.
> Yes → stringify.

| attribute | WHATWG Value | string meaning false? | list |
| --- | --- | --- | --- |
| `required`, `checked`, `disabled`, `selected`, `readonly`, `multiple`, `autofocus`, `async`, `defer`, `open`, … | Boolean attribute | **no** — value ignored | ✅ |
| `hidden` | *enumerated* (`until-found`/`hidden`/`""`) | **no** — see below | ✅ *despite type* |
| `download` | *Text* (filename) | **no** — `"false"` is a filename | ✅ *despite type* |
| `contenteditable` | enumerated `true`/`false`/`plaintext-only`/`""` | **yes** | ❌ |
| `draggable`, `spellcheck` | enumerated `true`/`false` | **yes** | ❌ |
| `aria-*` | enumerated `true`/`false` | **yes** | ❌ |
| `data-*` | author-defined | **yes** | ❌ |

**`hidden` is the proof that the type column lies.** Its *invalid value default*
is the **Hidden** state ([spec](https://html.spec.whatwg.org/multipage/interaction.html#the-hidden-attribute)),
so `hidden="false"` **hides the element**. Treating it as enumerated — the type
column's answer — would make `hidden={false}` render `hidden="false"` and hide
the element the author asked to show. It must be listed.

`download` is the same shape from the other side: typed `Text`, but a bool value
means presence, and `download="true"` would be a *filename*. Listing it costs
nothing, because the list only consults bool values — `download={"a.txt"}` still
renders `download="a.txt"`.

### Structure: derived and curated must be separate lists

The mechanical part and the judgement part are **two lists, never one**. Merged,
the next refresh from the spec silently drops `hidden` and `hidden={false}` starts
hiding elements again — the regeneration would look clean and reintroduce the bug.

```go
// booleanAttrs is the WHATWG index's "Boolean attribute" rows, derived
// mechanically (§Derivation). Regenerate wholesale; contains no judgement.
var booleanAttrs = []string{"allowfullscreen", "async", "autofocus", /* … */}

// presenceOnlyExtras are attributes the index does NOT type as "Boolean
// attribute" but for which no string means false, so only absence can express
// it. Hand-curated; each entry carries its reason; MUST survive a regeneration
// of booleanAttrs.
var presenceOnlyExtras = map[string]string{
	"hidden":   `invalid value default is the Hidden state — hidden="false" HIDES the element`,
	"download": `typed Text — download="true" would be a filename`,
}
```

A third list exists **only as a test**, not runtime code — a guard against a
future "fix" that re-adds an attribute which looks boolean but has a valid
`"false"`, reviving the inverted render this design exists to remove:

```go
// notPresenceOnly must never appear in the effective list.
var notPresenceOnly = []string{"contenteditable", "draggable", "spellcheck"}
```

The extras list is exactly two. `translate` (`yes`/`no`) and `autocapitalize`
(`on`/`off`/…) use non-boolean keywords, so a bool does not apply and the author
writes the string; `popover`'s bare form means `auto`, which is not what `={true}`
implies. Only `hidden` and `download` are cases where a bool genuinely means
presence while the type column disagrees.

### Derivation

**Do not hand-copy the WHATWG index, and do not trust a fetched summary.** Two
independent fetches of the attributes index during design contradicted each other
(one reported `required`/`selected`/`multiple` as Boolean, the other as absent);
the page is large enough that extraction truncates silently, and a truncated
answer is indistinguishable from a complete one.

Implementation must instead:

1. Parse the index table programmatically, taking rows whose Value is exactly
   "Boolean attribute". **Scope to the current index only** — the obsolete-features
   table also carries boolean-typed attributes (`nowrap`, `compact`, `declare`),
   which a naive parse will sweep in.
2. **Review every row against the false-string test** — the type column is a
   proxy, not the rule.
3. Cross-check against two independent implementations that maintain the same
   list — Vue's `isBooleanAttr` (`@vue/shared`) and React's property table. A
   disagreement is a signal to re-read the spec text for that attribute, not to
   pick a majority.
4. Record the spec URL and the derivation date beside `booleanAttrs`, and pin the
   derived list in a test so a refresh shows up as a reviewable diff rather than
   a silent behaviour change.

### Where it lives

Codegen owns it, exactly as it owns `navNames`/`imageNames`/`srcsetNames`, and
threads it into `Spread` as a `[]string` literal. One source of truth for both
the static and dynamic paths.

## `gsx.Toggle` — the override

```go
// Toggle forces boolean-attribute (presence) semantics on any attribute name,
// bypassing the presence-only list: true writes a bare ` name`, false writes
// nothing. It exists for names the HTML spec cannot know — web components,
// Datastar — where a plain bool would otherwise stringify.
//
// It is a value, not syntax, so the same expression works on an element, as a
// component prop, and in a hand-written bag.
type Toggle bool
```

A named `bool`, so `Toggle(expr)` is a conversion and the Go compiler enforces
bool-ness — no diagnostic needed. Boxing a bool into `any` does not allocate
(runtime `staticuint64s`).

### Overriding, direct and through components

The override **rides in the value's type**, so the component pass-down column is
*identical* to the direct column — there is no separate component mechanism:

| want | on an element | through a component | why |
| --- | --- | --- | --- |
| correct HTML (default) | `required={b}` | `<Comp required={b}/>` | leaf name decides |
| force the string | `required="foo"` | `<Comp required={"foo"}/>` | list only sees bools |
| force presence | `active={gsx.Toggle(b)}` | `<Comp active={gsx.Toggle(b)}/>` | `Toggle` rides in the bag |

This is the leaf-decides principle gsx already applies to class-merge and the URL
sinks: **a component tag binds a value; the element decides the HTML.** So
`<Modal open={b}/>` just sets a `bool` prop, and whether it renders as presence
depends entirely on which element the component puts it on — `required` →
presence, `aria-hidden` → `"false"`. Correct for both, with the same call site.

## Codegen lowering

| site | condition | emit |
| --- | --- | --- |
| element | `catBool`, listed | `gw.BoolAttr(name, bool(expr))` |
| element | `catBool`, not listed | stringify `"true"`/`"false"` |
| element | `catAnyMixed`, listed | `gw.AttrAnyToggle(name, expr)` (§Defect 1) |
| element | `catAnyMixed`, not listed | existing ` name="` + `AttrAny` |
| element | `Toggle`-typed | `gw.BoolAttr(name, bool(expr))` |
| element | any other | existing stringify path |
| bare `name` | — | `gw.BoolAttr(name, true)` |
| component prop | — | bind the value; **no HTML decision here** |
| component fallthrough | — | bag entry as-is; `Spread` resolves at the leaf |

### Defect 1: `catAnyMixed` on a listed name

`T string | bool` is not `catBool`, so the list would never be consulted and
`<Mixed[bool] req={false}/>` would render `required="false"` — required.

**Resolution: defer only the boolness, never the name.** Codegen has the literal
name, so it resolves list membership at generate time; only the value's type is
unknown until runtime:

```go
_gsxgw.S("<input")
_gsxgw.AttrAnyToggle("required", req)   // bool at runtime → presence; else ="…"
_gsxgw.S("/>")
```

`boolNames` therefore never threads into a static attribute write. Both
instantiations are correct with **no annotation**:

```go
component Mixed[T string | bool](req T) { <input required={req} /> }

<Mixed[bool]   req={false} />   → <input/>
<Mixed[bool]   req={true}  />   → <input required/>
<Mixed[string] req={"foo"} />   → <input required="foo"/>
```

This is exactly the flexibility `?=` could not provide (§Rejected alternatives).

## Runtime

### `AttrAnyToggle`

```go
// AttrAnyToggle writes one complete attribute whose name IS presence-only
// (codegen resolved the list at generate time) but whose value type is known only
// at runtime. A bool-kinded value writes presence — ` name` or nothing — and any
// other value writes ` name="escaped"`.
func (gw *Writer) AttrAnyToggle(name string, v any)
```

It owns the whole span — leading space, name, optional `="…"` — which is what
lets it omit a name codegen would otherwise bake into a static string.

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
fabricating a value declared absent. The plain-bool list lookup lives in
`default` (no listed name is also a URL name).

The key is never modified, so last-wins dedup, `Get`/`Has`, class-merge and
caller-wins precedence keep working.

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

Unify onto one table **before** this work, as its own PR. It is independently
valuable, it shrinks this change to a single recognition site rather than a fifth
table, and it has an obvious differential test: for every value, `classify` /
`Val` / `anyRenderString` / `toStr` must agree.

**A string return is not sufficient.** Three consumers need the *kind*:
`AttrAnyToggle` and `Spread` to choose presence, and `Val` to keep PR #122's
`gw.S` win rather than routing numerics back through the escape pass.

```go
func anyRenderVal(v any) (s string, k valKind, ok bool)   // kindString, kindBool, kindNumber, …
```

**Matching must be by underlying type**, or `Flag(false)` never reaches the
lookup. `reflect.Kind` classification is the correct general behaviour, so it is
**the implementation** — one table, no special cases, nothing to keep in sync.

**Do not pre-build a fast path.** An earlier draft specified keeping the existing
exact-type switch in front of reflect "so `bool`/`string`/`int` never pay for it".
That is a guess wearing architecture's clothes: it assumes a cost nobody has
measured, on a path nobody has profiled, and it is exactly the fast-path-reasoned-
correct-in-isolation shape the repo's correctness-first/optimize-simple rule
exists to prevent.

The order is: ship the correct general version, **then measure** the spread and
attribute paths. If the numbers justify an exact-type fast path, add it as an
optimisation — proven byte-identical to the general path by a differential test
over every case, exactly as `gw.S` was earned in PR #122. If they don't, the
simpler code stands.

## Diagnostics

One. `Toggle` is `type Toggle bool`, so the Go compiler rejects a non-bool
argument — no `toggle-requires-bool` diagnostic is needed.

**Custom-element hint.** `<my-toggle active={b}/>` — a *hyphenated* tag (a custom
element, which gsx already identifies) carrying a bool-valued attribute **not** in
the list now renders `active="false"`, silently changing from today's bare
`active`. gsx cannot know the element's intent, so warn: "custom-element
attribute with a bool value renders as a string; use `gsx.Toggle(b)` for
presence". Scoped to hyphenated tags so it stays silent on `data-*`/`aria-*`,
where stringifying is correct.

Diagnostics live in the analyzer, never the formatter.

## Security

No new sink. A listed name and `Toggle` both write a bare name through the
existing `BoolAttr`, which emits ` ` + name and never a value, so no escaping
context is entered.

`Toggle` short-circuits the URL/image/srcset sinks (§Spread), which is safe
*because* it emits no value: sanitizers police a value, and there is none. This is
a narrowing, not a bypass — the only reachable output is ` ` + a
`validAttrName`-checked name. The inverse placement would be the unsafe one.

The change **removes** a security-adjacent defect: `contenteditable={false}` and
`draggable={false}` currently render as their opposite.

## Rejected alternatives

**`?=` syntax (templ's operator).** Chosen twice during design, then rejected —
`Toggle` obsoletes it on every axis:

- **It cannot travel.** An override must reach the leaf, because the leaf makes
  the decision. `Toggle` is a value, so it crosses component boundaries, rides in
  bags, and survives generics. `?=` is an annotation on a source position and can
  be passed nowhere — so it needed `Toggle` as a runtime carrier *anyway*. It was
  always sugar over the mechanism.
- **It punishes flexible components.** `?=` demands a static bool, so
  `open?={"foo"}` must error. A component author who wants a flexible prop (a type
  parameter, a value they do not want to constrain) therefore cannot use `?=` and
  falls back to `open={var}` — the default path. The syntax serves only the author
  who already knows it is a bool *and* is off-list, which is exactly what
  `Toggle(b)` covers.
- **It costs the whole syntax protocol** — parser, AST, formatter + fmt corpus,
  tree-sitter, vscode-gsx, CodeMirror — for sugar.

**Config (`gsx.toml` list table).** Rejected: `Toggle` already covers every name a
list cannot know, per call site, visible in the file you are reading. A config
table adds project-wide invisible state — a reviewer of one file cannot see that
`gsx.toml` redefined what `open` means.

**Bag key suffix (`"required?"`).** Rejected: `lastValidAttrIndexes` and `Get`
compare keys exactly, so `"required"` and `"required?"` are distinct keys in
dedup, lookup, exclusion and class-merge. A caller override would emit **both**
(`<input required="false" required>`), and browsers take the first — silently
losing the override.

**Name-list only, no override.** Rejected: a list gap would ship wrong HTML with
no author recourse. `Toggle` is what makes the list safe to make semantic.

## Migration

1. **Bags with listed names — no change.** `gsx.Attrs{{Key: "disabled", Value:
   true}}` still renders bare `disabled`.
2. **Non-listed names with bool values now stringify** — `data-open={b}`,
   `aria-expanded={b}`. This *is* the fix; no author action, but goldens,
   `examples/` and docs change.
3. **Custom elements using off-list bool attribute names** need
   `{gsx.Toggle(b)}`. Silent behavior change, caught by the custom-element hint.
4. `val.go` is unaffected: `Val(true)` renders text `"true"`, unchanged.

Sweep `../structpages` and `../one-learning` for cases 2 and 3.

## Testing

No fmt corpus (no syntax change). No sibling-repo work.

**Semantic corpus** — `input.gsx` + `generated.x.go.golden` + `render.golden`:

- listed name, bool true/false → presence
- non-listed name, bool → `="true"`/`="false"` (the reversal)
- enumerated regression: `aria-expanded`, `aria-hidden`, `contenteditable`,
  `draggable`, `spellcheck`, `data-*`
- **`hidden={false}` → omitted, NOT `hidden="false"`** — the invalid-value-default
  trap; `hidden={"until-found"}` → `="until-found"`
- `download={true}` → bare; `download={"a.txt"}` → `="a.txt"`
- **`notPresenceOnly` guard** — assert `contenteditable`, `draggable`,
  `spellcheck` are absent from the effective list; this is the test that stops a
  future refresh or "fix" reviving the inverted render
- **`presenceOnlyExtras` survives regeneration** — assert `hidden` and `download`
  are in the effective list even though `booleanAttrs` does not contain them
- `booleanAttrs` pinned, so a spec refresh is a reviewable diff
- listed name, **string** value → `="foo"` (the CSS-selector guarantee)
- `gsx.Toggle(true/false)` on a listed and a non-listed name
- `catAnyMixed` on a listed name (`AttrAnyToggle`): `Mixed[bool] req={false}` →
  `<input/>`, `req={true}` → `<input required/>`, `Mixed[string] req={"foo"}` →
  `required="foo"` — one component, three renders, no annotation
- `catAnyMixed` on a non-listed name → unchanged emission
- named bool (`Flag(false)`) on a listed name, static and through a bag
- component pass-down: `<Comp required={b}/>` → presence at an `input`;
  the same prop landing on `aria-hidden` → `="false"`
- bare attribute unchanged
- spread override: `required={false}` then `Toggle(true)` → single ` required`
- custom-element hint fires on `<my-toggle active={b}/>`, silent on `<div data-x={b}/>`
- **update `bool_exact_dispatch_required.txtar`** — its goldens flip; it becomes
  this design's regression test

**Runtime unit tests** — `Toggle` through `Spread` (true/false, dedup/override,
precedence against a plain-bool entry, `Toggle` on a URL key renders bare); plain
bool listed vs non-listed; named bool on a listed name; `AttrAnyToggle` kinds.

Regenerate with `-update` (rewrites `coverage.golden`), then verify without.
`make ci` before merge.

## Docs

- `docs/guide/` — attributes page: the four-line rule and the two overrides.
  Concise; rationale lives here, not the guide.
- `docs/ROADMAP.md` — record the 2026-06-18 reversal and why.

## Process

Per CLAUDE.md: this spec → implementation plan → subagent-driven execution with
per-task reviews → one **independent adversarial reviewer** (throwaway probe
programs, not diff reading) before merge. Feature work in a git worktree.

**Sequencing:** dispatch unification PR → this change.

## Risks / open questions

1. **List accuracy is load-bearing.** Derive it per §Derivation — programmatically,
   reviewed against the false-string test, cross-checked against two independent
   implementations. Mitigated but not eliminated by `Toggle`.
2. **`AttrAnyToggle` restructures a static emission site.** It is the one place
   where an attribute's name leaves the static string and becomes a runtime
   argument, so the coalescer, `//line` directives and the attr-hoisting paths all
   see a shape they have not seen before. Probe it explicitly rather than trusting
   goldens.
3. **`Toggle` on an enumerated attribute** (`aria-hidden={gsx.Toggle(b)}`) produces
   the invalid bare form. Not blocked, per §Goals — the author asked. Detecting it
   would need a second (enumerated) list; deferred.
4. **`reflect` cost is unmeasured — and stays that way until it is implemented.**
   Not a design input. Ship the correct general version, benchmark the spread and
   attribute paths, and let the numbers decide whether an exact-type fast path is
   worth its complexity (§Prerequisite). Do not pre-optimise on a guess; PR #122
   is the precedent — the "obvious" `IntInto` optimisation measured 2.2× *slower*
   on the common case, and only benchmarking found the change that actually won.
