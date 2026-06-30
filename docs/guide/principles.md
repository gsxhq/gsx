# Principles

gsx's design follows a few firm commitments. They explain most of the syntax
decisions you'll meet in the [syntax guide](./syntax).

## Stay close to HTML and to Go

Markup looks like HTML/JSX; helpers, variant functions, and everything that isn't a
template are ordinary Go. There is no third language between them — the seam is the
`component` keyword and `{ }` interpolation.

## Syntax tidiness is the top priority

Parser complexity bends to serve clean syntax, never the reverse. Where it buys
tidier markup, gsx does targeted Go-expression boundary parsing rather than forcing
an awkward syntax to keep the parser simple.

## Lean on the Go compiler

Generated code is plain Go. Prop names, types, and call sites are checked by the Go
compiler, not by gsx at runtime. A wrong template is a compile error with a real
source location (gsx emits `//line` directives back to the `.gsx`), not a runtime
surprise.

## Embrace the build step

gsx compiles `.gsx` → `.x.go` → `go build` rather than interpreting templates at
runtime — that compile step is what makes the type-safety above possible. We treat it
as a feature, not a tax: `gsx dev` keeps generation warm, safely rebuilds and
restarts the Go server, and uses Vite for browser errors and reloads. `gsx init`
wires the whole loop to `npm run dev`, so a build step you never wait on is just
a faster, safer template.

## Secure by construction

Interpolation is auto-escaped by default, and escaping is **context-aware**: text,
attribute, URL, and CSS contexts each get the right treatment — determined at
codegen from the value's position, not by the author wrapping values manually. URLs
are checked against a scheme allow-list, `<style>`/`style=` values pass a CSS
value-filter, attribute values are always quoted, and JS contexts with no safe
encoding yet (bare expressions in `on*`/event handlers) are **compile errors**, not
runtime surprises.

The opt-outs are explicit and auditable — each a deliberate, grep-able bypass of one
specific check: `gsx.Raw(s)` for trusted HTML, `gsx.RawURL(s)` for a URL whose
scheme you vouch for, and `gsx.RawCSS(s)` for trusted CSS. Type-driven attributes
mean a `bool` renders as a bare or omitted attribute rather than the string `"true"`.

## Standard-library-only runtime

The `gsx` runtime package imports nothing outside the Go standard library. The
generator/CLI may use `golang.org/x/tools`, but what ships in your binary stays
stdlib-only.
