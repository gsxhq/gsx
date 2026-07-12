# Composition

gsx components are ordinary Go values — they compose by calling each other. A `component` declaration compiles to a Go function; calling it inside another component's body is the same as calling any Go function. There is no runtime template registry and no virtual-DOM layer: composition is just Go.

## Calling components

A tag that starts with an **uppercase letter** (or a dotted package path like `<ui.Button>`) is always a component call. A lowercase tag is also a component call when its name matches a package-level declaration — otherwise it's a plain HTML element. See [Basic Syntax — Element vs component](./basic-syntax.md#element-vs-component) for the full resolution rule and the wrapper pattern it enables. The component's declared params become a generated `<Name>Props` struct, and each attribute at the call site sets the corresponding field.

<!--@include: ./_generated/composition/010-components-props.md-->

`Card` declares three params — `title string`, `featured bool`, `count int` — so the codegen produces a `CardProps` struct with matching fields. `Page` calls `<Card title={t} featured count={n}/>`: the `featured` bare attribute (no `=` value) sets `Featured` to `true`, matching the Bool shorthand for boolean props.

The `Page` component itself has params `t string` and `n int`, so `Page(PageProps{T: "Hi", N: 3})` is valid Go at the call site. Component names are just Go identifiers; cross-package calls look like `<ui.Button label="X"/>` where `ui` is an imported package alias.

## Generic components

Components can declare Go type parameters after the component name. The
generated props type and component function carry the same type parameters.
Component tags can pass explicit type arguments with Go-style brackets, or omit
them when Go can infer the type arguments from the supplied props.

<!--@include: ./_generated/composition/070-generic-components.md-->

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

::: v-pre
Inference sees exactly the props you supply — like an ordinary Go generic
function call built from a partial argument list, not a check against the
component's full declared param list. A non-generic prop that isn't needed to
pin down the type parameters can be omitted entirely; the call still infers.
Given `component Button[T string | int](label T, size string)`, the call
`<Button label={7} />` omits `size` and still infers `T = int`, lowering to
`Button[int](ButtonProps[int]{Label: 7})` — `Size` takes its zero value.
Omitting a prop never blocks inference by itself; it only fails when the
props actually supplied don't mention every type parameter.
:::

<!--@include: ./_generated/composition/080-explicit-type-arguments.md-->

An inferred call works in any body position — inside `{ for … }` and
`{ if … }` control flow, and as a child-prop / named-slot value — exactly like
an explicit instantiation would; inference runs once per call site regardless
of where the tag sits.

::: v-pre
Identifiers starting with `_gsx` are **reserved for the generator** — component
params, method-component receiver names, and any other identifier you write in Go
position must not start with that prefix. gsx's own generated code (writer
locals, per-call-site inference helpers, filter-package aliases, and so on)
lives exclusively in the `_gsx*` namespace, so keeping it clear of your names is
what avoids collisions. gsx enforces this by lexing your Go: a `_gsx` identifier
anywhere it can see — a declaration, a local, a `{{ }}` GoBlock, an
interpolation — is a positioned error at generate time. See
[Raw Go](./raw-go.md#the-gsx-prefix-is-reserved) for the full rule.
:::

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

::: v-pre
Within the same module, gsx also discovers an imported component's declared
props — including its synthesized `Attrs gsx.Attrs` fallthrough field —
during module analysis, so a call like `<ui.Panel attrs=&#123;&#123; "data-a": "1" &#125;&#125;>`
behaves exactly as it would for a same-package component: bare fallthrough
attrs and the ordered-attrs literal split against the same declared field set
and merge the same way (see [Attributes — ordered-attrs literal](./attributes.md)).

For components gsx cannot analyze — packages outside the current module, or
plain Go packages with no `.gsx` files — call-site identifier attrs are
assumed to be prop fields instead of discovered ones, and
`attrs=&#123;&#123; … &#125;&#125;` requires the Props type to declare an `Attrs gsx.Attrs`
field explicitly (a missing field is a Go compile error at the generated
call site). When a same-module dependency's props cannot be analyzed (for
example, a parse or type error in its `.gsx` files), generation continues
and gsx emits an `imported-props-unavailable` warning naming the dependency,
falling back to the same assumed-prop treatment for its components.
:::

A capitalized or dotted tag can also resolve to a package-level *value* —
a `var` or `func` typed `func(gsx.Attrs) gsx.Node`, `func([]gsx.Attr) gsx.Node`,
or `func(...gsx.Attr) gsx.Node` — rather than a `component` declaration, as long
as the callee's package has no matching `<Name>Props` type. See
[Props — attrs-only component values](./props.md#attrs-only-component-values)
for the recognition rule and how call-site attrs merge in that case.

## Explicit attribute forwarding

Undeclared component attributes are rejected unless the component explicitly
uses the `attrs` bag. Place `{ attrs... }` on the element that should receive
them; gsx never infers a destination from the component's root.

<!--@include: ./_generated/composition/050-explicit-attribute-forwarding.md-->

Here `Button` explicitly forwards `class`, `data-test`, and `hx-post` to its
`button`. The explicit spread also makes wrapper components unambiguous: place
the bag on the inner control, split it across elements, or omit it to expose only
declared props.

A hole-free embedded-language literal — `` x-model=js`pdcaCategory` ``,
`` data-tip=`plain text` ``, or `` x-init=css`color:red` `` — forwards exactly
like a plain string attribute: it always falls through to the `attrs` bag as raw
text (JSX-style directive forwarding), never binding a declared prop. A literal
carrying an `@{ }` interpolation cannot yet be a component prop; move it to an
element inside the component, or forward the value through a declared prop.

### Precedence

This precedence rule applies to **every element spread** `{ x… }`, whatever
`gsx.Attrs` expression is spread — the implicit `attrs` bag, a byo component's
declared `Attrs` field, a generated component's own named param, a local
variable, a function result, or any other `gsx.Attrs`-typed expression all get
identical treatment. Whichever expression is spread onto an element, the
spread's position decides who wins, JSX-style:

- attributes written **before** the spread are defaults — a caller attribute
  with the same name overrides them;
- attributes written **after** the spread are forced — the component always
  wins and the caller's value never renders;
- a conditional attribute (`{ if cond { … } }`) follows the same rule for
  whichever branch is taken.

**`class` and `style` compose uniformly**: every same-name contribution on an
element merges into one attribute, whether it is static, composable, inside a
conditional branch, or supplied by a spread. This rule also applies when the
element has no spread. Class tokens retain source order and are deduplicated by
the configured class merger. Style declarations retain source order, with a
later declaration for the same CSS property winning. For example,
`style="color:red"` followed by a taken conditional `style="margin:0"` emits
one `style="color:red; margin:0"`; a later `style="color:blue"` replaces the
earlier `color` declaration. Untaken conditional branches are not evaluated.

Because `class` and `style` compose, they are exempt from the scalar position
rule around a spread: a contribution written after the spread still merges
rather than becoming forced.

Every element spread also sanitizes URL-classified attribute keys at the leaf,
for any bag, with no exceptions and no unsanitizing spread — see
[Attributes — Spread](./attributes.md#spread) for the full contract.
`gsx.RawURL` is the only, per-value opt-out. Compose a bag yourself
(`Merge`/`ConcatAttrs`) before spreading it if you want duplicate keys
resolved eagerly rather than at render time.

### Derived bags

The spread expression doesn't have to be a bare bag. Any expression that
evaluates to `gsx.Attrs` — the implicit `attrs` bag, a byo `p.Attrs` field, a
named `gsx.Attrs` param, a local variable, or something derived from any of
those — is forwarded with the same merge-and-override semantics, and is
evaluated exactly once:

```gsx
<input { attrs.Without("type")... }/>        // forward everything except type
<div { attrs.Merge(extra)... }>…</div>       // compose another gsx.Attrs bag in
<span { p.Attrs.Without("id")... }>x</span>  // byo declared bag, derived the same way
```

This is also how a component keeps final say over `class`: forward
`{ attrs.Without("class")... }` and the root's own `class` stands while the
caller's is dropped.

An element may carry multiple attribute spreads. They merge by source
order — later spreads win per key, `class`/`style` aggregate — the same rule
as any two attributes of the same name. `{ a... } { b... }` is `b` overriding
`a`.

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
