# Extending gsx

gsx is customised through `gen.Main` options wired in a project-owned
`cmd/gsx/main.go`. This is the same approach as `gen.WithFilters` for filter
packages — one Go file, type-checked by the compiler, no config format to
maintain.

## Custom attribute classification

gsx auto-escapes attribute values according to their **security context**
(JS, URL, CSS, or plain). The built-in set covers the standard HTML attributes,
Alpine, and HTMX. If your project uses a framework with its own event or URL
attributes (Vue `v-on:`, Livewire `wire:`, Stimulus `data-action`, etc.), you
can register additional rules so the escaper — and `@{ }` hole splitting —
treat those attributes correctly.

### Declarative rules (recommended)

Register rules via `gen.WithJSAttrs`, `gen.WithURLAttrs`, or `gen.WithCSSAttrs`.
Each takes one or more `attrclass.Rule` values. A rule matches either by **exact
name** (`Name` field, case-insensitive) or by **prefix** (`Prefix` field) — set
exactly one field.

```go
// cmd/gsx/main.go
package main

import (
	"github.com/gsxhq/gsx/gen"
)

func main() {
	gen.Main(
		// Livewire wire: attributes carry JS expressions.
		gen.WithJSAttrs(
			gen.Rule{Prefix: "wire:"},
		),
		// Vue v-on: event handlers are JS; v-bind: attrs may carry URLs.
		gen.WithJSAttrs(
			gen.Rule{Prefix: "v-on:"},
		),
		gen.WithURLAttrs(
			gen.Rule{Name: "v-bind:href"},
		),
	)
}
```

Rules are **additive** — they extend the built-in set, never replace or downgrade
it. The built-ins are checked first; your rules are consulted only for names that
the built-ins did not classify.

### Predicate escape hatch

When the matching logic cannot be expressed as a list of rules, install a
predicate via `gen.WithAttrClassifier`:

```go
gen.WithAttrClassifier("myFramework", func(name string) (gen.Context, bool) {
	if strings.HasPrefix(name, "data-js-") {
		return gen.CtxJS, true
	}
	return gen.CtxPlain, false // not handled by this predicate; return false to pass through
})
```

The predicate receives the original (non-lowercased) attribute name and is
consulted **only for attributes no rule matched**. Returning `false` is the
canonical "not handled / pass through" signal. Returning `(CtxPlain, true)` is
also treated as plain (a `CtxPlain` result is ignored). Like rules, the predicate
is additive — it cannot downgrade built-in classifications.

**Offline caveat:** predicate logic is a Go closure and cannot be serialised.
The manifest (see below) records a `hasPredicate` flag and the label you provide,
but the classification decisions themselves are only available when the project
binary can be run. Prefer declarative rules for attributes that need to be visible
to offline tools (a future LSP or `vet`).

**Cache invalidation:** predicate *bodies* are not part of the codegen cache key
(closures are not inspectable), consistent with `WithCSSMinifier`/`WithJSMinifier`.
After changing a predicate's logic, run `gsx clean --cache` to force full
regeneration.

## Registration pattern

The intended pattern is to maintain a `cmd/gsx/main.go` inside your own
module's repository that depends on `github.com/gsxhq/gsx/gen` and wires
options there. All public types (`gen.Rule`, `gen.Context`, `gen.CtxJS`, …)
are re-exported from the `gen` package — your code never needs to import
`internal/attrclass` directly:

```
myapp/
  cmd/gsx/
    main.go    ← gen.Main(gen.WithJSAttrs(...), gen.WithFilters(...))
  pages/
    home.gsx
```

Build and run this binary in place of the stock `gsx` command, or point
`//go:generate` at it. This is the same pattern as `gen.WithFilters`.

## Resolved-config manifest and `gsx info`

On each successful `gsx generate`, the resolved configuration is written as a
JSON manifest into the build cache (`~/.cache/gsx`, or `$GSXCACHE`). The manifest
records `schemaVersion`, `module`, `userRules`, `hasPredicate`, `predicateLabel`,
and `filters` — enough for offline tools to ground themselves on the last
successful build without re-running the project binary.

```sh
gsx info          # human-readable summary (includes "Attribute rules" section)
gsx info --json   # machine-readable JSON (same data)
gsx clean --cache # wipe the cache (needed after changing a predicate's logic)
```

The manifest is a **derived cache**, not a hand-edited config file — always
regenerated from the authoritative source (your `cmd/gsx/main.go`).

> **Note:** the manifest is refreshed only when gsx's incremental build cache is
> active. Projects that bypass the cache (e.g. by supplying a custom
> `WithCSSMinifier` or `WithJSMinifier`) should use `gsx info --json` to read the
> current resolved config instead of relying on the persisted manifest.
