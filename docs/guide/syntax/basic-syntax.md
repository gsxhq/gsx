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

1. Whitespace-only runs containing a newline are layout and disappear. Inline
   whitespace-only runs collapse to one space.
2. Other ASCII whitespace runs collapse to one space. This includes newlines
   inside text and at text edges facing a sibling. Newline indentation at the
   outer edges of a children list disappears.
3. Expression values are data and stay unchanged, including `{ " " }`,
   `{ "  " }` and whitespace inside strings. Unicode spaces and character
   references also keep their data.

```gsx
<p>Hello
    <b>world</b>
</p>
<p>Hello
    <Link>world</Link>
</p>
<p>Read
    <img src="/icon.png"/>
    more</p>
<span>A</span>
<span>B</span>
<p>{value}
    %
</p>
```

These preserve a separator after each `Hello`, on both sides of the image,
produce adjacent spans, and produce `42 %` when `value` is 42. The tag or
component beside text does not change the spacing rule. Neither do punctuation,
CSS classes, interpolation values or conditional output. Braced comments, HTML
comments and nonrendering Go blocks do not split a source whitespace run.

Write `<p>{value}%</p>` to produce `42%`. The same distinction applies to
`Hello <em>world</em>` versus `Hello<em>world</em>`: authored whitespace separates
content; adjacency keeps it joined. An ASCII space beside a nonbreaking space
remains an additional source separator, whether authored inline or as a newline.

To keep text joined to a control-flow block, put the opening brace directly
next to the text, with no intervening space or newline:

```gsx
<section>
    Total{ switch n {
        case 0:
            <div>none</div>
    } }
    <b>{ n }</b>
</section>
```

For `n = 0`, this emits
`<section>Total<div>none</div><b>0</b></section>`. With no matching case, `n = 3`
emits `<section>Total<b>3</b></section>`. Indentation inside the switch braces
is syntax and adds no separator at the `Total{` boundary. The formatter keeps
that boundary joined.

Writing `Total { switch ... }` or placing a newline between `Total` and `{`
preserves a space instead. The same adjacency rule applies to `if` and `for`
blocks, elements and interpolations.

A source separator remains even if an expression or conditional emits nothing,
just as an inline source space does. Put an explicit separator inside a branch
when it should appear only with that branch.

Padding immediately inside component, slot and control-flow braces is syntax.
Between tags or expressions, a whitespace-only newline is layout. Use
`{ " " }` when a separator must be explicit in every context.

This is GSX source normalization, rather than literal HTML whitespace emission.
The formatter can reindent markup without adding that indentation to output.
It can wrap internal text spaces, including beside punctuation, while keeping
adjacent text and markup joined.

Text inside `pre` and `textarea` subtrees, raw `script`/`style` bodies, static
attribute values and expression data retain their existing verbatim contracts.
