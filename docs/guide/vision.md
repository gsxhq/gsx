# Why gsx

gsx is a Go template compiler with JSX-style markup, `html/template`-style
pipelines, and generated Go output.

It is for teams that want to keep UI templates inside Go, but still want markup
that scans like HTML and call sites checked by `go build`.

## Markup That Reads Like HTML

Components use a Go-like header and an HTML-like body:

```gsx
component Card(title string, featured bool) {
  <section class={ "card", "card-featured": featured }>
    <h2>{ title }</h2>
    <div class="body">{ children }</div>
  </section>
}
```

- `<div>` is an HTML element; `<Card>` is a component.
- `{ name |> trim |> upper }` applies registered Go filter functions left to right.
- Helpers, types, imports, and branching logic outside markup are ordinary Go.

## Checked By Go

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
grep-able. See [Principles](./principles.md) for the full model.

## Build Step And Dev Loop

gsx compiles: `.gsx` → `.x.go` → `go build`. That build step gives Go type
checking and generated escaping code.

**`gsx dev`** runs that loop in one foreground process: watch `.gsx`, `.go`, and
`.env` files; regenerate with a warm compiler; rebuild and swap the Go server; and
supervise Vite. `gsx init` wires the starter project to `npm run dev`.

## Bounded Symbol Resolution

gsx resolves symbols with `go/packages` and type-checks with `go/types` so it can
handle type-aware interpolation, context-aware escaping, and props checked against
real Go types. The design keeps that resolution bounded.

The risk is open-ended *inference*, not resolution itself. Try to map markup
attributes onto *positional* function parameters, or to infer whether a lowercase
tag is a component, and the resolver has to chase those guesses across packages —
straight into overlay module-boundary bugs and performance cliffs. gsx never takes
that on.

gsx keeps resolution bounded:

- the **`component` keyword** identifies templates — no inference about what is a
  template;
- **capitalization** decides component-vs-element at the tag, with no type lookup;
- components take a **named props struct** — yours or generated — so attributes bind
  to struct fields, never to reverse-engineered positional parameters.

## Relationship to templ

gsx shares no code with templ, but it is built to interoperate. `gsx.Node` — the
universal renderable — has the **identical method set to `templ.Component`**
(`Render(ctx, w) error`). Because the method sets match, a `gsx.Node` is accepted
anywhere a `templ.Component` is expected (structpages and other templ-ecosystem
tools) **without importing templ**.

For a practical side-by-side with templ, `html/template`, JSX, see [Comparisons](./comparisons.md).

## What gsx is not

gsx is templating only: no router, no HTTP handlers, no framework. Everything
non-template is ordinary Go, and the runtime package imports nothing outside the
standard library.

---

> **Status — alpha.** See [Status](./status.md) for shipped commands, partial
> features, and known gaps.
