# Configuration — `gsx.toml`

A project configures gsx declaratively in a `gsx.toml` file that the **stock
`gsx` binary** reads. This covers the common cases — registering pipeline
filters and URL-attribute rules — with no per-project generator
program. The few options that are Go *functions* (a custom minifier, an
attribute formatter, a field matcher) still require a code-based setup; see
[Extensions](./extensions.md).

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

`host` sets the hostname in the generated `VITE_DEV_URL` (default `localhost`),
for when the dev server must be reachable under a specific name:

```toml
[dev]
host = "mstudio"   # → VITE_DEV_URL=http://mstudio:<port>
```

When `host` is unset, `gsx dev` takes the hostname from an existing
`VITE_DEV_URL` in the environment (typically a gitignored `.env`), so a
per-machine dev hostname needs no committed config. Only the host is taken —
the port still comes from `VITE_PORT` or the auto-picker.

Set `no_web = true` when another process manages Vite:

```toml
[dev]
no_web = true
```

CLI flags override this table. See the [`gsx dev` reference](./cli.md#gsx-dev).

## Example: pipeline filters

```toml
# gsx.toml — typically at the repo root
[filters]
url    = "github.com/jackielii/structpages.URLFor"
id     = "github.com/jackielii/structpages.ID"
target = "github.com/jackielii/structpages.IDTarget"
```

With that config, `gsx generate` makes `url`/`id`/`target` available as
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
it. It ships `upper`, `lower`, `trim`, `truncate`, `join`, `default`, `printf`
(a `fmt.Sprintf` wrapper with the piped value as the first verb:
`{ price |> printf("$%.2f") }`), `urlquery` (percent-encodes a URL query
component, like html/template's `urlquery`), and `dataURL` (assembles a base64
`data:` URL — see [Pipelines](./syntax/pipelines.md)). List `filterPackages` only for your own
packages, or to set precedence (later packages win on name collisions).

#### `std` is the lowest-precedence base

`std` sits at the **bottom** of the filter-precedence stack: it is always
present, but any later registration with the same name shadows just that one
built-in — the rest of `std` stays available. So you can override `dataURL`
(or `truncate`, `printf`, …) with your own function without re-declaring the
whole standard library. Precedence, low → high:

1. `std` — the built-in base, always present.
2. `filterPackages` (config) / `WithFilters` (code) — listed in order,
   **last package wins** on a name collision, and each wins over `std`.
3. `[filters]` aliases (config) / `WithFilter` (code) — a named single-function
   alias, highest precedence of all.

For example, `[filters] dataURL = "example.com/img.DataURL"` (or
`gen.WithFilter("dataURL", img.DataURL)`) replaces the built-in `dataURL`
while `upper`, `trim`, and the rest of `std` keep working. The programmatic
options layer on top of the config the same way — a code-registered filter
overrides a same-named config filter (see [What is *not* in
`gsx.toml`](#what-is-not-in-gsx-toml) below).

### `[renderers]` — type-directed value rendering

Third-party wrapper types — `pgtype.Text`, `sql.NullString` — are not
renderable and cannot be given a `String()` method you don't own. A renderer
teaches gsx how to display such a type everywhere, once:

```toml
[renderers]
"github.com/jackc/pgx/v5/pgtype.Text" = "example.com/app/filters.PgText"
```

```go
func PgText(t pgtype.Text) string {
	if !t.Valid {
		return ""
	}
	return t.String
}
```

With that registration a hole holding a `pgtype.Text` value renders through
`PgText` and the result is escaped/sanitized for its context exactly like a
pipe filter's output. A renderer may return `(R, error)`; the error propagates
like a failing pipe stage.

Each key is a fully-qualified named type, `<pkgPath>.<TypeName>`; prefix it
with `*` to register the **pointer** type instead — `"*github.com/.../pgtype.Text"`
matches only `*pgtype.Text` holes, never the value type. Pointer and value
registrations are distinct: registering one never covers the other, so a type
that shows up as both `pgtype.Text` and `*pgtype.Text` in your code needs two
entries (or a caller-side dereference).

Rules:

- **Render boundaries only.** A renderer applies wherever gsx renders a value
  into output — text and attribute holes, URL-attribute holes, style/script
  holes and their interpolated `` f`...` ``/`` js`...` ``/`` css`...` `` literals,
  composable `class`/`style` parts, conditional-attribute branches, and the
  values that land in a component's fallthrough attrs bag or an ordered-attrs
  (`{{ }}`) literal — including a component's own `class` prop expression. It
  does **not** apply to a plain component argument: passing a `pgtype.Text`
  value from a parent to a child's `pgtype.Text`-typed prop is ordinary Go and
  stays untouched; the renderer only fires where that value is *rendered*
  (e.g. read back out with `.String` inside the child, or interpolated
  directly).
- **Registration always wins.** If a type is registered, its renderer runs
  even when the type would otherwise classify as renderable on its own (for
  example, it also has a `String()` method) — there is no fallback tier.
- **Apply once, never chain.** The renderer's result type must itself
  classify as natively renderable. A renderer whose result type is itself a
  registered type — including its own parameter type (`func(T) T`) — is a
  generation-time error (`renderers apply once and never chain`), not a
  second application. There is no implicit chaining or cycle-following.
- **Matching is exact `go/types` identity** on the concrete named type — see
  the pointer note above.
- **Type parameters and runtime `any` values are out of scope.** A hole whose
  static type is a type parameter does not consult the registry, even when
  instantiated with a registered type. Values that only exist as `any` at
  runtime — entries in a user-supplied `gsx.Attrs`/attr-map or a spread —
  never see a renderer either; the registry is a compile-time, statically
  typed mechanism.

**Option layer.** `gen.WithRenderer("<pkgPath>.<TypeName>", fn)` registers a
renderer from a custom generator binary; it is appended after `[renderers]`,
so an option can override a file-level registration for the same type key
(last-wins), the same convention `WithFilter` uses over `[filters]`.

**Cache key.** Renderers change generated output, so registrations (file and
option layer) are folded into the codegen cache key — adding, removing, or
repointing a renderer entry busts the cache like any other content change.

::: warning gsx.toml key ordering
TOML attaches a bare key to whichever table header precedes it. If
`[renderers]` appears before a top-level key such as `filterPackages`, the
key is silently parsed as `renderers.filterPackages` instead of a top-level
setting. Put `[renderers]` (like `[filters]`) **after** any top-level keys in
the file, or give it its own file section with nothing but table headers
below it.
:::

**Limitations:**

- Type-parameter holes do not consult the registry (above).
- Runtime `any` values (attr-map entries, spreads) never see a renderer
  (above).
- Pointer and value registrations for the same underlying type are separate
  entries — registering `T` does not also cover `*T`, or vice versa.

### `[[urlAttrs]]` — URL attribute contexts

gsx treats ordinary `attr={expr}` values as attribute-escaped text, except for
URL attributes. The built-ins cover the standard HTML URL attributes (`href`,
`src`, `action`, `formaction`, `poster`, `cite`, `ping`, `data`, `background`,
`manifest`, `xlink:href`). If your project uses a framework with its own
URL-bearing attributes, register additional rules so those values get URL scheme
sanitization before attribute escaping.

Each rule matches by **exact name** (`name`, case-insensitive) **or by prefix**
(`prefix`) — set exactly one:

```toml
# A specific URL-bearing attribute.
[[urlAttrs]]
name = "data-href"

# A family of URL-bearing attributes.
[[urlAttrs]]
prefix = "data-url-"
```

Rules are **additive** — they extend the built-ins, never downgrade them. The
built-ins are checked first; your rules apply only to names they did not already
classify.

#### `url_presets` — named opt-in rulesets

Presets bundle a family of URL rules under a name. The only preset today is
`htmx`, which classifies the five htmx method attributes — `hx-get`, `hx-post`,
`hx-put`, `hx-delete`, `hx-patch` — as URL sinks so their values are
scheme-sanitized just like `href`:

```toml
url_presets = ["htmx"]
```

::: warning Default change
The htmx method attributes are **off by default** — the safety floor is pure
HTML. A project that renders htmx URLs from untrusted data must opt in with
`url_presets = ["htmx"]` (or `gen.WithURLPreset("htmx")` in a custom generator
binary). Without the preset, `hx-get={expr}` is written as plain
attribute-escaped text, not URL-sanitized. Only the five method attributes are
covered; `hx-swap` / `hx-target` / `hx-trigger` and other `hx-*` attributes are
not URLs and are never sanitized.
:::

Presets compose additively with `[[urlAttrs]]` and with `gen.WithURLPreset`; an
unknown preset name is a hard config error.

JavaScript and CSS-valued attributes do not need name configuration. Write them
explicitly with `` js`...` `` or `` css`...` `` at the call site:

````gsx
<button @click=js`save(@{id})`>Save</button>
<div data-style=css`color:@{color}`>...</div>
````

### `[formatter]` — `gsx fmt` / editor formatting

The `[formatter]` table configures the gsx formatter — both the `gsx fmt`
command and editor format-on-save via the language server read it.

```toml
[formatter]
print_width = 100   # line width the formatter wraps at (default 120)
tab_width   = 4     # columns one tab counts as, for measuring line width (default 2)
imports     = "goimports"   # "goimports" (default) or "gofmt"
```

`print_width` is the column budget for a line. An opening tag whose attribute
list fits stays on one line; one that exceeds the width wraps with one
attribute per line (and its children break onto their own indented lines).
The default is `120`. gsx markup nests, and each level of nesting spends part of the budget on indentation before a single character of content is printed; at `80` an element six levels deep has almost nothing left.

`tab_width` does **not** change how indentation is emitted — `gsx fmt` always
indents with tabs, never spaces. It only changes how wide a tab **counts** as
when the formatter measures a line against `print_width`. The default is `2`.

#### `.editorconfig`

`gsx fmt` also reads [`.editorconfig`](https://editorconfig.org/), honoring
exactly two keys:

| Key | Effect |
|-----|--------|
| `tab_width` | how many columns one tab counts as when measuring a line (falls back to `indent_size`, per the EditorConfig spec) |
| `max_line_length` | feeds `print_width`; `off` means "use gsx's default", since gsx has no unbounded width |

`indent_style` is **not** honored. gofmt always emits tabs for Go, and gsx
does not re-indent gofmt's output.

Resolution order, highest first:

```
option (programmatic) > gsx.toml [formatter] > .editorconfig > built-in (print_width 120, tab_width 2)
```

There is no CLI flag or environment variable for either setting — same as
`print_width`, which has never had one. An explicit `gsx.toml` setting wins
even when an `.editorconfig` sits closer to the file being formatted:
`.editorconfig` is a cross-tool baseline shared with other editors and
formatters, while `gsx.toml [formatter]` is gsx's own, more specific answer. A
key left **unset** in `gsx.toml` falls through to `.editorconfig` rather than
clobbering it with the built-in default. A missing or malformed
`.editorconfig` is ignored, never an error.

The language server resolves the same precedence per document, so
format-on-save always agrees with `gsx fmt`.

`imports` selects how `gsx fmt` and the language server treat the import
declarations in a file's pass-through Go, mirroring the two modes gopls offers:

- **`goimports`** (default) — remove unused imports, then merge every import
  declaration into one block, dedup identical specs, and sort within each group.
  A block with no blank lines is split into a standard-library group and an
  everything-else group.
- **`gofmt`** — format only: sort within an existing parenthesized group, but
  never remove, merge, dedup, or regroup imports.

`goimports` mode calls the real `goimports` formatter, so it inherits its
grouping rule: **blank lines you wrote are group boundaries, and they are never
merged away.** If you hand-split a block into groups, those groups survive — a
standard-library import in one and another in a second stay separated, exactly
as the `goimports` command leaves them. Delete the blank lines to get the plain
std / everything-else split.

Unlike real `goimports`, `gsx` cannot **add** a missing import: a gsx Go
chunk's body never references the surrounding template's imports, so there is
no symbol for the formatter to resolve to a package.

`print_width`, `tab_width`, and `imports` are all resolved **per directory**
from the nearest `gsx.toml` (same [discovery walk](#location--discovery) as
everything else), so files in different modules of a monorepo can format with
different settings.

Like `[dev]`, this table only affects formatting — it never changes generated
output, so it does not participate in the incremental codegen cache.

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

A [custom minifier](./extensions.md#minify-level)
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
#
# Top-level keys (filterPackages, class_merger, …) come BEFORE any [table]
# header — TOML attaches a bare key to whichever table precedes it, so
# e.g. class_merger after [minify] would silently become minify.class_merger.

# (optional) packages whose exported funcs are all registered as filters,
# named by lower-cased func name. std is always available and not listed.
filterPackages = ["example.com/myproject/templatefuncs"]

# Tailwind-aware class merger (omit to use gsx's built-in last-wins dedup).
class_merger = "myapp/twcfg.Merge"

# Named pipeline filters: { value |> name(args) }
[filters]
url    = "github.com/jackielii/structpages.URLFor"
id     = "github.com/jackielii/structpages.ID"
target = "github.com/jackielii/structpages.IDTarget"

# Type-directed renderers: a registered type renders through its func
# wherever gsx renders a value (see [renderers] below).
[renderers]
"github.com/jackc/pgx/v5/pgtype.Text" = "example.com/app/filters.PgText"

# URL attribute contexts beyond the built-ins.
[[urlAttrs]]
name = "data-href"

# Formatter settings for gsx fmt and editor formatting.
[formatter]
print_width = 100
tab_width   = 2
imports     = "goimports"

# Asset minification level (per asset; "none" default, "full" for prod).
[minify]
css = "full"
js  = "full"
```

## What is *not* in `gsx.toml`

Options whose value is a Go **function** cannot be expressed in TOML and stay
code-only, configured through a project `cmd/gsx/main.go` that calls `gen.Main`
(see [Extensions](./extensions.md)):

- a custom CSS/JS minifier (`gen.WithCSSMinifier` / `gen.WithJSMinifier`),
- a custom CSS/JS formatter (`gen.WithCSSFormatter` / `gen.WithJSFormatter`),
- a field matcher (`gen.WithFieldMatcher`).

When a project does use a `cmd/gsx` binary, `gen.Main` loads `gsx.toml` as the
**base** configuration and applies the programmatic options **on top** (filters
and URL rules from code are appended; a code-registered filter overrides a
same-named config filter). So even a code-configured project keeps its simple
filters/rules in `gsx.toml` and writes Go only for the function-valued options.

> **Unknown keys are rejected.** A typo (e.g. `filterz`, or a misspelled nested
> key) fails generation with an error naming the offending key — gsx does not
> silently ignore unrecognized configuration.

## Verifying with `gsx info`

`gsx info` is the single source of truth for the configuration in effect. It
prints the discovered `gsx.toml` path (or `config: none`), the fully-resolved
filters and URL attribute rules, the resolved **minify level** per asset, and an
**Environment** section listing every `GSX_*` override and whether it is
currently set — the answer to "which config is active, and is my `GSX_MINIFY`
actually taking effect":

```sh
gsx info          # human-readable: config path + filters + URL attrs + minify + env
gsx info --json   # machine-readable (same data)
```

## Generating

The stock binary reads the config — no per-project generator needed:

```sh
go install github.com/gsxhq/gsx/cmd/gsx@latest
gsx generate .            # one package
gsx generate ./a ./b      # several packages (e.g. a multi-package app)
```
