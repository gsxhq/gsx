---
name: templ-to-gsx-migration
description: Use when migrating templ components to gsx — covers the component-declaration rewrite, children syntax, cross-package calls, URL filter, and the critical children-boundary rule that determines migration order.
---

# Migrating templ components to gsx

Patterns from migrating a production templ UI to gsx.
The corpus at `internal/corpus/testdata/cases/` is the canonical syntax reference.

## Component declarations

| templ | gsx |
|---|---|
| `templ Foo(args) { }` | `component Foo(args) { }` |
| `templ (p P) M(props) { }` | `component (p P) M(props) { }` |

Method components generate `func (p P) M(props) gsx.Node`.
Invocation: `P{}.M(props)` (or `{P{}.M(props)}` inline).

## Signatures are verbatim — no generated `Props`

gsx emits the authored parameter list unchanged; there is **no** generated
`<Name>Props` struct (details in the `gsx` skill). Markup binds params by name
(`<Card title="Hi" featured/>`); Go callers use the positional signature
(`Card("Hi", true)`). `children`/`attrs` are explicit params, declared only when
used:

```gsx
component Panel(children gsx.Node, attrs gsx.Attrs) {
    <section { attrs... }>{children}</section>
}
```

Migration impact:
- A templ component that took a single author-declared struct
  (`component sec(props secProps)`) keeps it — `sec(meta)` callers are unchanged.
- Components that used to generate `<Name>Props` no longer do: keyed Go callers
  (`Card(CardProps{Title:"Hi"})`) become positional (`Card("Hi", true)`). This
  is the bulk of a real conversion (hundreds of call sites).

## Generic components

Use native gsx generics. Do **not** keep templ-era Go wrapper functions whose
only job is to normalize a type parameter and delegate to a private gsx
component — that recreates templ's quirks instead of using gsx.

```gsx
import "github.com/jackc/pgx/v5/pgtype"

component DisplayCheckbox[T bool | pgtype.Bool](label string, checked T) {
    {{ boolean := false }}
    {{ if v, ok := any(checked).(pgtype.Bool); ok { boolean = v.Valid && v.Bool } }}
    {{ if v, ok := any(checked).(bool); ok { boolean = v } }}

    <input type="checkbox" disabled { if boolean { checked } }/>
}
```

Same-package element calls infer the type argument:

```gsx
<DisplayCheckbox label="Active" checked={props.Active}/>
```

When a Go test must call the generated function directly, instantiate the type
param and pass args positionally — the signature is verbatim, there is no
`Props[T]`:

```go
field.DisplayCheckbox[pgtype.Bool]("Active", value)
```

Do not add production wrappers for generated generic components.

## Children syntax

**Placement inside the component body:**

```
// templ
{ children... }

// gsx
{children}
```

**Passing children at the call site:**

```
// templ
@Child(args) { <span>content</span> }

// gsx
<Child ...><span>content</span></Child>
```

**Embedding a bare `gsx.Node` value (childless):**

```gsx
{ node }
```

## Cross-package component calls

Import the package; the tag prefix is the package alias.

```gsx
import "example.com/app/ui"

component Page() {
    <ui.Button label="Save"/>
}
```

This lowers to `ui.Button("Save")` (positional). With children,
`<ui.Button label="Save">text</ui.Button>` passes the body as the component's
`children` param: `ui.Button("Save", gsx.Func(...))`.

Imported generic components infer type arguments in element-call syntax too:

```gsx
import "example.com/app/ui/field"

<field.DisplayCheckbox label="Active?" checked={data.Active}/>
```

This lowers to the generated generic Go call, e.g.
`field.DisplayCheckbox[pgtype.Bool]("Active?", v)`. Prefer the element
form over a Go node-expression call for migrated generic components — it is the
intended gsx API and infers the type argument.

Imported `.gsx` components resolve like local ones — prefer direct fallthrough
attrs and ordered-attrs literals over hand-written `gsx.Attrs` struct literals.

```gsx
<card.DataGrid class="md:grid-cols-2">…</card.DataGrid>
<field.LongDisplayField
    label="X"
    value={v}
    class="col-span-full"
/>
<card.Panel Attrs={{ "data-a": "1", "data-b": "2" }}>panel</card.Panel>
```

The configured class merger still runs on the merged result. Use explicit
`Attrs={{ ... }}` only when you need to pass a whole ordered attr bag as one
prop; for normal class/data/hx attrs, write the attrs directly on the component.

For local presentational helpers, prefer the same JSX shape over hand-written
attribute-bag props:

```gsx
<StatusCard status="O" label="Open" class="bg-yellow-50"/>

component StatusCard(status string, label string) {
    <a class="card" { attrs... }>{label}</a>
}
```

This is cleaner than `ExtraAttrs gsx.Attrs` / `gsx.Attrs{{Key:"class", ...}}`.
Referencing `{ attrs... }` is what generates the `Attrs` prop; `attrs` is
reserved and must not be declared as a normal parameter.

**Lowercase (unexported) package symbols** can use element syntax when gsx
resolves the name to a component declaration or attrs-only component value:
`<entityMetaSection .../>`. An unresolved lowercase name remains a literal HTML
element. Keep helpers unexported unless another package needs them; capitalization
is no longer required merely to get JSX-like call sites.

For semantic wrappers whose only API is forwarded attributes, use an attrs-only
component value and return the real element directly. Do not add a public generic
renderer component or manually merge attrs/classes:

```gsx
func named(name string) func(...gsx.Attr) gsx.Node {
	return func(attrs ...gsx.Attr) gsx.Node {
		return <svg
			viewBox="0 0 24 24"
			stroke="currentColor"
			class="size-5"
			{ attrs... }
		>
			{ rawIcon(name) }
		</svg>
	}
}

var Search = named("search")
```

Call it naturally across packages: `<icon.Search class="size-4"/>`. Scalar
attrs are caller-wins and classes go through the configured class merger, so
the wrapper needs neither `Attrs.Merge` nor a Tailwind-specific helper.
Keep the registry lookup (`rawIcon`) private; callers should only see named
values. Do not retain a second package of one-component-per-icon wrappers or a
public generic `<Icon name="..."/>` escape hatch. Migrate call sites to the
named package API and delete the duplicate layer. When preserving a legacy
default size, place its `class` before caller attrs/classes so authored-order
merging still lets the caller win.

## Conditional attrs instead of if/else element pairs

Don't keep templ-era `if cond { <a href=…>X</a> } else { <span>X</span> }`
pairs for enabled/disabled links. Collapse to ONE element: conditional
attribute for the `href`, conditional class parts for the look:

```gsx
<a
    class={
        "rounded border px-2 py-1", "hover:bg-slate-50": p.HasPrev(), "text-slate-300": !p.HasPrev()
    }
    { if p.HasPrev() { href={p.URL(p.Page - 1)} } }
>
    ← Prev
</a>
```

An `<a>` without `href` is the standard disabled-link form. Same rule for any
attr that exists only in one branch — the branches almost always differ by
attrs/classes, not structure.

Related idioms from the same review pass: pass a ready-made byo props struct to
an imported component with the splat element form
(`<components.Pagination {props.Pagination...}/>`, not
`{ components.Pagination(props.Pagination) }`), and delete signature-identical
wrapper funcs (`func URL(...) = structpages.URLFor(...)` adds nothing — call
the real function).

## Class composition

Do not preserve templ-era `cls := "..."; if cond { cls += " ..." }` scaffolding
unless the class really must be assembled as a string. Prefer gsx's composable
class value in markup:

```gsx
<a
    class={
        "w-full rounded-md text-gray-700",
        "bg-gray-100 font-medium": active,
        "hover:bg-gray-100": !active,
        color
    }
>
```

This keeps styling decisions next to the element, mirrors JSX class composition,
and lets the configured class merger see each source directly.

## URL / routing

templ required explicit `ctx` threading:

```go
// templ
href={ UrlFor(ctx, page, args...) }
```

gsx uses the `url` filter — `ctx` is auto-injected when the first parameter is `context.Context`,
and a `(string, error)` return is auto-unwrapped:

```gsx
href={ page |> url }            // no extra args
href={ page |> url(id) }        // fixed arg
href={ page |> url(args...) }   // variadic spread
```

`url` is wired to `structpages.URLFor` in structpages-integrated apps. Register
sibling filters in `gsx.toml` `[filters]` for the related helpers — they share
the same `(ctx, value, args...) (string, error)` shape:

```toml
url    = "github.com/jackielii/structpages.URLFor"   # href / hx-get / hx-post
id     = "github.com/jackielii/structpages.ID"        # raw HTML id attribute
target = "github.com/jackielii/structpages.IDTarget"  # hx-target ("#id" selector)
```

```gsx
id={ GetEntityComments.Page |> id }          // → structpages.ID(ctx, …)
hx-target={ GetEntityComments.Page |> target }
hx-post={ CreateComment{} |> url(eventID) }
```

Migrate direct `UrlFor(ctx, X, args...)` / `IdFor(ctx, X)` calls to the filter
form for the idiomatic style. A thin app wrapper (e.g. `ui.UrlFor` that only adds
a shortcut over `structpages.URLFor`) is behavior-identical to the `url` filter
when the call site doesn't use the shortcut — verify, then convert.

## JavaScript and CSS attributes

Attribute language is explicit at the call site (there is no `[[jsAttrs]]`
config) — mark JS/CSS-valued attributes with a `js`/`css` literal:

```gsx
@click=js`open = !open`
x-data=js`{ open: false, id: @{id} }`
:class=js`active ? 'on' : 'off'`
style=css`display: none;`
style={css`max-width:@{width}px`}
```

`@{ expr }` holes are escaped for the embedded language, and the `js`/`css`
literals are typed Go values (`gsx.RawJS`/`gsx.RawCSS`). Use them for Alpine
expressions, event handlers, `x-model`, `hx-on:*`, and CSS-valued attrs; keep
non-code Alpine strings plain (`x-ref`, `x-transition:*`, `x-teleport="body"`).

**JS-dev gotcha:** don't write a dynamic JS expression as a plain `{ }` attr —
`attr={ goExpr }` serializes the Go value *as a JS literal*, so a string comes
out quoted and Alpine runs a dead string:

```gsx
@click={ fmt.Sprintf("tab='%s'", id) }   // WRONG — renders a quoted dead string
@click=js`tab = @{id}`                    // RIGHT
```

A Go value that is already a JS expression fragment is `gsx.RawJS` — pass it
directly, or compose it inside a `js` literal where JS expects a value:

```gsx
x-show={tab.Cond}               // tab.Cond is gsx.RawJS
x-show=js`Boolean(@{tab.Cond})`
```

A helper that builds JS returns `gsx.RawJS` and composes `js` literals — never
`fmt.Sprintf` + wrap.

## THE CHILDREN-BOUNDARY RULE (most important for migration order)

**templ and gsx pass children differently:**

- **templ** threads children through `context` via `templ.WithChildren` / `templ.GetChildren`.
- **gsx** passes children as a declared `children gsx.Node` parameter,
  set by the `<Comp>…</Comp>` call site.

**Consequence:** a component that *takes children* cannot be called across the boundary.
Caller and the child-accepting component must be on the **same side**.

**Childless** components (no `{children}` placement) embed freely in *both* directions
because `gsx.Node` and `templ.Component` are the same interface:

```go
type Component interface {
    Render(ctx context.Context, w io.Writer) error
}
```

Because `gsx.Node` and `templ.Component` are structurally identical interfaces
(`Render(ctx context.Context, w io.Writer) error`), a childless gsx node is used
directly wherever a `templ.Component` is expected — no adapter is needed.
(`templ.FromGoHTML` is for `html/template` objects, not gsx nodes.)

**Migration strategy: bottom-up by subtree.**
Convert leaf components (no child slots) first. A parent page migrates to gsx only
once *every child-accepting sub-component it calls* is already gsx.

```
Templ page
  └── templ ChildA (takes children)  ← must migrate FIRST
        └── gsx LeafButton           ← already migrated (childless, crosses freely)
```

Once `ChildA` is gsx, the parent page can be migrated.

## Build coexistence

Generated files from both tools live side-by-side:

| Generator | Output pattern | gitignore |
|---|---|---|
| `gsx generate` | `*.x.go` | yes |
| `templ generate` | `*_templ.go` | yes |

Run both generators in the build; both outputs are regenerated, never committed.

Invoke gsx via module path to avoid Ghostscript name collision:

```sh
go run github.com/gsxhq/gsx/cmd/gsx generate
```

**gsx generation is all-or-nothing per package.** It type-checks the whole
package (including templ's `_templ.go`); if any referenced gsx-generated symbol
(a component function) is undefined, gsx emits **zero** `.x.go` for the package.
So in a fresh checkout with no generated files, bootstrap in this order:

```sh
go tool templ generate            # emits _templ.go (no Go type-check)
go tool gsx generate ./pkg...     # now the package type-checks; emits .x.go
go tool templ generate            # _templ.go now sees the new gsx functions
go build ./pkg...
```

Generate **every** gsx package that's referenced (e.g. `./ds ./ui`, not just
`./ui`) — a missing `ds` `.x.go` breaks the build the same way.

Current gsx keeps locals live when they are used only in a conditional boolean
attribute condition:

```gsx
{{ boolean := compute() }}
<input { if boolean { checked } }/>
```

If an older generator reports `declared and not used: boolean` for this pattern,
update gsx. Do not add `_ = boolean` liveness shims to migrated components.

**Orphaned `_templ.go` on full-file conversion.** When a `.templ` file is
*entirely* converted to `.gsx` (the `.templ` is deleted), its previously-generated
`_templ.go` is orphaned — `templ generate` won't remove it, so the now-duplicate
symbols cause `X redeclared` and gsx emits nothing. Delete the stale
`<name>_templ.go` after removing the `.templ`. (Files that keep their `.templ` —
only some templates removed — don't hit this.)

## Prefer compound components for typed tables

Do not port a templ table API built around `map[string]any`, row adapters,
renderer callbacks, or context thunks. In gsx, the table package should own
only the semantic structure and styling while the caller owns typed iteration
and cell rendering:

```gsx
<table.Table>
	<table.Header>
		<table.Row>
			<table.Column>Subject</table.Column>
			<table.Column>Updated</table.Column>
		</table.Row>
	</table.Header>
	<table.Body>
		{ for _, ticket := range props.Tickets {
			<table.Row>
				<table.Cell class="max-w-64 truncate" title={ticket.Subject}>
					{ ticket.Subject }
				</table.Cell>
				<table.Cell>{ ticket.UpdatedAt }</table.Cell>
			</table.Row>
		} }
	</table.Body>
</table.Table>
```

Register package renderers for generic domain/scalar display (nullable text,
timestamps, IDs), then interpolate values directly. Keep table-specific links,
badges, truncation, and accessibility in the caller where the column meaning is
known. Root attribute fallthrough provides class merging and caller overrides;
do not add explicit `attrs` plumbing to each primitive.

Default renderers are the bootstrap, not an excuse for table callbacks. Register
them once for values whose display is universal, then render the typed value:

```gsx
<table.Cell>{ row.CreatedAt }</table.Cell>
```

Keep column semantics in markup. For example, truncate long text with CSS while
preserving the full value in `title`; do not byte-truncate it in a renderer or
build a temporary display string:

```gsx
<table.Cell class="max-w-64 truncate" title={row.Subject}>
	{ row.Subject }
</table.Cell>
```

Likewise, prefer general pipeline filters (`value |> format("%d")`, plural,
integer conversion) over domain-specific filters such as `rank`. Filters that
can reject input return `(T, error)` so render failures propagate normally.

For sortable/paginated tables, build query state with `net/url.Values` and let
the final attribute hole propagate the route error. Never concatenate raw query
values or hide route failures behind `must()`.

The page props should carry typed rows plus explicit pagination/query state,
not a `GetAttrs(ctx, page)` thunk:

```gsx
component FeedbackTable(props FeedbackProps) {
	<table.Table>…typed rows…</table.Table>
	<pagination.Root>
		<pagination.Previous
			href={FeedbackPage{} |> urlQuery(props.Query, "page", props.Page-1)}
		/>
		…
	</pagination.Root>
}
```

When several links share query state, expose a small typed method such as
`props.Query()` or `props.FilterQuery()` returning `url.Values`. This is the Go
equivalent of JSX code owning a `URLSearchParams`; it removes repeated key/value
pairs without hiding routing or rendering behind callbacks. Compose
`ds/pagination` at the call site so disabled states, targets, and route errors
remain visible in GSX.

Query filters should accept typed scalar values and return errors. Do not force
every call site through `strconv.Itoa` merely because the filter was declared
with `pairs ...string`:

```gsx
href={ListPage{} |> urlQuery("page", props.Page+1, "active", props.Active)}
```

Keep keys string-only. Convert values through a deliberate scalar contract
(strings, booleans, numbers, pointers, `encoding.TextMarshaler`, and
`fmt.Stringer`) and reject unsupported structs/maps/slices. This keeps the GSX
call typed and ergonomic without silently accepting arbitrary `fmt.Sprint`
output.

Delete the old generic table after the last consumer migrates. A compatibility
API built from `map[string]any`, `AttrFunc`, `TableAction`, or pagination attr
thunks has no residual value and preserves the templ constraints the compound
design replaced.

### Migrating existing raw `<table>` markup to `ds/table`

For pages that already have hand-written `<table>…</table>`, the migration is
tag-rename + class-edit only. The app's tailwind-merge `class_merger` makes it
clean: a class you pass to a `table.*` component **overrides** the component's
baked class (passed wins on conflict), so you restyle per call site without
touching the component.

- Rename tags: `<table>`→`<table.Table>`, `<thead>`→`<table.Header>`,
  `<tbody>`→`<table.Body>`, `<tr>`→`<table.Row>`, `<th>`→`<table.Column>`
  (drop the baked `scope="col"`/`text-left`), `<td>`→`<table.Cell>`.
- **Change ONLY the table tags.** Keep every surrounding wrapper `<div>` (card,
  `overflow-x-auto`, ring/rounded) byte-for-byte — deleting a card wrapper is a
  silent visual regression. Cheap gate: the diff must contain no added/removed
  non-table element lines (`git diff | grep '<div'` should be empty).
- **`table.Cell` bakes `whitespace-nowrap`.** Cells holding long free text that
  wrapped before (titles, descriptions, comma-joined lists, error messages) need
  an explicit `whitespace-normal` to keep wrapping; a cell with `truncate`
  correctly stays nowrap; short/data cells adopt the nowrap default. (`break-all`
  is inert under `nowrap` — such a cell also needs `whitespace-normal`.)
- **`border-b` rows double with the baked `divide-y`.** A legacy table that drew
  row lines with per-row `border-b` (and no `divide-y`) shows *doubled* borders
  after migration, because `table.Table`/`table.Body` bake `divide-y`. Pass
  `divide-y-0` to cancel the baked dividers (keeping `border-b`), or drop
  `border-b` and adopt `divide-y`.
- **`table.Table`'s wrapper breaks `sticky` headers.** Its responsive wrapper is
  `<div class="-mx-4 -my-2 overflow-x-auto …">`; `overflow-x:auto` forces
  `overflow-y:auto`, so that div becomes a scroll container. A `sticky top-0`
  `<thead>` inside a `max-h-*` scroll box then sticks to the wrong ancestor and
  stops working. Do NOT migrate a sticky-header-in-scroll table (or the
  sortable-component-header list/DataTable) to `table.Table` — leave it raw until
  `ds/table` grows a wrapper-less variant. Skipping some tables in a file is fine.
- The wrapper's negative margins (`-mx-4 sm:-mx-6 lg:-mx-8`) are net-zero at
  `sm:`+ (cancelled by the inner `sm:px-6 lg:px-8`) and go edge-to-edge on
  mobile; it sits fine in a page container or a `shadow/ring rounded-lg` card.
- Also skip non-data tables: key-value summaries with no `<thead>`, layout/
  form tables, and reference tables embedded in doc prose.

Goal is **sensible UI, not 1:1 with the old markup** — adopt `ds/table`'s
defaults where they look right; override a baked class only when the default is
actually bad UI.

## Whitespace parity: gsx collapses like JSX, templ did not

gsx normalizes whitespace with **JSX/React rules** (`internal/wsnorm`): a newline
between two elements is dropped entirely (no space), while a same-line space is
preserved. templ preserved inter-element whitespace, so token-per-span content
that relied on the newline-as-space renders **cramped** after migration:

```gsx
<span>{ c.Field }:</span>
<span class="line-through">{ c.Old }</span>
<span>&#8594;</span>
<span>{ c.New }</span>
// renders  Active:true→false   (JSX-correct — NOT a gsx bug)
```

Add explicit spaces **inside** the spans where you want them:

```gsx
<span>{ c.Field }: </span>
<span class="line-through">{ c.Old }</span>
<span> &#8594; </span>
<span>{ c.New }</span>
// renders  Active: true → false
```

Same fix for "Read more →" links (`Read more <span> &rarr;</span>`) and
`Old → New` type-change labels. An arrow inside a `flex justify-between` span is
positioned by layout, not inline whitespace, so it needs no fix.

**Verification blind spot:** a DOM-equivalence harness that normalizes whitespace,
and `assertContains` substring checks, both miss this — tests stay green while the
text is cramped. Confirm inline text spacing **visually** (browser, both apps side
by side).

## After the migration: merge template-adjacent .go into the .gsx

The `login.go` + `login.templ` file split existed only because templ's LSP broke
constantly. The gsx LSP handles Go inside `.gsx` files, so once a feature is
migrated, fold its Go half (route structs, `Props`, `ServeHTTP` handlers,
validation helpers) into the `.gsx` — one file per feature. gsx passes top-level
Go through verbatim; handlers/types read naturally beside the components they
serve (structpages `examples/todo/pages.gsx` and `examples/blog/admin/*.gsx`
are the reference layout).

What stays plain `.go`: `main.go`, route-wiring files (`routes.go`), data/store
and auth layers, and any file whose **line numbers are load-bearing** (e.g. a
lint fixture whose test findings anchor to `pages.go:NN`).

Mechanics: move declarations verbatim, merge the import lists into the `.gsx`'s
single import block (gsx fmt in goimports mode dedupes/orders), delete the
`.go`. The deleted file had no generated sibling, so nothing else to clean up.

## RenderComponent with element literals (structpages)

Once a handler lives in a `.gsx` file, element literals are available in
Go-expression position, and `structpages.RenderComponent` accepts a direct
component instance ("Direct component" pattern):

```gsx
func (LoginPage) ServeHTTP(w http.ResponseWriter, r *http.Request, a *auth.Service) error {
    …
    return structpages.RenderComponent(<LoginShell errMsg={msg}/>)
}
```

Prefer this over the method-expression form (`RenderComponent(Page.Method)`)
when the handler renders one concrete component — args are visible inline and the
LSP checks them. `target.Is(X)` branch routing composes with it: `Is` routes, the
literal renders. Two caveats: `RenderComponent(target, args...)` flows keep the
target form, and a component instance can't take extra `RenderComponent` args
(express everything in the literal).

## No `must()` in examples — propagate (string, error) through holes

Never introduce a `must[T](v T, err error) T` panic helper to adapt an
error-returning URL/ID call to a plain-string position. gsx attribute holes
auto-unwrap `(string, error)` and propagate the error through the render, so
the real fix is to move the error to the hole:

```gsx
// helper returns (string, error); the attr hole unwraps it
func postFormAction(ctx context.Context, p store.Post) (string, error) {
    if p.ID == 0 {
        return components.URL(ctx, postCreateHandler{})
    }
    return components.URL(ctx, postUpdateHandler{}, "id", p.ID)
}

<form action={postFormAction(ctx, p)} method="post">
```

When the fallible call is in a **statement** position — a `{{ }}` block computing
a value, not a hole — `return err` directly. A `{{ }}` block runs inside the
component's render closure, so a bare `return err` propagates the render error
exactly like a hole does:

```gsx
component Card(key string) {
    {{
        id, err := lookupID(ctx, key)
        if err != nil {
            return err   // propagates from Render — no must()/panic
        }
    }}
    <Inner id={id}/>
}
```

This is not obvious (the component *looks* like it returns `gsx.Node`), but it is
the idiom — never `panic(err)` or a `must()` wrapper in a component body.

## Repo integration: committed .x.go, hooks, and CI drift gates

Two valid `.x.go` strategies. During mixed templ+gsx coexistence (table above),
gitignore both generators' outputs and regenerate in the build. For a
**pure-gsx example/consumer repo**, committing `.x.go` (so `go run .` works from
a fresh clone) needs three things to agree:

1. **Pre-commit hooks must not reformat `*.x.go`.** A repo hook running
   `goimports -w`/`gofmt -w` over all `*.go` silently canonicalizes generated
   files, so committed output ≠ raw `gsx generate` output and any CI freshness
   check fails. Add `-not -name "*.x.go"` to the hook's find. A hook rewriting
   generated files is automated hand-editing.
2. **Pin gsx and generate with the module's own pin**:
   `go run github.com/gsxhq/gsx/cmd/gsx generate -no-cache .` inside the module
   locks codegen to the `go.mod` version — bumping gsx is one `go get`.
   Pinning gotchas: a stale `require github.com/gsxhq/gsx v0.0.0` placeholder
   (left from a deleted `replace`) blocks `go get @main` — `go mod edit
   -droprequire` first. And `go mod tidy` does NOT pull the tool's transitive
   deps into `go.sum` for `go run`; one `GOFLAGS=-mod=mod go run …gsx …`
   populates it.
3. **CI drift gate**: regenerate, then `git status --porcelain .` (not
   `git diff --exit-code` — porcelain also catches newly created generated
   files).

## Tooling parity: anything that parses .templ goes blind

Lint/analysis tools built on templ's parser (or that glob `*.templ`) silently
lose coverage after migration — e.g. structpages-lint's `[url-attr]` rule no
longer sees the deliberate bad-URL fixtures once they live in `.gsx`. Audit for
`*.templ` globs and templ-parser imports when migrating; either port the tool
to gsx sources or re-pin its expectations with the gap documented. Don't let a
green test hide vanished coverage.

## Migrating a page wrapper (Content/Page) and the app shell

A detail page's `Content` method migrates to a gsx method component once its
composed sub-components are gsx. The thin `Page` wrapper that does
`@AppShellLayout() { @p.Content(props) }` **stays templ** until `AppShellLayout`
itself is gsx: `AppShellLayout` takes children, and passing children into a templ
component *from gsx* crosses the boundary. Leaving `Page` templ is fine — it
embeds the gsx `Content` as a leaf child (a `gsx.Node` where a `templ.Component`
is expected), which crosses freely.
