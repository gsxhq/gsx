# Raw Go

::: v-pre
gsx lets you embed arbitrary Go code inside a component body when you need to compute a local value, call a function for its side-effect, or otherwise run a Go statement that produces no markup output. The raw-Go form is the `{{ stmt }}` **GoBlock** — double braces signal "this is a Go statement, not an interpolation".
:::

Note: single-brace `{ expr }` is **interpolation** (it emits an escaped value into the output) — a different feature. See [Interpolation](./interpolation.md) for details.

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
| **Attribute value** | `name={{ "key": value }}` | Ordered-attrs literal — produces a `gsx.Attrs` (ordered slice) |

When `{{ … }}` appears as a child of an element or at the top of a component body, it is always a GoBlock. When it appears after `=` as the value of an attribute, it is always an ordered-attrs literal producing a `gsx.Attrs` ordered slice that renders attributes in declaration order (useful for `data-*` directive ordering).

There is no ambiguity: the parser knows which context it is in before reading the `{{`.
:::

## The reverse direction: elements in Go expression position

GoBlock and the ordered-attrs literal both embed **Go inside markup**. The other direction also works: a `<tag>…</tag>` can appear in ordinary Go **expression** position — a `var` initializer, a `return`, a call argument, a struct field, or as an operand inside a `{ … }` interpolation — evaluating to a `gsx.Node`, without a `component` body wrapping it. A fragment, `<>…</>`, works in every one of those positions too, evaluating to a `gsx.Node` with no wrapper element. See [Elements — Elements as values](./elements.md#elements-as-values), [Fragments as values](./elements.md#fragments-as-values), and [Inside interpolations](./elements.md#inside-interpolations).

An `f`-prefixed interpolating literal (`` f`…@{ expr }…` `` or `f"…@{ expr }…"`) is a first-class Go **value** the same way — it evaluates to a `string` and is valid anywhere a `string` expression is, including a `var` initializer, a call argument, or a name referenced from inside `{ … }`. See [Interpolation — As a first-class Go value](./interpolation.md#as-a-first-class-go-value). `` js`...` `` and `` css`...` `` stay attribute-context literals; they are not valid as standalone Go expression values.

## The `gsx` package is an ordinary import

Inside a `.gsx` file, `github.com/gsxhq/gsx` is a Go package like any other. Reference `gsx.X` in your Go and you import it; don't, and you don't. An unused import is an error, exactly as with `fmt`.

```gsx
package ui

import "github.com/gsxhq/gsx"

func wrap(n gsx.Node) gsx.Node { return n }
```

Markup does **not** make the import necessary. A component, an element literal, or an `f"…"` literal needs nothing from you — the generated code reaches the runtime through reserved `_gsx`-prefixed aliases you never see:

```gsx
package ui

// No import: no Go here names the gsx package.
var X = <div><b>hi</b></div>
```

A `.gsx` whose Go names the gsx package **and** whose markup makes the generator reach the runtime produces one import path under two names — the plain one you wrote, and the generator's alias. That is legal Go, and it is the mechanism that keeps the two namespaces from ever interfering.

Because generated code never uses the plain names, your file may bind `gsx`, `context`, `io`, or `strconv` to whatever it likes:

```gsx
package ui

import gsx "strings"

component Shout(s string) {
	<b>{ gsx.ToUpper(s) }</b>
}
```

The same freedom extends to component parameters and to method-component receiver variables: `component Shout(gsx string)` and `component (strconv Cfg) Render()` are both legal.

### The `_gsx` prefix is reserved

That freedom is a trade. The generator gave the plain names back by retreating into a single reserved identifier space: any name beginning `_gsx`. Reserving one prefix rather than a list of concrete names is what makes the promise durable — which aliases the generator emits is an implementation detail that grows with every codegen feature, and your file should never have to track it.

Writing a `_gsx` identifier **anywhere gsx sees your Go** is an error, reported with a clean, positioned diagnostic. That is every Go fragment in a `.gsx` file: top-level `var`/`const`/`func`/`type` declarations and import aliases, function-body locals, `{{ }}` GoBlock statements, `{ if/for/switch }` clauses, interpolations, attribute and `class`/`style` expressions, and pipe-stage arguments.

```gsx
package ui

import _gsxstrings "strings" // error: identifier "_gsxstrings" uses the reserved _gsx prefix

var _gsxfoo = 1              // error

func helper() gsx.Node {
	_gsxio := 4              // error — a local counts too
	_ = _gsxio
	return <b/>
}

component C() {
	{{ _gsxgw := "z" }}      // error — a GoBlock binding counts too
	<b>{ _gsxbar }</b>       // error — even a reference
}
```

Component parameters and method-component receiver variables are covered by the same rule (a param or receiver beginning `_gsx` is rejected). Two more names are reserved on component parameters for a related reason — the generated render closure binds them: `ctx` (the ambient context your interpolations reference), `children` (the implicit children slot), and `attrs` (explicit attribute forwarding).

gsx enforces the prefix by lexing your Go, so the reservation is on *identifiers* only. These are **not** affected, because they are not Go identifiers gsx can see:

- **Text that isn't Go** — the string `_gsxfoo` inside a Go string literal (`"_gsxfoo"`), inside a Go comment, or in markup prose (`<p>_gsxfoo</p>`) is not an identifier and is left alone.
- **Blank and dot imports** — `import _ "x"` and `import . "x"` bind no `_gsx` name.
- A **component** name cannot reach the space at all: the parser's component-name scan admits no `_`, so `component _gsxX()` is already a syntax error.

The one place the reservation does *not* reach is a **hand-written `.go` file** in the same package: gsx never reads its bodies (only its struct field names, for [BYO props](./composition.md)), so a `var _gsxio = 1` there is caught by `go build`, not by gsx. Don't write `_gsx` names there either.

::: tip
`gsx fmt` will not add a missing `gsx` import for you. Writing `gsx.Node` without importing the package yields `undefined: gsx` from `gsx generate`; add the import by hand.
:::
