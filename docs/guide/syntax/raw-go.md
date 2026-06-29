# Raw Go

gsx lets you embed arbitrary Go code inside a component body when you need to compute a local value, call a function for its side-effect, or otherwise run a Go statement that produces no markup output. There are two syntactic forms: the `{{ stmt }}` **GoBlock** for inline statements within an element's children, and the plain `{ … }` **Go block** for multi-statement sequences at the body level.

## Go code blocks `{ … }`

Control-flow blocks (`if`, `for`, `switch`) in gsx body position already use `{ … }` — they are Go blocks whose bodies happen to contain markup. You can also write a `{{ stmt }}` GoBlock to run a single Go statement inline without producing any output.

The most common use is assigning a local variable before interpolating it:

<!--@include: ./_generated/raw-go/010-go-code-block.md-->

The `{{ … }}` (double-brace) form signals "this is a Go statement, not an interpolation". It can appear between elements or text nodes anywhere a child can appear. The statement is emitted verbatim into the generated Go and produces no HTML output; the assigned variable is available to all subsequent children in the same scope.

## `{{ stmt }}` GoBlock vs the `{{ }}` ordered-attrs literal

The double-brace syntax appears in two distinct positions, and **position alone disambiguates them**:

| Position | Syntax | Meaning |
|---|---|---|
| **Body** (between child nodes) | `{{ full := x + y }}` | GoBlock — a Go statement, no output |
| **Attribute value** | `name={{ "key": value }}` | Ordered-attrs literal — produces a `gsx.OrderedAttrs` |

When `{{ … }}` appears as a child of an element or at the top of a component body, it is always a GoBlock. When it appears after `=` as the value of an attribute, it is always an ordered-attrs literal producing a `gsx.OrderedAttrs` that renders attributes in declaration order (useful for `data-*` directive ordering).

There is no ambiguity: the parser knows which context it is in before reading the `{{`.
