# Why gsx

Generating HTML from Go has always meant giving something up.

`html/template` ships in the standard library, auto-escapes, and has the
ergonomic everyone quietly loves — the **pipe**: <code v-pre>{{ .Name | upper }}</code> reads
left-to-right and composes cleanly. But it is stringly-typed. Templates parse at
runtime, errors surface late, and refactoring across templates is unsafe.

[JSX](https://react.dev/learn/writing-markup-with-jsx) went the other way on
ergonomics: markup that *reads like HTML*, with components that nest and compose
exactly like elements. It made writing UI feel like writing HTML again — but it
lives in JavaScript/TypeScript, with none of Go's guarantees.

[templ](https://templ.guide) brought type-safety to Go templating by compiling
templates to Go. It solved the late-errors problem — but its component syntax is
its own dialect, and writing markup in it never quite feels like writing HTML.

gsx takes the ergonomics people already reach for — **JSX's markup and
html/template's pipe** — and compiles them to type-safe, auto-escaping Go.

## Ergonomics first: markup that reads like HTML

This is the whole point. You write markup, not a bespoke template dialect:

```gsx
component Card(title string, featured bool) {
  <section class={ "card", "card-featured": featured }>
    <h2>{ title }</h2>
    <div class="body">{ children }</div>
  </section>
}
```

- **JSX-style inline components.** `<Card>` nests and composes exactly like
  `<div>`. **Capitalization** decides the meaning — `<div>` is an HTML element,
  `<Card>` is a component — so there is no inference about what a lowercase tag is.
- **The pipe, kept.** html/template's <code v-pre>{{ . | f }}</code> is the transform ergonomic gsx
  preserves as a `|>` filter pipeline — `{ name |> trim |> upper }` — except the
  filters are real, type-checked Go functions resolved at codegen, not stringly
  dispatched at runtime.
- **Everything else is ordinary Go.** Helpers, variant logic, anything that isn't
  a template is plain Go. The only seam is the `component` keyword and `{ }`
  interpolation — there is no third language in between.

## Type-safe by construction

Components compile to plain Go, and a component's props are a Go struct — either one
**you define and own** (pass a single struct param and gsx uses your type verbatim)
or one **gsx generates** from inline params. Either way, attributes map onto *named
struct fields* that the Go compiler checks. A wrong prop name or type is a **compile
error with a real source location** — gsx emits `//line` directives back to the
`.gsx` — not a runtime surprise.

Interpolation is auto-escaped, and escaping is **context-aware**: text, attribute,
URL, and CSS contexts each get the right treatment, determined at codegen from where
the value sits. JS contexts with no safe encoding are compile errors rather than
silent holes. The opt-outs (`gsx.Raw`, `gsx.RawURL`, `gsx.RawCSS`) are explicit and
grep-able. This compile-time safety is gsx's core differentiator. See
[Principles](./principles) for the full model.

## The design lesson: bounded symbol resolution

To deliver those ergonomics, gsx *does* resolve symbols — it loads packages with
`go/packages` and type-checks with `go/types` so it can do type-aware interpolation,
context-aware escaping, and props checked against real Go types. The question was
never whether to resolve symbols, but how to keep that resolution from becoming a
tar pit.

The trap is the open-ended *inference*, not the resolution itself. Try to map markup
attributes onto *positional* function parameters, or to infer whether a lowercase
tag is a component, and the resolver has to chase those guesses across packages —
straight into overlay module-boundary bugs and performance cliffs. gsx never takes
that on.

gsx makes design choices that keep its resolution bounded — it asks the type checker
for facts instead of guessing:

- the **`component` keyword** identifies templates — no inference about what is a
  template;
- **capitalization** decides component-vs-element at the tag, with no type lookup;
- components take a **named props struct** — yours or generated — so attributes bind
  to struct fields, never to reverse-engineered positional parameters.

A related discipline: where markup embeds Go, gsx **finds where the Go expression
ends and hands it to the real `go/parser`** rather than reimplementing one. And
`{ <div/> }` (markup) versus `{ a < b }` (Go) is disambiguated positionally — the
same rule Babel uses for JSX. What's left is plain Go that `go/types` checks
directly.

## Relationship to templ

gsx shares no code with templ, but it is built to interoperate. `gsx.Node` — the
universal renderable — has the **identical method set to `templ.Component`**
(`Render(ctx, w) error`). Because the method sets match, a `gsx.Node` is accepted
anywhere a `templ.Component` is expected (structpages and other templ-ecosystem
tools) **without importing templ**. You can adopt gsx incrementally inside an
existing templ project.

## What gsx is not

gsx is templating only — no router, no HTTP handlers, no framework. It is a way to
write HTML as a first-class, composable Go value. Everything non-template is
ordinary Go, and the runtime package imports nothing outside the standard library.

---

> **Status — alpha.** gsx is runnable end-to-end, and it's a toolchain, not just a
> compiler:
>
> - **CLI** — `gsx generate` compiles `.gsx` → `.x.go`; plus `gsx fmt`, `gsx info`,
>   and `gsx init` to scaffold a ready-to-run project.
> - **Editor** — `gsx lsp` language server: Go ↔ `.gsx` navigation, formatting, and
>   diagnostics.
> - **Dev loop** — a Vite plugin (`@gsxhq/vite-plugin-gsx`) regenerates and
>   live-reloads on save, paired with the `github.com/gsxhq/vite` Go helper for
>   asset manifests; `gsx init` wires both together.
>
> Codegen covers interpolation, control flow, attributes with contextual escaping,
> the `|>` pipeline + filters, components/props/`{children}`, method components,
> named slots, attribute fallthrough, and `style` composition. See the
> [roadmap](https://github.com/gsxhq/gsx/blob/main/docs/ROADMAP.md).
