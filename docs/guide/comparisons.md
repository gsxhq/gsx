# Comparisons

gsx sits between Go template engines and JSX-like markup systems: templates stay close to HTML, data stays typed Go, and output compiles to ordinary Go.

## gsx and templ

Both gsx and templ compile components to Go values with `Render(ctx, w) error`. gsx differs in surface syntax: component declarations are templ-style, while the body is JSX-style markup. This makes HTML-like structure easier to scan while keeping structural compatibility with `templ.Component`.

Use gsx when you want JSX-like authoring inside Go projects. Use templ directly when you prefer templ's existing syntax, ecosystem, or render-once primitive.

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
