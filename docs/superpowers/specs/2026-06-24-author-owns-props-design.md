# Design: author-owns-Props component model

**Date:** 2026-06-24
**Status:** Approved (brainstorm), pending implementation plan
**Worktree/branch:** `feat/author-owns-props`

---

## 1. Goal & scope

Today gsx **generates** a per-component props struct `<Name>Props` (for a method component, `<Recv><Name>Props`) from the component's parameter list, and a component is invoked only as a named-attr tag. Dogfooding gsx against three real codebases (the `structpages` examples, `~/work/one-learning` — 102 templ files / 272 method components, and `~/work/his-project/design-system`) showed this single decision fights real usage:

- **Per-method wrapper breaks shared-Props dispatch.** `structpages` calls `Props()` once and dispatches to `Page`/`Content`/`Partial` — *all sharing one author `Props` type*. gsx's distinct `XPageProps` / `XContentProps` can't be fed one `Props()` value. (`Page`+`Content` pairs are pervasive; one-learning has trios sharing `TableViewProps`.)
- **`<Name>Props` collides** with a hand-written type of the same name.
- **Migration friction.** Real components are invoked positionally / by struct (`@DisplayField(...)` 304×, `@p.Content(props)` 75×), and a mature design-system already documents the convention *"every component takes a single `p Props`; zero value = defaults."*

**This redesign:** the **author owns the `Props` type**; gsx generates no wrapper when the component's sole non-receiver parameter is an author-declared struct. A heuristic keeps the convenient generated path for simple inline-param components.

**In scope:** the byo heuristic + codegen; tag field-mapping + whole-struct splat (`{ x... }`); the default field matcher (identifier + kebab→Camel) and the `gen.WithFieldMatcher` extension; explicit `Children`/`Attrs` on the byo path; method-component pass-through; the spread-operator migration (leading→trailing); LSP preservation.

**Out of scope:** changing the generated path's behavior for inline params (kept as-is); auto-fallthrough redesign on the generated path (kept); a structpages-side change (none needed — gsx now emits structpages-compatible signatures).

**Global constraints:** runtime stays stdlib-only; emit ≡ probe; escaping unchanged; the LSP's skeleton + `ExprMap` + `//line` discipline preserved.

---

## 2. The heuristic — param shape decides

For a component `component [recv] Name(params…)`, look at the **sole non-receiver parameter**:

- **Single parameter whose resolved type is a named `struct`** → **bring-your-own (byo)**: use that type directly; generate **no** wrapper. (`component Button(p Props)`, `component (p Home) Page(d HomeData)`.)
- **Zero params, multiple params, or a single non-struct param** (scalar / `gsx.Node` / slice / interface) → **generated**: `NameProps{…}` exactly as today. (`component Card(title gsx.Node, n int)`, `component Greeting(name string)`, `component (p P) Grid(sort string)`.)

The discriminator ("is the lone non-receiver param a named struct?") is resolved via `go/types`. It is *discoverable*: writing `(p Props)` opts you onto the explicit path.

Receiver params are never counted. A nullary method component (`component (p Home) Page()`) stays as today (receiver is the page data; no props struct).

---

## 3. Bring-your-own-Props model

`component Button(p Props) { … }` → `func Button(p Props) gsx.Node { … }` — the author's `Props` (same package), no wrapper. The body refers to `p.Field`. Optional props are the struct's zero values (the documented convention).

`Props` may be declared **in the `.gsx` file** (a `GoChunk` type) or in an external `.go` file in the same package (e.g. structpages' `Props()` returns it). Both are supported; §9 covers how codegen learns its fields.

---

## 4. Tag invocation — field-build (default) + whole-struct splat

A byo component is invoked two ways at a tag:

### 4a. Field-build (default)
`<Button variant="primary" featured full-width data-id="7">Save</Button>` →
```go
Button(Props{
    Variant:  "primary",
    Featured: true,           // bool field → bare attr = true
    FullWidth: true,          // kebab→Camel (§5)
    Attrs:    gsx.Attrs{"data-id": "7"},  // no matching field → explicit Attrs field
    Children: gsx.Func(… "Save" …),       // children → explicit Children field
})
```
Each identifier/kebab attr maps to a field (§5); an attr with no matching field routes to the `Attrs` field; children route to the `Children` field. Untyped-string-const attrs assign to named-string fields (`variant="primary"` → `Variant Variant`).

### 4b. Whole-struct splat — `{ x... }`
`<Button { data... } />` → `Button(data)`. Passes a prebuilt struct as the whole prop value — the dominant real pattern (`@p.Content(props)`, structpages `Page(data)`). Splat is all-or-nothing (no field overrides alongside it; build/modify the struct first if needed).

`{ x... }` is gsx's spread operator, **overloaded by context** (§8): on an element it spreads `gsx.Attrs` as HTML attributes; on a component it splats the param struct. The choice needs no type resolution (the tag — element vs component — decides).

---

## 5. Field matcher — default + extension

Mapping an attr name to a `Props` field. The default tries to find a **matching exported field**; if none exists, the attr **falls through** to the `Attrs` field:

1. **identifier** → Go-capitalized: `variant`→`Variant`, `fullWidth`→`FullWidth`
2. **kebab→Camel**: `full-width`→`FullWidth`, `aria-label`→`AriaLabel`
3. **no field of that name** (e.g. `data-id`, `hx-get`, `@click`) → fallthrough to the `Attrs` field

The **field-exists-else-fallthrough** rule is load-bearing: it binds `full-width` when the struct has `FullWidth`, while letting `data-*`/`hx-*`/non-identifier attrs flow to the bag.

**Extension (mirrors `WithJSAttrs`/`WithURLAttrs`/`WithAttrClassifier`):**
```go
// fields: the author struct's exported field names (from go/types).
// Return the matched field + true, or "", false to fall through to Attrs.
gen.WithFieldMatcher(func(attr string, fields []string) (field string, ok bool))
```
Default matcher = rules 1–3 above. A project registers its own (other conventions). Like the attr-classifier extensions, the resolved matcher folds into the build-cache manifest (cache-key correctness) and is reportable via `gsx info --json`.

---

## 6. Children & Attrs on the byo path (explicit)

The byo path is **explicit** — `Children`/`Attrs` are real fields the author controls, not a hidden bag:

- **`{children}`** requires the `Props` to have a `Children gsx.Node` field. If missing: a clear codegen error — `"component Button uses {children} but Props has no `Children gsx.Node` field"`. When `Props` is declared **in the `.gsx` file**, gsx may auto-add the field; for an external `.go` struct the author adds it.
- **Fallthrough** requires an `Attrs gsx.Attrs` field (the field rule routes unmatched attrs there). No field → unmatched attrs are an error (`"attr `data-id` matches no Props field and Props has no `Attrs gsx.Attrs` field"`). The author spreads it in the markup (`<div { p.Attrs... }>`), exactly the design-system `{ p.Attributes... }` idiom.

The **generated path keeps today's behavior**: auto `Children` field + auto class-merge/spread fallthrough.

---

## 7. Method components & structpages

The heuristic applies to the sole non-receiver param:

```go
type pageData struct{ Title string }
component (p Home) Page(d pageData)    { <html><body><h1>{ d.Title }</h1>{ p.nav() }</body></html> }
component (p Home) Content(d pageData) { <h1>{ d.Title }</h1> }
component (p Home) Partial(d pageData) { … }
```
→ `func (p Home) Page(d pageData) gsx.Node`, `…Content(d pageData)…`, `…Partial(d pageData)…` — **all take the author's `pageData` directly**. So:
- `structpages` calls `Props() (pageData, error)` then dispatches `Page(pd)` / `Content(pd)` / `Partial(pd)` — **one shared type**. The gap-#2 blocker is gone; no structpages change needed.
- Tag-invoke a method component: field-build `<p.Content title="Hi"/>` → `p.Content(pageData{Title:"Hi"})`, or splat `<p.Content { pd... }/>` → `p.Content(pd)`.

A nullary method component (`Page()`) is unchanged (receiver is the data).

---

## 8. Spread operator — Go-convention trailing, overloaded by context

gsx currently spreads with **leading** dots `{...attrs}` (printer.go) — divergent from both Go (`f(x...)`) and templ (`{ p.Attributes... }`). Migrate to **trailing** `{ x... }`, honoring "inside `{}` it's Go":

- **element** `<div { attrs... }>` (attrs is `gsx.Attrs`) → HTML attribute spread (today's behavior, new syntax).
- **component** `<Card { data... } />` (data is the byo param struct) → `Card(data)` whole-prop splat (§4b). Splat applies only to **byo** components (which have a single author struct to splat); a **generated**-path component — inline params, no single author struct — is field-build only.

One operator, context-disambiguated (no type resolution to choose). This is a **breaking syntax change**: every existing `{...x}` becomes `{ x... }`. The formatter (`gsx fmt`) rewrites it mechanically; the corpus and the rewritten structpages examples are migrated as part of the work.

---

## 9. Codegen impact

byo prop fields are **type-driven** (the author struct's fields via `go/types`) vs today's cheap **AST-derived** param names. The field-build literal `Props{Field: x}` must know `Props`'s fields *before* the skeleton is resolved — the same probe/pre-resolution shape as `gsx.Val`:

- **`Props` declared in the `.gsx`** (GoChunk): gsx parses the struct decl → knows fields → builds the literal in both emit and probe. No extra pass.
- **`Props` external (`.go`)**: a **preliminary `go/types` load** of the package's existing `.go` files (which are valid Go independent of the `.gsx`) enumerates `Props`'s fields; then the skeleton/emit build the literal. The whole-struct splat (`{ x... }`) needs **no** field knowledge (it emits `Comp(x)`), so the preliminary load is only needed for field-build tags.

**emit ≡ probe:** field-build and splat are emitted identically (modulo `rtPkg` alias) by `childPropsLiteral` on both paths — the byo field set comes from the same resolved `Props` type for both. The `nodeProps` (node-field) signal for promotion is derived from the author struct's `gsx.Node` fields rather than AST params.

`childPropsLiteral` / `componentPropFieldsFor` grow a byo branch (type-driven fields) beside today's AST-derived one. `version.go` bumps (emit changes).

---

## 10. LSP impact (preserve, then improve)

The LSP (`gen/lsp.go` builds the codegen skeleton in-memory; `analysis.go`'s `ExprMap` maps each gsx `Interp`/`ExprAttr` → skeleton `go/ast` expr honoring `//line`; `definition.go` does tag-name→decl and Go-expr→gopls) does **not** reference the generated `<Name>Props`.

- **Preserve:** byo codegen MUST still emit a valid skeleton, populate `ExprMap` for attr/interp value exprs, and keep `//line` maps — so tag-name→decl and expr→gopls keep working. This is a hard requirement, validated by the existing LSP corpus.
- **Improve (future):** attr→field go-to-def/completion resolves to the author's **real** `Props.Variant` field (stable source) rather than a synthetic generated type — strictly better than the generated model would have been.

---

## 11. Backward compatibility & migration

- **Generated path unchanged:** inline-param + nullary components behave exactly as today (`Card(title gsx.Node, n int)` → `CardProps`). No churn.
- **Flips to byo:** a component whose sole non-receiver param is a named struct (e.g. existing `methods/` case `Row(user User)`) now passes through. Its generated golden + any field-build call sites change; audit `methods/` and update.
- **Spread migration:** `{...x}` → `{ x... }` corpus-wide + in the rewritten structpages examples (formatter-assisted). The structpages `htmx-render-target`/`blog` Props **workarounds** are removed (net simplification).
- **Hand-written references to a generated `<Name>Props`** for a now-byo component break (intended — the author owns the type).

---

## 12. Testing

Seeded RED cases (committed `d6477d2`): `props/byo_single_struct`, `props/byo_method_shared_props` (reproduces gap #2). Add, per the spec:

- **byo field-build**: identifier + bool-bare + kebab→Camel + fallthrough-to-`Attrs` + `Children`; pin generated `Props{…}` + render.
- **byo whole-struct splat** `{ x... }`: a tag and a method (`<p.Content { pd... }/>`); pin `Comp(x)` + render.
- **byo external `Props`** (struct in a sibling `.go`): proves the preliminary type-load.
- **method trio sharing one `Props`** end-to-end (Page/Content/Partial), plus an `httptest`-style structpages render in the examples.
- **heuristic boundaries**: single-scalar param → generated; multi-param → generated; single-struct → byo.
- **children-missing-field** and **fallthrough-missing-`Attrs`** → clear errors.
- **spread migration**: element `{ attrs... }` renders; a `{...x}` (old) is a parse error or formatter-rewritten — pin the decision.
- **`gen.WithFieldMatcher`**: a custom matcher overrides the default (unit/integration).
- **LSP**: existing LSP corpus stays green; a byo component's tag-name→decl + expr→gopls resolve.

Every syntax change ships per-context corpus coverage (project rule). `go build`/`vet`/`test ./...` green; `gsx fmt` faithful+idempotent over the migrated corpus.

---

## 13. Risks

- **Probe/pre-resolution for external `Props`** (§9) — the preliminary type-load is the riskiest new machinery; mirror `gsx.Val`'s discipline and pin with the external-`Props` corpus case.
- **Spread migration is breaking** — wide but mechanical; the formatter rewrites it; corpus + examples updated in-PR.
- **emit ≡ probe for byo field-build** — the byo field set must be identical on both paths; derive from the one resolved `Props` type.
- **LSP regression** — keep `ExprMap`/`//line` intact; the existing LSP corpus is the guard.
- **Heuristic surprise** — a component author expecting a generated wrapper but writing a single struct param gets byo; the discriminator is documented and `gsx info` can report which path a component took.
