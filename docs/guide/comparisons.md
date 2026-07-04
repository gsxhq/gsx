# Comparisons

gsx sits between Go template engines and JSX-like markup systems: templates stay close to HTML, data stays typed Go, and output compiles to ordinary Go.

## gsx and templ

Both gsx and templ compile components to Go values with `Render(ctx, w) error`. A `gsx.Node` has the identical method set to `templ.Component`, so it is accepted anywhere a `templ.Component` is expected — gsx interoperates with the templ/HTMX ecosystem without importing templ.

The difference is a deliberate tradeoff. templ keeps a simple, explicit compiler model with syntax close to Go. gsx spends more in the toolchain — it analyzes real Go types with `go/packages` and `go/types` — to make the authoring experience more HTML-like and to push more checks to compile time.

That extra analysis buys ergonomics that are hard to retrofit onto a simpler model:

- **HTML-style component calls** — `<Card title="…"/>` reads like markup, and capitalization alone decides component-vs-element.
- **Named props checked by Go** — attributes map to struct fields, so a wrong prop name or type is a compile error with a real source location, not a runtime surprise.
- **Context-aware escaping** across text, attribute, URL, CSS, and JavaScript positions, decided at codegen.
- **Class and attribute merging** as structured values rather than string concatenation.
- **Explicit JavaScript-valued attributes** and automatic JSON interpolation for data islands and attributes like `hx-vals`.

Many of these are long-standing requests on templ itself — HTML-style authoring, inline components, passing Go data to JavaScript, JSON helpers, class ergonomics — which are difficult to add incrementally to a model optimised for simplicity. gsx starts from a design where they compose. For the full reasoning, see [Why I built gsx](https://jackieli.dev/posts/why-i-built-gsx/).

Use gsx when you want JSX-like authoring and richer compile-time ergonomics inside Go. Use templ when you prefer its simpler compiler model and established ecosystem.

## gsx and html/template

`html/template` is stable, standard-library, and contextually auto-escaping. gsx preserves contextual escaping while adding typed components, generated props, compiler-checked composition, and a formatter/LSP path.

Use `html/template` when you need runtime-loaded templates or the standard package alone. Use gsx when templates are part of the compiled application.

## gsx and JSX

JSX makes markup part of JavaScript. gsx borrows the readable tag structure, but expressions are Go, components compile to Go, and there is no virtual DOM.

Use client-side JSX for interactive browser applications. Use gsx for server-rendered Go components, optionally with JavaScript islands.

## gsx and Jinja-style templates

Jinja-style templates provide a compact template language with filters, blocks, and inheritance. gsx instead leans on Go for data, functions, imports, and control flow, with pipelines for common formatting.

Use Jinja-style templates when a dynamic template language is the product requirement. Use gsx when compile-time checking and Go-native component composition matter more.

## Interop

See [Interop](./syntax/interop) for examples that compose gsx with templ, `html/template`, and client-side islands.
