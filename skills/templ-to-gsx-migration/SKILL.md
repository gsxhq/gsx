---
name: templ-to-gsx-migration
description: Use when migrating templ components to gsx, or running templ and gsx side by side in one module — component declarations, children, cross-package calls, URL helpers, build coexistence, and the children-boundary rule that decides migration order.
---

# Migrating templ components to gsx

**REQUIRED:** use the `gsx` skill for the target idioms. A migration that only
changes syntax carries templ habits over — class helpers, string-built JS and
CSS, Props structs, `{ Comp(...) }` calls — and the `gsx` skill's rewrite table
is what to convert them to.

## Declarations and signatures

| templ | gsx |
|---|---|
| `templ Foo(args) { }` | `component Foo(args) { }` |
| `templ (p P) M(args) { }` | `component (p P) M(args) { }` |
| `@Foo(a, b)` | `<Foo x={a} y={b}/>` |
| `@p.M(props)` | `<p.M props={props}/>` |
| `{ children... }` | `{children}`, with a declared `children gsx.Node` parameter |
| `@Child(args) { <span>…</span> }` | `<Child …><span>…</span></Child>` |

gsx emits the authored parameter list unchanged; there is **no** generated
`<Name>Props` struct. Markup binds parameters by exact name
(`<Card title="Hi" featured/>`); Go callers use the positional signature
(`Card("Hi", true)`). `children` and `attrs` are ordinary declared parameters:

```gsx
component Panel(children gsx.Node, attrs gsx.Attrs) {
	<section { attrs... }>{children}</section>
}
```

- A templ component that took one author-declared struct keeps it; its Go
  callers are unchanged.
- Callers of components whose props struct you dissolve into parameters become
  positional. In a real conversion that is most of the call-site churn.

## Generic components

Use gsx generics directly. Do not keep templ-era wrappers whose only job is to
pin a type parameter and delegate.

```gsx
component Select[T ~string](name string, options []T, selected T, attrs gsx.Attrs) {
	<select name={name} { attrs... }>
		{ for _, o := range options {
			<option selected={o == selected}>{ o }</option>
		} }
	</select>
}

<Select name="size" options={sizes} selected={current}/>
```

`T ~string` fits typed string enums; a type parameter renders directly only
when its constraint is one kind. Element calls infer the type argument, same
package or imported
(`<field.Select …/>`). A Go caller instantiates explicitly and passes
positionally: `field.Select[string]("size", sizes, current, nil)`.

## Cross-package calls and attributes

The tag prefix is the package alias: `<ui.Button variant="primary">Save</ui.Button>`.
Lowercase names resolve to package-local components too (`<entityMeta …/>`), so
helpers need not be exported just to get tag syntax.

Write caller attributes directly on the tag; they land in the component's
declared `attrs` bag, and `class` merges through the configured class merger:

```gsx
<card.Panel class="md:col-span-2" data-id={id} hx-get={u}>…</card.Panel>
```

Don't port templ's `templ.Attributes{…}` / hand-built `gsx.Attrs{{Key: …}}`
props. When one call site must pass a whole ordered bag, use
`attrs={{ "data-a": "1", "data-b": "2" }}` or `attrs={bag}`.

A semantic wrapper whose only API is forwarded attributes can be an
attrs-only value returning the element directly:

```gsx
func named(name string) func(attrs ...gsx.Attr) gsx.Node {
	return func(attrs ...gsx.Attr) gsx.Node {
		return <svg viewBox="0 0 24 24" class="size-5" { attrs... }>{ iconPath(name) }</svg>
	}
}

var Search = named("search")   // <icon.Search class="size-4"/>
```

## URL helpers become filters

templ threads `ctx` by hand: `href={ URLFor(ctx, Page{}, id) }`. In gsx,
register the helper as a pipeline filter in `gsx.toml`; a leading
`context.Context` parameter is injected and a `(string, error)` result
unwraps in the attribute hole, propagating the error from `Render`:

```toml
[filters]
url = "example.com/app/routes.URLFor"   # func(ctx, page any, args ...any) (string, error)
```

```gsx
<a href={ Page{} |> url(id) }>…</a>
```

Delete thin wrappers that only forward to the real helper.

## No `must()` — put the fallible call in the hole

templ code wraps fallible helpers in `must(...)`. gsx unwraps a `(T, error)`
result in every expression position — text, native attributes, component
inputs, `f`/`js`/`css` holes, pipeline stages — hoisting the call ahead of the
write and returning the error from `Render`. Delete `must()` and pass the call
straight through:

```gsx
<form action={formAction(ctx, p)} method="post">
<Inner id={lookupID(ctx, key)}/>
<a href=f`/items/@{lookupID(ctx, key)}`>…</a>
```

Don't pre-compute into a `{{ }}` block with `if err != nil { return err }` for
a value used once; the hole already does that. Reach for the block only when
one result feeds several holes and must be computed once, or when the error
needs wrapping. A helper called with `ctx` across many templates is better
registered as a filter (the leading `context.Context` is injected):
`{ key |> lookupID }`. To handle an error locally instead of returning it, use
an `if` init: `{ if id, err := lookupID(ctx, key); err == nil { … } }`.

## THE CHILDREN-BOUNDARY RULE (decides migration order)

- **templ** threads children through `context` (`templ.WithChildren` /
  `templ.GetChildren`).
- **gsx** passes them as a declared `children gsx.Node` parameter.

So a component that **takes children** cannot be called across the boundary:
the caller and the child-accepting component must be on the same side.
**Childless** components cross freely both ways, because `gsx.Node` and
`templ.Component` are the same interface (`Render(ctx, w) error`) — no adapter.

**Migrate bottom-up by subtree.** Leaves first. A parent moves to gsx only once
every child-accepting component it calls is gsx.

```
templ Page
  └── templ Layout (takes children)   ← must migrate first
        └── gsx Button                ← already gsx; childless, crosses freely
```

A thin templ page wrapper doing `@Layout() { @p.Content(props) }` can stay
templ until `Layout` is gsx: the gsx `Content` embeds as a childless node.

## Build coexistence

| Generator | Output |
|---|---|
| `gsx generate` | `*.x.go` |
| `templ generate` | `*_templ.go` |

- gsx type-checks the whole package, templ output included. In a fresh
  checkout with nothing generated, bootstrap: `templ generate` → `gsx generate
  ./...` → `templ generate` (templ now sees the gsx functions) → `go build`.
- Generate **every** package with `.gsx` sources that others reference, not
  just the one you edited.
- Converting a whole `.templ` file: delete its `<name>_templ.go` too. templ
  won't remove it, and the duplicate symbols break the package.
- `gsx` may collide with Ghostscript on PATH. Use `go tool gsx` (tool
  directive) or `go run github.com/gsxhq/gsx/cmd/gsx`.

Committing generated `.x.go` (so `go run .` works from a clone) needs:
pre-commit hooks that skip `*.x.go` (a `gofmt -w` hook is hand-editing
generated code), generation pinned to the module's own gsx version, and a CI
drift gate using `git status --porcelain` (it also catches new files).

## Whitespace parity

templ kept a newline between inline elements as a space; gsx drops a line
break next to an element or `{expr}` (the rule is in the `gsx` skill). Migrated
token-per-line markup renders cramped: `Active:true→false`. Put the space
inside an element (`<span> → </span>`) or on one line.

DOM-equivalence tests that normalize whitespace and substring assertions both
miss this. Check inline text spacing visually.

## After the migration

- **Fold template-adjacent Go into the `.gsx`.** The `x.go` + `x.templ` split
  existed for templ's tooling; gsx passes top-level Go through verbatim and its
  LSP handles it. Keep `main.go`, route wiring, and data/auth layers as `.go`.
- **Audit tooling that parses `.templ`.** Linters and scripts that glob
  `*.templ` or use templ's parser silently lose coverage. Port them or record
  the gap; a green run can hide vanished checks.
- **Delete compatibility layers** (generic table adapters, attr thunks, wrapper
  packages) once the last templ consumer is gone.
