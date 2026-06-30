# Basic syntax

A `.gsx` file is ordinary Go — package declaration, imports, type definitions, helper functions — plus `component` declarations. Everything that is valid Go in a `.go` file is valid in a `.gsx` file. The compiler just adds one new declaration form.

## Package & imports

A `.gsx` file begins with a `package` declaration, followed by any Go imports your component bodies need. Standard library imports, third-party packages, and your own packages all work the same as in Go.

```gsx
package views

import (
	"fmt"
	"time"
)

// helper func — plain Go, valid in .gsx
func formatDate(t time.Time) string {
	return t.Format("2006-01-02")
}
```

You never need to import the `gsx` package for the template syntax itself — the code generator writes those calls. You do import it when you use runtime helpers like `gsx.Raw` or `gsx.Attrs` directly in your code.

## The `component` declaration

A component is declared with the `component` keyword, a name, a param list, and a brace-delimited markup body. The body **is** the result — there is no return type and no `return` statement.

<!--@include: ./_generated/basic-syntax/010-component-declaration.md-->

The code generator produces a typed props struct (`GreetingProps` here) with one exported field per param. The component function takes that struct and returns a `gsx.Node`.

## Element vs component

Capitalization is the one rule that separates HTML elements from component calls:

| Tag form | Meaning | Example |
|---|---|---|
| lowercase | HTML element | `<div>`, `<section>`, `<p>` |
| hyphenated | HTML custom element | `<el-dialog>`, `<my-card>` |
| Capitalized | component call | `<Card>`, `<Button>` |
| dotted | package-qualified component | `<ui.Button>`, `<p.Content>` |

No import or registration is needed — `<Card>` is a direct call to the `Card` component function in scope, just as calling `Card(props)` in Go would be.
