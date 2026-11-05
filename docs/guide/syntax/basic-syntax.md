# Basic syntax

A `.gsx` file combines ordinary Go with `component` declarations and JSX-like markup. Start here to see the shape of a file and how gsx decides whether a tag is an HTML element or a component.

## Package and imports

Begin with a package declaration and import any packages used by your Go code. You only need to import `github.com/gsxhq/gsx` when you refer to runtime names such as `gsx.Node`, `gsx.Raw`, or `gsx.Attrs` yourself.

```gsx
package views

import "github.com/gsxhq/gsx"

func wrap(n gsx.Node) gsx.Node { return n }
```

## Declare a component

A component has a name, typed parameters, and a markup body. The body is the result, so it has no return type or `return` statement.

<!--@include: ./_generated/basic-syntax/010-component-declaration.md-->

The authored parameter list determines how Go and markup callers pass values.
See [Component signatures](./props.md).

## Element or component? {#element-vs-component}

Lowercase and hyphenated tags normally render HTML elements. Capitalized and dotted tags call components, and a lowercase tag also calls a package-level declaration with the same name.

| Tag | Meaning |
|---|---|
| `<div>` | HTML element when `div` is not declared |
| `<my-card>` | HTML custom element |
| `<Card>` | component |
| `<ui.Button>` | qualified component |
| `<card>` | component when `card` is declared in the package |

No registration is required. See [Elements](./elements.md) for element syntax.

### Lowercase wrapper components

A lowercase component can wrap the HTML element with the same name. Inside `component div`, the self-named `<div>` stays an element instead of calling the component recursively.

<!--@include: ./_generated/basic-syntax/020-wrapper-pattern.md-->

Other lowercase tags still follow the normal declaration rule. To recurse from a lowercase component, use an ordinary Go call rather than a self-named tag.

## Whitespace {#whitespace}

GSX normalizes source whitespace to keep rendered HTML compact:

- Discard layout-only whitespace: newlines and indentation between sibling
  elements do not create a separator.
- Preserve word separation in prose: whitespace between words collapses to one
  space. A source newline, inside text or beside text-bearing content, preserves
  one space when that boundary needs a prose separator.
- Preserve authored attachment: punctuation stays attached when the source has
  no separator. A space or prose newline before `%`, punctuation or an ellipsis
  remains a separator; GSX does not infer typography from the character.
- Explicit spaces override inference. Inline spaces at text edges and in HTML
  content normalize to one space; expression values such as `{ " " }`,
  `{ "  " }` and nonbreaking spaces
  keep their data, even next to punctuation. An existing rendered gap, including
  a nonbreaking space, satisfies newline spacing; an extra authored inline space
  beside it remains explicit.

```gsx
<p>Hello
    <b>world</b>
</p>
<span>A</span>
<span>B</span>
<p>{value}
    %
</p>
```

These produce `Hello <b>world</b>`, adjacent spans, and `42 %` when `value` is 42.
Write `<p>{value}%</p>` to produce `42%`. The same distinction applies to
`Hello <em>world</em>` versus `Hello<em>world</em>`: authored prose whitespace
separates content; adjacency keeps it joined.
This is GSX source normalization, rather than literal HTML whitespace emission.
The formatter can reindent markup without adding that indentation to output.

Known text-bearing HTML elements such as `a`, `b`, `em`, `span`, `strong`,
`button`, `label` and `output` participate in prose boundaries through their
child content. Block elements, empty elements and component tags have opaque
output, so no separator is inferred across them. Interpolations conservatively
count as possible text; runtime values do not change source normalization.

Boundary analysis uses the authored syntax, including possible `if`/`for`/
`switch` output. It does not evaluate Go conditions or inspect CSS classes.
An inferred space belongs to its source text, like an inline authored space:
if optional content renders nothing, the source space can remain. Put an
explicit separator inside a branch when it should appear only with that branch.

Between markup siblings in a syntactic component or slot body, whitespace
remains layout. Use `{ " " }` when a separator must be explicit in every context.

Text inside `pre` and `textarea` subtrees, raw `script`/`style` bodies, static
attribute values and expression data retain their existing verbatim contracts.
