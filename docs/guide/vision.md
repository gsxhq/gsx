# Why gsx

Generating HTML from Go has always meant a trade-off. `html/template` ships in the
standard library and auto-escapes, but it is stringly-typed: templates parse at
runtime, errors surface late, and refactoring across templates is unsafe.
[templ](https://templ.guide) solved the type-safety problem by compiling templates
to Go — but its component syntax is its own, and an experimental branch that tried
to bring JSX-style inline components to templ ran into a wall.

gsx is a fresh take on that JSX-for-Go idea, designed around the lesson that broke
the first attempt.

## The bet: no symbol resolver

The experimental templ branch tried to map markup attributes onto *positional*
function parameters and to infer whether a lowercase tag was a component. Doing
that across packages drove it to a ~5,000-line symbol resolver on `go/packages`
and overlays — hitting overlay module-boundary bugs and performance cliffs.

gsx designs that whole class of work away:

- the **`component` keyword** identifies templates — no inference about what is a
  template;
- **capitalization** decides component-vs-element: `<div>` is HTML, `<Card>` is a
  component;
- gsx **generates every component's props struct**, so it always owns the field
  names.

What's left is plain Go the compiler type-checks. There is no symbol resolver to go
wrong.

## Lessons borrowed from templ

1. **Symbol resolution is the tar pit** — so gsx designs it away (above).
2. **Find Go boundaries; don't re-parse Go.** templ locates where a Go expression
   ends instead of reimplementing a Go parser. gsx does the same for embedded
   expressions and hands the rest to the real `go/parser`.
3. **Markup-vs-Go detection is subtle.** `{ <div/> }` (markup) versus `{ a < b }`
   (Go) is resolved positionally — the same rule Babel uses for JSX.

## Type-safe by construction

Because components lower to plain Go and props are generated structs, a wrong prop
name or type is a **compile error with a real source location** — gsx emits
`//line` directives back to the `.gsx` — not a runtime surprise. Interpolation is
auto-escaped, and the design treats escaping as context-aware (text, attribute,
URL, script/style). This compile-time safety is gsx's core differentiator.

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
ordinary Go.

---

> **Status — alpha.** Language design is stable; parser, runtime, and codegen
> phase 1 are done. The CLI is a work in progress, so gsx is **not yet runnable
> end-to-end**. See the [roadmap](https://github.com/gsxhq/gsx/blob/main/docs/ROADMAP.md).
