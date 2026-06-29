---
name: templ-to-gsx-migration
description: Use when migrating templ components to gsx — covers the component-declaration rewrite, children syntax, cross-package calls, URL filter, and the critical children-boundary rule that determines migration order.
---

# Migrating templ components to gsx

Learned while migrating one-learning's button/UI layer from templ to gsx.
The corpus at `internal/corpus/testdata/cases/` is the canonical syntax reference.

## Component declarations

| templ | gsx |
|---|---|
| `templ Foo(args) { }` | `component Foo(args) { }` |
| `templ (p P) M(props) { }` | `component (p P) M(props) { }` |

Method components generate `func (p P) M(props) gsx.Node`.
Invocation: `P{}.M(props)` (or `{P{}.M(props)}` inline).

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

This lowers to `ui.Button(ui.ButtonProps{Label: "Save"})`.
With children: `<ui.Button label="Save">text</ui.Button>` sets `Children: gsx.Func(...)`.

Corpus reference: `internal/corpus/testdata/cases/xpkg/cross_package.txtar`

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

`url` is wired to `structpages.URLFor` in structpages-integrated apps.

## THE CHILDREN-BOUNDARY RULE (most important for migration order)

**templ and gsx pass children differently:**

- **templ** threads children through `context` via `templ.WithChildren` / `templ.GetChildren`.
- **gsx** passes children as a `Children gsx.Node` field in the generated `Props` struct,
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
