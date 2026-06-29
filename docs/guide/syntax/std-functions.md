# Runtime helpers reference

This page is the index of every user-facing type and function exported by the `gsx` runtime package (`github.com/gsxhq/gsx`). Each entry gives the exact Go signature or type declaration, a one-line description, and a link to the page that shows it in context.

The runtime is **standard-library only** — no external dependencies. Generated code calls these helpers directly; you call them when you need to box a trusted value, construct an attribute bag in Go code, or implement the `Node` interface yourself.

## Core interface

| Name | Kind | Signature | Description |
|------|------|-----------|-------------|
| `gsx.Node` | interface | `Render(ctx context.Context, w io.Writer) error` | The rendering interface every gsx component satisfies. Any value implementing this method is renderable in a `gsx.Node` prop or `{children}`. See [Composition](./composition). |
| `gsx.Func` | type | `type Func func(ctx context.Context, w io.Writer) error` | Adapts a plain function to `gsx.Node`; implements `Render` by calling itself. Useful when writing a one-off Node in Go without declaring a struct. See [Raw HTML](./raw-html). |

`gsx.Node`'s method set is identical to `templ.Component`'s (as of templ ≥ v0.3), so a `gsx.Node` satisfies `templ.Component` structurally and vice-versa — no adapter or import needed.

## Trusted-value helpers

These are the explicit opt-outs from gsx's context-aware auto-escaping. Each one vouches that the value is safe for its target context. **Do not use them on untrusted input.**

| Name | Kind | Signature / type | Description |
|------|------|-----------------|-------------|
| `gsx.Raw` | func | `func Raw(html string) Node` | Emits `html` verbatim — no entity encoding, no escaping. Use only for already-safe HTML (e.g. pre-sanitised Markdown). See [Raw HTML](./raw-html). |
| `gsx.RawURL` | type | `type RawURL string` | A URL whose scheme is trusted. In a URL attribute (`href`, `src`, etc.) a `gsx.RawURL` value skips the scheme allow-list check; the string is still attribute-escaped (it cannot break out of quotes). Use as a conversion: `gsx.RawURL("app://…")`. See [Escaping](./escaping). |
| `gsx.RawJS` | type | `type RawJS string` | A JavaScript string the author vouches for. In a `<script>` body or JS-context attribute a `gsx.RawJS` value is emitted verbatim, bypassing JSON-encoding. Use as a conversion: `gsx.RawJS("openMenu()")`. See [JavaScript](./javascript). |
| `gsx.RawCSS` | type | `type RawCSS string` | A CSS value the author vouches for. In a `<style>` body or `style=` attribute a `gsx.RawCSS` value is emitted verbatim, bypassing the CSS value-filter. Use as a conversion: `gsx.RawCSS("rgb(0,128,0)")`. See [Styling](./styling). |

`RawURL`, `RawJS`, and `RawCSS` are named **string types**, not functions. Use them as type conversions — `gsx.RawJS(expr)` — not as function calls.

## Attribute bags

| Name | Kind | Signature / type | Description |
|------|------|-----------------|-------------|
| `gsx.Attrs` | type | `type Attrs map[string]any` | Unordered attribute bag. Keys are sorted alphabetically at render time. Used for attribute spread (`{ bag… }`) and as the type of a `Children gsx.Attrs` fallthrough field on bring-your-own props. See [Attributes](./attributes). |
| `gsx.Attr` | type | `type Attr struct{ Key string; Value any }` | A single key-value attribute pair. The element type of `gsx.OrderedAttrs`. |
| `gsx.OrderedAttrs` | type | `type OrderedAttrs []Attr` | Insertion-ordered attribute bag. Keys render in slice order (not sorted). Populated by the `{{ "key": value }}` literal in a component invocation. See [Attributes — ordered attributes](./attributes#ordered--→-gsxorderedattrs). |

## Node boxing

These helpers box ordinary Go values into `gsx.Node` so they can be passed to a `gsx.Node`-typed prop.

| Name | Kind | Signature | Description |
|------|------|-----------|-------------|
| `gsx.Val` | func | `func Val(v any) Node` | Boxes any renderable value as a Node. Accepts `Node`, `string`, `[]byte`, `fmt.Stringer`, numeric types, and `bool`; panics (at render time) on other types. See [Props](./props). |
| `gsx.Text` | func | `func Text(s string) Node` | Boxes a plain string as an HTML-escaped text Node. Equivalent to `{ s }` inline but usable as a value in Go code. |
| `gsx.Fragment` | func | `func Fragment(nodes ...Node) Node` | Groups multiple Nodes into a single Node that renders them in order with no wrapper element. The value-level equivalent of `<> … </>`. See [Fragments](./fragments). |

## Class and style helpers (generated; rarely called directly)

The following helpers are what `class={ … }` and `style={ … }` sugar compiles to. You rarely call them by name — the code generator emits them for you. They are documented here for completeness and for authors implementing custom Node types that must participate in the class/style machinery.

| Name | Kind | Signature | Description |
|------|------|-----------|-------------|
| `gsx.Class` | func | `func Class(s string) ClassPart` | Returns an always-on class contribution. |
| `gsx.ClassIf` | func | `func ClassIf(s string, on bool) ClassPart` | Returns a conditional class contribution included only when `on` is true. |
| `gsx.StyleValue` | func | `func StyleValue(v any) string` | Returns a CSS-safe string: a `gsx.RawCSS` value passes through verbatim; any other value is run through the CSS value-filter. |

See [Styling](./styling) for the full composable `class`/`style` reference.
