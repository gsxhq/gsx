# Configuration — `gsx.toml`

A project configures gsx declaratively in a `gsx.toml` file that the **stock
`gsx` binary** reads. This covers the common cases — registering pipeline
filters and attribute-classification rules — with no per-project generator
program. The few options that are Go *functions* (a custom minifier, an
attribute-classifier predicate, a field matcher) still require a code-based
setup; see [Extensions](./extensions.md).

## `[dev]` — development loop

`gsx dev` works without configuration: it runs `npx vite`, builds the current
package to a per-project operating-system cache directory, and runs that binary.
The `[dev]` table customizes those commands:

```toml
[dev]
web = ["pnpm", "vite"]
build = ["go", "build", "-tags", "dev", "-o", "tmp/app", "."]
run = ["tmp/app"]
log = "tmp/dev.log"
```

`web`, `build`, and `run` are argument arrays executed directly, without a
shell. If `build` changes the output path, update `run` to match it. `log` is
optional and off by default; a configured path may write into the working tree.

Set `no_web = true` when another process manages Vite:

```toml
[dev]
no_web = true
```

CLI flags override this table. See the [`gsx dev` reference](./cli#gsx-dev).

```toml
# gsx.toml — typically at the repo root
[filters]
url    = "github.com/jackielii/structpages.URLFor"
id     = "github.com/jackielii/structpages.ID"
target = "github.com/jackielii/structpages.IDTarget"
```

With that file in place, `gsx generate` makes `url`/`id`/`target` available as
pipeline filters in any `.gsx` file:

```gsx
<a href={ aboutPage{} |> url }>About</a>
<div id={ MyPage.UserList |> id } hx-target={ MyPage.UserList |> target }>…</div>
```

## Location & discovery

gsx finds `gsx.toml` by walking **up** from the directory it generates in and
using the **first one it finds**:

- The walk is **bounded by the git repo root** (the nearest ancestor containing
  `.git`). A `gsx.toml` above the repo is never read.
- If the directory is not in a git repo, the bound falls back to the **module
  root** (the nearest `go.mod`).
- There is **no global / `$HOME` config** — every key is a Go import path, which
  is project-specific by nature, so the walk never escapes the project.

Because the walk crosses module boundaries (it stops at the repo, not at each
`go.mod`), **one `gsx.toml` at the repo root serves every module in the repo**:

```
myrepo/
  .git/
  gsx.toml          ← discovered by both modules below
  service/  go.mod  pages.gsx
  admin/    go.mod  pages.gsx
```

A nearer `gsx.toml` wins wholesale over an ancestor one (there is no merging
across files), so a single module can override the shared config by dropping its
own `gsx.toml`.

> A config beside `go.mod` is also a stable project-root anchor for editor
> tooling.

## Options

### `[filters]` — named pipeline filters

A table of `name = "<pkgPath>.<Func>"`. Each entry registers one Go function as
a pipeline filter named `name`, usable as `{ value |> name(args…) }`.

```toml
[filters]
url    = "github.com/jackielii/structpages.URLFor"
upper  = "example.com/text.ToUpper"
```

The lowering follows the standard [pipeline rules](./syntax.md): the piped value
becomes the first non-`ctx` argument, `ctx` is injected automatically when the
function's first parameter is `context.Context`, the stage's own arguments
follow, and a `(T, error)` return is auto-unwrapped. So:

```gsx
{ toggle{} |> url("id", todo.ID) }
```

lowers to `structpages.URLFor(ctx, toggle{}, "id", todo.ID)` with the error
propagated out of `Render`.

The function must be an **exported top-level function** in a package the
generated module can import (it is resolved by the Go type-checker against that
module). A non-exported name, a method value, or a missing function is a clear
config error.

### `filterPackages` — bulk filter packages

A list of package import paths. **Every** exported function in each listed
package is registered as a filter, named by its lower-cased function name
(`Upper` → `upper`, `Truncate` → `truncate`). This is the bulk form of
`[filters]`; use it when you want a whole package of helpers available without
naming each one.

```toml
filterPackages = ["example.com/myproject/templatefuncs"]
```

The gsx built-in `std` filter package is **always available** — you do not list
it. It ships `upper`, `lower`, `trim`, `truncate`, `join`, `default`, and
`format` (a `fmt.Sprintf` wrapper with the piped value as the first verb:
`{ price |> format("$%.2f") }`). List `filterPackages` only for your own
packages, or to set precedence (later packages win on name collisions).

### `[[jsAttrs]]` / `[[urlAttrs]]` / `[[cssAttrs]]` — attribute contexts

gsx auto-escapes attribute values by **security context** (JS, URL, CSS, or
plain). The built-ins cover standard HTML, Alpine, and HTMX. If your project
uses a framework with its own event/URL/style attributes, register additional
rules so the escaper — and `@{ }` hole splitting — treat them correctly.

Each rule matches by **exact name** (`name`, case-insensitive) **or by prefix**
(`prefix`) — set exactly one:

```toml
# Livewire wire: attributes carry JS expressions.
[[jsAttrs]]
prefix = "wire:"

# A specific URL-bearing attribute.
[[urlAttrs]]
name = "data-href"

# A specific style-bearing attribute.
[[cssAttrs]]
name = "data-style"
```

Rules are **additive** — they extend the built-ins, never downgrade them. The
built-ins are checked first; your rules apply only to names they did not already
classify. (This is the declarative half of [custom attribute
classification](./extensions.md#custom-attribute-classification); the predicate
escape hatch remains code-only.)

### `[minify]` — asset minification level

gsx can minify the CSS inside `<style>` and the JavaScript inside `<script>` at
codegen time. The `[minify]` table sets the level **per asset** — `css` and `js`
are independent — each either `"none"` or `"full"`. The default is `"none"`:
minification is **off by default** (fast, readable dev output); you opt into
`"full"` for production builds.

```toml
[minify]
css = "full"   # "none" (default) | "full"
js  = "full"
```

| Level | What it does |
|-------|--------------|
| `none` *(default)* | Emit the asset **verbatim** — no minification. Keeps generated output readable and the incremental cache warm; best for the dev loop. A custom minifier (below) is not called. |
| `full` | Maximal **safe** compression via a full parse (tdewolff): collapses whitespace *and* newlines (ASI-safe — explicit semicolons are emitted) for the smallest output. It **never renames identifiers and never obfuscates** — variable names are preserved. Best for production builds; note it **bypasses the incremental codegen cache**, so reserve it for prod rather than the dev loop. |

A [custom minifier](./extensions.md#custom-cssjs-minifier)
(`gen.WithCSSMinifier` / `gen.WithJSMinifier`), if installed, **replaces** the
built-in `full` minifier. At `none` no minifier runs.

**Overrides & precedence — `option > env > config-file`:**

- The `[minify]` table is the **file default**.
- The `GSX_MINIFY` environment variable is the **dev↔prod switch** that overrides
  the file: `none` or `full`, applied to **both** assets — `GSX_MINIFY=full` for
  a production build, `GSX_MINIFY=none` for the dev loop.
- `gen.WithMinifyLevel(css, js)` in a `cmd/gsx` binary wins over **both** (code
  is the most deliberate layer).

`gsx info` reports the resolved level for each asset and which environment
overrides are in effect (see below).

### `class_merger` — Tailwind-aware class merge strategy

gsx composes `class` attributes from static parts, `clsx`-style toggles, and
explicitly forwarded attrs, then passes the raw per-source class strings through a *merge
strategy* that produces the final value. The default (`gsx.DefaultClassMerge`)
returns a single source verbatim and dedupes multiple sources last-wins — correct
for vanilla CSS but not for Tailwind, where conflicting utilities like `px-4 px-8`
must collapse to `px-8`.

Set `class_merger` to replace the default with a Tailwind-aware implementation:

```toml
class_merger = "myapp/twcfg.Merge"   # an exported func([]string) string (func or var)
```

**Signature contract.** The named identifier must be an **exported package-level
identifier** (a func declaration *or* a package-level var of a func type) with the
signature **exactly `func([]string) string`**. gsx emits a **direct reference** to
the symbol — no generated adapter. Any other signature — variadic, wrong arity,
non-string return — is a **generate-time error** that names the bad signature and
points at the wrapper idiom below. For example, naming `tailwind-merge-go.Merge`
directly fails because its type is `func(...ClassNameValue) string`, not
`func([]string) string`.

**What the merger receives.** Each element is the **raw, un-split class string of
one source** — a component with `class="px-4 py-2"` and an explicitly forwarded
`class="px-8"` pass `["px-4 py-2", "px-8"]`. gsx does not pre-split or pre-join:
a real Tailwind merger splits and resolves conflicts itself.
`tailwind-merge-go`'s merge function accepts a `[]string` directly (each element is
split internally), so a wrapper passes the slice straight through — **no join**.

**The wrapper idiom for custom-configured mergers.** `tailwind-merge-go` mergers
are runtime-constructed values, not named top-level functions, so they cannot be
named directly in `gsx.toml`. Put a thin exported wrapper in your own utilities
package and name that instead:

```go
// package myapp/twcfg
import "github.com/jackielii/tailwind-merge-go/pkg/twmerge"

var merger = twmerge.CreateTwMerge(twmerge.GetDefaultConfig())
// or: twmerge.ExtendTailwindMerge(&twmerge.ConfigExtension{...})

// Merge is what gsx.toml names. Already func([]string) string — no join.
func Merge(classes []string) string { return merger(classes) }
```

```toml
class_merger = "myapp/twcfg.Merge"
```

The wrapper already has the canonical signature, so gsx emits a direct reference
with no adapter. The merger library, its custom configuration, its cache, and its
version all live in `myapp/twcfg` and your project's `go.mod` — gsx's runtime
never imports the library, so upgrading or swapping `tailwind-merge-go` is a
`go.mod` bump and a `gsx generate`, not a gsx release.

**Option route (custom binary).** `gen.WithClassMerger(fn)` in a project
`cmd/gsx/main.go` accepts a Go function value (e.g. a top-level
`func Merge([]string) string`). Precedence is **option > config**: when both are
set, the option wins. The option route requires a [project
`cmd/gsx`](./extensions.md); prefer `gsx.toml` unless you already maintain one.

## Full example

```toml
# gsx.toml

# Named pipeline filters: { value |> name(args) }
[filters]
url    = "github.com/jackielii/structpages.URLFor"
id     = "github.com/jackielii/structpages.ID"
target = "github.com/jackielii/structpages.IDTarget"

# (optional) packages whose exported funcs are all registered as filters,
# named by lower-cased func name. std is always available and not listed.
filterPackages = ["example.com/myproject/templatefuncs"]

# Attribute escaping contexts beyond the built-ins.
[[jsAttrs]]
prefix = "wire:"
[[urlAttrs]]
name = "data-href"

# Asset minification level (per asset; "none" default, "full" for prod).
[minify]
css = "full"
js  = "full"

# Tailwind-aware class merger (omit to use gsx's built-in last-wins dedup).
class_merger = "myapp/twcfg.Merge"
```

## What is *not* in `gsx.toml`

Options whose value is a Go **function** cannot be expressed in TOML and stay
code-only, configured through a project `cmd/gsx/main.go` that calls `gen.Main`
(see [Extensions](./extensions.md)):

- a custom CSS/JS minifier (`gen.WithCSSMinifier` / `gen.WithJSMinifier`),
- an attribute-classifier **predicate** (`gen.WithAttrClassifier`),
- a field matcher (`gen.WithFieldMatcher`).

When a project does use a `cmd/gsx` binary, `gen.Main` loads `gsx.toml` as the
**base** configuration and applies the programmatic options **on top** (filters
and rules from code are appended; a code-registered filter overrides a
same-named config filter). So even a code-configured project keeps its simple
filters/rules in `gsx.toml` and writes Go only for the function-valued options.

> **Unknown keys are rejected.** A typo (e.g. `filterz`, or a misspelled nested
> key) fails generation with an error naming the offending key — gsx does not
> silently ignore unrecognized configuration.

## Verifying with `gsx info`

`gsx info` is the single source of truth for the configuration in effect. It
prints the discovered `gsx.toml` path (or `config: none`), the fully-resolved
filters and attribute rules, the resolved **minify level** per asset, and an
**Environment** section listing every `GSX_*` override and whether it is
currently set — the answer to "which config is active, and is my `GSX_MINIFY`
actually taking effect":

```sh
gsx info          # human-readable: config path + filters/rules + minify + env
gsx info --json   # machine-readable (same data)
```

## Generating

The stock binary reads the config — no per-project generator needed:

```sh
go install github.com/gsxhq/gsx/cmd/gsx@latest
gsx generate .            # one package
gsx generate ./a ./b      # several packages (e.g. a multi-package app)
```
