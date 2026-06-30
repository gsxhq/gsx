# Raw Go

::: v-pre
gsx lets you embed arbitrary Go code inside a component body when you need to compute a local value, call a function for its side-effect, or otherwise run a Go statement that produces no markup output. The raw-Go form is the `{{ stmt }}` **GoBlock** — double braces signal "this is a Go statement, not an interpolation".
:::

Note: single-brace `{ expr }` is **interpolation** (it emits an escaped value into the output) — a different feature. See [Interpolation](./interpolation) for details.

## GoBlock

::: v-pre
A GoBlock runs a single Go statement inline without producing any HTML output. The most common use is assigning a local variable before interpolating it:
:::

<!--@include: ./_generated/raw-go/010-go-code-block.md-->

::: v-pre
A GoBlock can appear between elements or text nodes anywhere a child can appear. The statement is emitted verbatim into the generated Go and produces no HTML output; the assigned variable is available to all subsequent children in the same scope.
:::

## GoBlock vs ordered-attrs literal

The double-brace syntax appears in two distinct positions, and **position alone disambiguates them**:

::: v-pre
| Position | Syntax | Meaning |
|---|---|---|
| **Body** (between child nodes) | `{{ full := x + y }}` | GoBlock — a Go statement, no output |
| **Attribute value** | `name={{ "key": value }}` | Ordered-attrs literal — produces a `gsx.OrderedAttrs` |

When `{{ … }}` appears as a child of an element or at the top of a component body, it is always a GoBlock. When it appears after `=` as the value of an attribute, it is always an ordered-attrs literal producing a `gsx.OrderedAttrs` that renders attributes in declaration order (useful for `data-*` directive ordering).

There is no ambiguity: the parser knows which context it is in before reading the `{{`.
:::
