# gox — Templating Language Design

**Date:** 2026-06-18
**Status:** Approved (design); runtime model decided — lazy streaming,
`templ.Component`-compatible (§10)
**Module:** `github.com/goxhq/gox`

## Summary

gox is a templating language for Go with code generation. It pairs a **templ-style
declaration** (`component X(…) { … }`) with a **JSX-style markup body** (HTML-like
tags, Capitalized component tags, `{ }` interpolation). Source files (`.gox`) hold
ordinary Go (imports, types, `func`s) plus `component` declarations; a generator
lowers each `component` to plain Go in a generated `.x.go` file that the Go
compiler type-checks and builds.

gox is **templating only** — no router, handler, or HTTP machinery. It is a way to
write HTML as a first-class, composable Go value.

### Mnemonic / vocabulary

- **`Node`** — the universal renderable. The Go type `gox.Node` is an **interface
  with the identical method set to `templ.Component`**:
  `Render(ctx context.Context, w io.Writer) error`. A node renders **elements**,
  **text**, **fragments**, **raw** HTML. Because the method set matches, a
  `gox.Node` is accepted anywhere a `templ.Component` is expected (structpages and
  other templ-ecosystem tools) — **without importing templ** (§10).
- **`Element`** — what a node renders for a concrete HTML element (tag + attributes
  + children). Produced by lowercase/hyphenated tags: `<div>`, `<el-dialog>`.
- **`Component`** — a function `Props → Node`, declared with `component`, invoked
  with a Capitalized or dotted tag: `<Card>`, `<ui.Button>`, `<p.Content>`. It
  lowers to `func X(Props) gox.Node` returning a render closure.

### Relationship to templ, and the lessons borrowed

gox is informed by, but shares no code with, an experimental templ branch that
tried to add JSX-like inline components. Lessons carried over:

1. **Symbol resolution is the tar pit.** Mapping attributes onto *positional*
   parameters and resolving whether a lowercase tag is a component drove templ to
   a ~5,000-line `symbolresolver` on `go/packages` + overlays, hitting overlay
   module-boundary bugs (golang/go#71075, #71098) and perf cliffs. gox avoids this
   whole class of work: the `component` keyword identifies templates, casing
   decides component-vs-element, and gox **generates every component's props
   struct so it always owns the field names** (§3) — generated code is plain Go
   the compiler checks.
2. **Find Go boundaries, don't re-parse Go.** templ's `goexpression` locates where
   a Go span ends rather than re-implementing a Go parser. gox does the same for
   embedded expressions and hands the rest to the real `go/parser` (§9).
3. **Markup-vs-Go-expression detection is subtle.** `{ <div/> }` (markup) vs
   `{ a < b }` (Go) is resolved positionally — the Babel rule (§9).

## Guiding Principles

- **Stay close to HTML and to Go.** Markup looks like HTML/JSX; helpers, variant
  functions, and everything non-template are ordinary Go.
- **Syntax tidiness is the top priority.** Parser complexity bends to serve clean
  syntax, never the reverse. We do targeted Go-expression boundary parsing where
  it buys tidier markup.
- **Lean on the Go compiler.** Generated code is plain Go; prop names, types, and
  field correctness are validated by the compiler.
- **Type-aware where it pays; resolution-free where it doesn't.** gox **is a real
  compiler** — it runs the Go type checker over the package and uses resolved types
  to render *values* precisely (interpolation and attribute values — §5), so
  `{ count }` (an `int`) needs no `strconv`. This applies the templ-fork's resolver
  know-how (single `packages.Load`, module grouping, `LoadSyntax`) where it adds
  power. But gox never *depends* on resolution for **structural** decisions:
  component identity is **casing**, props binding is generated **`XProps`** +
  compiler check. Identity/props stay robust and resolution-independent; type
  resolution is layered on for value rendering.
- **Built-in behaviors are syntax, not runtime calls — the runtime stays
  implicit.** Where gox has a built-in behavior (escaping, class composition,
  conditional classes, attribute spreading), it is expressed in gox *syntax* and
  lowered to an implicit runtime; the author does not call helpers like `gox.KV`.
  This is the deliberate departure from templ, which surfaces `templ.KV`,
  `templ.URL`, `templ.Classes`, etc. and so makes templates "Go with HTML bolted
  on." A `component` body may freely contain Go expressions and (via `{{ }}`)
  statements — but gox's own behaviors do not require runtime calls. Runtime
  functions appear in source **only when they are obviously explicit intent** —
  trusted-value opt-outs (`gox.Raw`, `gox.SafeURL`, `gox.SafeCSS`, `gox.SafeJS`)
  and explicit encoders (`gox.JSON`).

## 1. File & Package Model

- Source extension **`.gox`**; generated output **`.x.go`**, written beside it.
- A `.gox` file is ordinary Go — `package`, imports, `type`/`const`/`var`, `func` —
  **plus** `component` declarations. Everything that is not a `component` is plain
  Go, carried through verbatim. Each `component` is lowered.
- The `.gox` file is **not** compiled by Go; only the generated `.x.go` is.
- Once generated, a `.gox` participates in its package normally; a `.gox` and a
  hand-written `.go` in the same directory share the package.

## 2. Elements vs. Components

Decided by the tag, **no symbol resolution**:

- **Element** — lowercase tag or a tag containing `-` (`<div>`, `<el-dialog>`).
  Attributes are emitted as literal HTML; subject to escaping (§5).
- **Component** — Capitalized tag or dotted tag (`<Card>`, `<ui.Button>`,
  `<p.Content>`). Lowered to a function/method call (§3).

A dotted tag's left identifier is disambiguated using the **parsed scope** that
`go/parser` already gives us: if it is an imported package → cross-package
component (`<ui.Button>`); if it is a local variable/receiver → method component
(`<p.Content>`). No type-checking required.

## 3. Components: declaration, props, children

### Declaration

A component is declared with `component`. It has **no return type and no `return`**
— it implicitly produces a `Node` and may implicitly propagate errors (§5, `?`).

```go
component Card(title string, featured bool) {
    <section class="card">
        <h2>{title}</h2>
        { if featured { <span class="badge">Featured</span> } }
    </section>
}
```

Method components use a receiver (the Page/Content pattern from real projects):

```go
component (p ProductPage) Content(rows []Row) {
    <div class="report">
        <h1>{p.Title}</h1>
        { for _, r := range rows { <div>{r.Name}</div> } }
    </div>
}
```

### Inline params → generated `XProps`

Params are written inline (templ-style). The generator emits a props struct and a
wrapper:

```go
// component Card(title string, featured bool) { … }   generates:
type CardProps struct {
    Title    string
    Featured bool
}
func Card(p CardProps) gox.Node { return gox.Func(func(ctx, w) error { … }) }
```

The returned `gox.Node` is a render closure (`Render(ctx, w) error`); errors stream
out of `Render` (see §5, §10, and the codegen walkthrough). There is no
construction-time `error` return — the function itself does not fail; rendering
does.

- **Attribute → field/param mapping (default rule, pluggable).** The default is
  **first-letter-upper**: `title` → `Title`; an already-uppercase attribute is
  verbatim (`URL` → `URL`). Chosen over *strict-equal* (which would force
  PascalCase attributes, fighting the HTML/JSX feel). The default is
  **resolution-free** — gox transforms and emits `XProps{Title: …}`; the Go
  compiler validates the field. Go **initialisms** (`id`→`Id`, `url`→`Url`) are the
  known friction; write the uppercase form (`<Card ID="x"/>`) or install a mapper
  extension. The mapping rule is an **extension point** (§11): a case-insensitive /
  initialism-aware mapper (`id`→`ID`, using the resolved field set) or a
  kebab/snake→Pascal mapper can replace the default. Smarter mappers are opt-in so
  the default stays resolution-free and robust.
- **gox owns the generated field names**, so binding is deterministic and stays
  resolution-free **even cross-package**: `<ui.Button variant="x"/>` →
  `ui.Button(ui.ButtonProps{Variant: "x"})` via the generated `XProps`.
- **`ctx` is ambient — never declared.** Every component body has an implicit
  `ctx context.Context` in scope (the render `ctx`), exactly like templ. You do
  **not** declare it as a parameter; just use `ctx`. It is never an attribute and
  never passed at a call site: `<p.Content/>` just works.
- A plain `func … gox.Node` remains a manual escape hatch (explicit `return`); but
  `<X/>` tags resolve to `component` declarations.

### Two organizational styles (both supported)

Both appear in the real codebases and serve different jobs (see the
**component-styles** doc, `2026-06-18-gox-component-styles.md`):

- **Function components in a package** — reusable component libraries (`ds/*`).
  One package per family; each part is a `component` function; props come from
  inline params (→ `XProps`) or a bring-your-own struct.
- **Method components on a struct** — app page composition + partial rendering
  (structpages). The **receiver struct is the page data** (`p.Field`, built once in
  Go); each method renders the page or a swappable partial. Method *params* (if
  any) become a generated `<Receiver><Method>Props` — receiver-prefixed to dodge
  collisions (`DetailPage.Body` vs `EmptyPage.Body`) — referenced by bare name; a
  nullary method has no props struct.

The correctness seam is `<x.Foo/>`: `go/parser` scope decides it with no
ambiguity — no dot → same-package function; `x` is an import → cross-package
function; `x` is a local var → method.

### Bring-your-own props struct

When a component's single parameter is an **existing named struct type** (e.g. a
protobuf/sqlc-generated type), gox uses *that type* as the props — it generates
nothing, avoiding duplication. Fields are referenced qualified (`u.Name`):

```go
component UserCard(u pb.User) {        // pb.User IS the props; no UserCardProps generated
    <div class="card"><h3>{u.Name}</h3><p>{u.Email}</p></div>
}
// <UserCard {...someUser}/>          → UserCard(someUser)              (spread a whole value)
// <UserCard name="Jo" email="x"/>    → UserCard(pb.User{Name:"Jo", …}) (set fields)
```

Disambiguation: a **single** param of an existing named struct → bring-your-own
(qualified refs); inline scalars / multiple params → generated `XProps` (bare
refs). Caveat: implicit `children`/`attrs` only auto-add fields to a struct gox
generates — a bring-your-own struct must already declare `Children gox.Node` /
`Attrs gox.Attrs` if the body uses them.

### `children` and `attrs` (see also `examples/12_children_attrs.gox`)

**`children` — explicit placement.** Reference `{children}` where nested markup
should go (every framework requires this — there is no sensible default position).
Referencing it adds a `Children gox.Node` field. Passing children to a component
that **never places `{children}`** is a **compile error** (the content would
vanish) — not silently dropped.

```go
component Card(title string) {
    <section class="card"><h2>{title}</h2>{children}</section>
}
```

Named slots are ordinary `gox.Node` params, placed explicitly:

```go
component Panel(header gox.Node, footer gox.Node) {
    <div class="panel"><div class="head">{header}</div>{children}<div class="foot">{footer}</div></div>
}
// <Panel header={ <h1>Title</h1> }>…</Panel>
```

**`attrs` — automatic fallthrough (Vue-style), with an ambiguity guard.** This
fixes templ's missing rest-attrs:

- **Default:** undeclared attributes (those not matching a declared prop)
  automatically apply to the component's **single root element** — no `{...attrs}`,
  no declaration. `class`/`style` **merge** into the root's list; others are added.
  You no longer declare a `class` prop just to let callers add classes.
- **Touching `attrs` disables auto-fallthrough.** Reference `attrs` — spread
  `{...attrs}` *or* a method call — and you take over placement (e.g. route attrs
  to a non-root element). Same trigger as Vue's `inheritAttrs:false`.
- **Ambiguous root → compile error.** If a component has no single root
  (fragment / multiple / conditional roots) and a caller passes undeclared attrs
  without an explicit `{...attrs}`, that is a generation-time error — never
  guessed.

The prop-vs-fallthrough split is decided by the component's declared params, which
the **type-aware compiler knows** (even cross-package).

**`attrs` is a rich built-in `gox.Attrs`, not an opaque bag** — gox ships the
utilities so nobody hand-rolls `classFromAttrs`/`hasAttr`/`extractAttr`:
`attrs.Class()` (merged class string), `attrs.Has(key)`, `attrs.Get(key)`,
`attrs.Without(keys…)`, `attrs.Take(key) (value, rest)`, `attrs.Merge(other)`,
`attrs.With(k, v)`. Splitting the bag is just `rest := attrs.Without("class")` —
no special destructuring syntax; Go multi-return covers it.

## 4. Attribute Value Forms

- `name="literal"` — static string.
- `name={ expr }` — any Go expression. On an element, stringified + escaped per
  context; on a component, must match the field type (compiler-checked).
- bare `name` — boolean `true` (`<input disabled>`, `<Modal open>`).
- `{...expr}` — spread (§6).

### Boolean attributes are type-driven

No `?=` operator. A `name={ expr }` whose value is **bool-typed** gets
boolean-attribute semantics — bare when `true`, omitted when `false`. gox knows the
value's type at compile time (§"type-aware", Principles), so it emits boolean-attr
code directly; a string-typed value emits an ordinary escaped attribute.

```go
<input disabled={ p.Disabled } />     // true → <input disabled>, false → <input>
```

### Conditional attributes — two forms (both supported)

1. **In-tag statement embedding** for one-offs:
   ```go
   <input { if required { required } } { if id != "" { id={id} } } />
   ```
2. **Spread** for bulk/dynamic (§6). The two compose on one element.

### `class` and `style` are composable (built-in syntax, no runtime calls)

`class` and `style` are **special composable attributes**. Their `{ }` value is a
**comma-separated list** of contributions (a gox grammar extension, not a single Go
expression). Conditional contributions use the **`"classes": cond` sugar**
(clsx / `map[string]bool` style) — *not* a runtime helper:

```go
<a class={
    "group flex gap-x-3 rounded-md p-2",     // unconditional
    "bg-gray-100 text-blue-600": isActive,   // included when isActive
    "text-gray-700 hover:bg-gray-50": !isActive,
    class,                                    // caller override, wins last
}>
```

- Each contribution is either an unconditional string/`[]string`/string-expr, or a
  **`<classes-expr> : <cond-expr>`** conditional (the classes are emitted when the
  bool `cond` is true). Commas and the `:` split at bracket depth 0, so a Go
  expression containing `:` or `,` inside brackets is one contribution.
- There is **no `gox.KV` / `gox.Classes` in source** — this is the built-in
  conditional-class behavior expressed as syntax; gox lowers it to an implicit
  runtime.
- Contributions are flattened, empties dropped, joined (space for `class`, `;` for
  `style`), then run through a **pluggable merger** — default dedupe/join; install a
  Tailwind-aware merger (e.g.
  [tailwind-merge-go](https://github.com/jackielii/tailwind-merge-go)) via the
  extension mechanism (§11) to resolve conflicting utilities.
- Static `class="a b c"` still works.

### Special attribute names

Element attribute names may contain `-`, `:`, `@`, `.`, `_`, `::` (`hx-get`,
`:class`, `@click.away`, `hx-on::click`, `x-data`, hyperscript `_`). Lexed as
opaque names, emitted verbatim.

## 5. Body: emission, control flow, interpolation, errors

A `component` body is **emission-style and markup-only**: markup written in the
body is the result, there is no `return`, and **bare Go statements are not allowed**
anywhere in it (a half-Go / half-template body reads badly). A component body
contains *only* markup, `{ }` interpolation/control-flow, and `{{ }}` Go-statement
blocks.

To run Go inside a component — declare a local, capture `(val, err)`, build a
markup value — use the **`{{ }}` escape hatch**. For heavier logic, write an
ordinary **`func`** instead: a `func` body is plain Go, markup is a returnable
value there (`return <div/>`, `x := <div/>`), and you call it from markup via
`{ helper(...) }`. This keeps the two block kinds cleanly separated:

- **`func` block** — ordinary Go; markup is a value; `return` to produce a Node.
- **`component` block** — markup-only emission; Go only via `{{ }}`; no `return`.

Three brace forms inside a component body, by leading token:

- **`{ expr }` — interpolation, type-aware.** gox resolves `expr`'s Go type and
  renders it accordingly — no manual conversions for common types:

  | resolved type | rendering |
  |---------------|-----------|
  | `string`, `[]byte` | HTML-escaped text |
  | numerics (`int`, `float64`, …) | formatted, then text |
  | `bool` | `true`/`false` text (in an attribute → boolean-attr semantics, §4) |
  | `gox.Node` (anything with `Render(ctx,w) error`) | rendered inline |
  | `[]gox.Node` | each rendered in order |
  | `fmt.Stringer` | `.String()`, then HTML-escaped |
  | `gox.Raw` / `gox.SafeURL` / … | per their opt-out semantics |
  | anything else | **compile-time error** (clear message) |

  Because the type is known at compile time, the generator emits the precise
  rendering call (`gw.Text`, `gw.Int`, `gw.Node`, …) — efficient and type-checked,
  no runtime reflection. Attribute values resolve the same way (e.g. a `bool`
  attribute value → boolean attribute; a URL-context attribute → sanitized).
- **`{ if … }` / `{ for … }` / `{ switch … }` — control flow.** Markup bodies
  contribute children (or attributes, in a tag — §4).
- **`{{ stmt }}` — Go statement escape hatch.** Pure Go statements with no output —
  local declarations, `(val, err)` handling, a derived value reused by several
  siblings. The *only* way to run Go statements in a component body; usable at the
  top of the body or between markup siblings. Markup is a value inside it
  (`{{ header := <h1>…</h1> }}`).

### Native `(T, error)` — the `?` try-marker

A `?` suffix on an expression unwraps a `(T, error)` call: it uses `T` and lowers
to `if err != nil { return err }` **inside the component's `Render` method**,
short-circuiting the render. Because every component's `Render` returns `error`
unconditionally (§10), `?` is *always* valid in a component body. Zero type
resolution — the marker declares intent (Rust/Zig `try`).

```go
component UserForm() {            // ctx is ambient, not declared
    <form
        hx-post={ route.URL(ctx, CreateUser{})? }
        hx-target={ route.IDTarget(ctx, UsersList{}) }
    >
        <h1>{ pageTitle(ctx) }</h1>
        <li>{ render(item)? }</li>
    </form>
}
```

Nested component tags propagate failures automatically — rendering a child calls
its `Render`, whose error streams out of the parent's `Render`. Errors are never
ignored or panicked. (`?` only lacks a return path inside a hand-written plain
`func` that returns no `error`; there it is a generation-time error.)

### Other constructs

- **Fragments:** `<>…</>` groups siblings without a wrapper.
- **Escaping — automatic and context-aware (safe by default).** Like Go's
  `html/template`, gox chooses the escaper from the *context* of each interpolation,
  determined at codegen from the value's position. The author never wraps a value
  for safety; helpers exist only to **opt out** when a value is trusted:
  | Context | Determined by | Default | Opt-out |
  |---------|---------------|---------|---------|
  | HTML text | element body | HTML-escape | `gox.Raw(s)` |
  | HTML attribute | `name={ … }` | attribute-escape | — |
  | URL attribute | `href`/`src`/`action`/`formaction`/`poster`/`cite`/`srcset`/… | sanitize scheme (neutralise `javascript:` etc.) | `gox.SafeURL(s)` |
  | CSS | `style` / `<style>` body | CSS-escape | `gox.SafeCSS(s)` |
  | JS | `on*` handlers / `<script>` body | JS-escape | `gox.SafeJS(s)` |
  There is no `gox.URL(...)` wrapper — sanitizing is the default for URL attributes;
  `gox.SafeURL` is the trusted opt-out. The generator knows the attribute name and
  element, so it picks the escaper with no type resolution.
- **Comments:** HTML comments `<!-- … -->` pass through.
- **Raw-text elements:** `<script>`/`<style>` bodies are raw text, not markup;
  `{ expr }` interpolation is still available, escaped for the JS/CSS context
  (exact rules: open question).

## 6. Spread / Rest

### Explicit spread — `{...expr}`

- **On an element:** `expr` is `gox.Attrs`, merged into the element's attributes at
  runtime. No resolution.
  ```go
  <div class="card" {...attrs} id={id}>…</div>
  ```
- **On a component:** lowered to copy-then-override, compiler-checked:
  ```go
  <Button {...base} variant="primary"/>
  // → Button(func() ButtonProps { p := base; p.Variant = "primary"; return p }())
  ```

### Rest attributes (fallthrough)

Covered in §3: undeclared call-site attributes **automatically fall through** to the
component's single root element (no `{...attrs}` needed); `class`/`style` merge.
Reference `attrs` (spread or a `gox.Attrs` method) to take over placement; an
ambiguous root with undeclared attrs and no explicit `{...attrs}` is a compile
error.

### Merge semantics (proposed)

Source order applies; `class`/`style` concatenate (then run through the configured
merger); other keys last-wins; bool attrs render when true.

## 7. Worked Example

```go
// views/page.gox
package views

import (
    "context"

    "github.com/goxhq/gox"
    "github.com/goxhq/gox/examples/ui"
)

component Card(title string, featured bool) {
    <section class={ "card", "card-featured": featured }>
        <h2>{title}</h2>
        {children}
    </section>
}

component (p ReportPage) Content(rows []Row) {     // ctx ambient
    <main>
        <h1>{p.Title}</h1>
        <ui.Button variant="primary" hx-get={ route.URL(ctx, Refresh{})? }>Refresh</ui.Button>
        <Card title="Recent" featured>
            <ul>
                { for _, r := range rows { <li>{r.Name}</li> } }
            </ul>
        </Card>
    </main>
}
```

## 8. (reserved)

## 9. Parser / Codegen Pipeline

The `component` keyword makes template regions explicit, which simplifies the
pipeline:

1. **Scan** the file for `component` declarations (brace-balanced bodies).
   Everything outside is plain Go.
2. **Plain Go** (imports, `func`s, types) → parsed by the real **`go/parser`**.
3. **Each `component` body** → gox's markup parser. Markup islands are found via
   the **Babel rule**: inside `{ … }`, a `<` starts markup only in
   expression-start position followed by a tag-name letter / `/` / `>`; otherwise
   `<` is a Go operator. Embedded Go expressions are delimited with a
   `goexpression`-style boundary finder; the real `go/parser` validates them.
4. **Generate** the `XProps` struct + wrapper func with the lowered body
   (interpolations, control flow, `?` error checks, class/style composition,
   spread, implicit children/attrs).

We never re-implement a Go parser. Targeted Go-expression boundary parsing is used
where it yields tidier syntax (per the principles); we do not shy away from it.

## 10. Runtime Model — Lazy Streaming, `templ.Component`-compatible

**Decided.** `gox.Node` is an interface whose method set is **identical to
`templ.Component`**:

```go
type Node interface {
    Render(ctx context.Context, w io.Writer) error
}
```

- A `component` lowers to `func X(Props) gox.Node` returning a **render closure**
  (`gox.Func(func(ctx, w) error { … })`) that writes HTML straight to `w`.
  Streaming, near-zero allocation (no per-node tree).
- **Ecosystem interop is the deciding reason.** Because the method set matches
  `templ.Component`, gox output is accepted anywhere a `templ.Component` is
  expected — **structpages** and other templ-ecosystem tools work with gox
  generation **without gox importing templ** (structural interface satisfaction).
  gox keeps its **own** interface — no `templ` alias, no `templ` dependency.
- **Errors stream out of `Render`** and are never ignored or panicked; `?` lowers
  to `return err` inside `Render` (§5).
- Generated code uses an error-threading writer (`gox.W(w)`) and built-in runtime
  calls (`gw.Class`/`gw.URL`/`gw.Text`/`gw.Spread`/`gw.Node`); source stays sugar.

The eager node-tree (vdom) alternative was rejected: it would not be
`templ.Component`-compatible and would allocate a node per element. See the
**codegen walkthrough** (`2026-06-18-gox-codegen-walkthrough.md`) for hand-written
generated code validating this model.

## 11. Extensions — direction only; API deliberately deferred

**Decision: do not design the extension/hook API yet.** Fixing hook interfaces
before the parser, compiler, and runtime exist would be premature and overly
constraining. The shape is best discovered from the implementation, not guessed.

What is settled is the **direction**:

- gox does **not** bake in framework-specific behavior (e.g. Tailwind class
  merging). Such things are **external libraries**.
- The preferred integration is **compile-time transformation** over runtime
  function calls — a compile-time hook can statically merge, optimize, and validate
  in ways a runtime call cannot. (e.g. a class merger statically merges constant
  class parts and emits a runtime call only for the dynamic remainder.)

**North-star for discovering the API (the dogfooding principle):** implement gox's
*own* built-ins — class composition, context-aware escaping, attribute handling,
the default **attribute→field name mapper** (§3) — **as transformations over a
shared internal compile pipeline**. Once the core is built that way, the public
extension API is simply "expose that same seam." The extension points will be
*discovered* from how the built-ins are written, not designed up front.

Candidate hooks already identified (to validate the eventual API against): class
merger, **attribute→field mapper** (case-insensitive/initialism-aware,
kebab/snake→Pascal), custom tags/directives, markup-AST transforms. Revisit once
parser/compiler/runtime exist.

## Out of Scope (this spec)

- The exact `gox.Writer` helper surface (§10 fixes the model; method
  naming/shape is an implementation detail).
- Routing, HTTP handlers, request/response — gox is templating only.
- Hot reload / watch mode, editor/LSP tooling, formatter.

## Open Questions

- Concrete `gox.Attrs` representation (ordered map vs. slice of pairs). `gox.Node`
  is settled (§10).
- **Extension model (§11) — direction set, API deferred by decision** until
  parser/compiler/runtime exist; to be discovered by implementing gox's own
  built-ins as transformations (dogfooding), not designed up front.
- Exact escaper implementations per context (§5 table fixes the *contexts* and
  opt-outs; the precise JS/CSS escaping algorithms are an implementation detail).
