# Raw Go

Use raw Go when a component needs to run a statement, create a local value, or place markup in an ordinary Go expression.

## GoBlock

::: v-pre
A `{{ statement }}` GoBlock runs one Go statement without rendering output. Values it declares are available to the following children in the same scope.
:::

<!--@include: ./_generated/raw-go/010-go-code-block.md-->

Use `{ expression }` when a value should render instead. See [Interpolation](./interpolation.md) for expressions and escaping.

## GoBlock or ordered attributes?

::: v-pre
The position of `{{ … }}` determines its meaning:

| Position | Example | Meaning |
|---|---|---|
| Between child nodes | `{{ full := first + last }}` | run a Go statement; render nothing |
| After an attribute `=` | `attrs={{ "id": id }}` | create an ordered attribute list |
:::

See [Attributes](./attributes.md) for ordered attribute values.

## Elements in Go expressions

An element or fragment can appear wherever a Go expression is accepted in a `.gsx` file, including a variable initializer, return value, function argument, struct field, or interpolation operand. It evaluates to a `gsx.Node`.

See [Elements as values](./elements.md#elements-as-values) for the common forms.

## Importing `gsx`

`github.com/gsxhq/gsx` is an ordinary Go import. Import it when your source names `gsx.Node`, `gsx.Raw`, or another runtime API; markup alone does not require an import.

```gsx
package views

import "github.com/gsxhq/gsx"

func wrap(n gsx.Node) gsx.Node { return n }
```

::: warning Reserved prefix
Do not declare or reference Go identifiers beginning with `_gsx` in a `.gsx` file. That prefix is reserved, including in imports, helpers, component parameters, GoBlocks, control-flow clauses, interpolations, and attribute expressions; `_gsx` inside strings, comments, or markup text is unaffected.
:::
