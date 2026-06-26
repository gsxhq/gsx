# Extending gsx

Most projects configure gsx declaratively in a [`gsx.toml`](./config.md) file
read by the stock binary — pipeline filters and attribute-classification rules
need no code. This page covers the **code escape hatch**: a project-owned
`cmd/gsx/main.go` that calls `gen.Main`, needed only for options whose value is
a Go *function* and therefore cannot live in TOML:

- a custom CSS/JS minifier (`gen.WithCSSMinifier` / `gen.WithJSMinifier`),
- a custom CSS/JS **formatter** (`gen.WithCSSFormatter` / `gen.WithJSFormatter`),
- an attribute-classifier **predicate** (`gen.WithAttrClassifier`),
- a field matcher (`gen.WithFieldMatcher`).

The **minify level** (`none`/`safe`/`full`) is configured declaratively in
[`gsx.toml`](./config.md#minify-asset-minification-level) (or the `GSX_MINIFY`
env var); the code equivalent is `gen.WithMinifyLevel(css, js)`, which overrides
both.

`gen.Main` loads `gsx.toml` as the base configuration and applies these
programmatic options on top, so a code-configured project still keeps its
filters and declarative rules in `gsx.toml` and writes Go only for the
function-valued options.

> The declarative forms of attribute classification and filter registration
> below are equivalently expressible in [`gsx.toml`](./config.md); prefer the
> config file unless you need the predicate/function escape hatch.

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

## Custom CSS/JS formatter

`gsx fmt` re-indents the CSS inside `<style>` and the JavaScript inside
`<script>` with a small built-in formatter (it fixes indentation to consistent
tabs; it does not reflow or restyle your code). When you want fuller fidelity —
Prettier, Biome, or a house style — replace the built-in with your own via
`gen.WithCSSFormatter` / `gen.WithJSFormatter`:

```go
// cmd/gsx/main.go
gen.Main(
	gen.WithCSSFormatter(func(src []byte) ([]byte, error) {
		// Shell out to prettier (or any tool). Return the formatted bytes,
		// or an error to fall back to verbatim rendering of this body.
		return runPrettier(src, "--parser", "css")
	}),
)
```

A formatter is a `func(src []byte) ([]byte, error)`. It receives the embedded
language's source as a **self-contained document** (formatted from column 0; gsx
re-indents the result to the tag's depth afterward) and returns the formatted
bytes. Two contracts make it safe:

- **Holes are pre-substituted.** Each `@{ … }` interpolation in the body is
  replaced with a collision-free placeholder token (a valid CSS/JS identifier)
  *before* your formatter runs; gsx restores the real holes afterward. Leave
  those tokens untouched — don't parse or rewrite them.
- **Errors are not fatal.** Returning an error (or panicking) makes gsx render
  *that* body verbatim instead, so a formatter that chokes on one file never
  breaks `gsx fmt`. This is the same correct-or-verbatim rule the built-in uses.

Like the minifiers, this is a **function-valued, code-only** option: `nil` means
the built-in default applies, `gsx.toml` cannot set it, and it bypasses the
codegen cache (run `gsx clean --cache` after changing formatter logic). Shelling
out to an external tool is a user-written wrapper — gsx ships only the in-process
plug point and a minimal built-in, not a subprocess adapter.

The built-in re-indenter is intentionally minimal: it normalizes leading
indentation (block scope drives the depth) and leaves everything else — line
breaks, blank lines, and intra-line spacing — exactly as you wrote it. Reach for
`WithCSSFormatter` / `WithJSFormatter` when you want true reflow.

## Minify level

The built-in CSS/JS minifiers (and any custom one) run at a **level** set
declaratively — see [`[minify]` in the config guide](./config.md#minify-asset-minification-level)
for `none` / `safe` / `full`, the `GSX_MINIFY` env switch, and precedence. The
code equivalent, which overrides both the config file and the env var, is:

```go
// cmd/gsx/main.go — force full minification regardless of gsx.toml.
gen.Main(
	gen.WithMinifyLevel(gen.MinifyFull, gen.MinifyFull), // (css, js)
)
```

`WithMinifyLevel` **gates** the pass: at `safe` it uses the built-in (or your
`WithCSSMinifier` / `WithJSMinifier`); at `none` the asset is emitted verbatim
and a custom minifier is not called; at `full` gsx applies its maximal,
non-obfuscating minifier.

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
