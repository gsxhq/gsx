# Basic syntax

A `.gsx` file combines ordinary Go with `component` declarations and JSX-like markup. Start here to see the shape of a file and how gsx decides whether a tag is an HTML element or a component.

## Package and imports

Begin with a package declaration and import any packages used by your Go code. You only need to import `github.com/gsxhq/gsx` when you refer to runtime names such as `gsx.Node`, `gsx.Raw`, or `gsx.Attrs` yourself.

```gsx
package views

import "time"

func formatDate(t time.Time) string {
	return t.Format("2006-01-02")
}
```

## Declare a component

A component has a name, typed parameters, and a markup body. The body is the result, so it has no return type or `return` statement.

<!--@include: ./_generated/basic-syntax/010-component-declaration.md-->

The declaration shape determines how callers pass values. See [Props](./props.md) for the available models.

## Element or component?

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
