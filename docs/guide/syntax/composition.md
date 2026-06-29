# Composition

gsx components are ordinary Go values — they compose by calling each other. A `component` declaration compiles to a Go function; calling it inside another component's body is the same as calling any Go function. There is no runtime template registry and no virtual-DOM layer: composition is just Go.

## Calling components

A tag that starts with an **uppercase letter** (or a dotted package path like `<ui.Button>`) is a component call; lowercase and hyphenated tags are HTML elements. The component's declared params become a generated `<Name>Props` struct, and each attribute at the call site sets the corresponding field.

<!--@include: ./_generated/composition/010-components-props.md-->

`Card` declares three params — `title string`, `featured bool`, `count int` — so the codegen produces a `CardProps` struct with matching fields. `Page` calls `<Card title={t} featured count={n}/>`: the `featured` bare attribute (no `=` value) sets `Featured` to `true`, matching the Bool shorthand for boolean props.

The `Page` component itself has params `t string` and `n int`, so `Page(PageProps{T: "Hi", N: 3})` is valid Go at the call site. Component names are just Go identifiers; cross-package calls look like `<ui.Button label="X"/>` where `ui` is an imported package alias.

## Children `{children}`

When a component wraps nested markup, it accesses that markup through the special `{children}` placeholder. The caller places any content between the open and close tags; the component decides where it appears by writing `{children}` in its body.

<!--@include: ./_generated/composition/020-children.md-->

`Card` renders `{children}` inside `<div class="card__body">`. The caller `<Card title="Hello"><em>composed</em></Card>` supplies `<em>composed</em>` as the children node. The component does not declare `Children` in its param list — the codegen adds an implicit `Children gsx.Node` field to the generated props struct whenever `{children}` appears in the body.

Children is an explicit placement: nothing renders at the call-site location; content appears exactly where `{children}` sits inside the component, and only there. A component that does not write `{children}` silently drops the caller's content.

## Named slots

When a component needs more than one content hole — a header, a body, a footer — declare additional params of type `gsx.Node`. These typed params work like named slots: the caller passes markup inline as an attribute value.

<!--@include: ./_generated/composition/030-named-slots.md-->

`Panel` declares `header gsx.Node` and `footer gsx.Node`. The call site passes markup as `header={ <h1>H</h1> }` (JSX-style expression attribute) and a string literal as `footer="F"`. Strings satisfy `gsx.Node`, so either works. The component renders each slot by interpolating `{header}` and `{footer}` exactly like any other variable.

This is distinct from `{children}`: a `gsx.Node` param is a named, typed field in the props struct, passed as a named attribute. `{children}` is the implicit content between the open and close tags.

## Cross-file &amp; cross-package

Multiple `.gsx` files in the same package share a single Go package, so components defined in one file are available in all others — exactly like ordinary Go. There is no explicit import between files in the same package; just split the declarations across files as you would with any Go source.

<!--@include: ./_generated/composition/040-template-composition.md-->

`components.gsx` defines `Button` and `Card`. `page.gsx` defines a `HomePage` struct and a method component `Render` that composes them. The codegen treats each file as a normal Go source file within the same package build; visibility follows standard Go rules (exported identifiers are available across packages; unexported ones within the package).

Cross-package calls import the other package and use its alias: `<ui.Button label="Save"/>`. The generator resolves the tag through the Go type system, so refactoring — renaming a type, moving a package — is caught by the compiler like any other Go identifier.

## Attribute fallthrough

Attributes at the call site that are **not** in the component's declared params do not cause an error — they fall through to the component's root element automatically. This lets utility attributes like `class`, `data-*`, and HTMX directives (`hx-*`, `hx-post`, etc.) be passed without the component author having to declare them.

<!--@include: ./_generated/composition/050-fallthrough-attributes.md-->

`Button` declares only `variant string`. At the call site, `class="w-full"`, `data-test="x"`, and `hx-post="/go"` are all undeclared — they fall through to the `<button>` root element. The `class` attribute is **merged, not replaced**: the component's own `class="btn"` is kept, and the caller's `class="w-full"` is appended (`"btn w-full"`). Caller classes are appended after component classes, so if both sides specify the same utility the caller's value comes last (CSS cascade wins for equal-specificity rules).

All other fallthrough attrs are appended verbatim to the root element's attribute list. The generated props struct grows a `gsx.Attrs` field behind the scenes; the component author does not need to spread it explicitly on the root element.

## Method components

A component can be declared as a **method** on a named struct, binding it to a receiver. The receiver type carries page-level state (loaded once); the component's params carry per-call data.

<!--@include: ./_generated/composition/060-method-components.md-->

`component (p UsersPage) Grid(sort string)` compiles to a method `func (p UsersPage) Grid(props UsersPageGridProps) gsx.Node`. Inside the body, `p` is the receiver (page state such as `p.Title`) and `sort` is a call-time param from the generated `UsersPageGridProps{Sort: "name"}`.

Method components are useful for page handlers: the HTTP handler builds the struct from the request, then the template methods read from it without threading data through every call. Multiple method components on the same receiver share the receiver's fields without any additional passing.
