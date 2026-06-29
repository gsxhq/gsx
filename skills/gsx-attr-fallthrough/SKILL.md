---
name: gsx-attr-fallthrough
description: Use when understanding or using gsx's attribute fallthrough — how caller attrs auto-apply to a root element, how class merges, how type/non-class attrs use caller-wins semantics, and how to configure a Tailwind-aware ClassMerger.
---

# gsx attribute fallthrough

Learned while migrating one-learning's button layer from templ to gsx.
This feature completely replaces templ's hand-written `classFromAttrs`/`attrFromAttrs` helpers.

Corpus reference: `internal/corpus/testdata/cases/fallthrough/`

## Auto-generated Props (no explicit `attrs` param needed)

`Attrs gsx.Attrs` is injected for **any single-root-element component** — with
or without `{children}`. The `Children gsx.Node` field is added separately,
only when the component body contains a `{children}` placement.

Example with both:

```gsx
// button.gsx
component Button(variant string) {
    <button class="btn" type="button" data-variant={variant}>{children}</button>
}
```

Generated props (simplified):

```go
type ButtonProps struct {
    Variant  string
    Children gsx.Node
    Attrs    gsx.Attrs   // injected automatically; caller attrs land here
}
```

You do NOT need to declare `attrs gsx.Attrs` yourself.

## Non-class attribute semantics: caller-wins

Static defaults on the root element are **overridden** by matching caller attributes.
The generated guard looks like:

```go
if !_gsxp.Attrs.Has("type") {
    // emit type="button"
}
```

Example:

```gsx
// definition
component Button(variant string) {
    <button type="button">{children}</button>
}

// call site
<Button variant="primary" type="submit">Save</Button>
// renders: <button type="submit">Save</button>
```

## `class` semantics: merge, not override

`class` values from the component definition and from the call site are **merged**,
not overridden. By default, `gsx.ClassMerger` deduplicates tokens with last-occurrence-wins
and joins with a space.

```gsx
// definition
component Button(variant string) {
    <button class="btn">{children}</button>
}

// call site — caller adds extra classes
<Button variant="primary" class="w-full" hx-post="/go">Save</Button>
// renders: <button class="btn w-full" data-variant="primary" hx-post="/go">Save</button>
```

Corpus: `internal/corpus/testdata/cases/fallthrough/call_site_button.txtar`

## Spread and `Attrs.Without`

The generated spread excludes `class` and `style` (which have dedicated merge paths)
to prevent double-emission:

```go
// generated (simplified)
_gsxgw.Spread(ctx, _gsxp.Attrs.Without("class", "style"))
```

## Pluggable `gsx.ClassMerger` for Tailwind

`gsx.ClassMerger` is a package-level hook (set once at `init`):

```go
var ClassMerger func(tokens []string) string
```

To install a Tailwind-aware merger that resolves conflicting utility classes
(e.g. `p-2` vs `p-4` → last wins per Tailwind specificity rules):

```go
import twmerge "github.com/jackielii/tailwind-merge-go"

func init() {
    gsx.ClassMerger = func(tokens []string) string {
        return twmerge.Merge(strings.Join(tokens, " "))
    }
}
```

Without this, `class="p-2 p-4"` both render (CSS cascade decides); with it,
`class="p-4"` is emitted — the correct Tailwind behaviour.

## What this replaces in templ

templ required explicit helper functions at every component boundary:

```go
// templ (hand-written boilerplate)
func classFromAttrs(attrs templ.Attributes) string { … }
func attrFromAttrs(attrs templ.Attributes) templ.Attributes { … }

templ Button(attrs templ.Attributes) {
    <button class={ "btn", classFromAttrs(attrs) } { attrFromAttrs(attrs)... }>
        { children... }
    </button>
}
```

In gsx, none of that boilerplate exists. The compiler handles it.
