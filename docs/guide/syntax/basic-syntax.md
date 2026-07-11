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

A tag resolves in exactly one of three ways:

1. **Capital-first or dotted tag** — always a component call. Codegen emits
   the call and `go build` resolves the name, including function-local names
   (an element-literal `local := <div>...</div>` makes `<local>` callable) and
   struct methods (`<p.Content>` calls the `Content` method on receiver `p`).
2. **Lowercase simple tag whose name matches a package-level declaration** —
   also a component call, lowered as an invocation of that name.
3. **Lowercase simple tag with no matching declaration** — an HTML element,
   rendered as-is.

| Tag form | Meaning | Example |
|---|---|---|
| lowercase, undeclared | HTML element | `<div>`, `<section>`, `<p>` |
| hyphenated | HTML custom element | `<el-dialog>`, `<my-card>` |
| lowercase, declared | component call | `<card>` when `component card(...)` exists in the package |
| Capitalized | component call | `<Card>`, `<Button>` |
| dotted | package-qualified component / method call | `<ui.Button>`, `<p.Content>` |

No import or registration is needed — `<Card>` is a direct call to the `Card`
component function in scope, just as calling `Card(props)` in Go would be. The
same is true of a lowercase tag once it matches a declaration: `<card>` calls
`card(props)`.

There is no reserved table of HTML element names driving this — `<div>` is an
element only because nothing in the package declares `div`. The **declared
name set** is gathered from every `func`, `var`, `type`, and `const` at
package level, across all `.gsx` and `.go` files in the directory (skipping
import names and names declared only in `_test.go` files). Tags that aren't
valid Go identifiers — dashed custom elements like `<my-widget>` — can never
match a declaration and are always elements.

Note the asymmetry: capital tags resolve function-local names too (`go build`
resolves the emitted call), but lowercase tags resolve **only** package-level
declarations — a local `item := ...` inside a component body does not make
`<item>` a component.

If a lowercase tag matches a declaration that isn't invocable as a component
(`var data int`, `type data string`), codegen still lowers it as a call and Go
reports the mismatch — resolution never consults type information, so a
matching name always becomes a call and `go build` is the final arbiter.

### The wrapper pattern

Because a lowercase tag can now name a component, wrapping a plain element
requires one rule: **inside the body of the declaration that declares name
`x`, the tag `<x>` is always a leaf element**, never a self-call. This makes
wrapper components work with zero extra syntax:

<!--@include: ./_generated/basic-syntax/020-wrapper-pattern.md-->

Every other lowercase tag in that body still resolves normally: inside
`div`'s body, `<span>` would call the package's `span` component if one were
declared. Exclusion is keyed by the enclosing declaration's **name** and
covers its whole body — methods and `var` initializers included: inside
`component (p page) div()`, the tag `<div>` is still the leaf element even if
a package-level `div` component is also declared (the package component
remains reachable via the call form), and inside `var card = ...`, the tag
`<card>` is a leaf. Recursion for a lowercase component uses the Go call
form in a hole (`{item(...)}`) or gives the component a capital name — a
self-named tag never recurses. A self-named lowercase tag whose name isn't a
standard HTML element name is almost always a recursion mistake rather than
an intentional wrapper, so gsx warns on it:

```text
<item> inside component item renders as a leaf element; for recursion call item(...).
```

Two wrapper components that render each other (`div` renders `<span>`,
`span` renders `<div>`) compile cleanly but recurse forever at render time.
gsx reports this as a **wrapper cycle** warning when every edge in the cycle
is unconditional (a tag under `if`/`for` legitimately breaks the cycle at
runtime, so conditional edges are excluded):

```text
wrapper cycle div → span → div will recurse infinitely at render.
```

Static syntax highlighters (tree-sitter, CodeMirror) can't run symbol
resolution, so a lowercase component tag highlights as a plain element.
The gsx LSP resolves the same rule as codegen, so hover and go-to-definition
on a lowercase component tag work correctly today; correcting the
*highlighting* in-editor via LSP semantic tokens is a tracked follow-up (see
the [Roadmap](https://github.com/gsxhq/gsx/blob/main/docs/ROADMAP.md)).
