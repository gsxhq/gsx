# Composition

gsx components are ordinary Go values — they compose by calling each other. A `component` declaration compiles to a Go function; calling it inside another component's body is the same as calling any Go function. There is no runtime template registry and no virtual-DOM layer: composition is just Go.

## Calling components

A tag that starts with an **uppercase letter** (or a dotted package path like `<ui.Button>`) is a component call; lowercase and hyphenated tags are HTML elements. The component's declared params become a generated `<Name>Props` struct, and each attribute at the call site sets the corresponding field.

<!--@include: ./_generated/composition/010-components-props.md-->

`Card` declares three params — `title string`, `featured bool`, `count int` — so the codegen produces a `CardProps` struct with matching fields. `Page` calls `<Card title={t} featured count={n}/>`: the `featured` bare attribute (no `=` value) sets `Featured` to `true`, matching the Bool shorthand for boolean props.

The `Page` component itself has params `t string` and `n int`, so `Page(PageProps{T: "Hi", N: 3})` is valid Go at the call site. Component names are just Go identifiers; cross-package calls look like `<ui.Button label="X"/>` where `ui` is an imported package alias.

## Generic components

Components can declare Go type parameters after the component name. The
generated props type and component function carry the same type parameters.
Component tags can pass explicit type arguments with Go-style brackets, or omit
them when Go can infer the type arguments from the supplied props.

```gsx
component Box[T string | int](value T) {
	<span>box</span>
}

component Page() {
	<Box[int] value={7} />
	<Box[string] value={"ok"} />
	<Box value={"inferred"} />
}
```

This lowers to generic Go declarations shaped like
`type BoxProps[T string | int] struct { ... }` and
`func Box[T string | int](p BoxProps[T]) gsx.Node`. A generic tag call lowers to
an explicit Go instantiation:

```go
Box[int](BoxProps[int]{Value: 7})
Box[string](BoxProps[string]{Value: "ok"})
```

For omitted tag type arguments, gsx asks Go's type checker to infer them during
generation and then emits the same explicit shape in the final `.x.go`:

```go
Box[string](BoxProps[string]{Value: "inferred"})
```

Inference only uses information available at the component call site. If Go
cannot infer a type parameter from the supplied props, generation fails with a
diagnostic asking for an explicit instantiation:

```text
type inference failed for <Box>; please instantiate with <Box[type] ...>
```

Use explicit type arguments when a type parameter does not appear in a supplied
prop, when the value is ambiguous, or when you want the call site to state the
instantiation directly.

### Renderable type parameters

Interpolating a value of type parameter `T` directly — `{value}` where `value T` — only compiles when `T`'s constraint fits one of two shapes:

- **Same kind**: every term is the same basic kind, tilde or not (`~string`, `int | ~int64`, a single named type like `Slug`). Codegen emits a static conversion (`string(value)`, `int64(value)`, …) that compiles for the whole type set.
- **Mixed kinds, all dispatchable**: terms mix kinds but every term is either an unnamed predeclared type (`string`, `int`, `bool`, …), an unnamed `[]byte`, or implements `fmt.Stringer` — for example `string | int` or `MyStringer | string`. Codegen emits a runtime type switch that has a matching case for each term.

Anything else — a tilde term mixed with another kind (`~string | int`), or a named scalar term with no `String()` method mixed with another kind (`Slug | int` where `type Slug string`) — is rejected at generate time with `error[unrenderable]`, because neither the static conversion nor the runtime switch covers every type in the set. Convert explicitly in the expression instead, e.g. `{string(value)}`.

## Children `{children}`

When a component wraps nested markup, it accesses that markup through the special `{children}` placeholder. The caller places any content between the open and close tags; the component decides where it appears by writing `{children}` in its body.

<!--@include: ./_generated/composition/020-children.md-->

`Card` renders `{children}` inside `<div class="card__body">`. The caller `<Card title="Hello"><em>composed</em></Card>` supplies `<em>composed</em>` as the children node. `{children}` is an explicit placement point in the markup: the codegen adds a `Children gsx.Node` field to the generated props struct whenever it appears in the body, and the caller's content is bound to that field — not passed through a templ-style context.

Content appears exactly where `{children}` sits inside the component, and only there. A component that does not write `{children}` silently drops the caller's content.

## Named slots

When a component needs more than one content hole — a header, a body, a footer — declare additional params of type `gsx.Node`. These typed params work like named slots: the caller passes markup inline as an attribute value.

<!--@include: ./_generated/composition/030-named-slots.md-->

`Panel` declares `header gsx.Node` and `footer gsx.Node`. The call site passes
markup as `header={ <h1>H</h1> }` and a string literal as `footer="F"`. For
`gsx.Node` params, codegen converts string literals to renderable text nodes.
The component renders each slot by interpolating `{header}` and `{footer}`
exactly like any other variable.

This is distinct from `{children}`: a `gsx.Node` param is a named, typed field in the props struct, passed as a named attribute. `{children}` is the implicit content between the open and close tags.

## Cross-file &amp; cross-package

Multiple `.gsx` files in the same package share a single Go package, so components defined in one file are available in all others — exactly like ordinary Go. There is no explicit import between files in the same package; just split the declarations across files as you would with any Go source.

<!--@include: ./_generated/composition/040-template-composition.md-->

`components.gsx` defines `Button` and `Card`. `page.gsx` defines a `HomePage` struct and a method component `Render` that composes them. The codegen treats each file as a normal Go source file within the same package build; visibility follows standard Go rules (exported identifiers are available across packages; unexported ones within the package).

Cross-package calls import the other package and use its alias: `<ui.Button label="Save"/>`. The generator resolves the tag through the Go type system, so refactoring — renaming a type, moving a package — is caught by the compiler like any other Go identifier.

## Explicit attribute forwarding

Undeclared component attributes are rejected unless the component explicitly
uses the `attrs` bag. Place `{ attrs... }` on the element that should receive
them; gsx never infers a destination from the component's root.

<!--@include: ./_generated/composition/050-explicit-attribute-forwarding.md-->

Here `Button` explicitly forwards `class`, `data-test`, and `hx-post` to its
`button`. The explicit spread also makes wrapper components unambiguous: place
the bag on the inner control, split it across elements, or omit it to expose only
declared props.

## Method components

A component can be declared as a **method** on a named struct, binding it to a receiver. The receiver type carries page-level state (loaded once); the component's params carry per-call data.

<!--@include: ./_generated/composition/060-method-components.md-->

`component (p UsersPage) Page()` and `component (p UsersPage) Grid(sort string)` each compile to methods on `UsersPage`. Inside any method component's body, the receiver name (`p`) is in scope for reading page state, and the declared params (`sort`) carry per-call data from the generated props struct.

A method component invokes another method component with `<receiver.Method .../>`, where `receiver` is the receiver variable name. In the example, `Page` calls `<p.Grid sort={p.Sort}/>`: the `p.` prefix routes the call to the `Grid` method on the same receiver value, passing `sort` from the current page state. This is analogous to `<pkg.Name/>` for package-qualified components, except `p` is a local variable rather than an import alias.

Method components may also declare method-owned type parameters, for example
`component (p Page) Row[T any](value T) { ... }`, and are invoked as
`<p.Row[int] value={1} />`. Generated Go for that form requires a go1.27+
toolchain (the first release whose `go/parser` accepts methods with type
parameters). On an older toolchain, gsx skips the component and reports
`error[unsupported-toolchain]`; generation continues for the rest of the
package.

Method components are useful for page handlers: the HTTP handler builds the struct from the request, then the template methods read from it without threading data through every call. Multiple method components on the same receiver share the receiver's fields without any additional passing.
