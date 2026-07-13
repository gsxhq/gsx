# Configuration

Use `gsx.toml` for project-wide commands, filters, formatting, URL rules,
minification, and class merging.

## Start with `gsx.toml`

Most projects need only a few settings:

```toml
# gsx.toml
[dev]
web = ["pnpm", "vite"]

[filters]
url = "github.com/jackielii/structpages.URLFor"
```

This changes the front-end command used by `gsx dev` and makes `url` available
in pipelines such as `{ page |> url }`.

## Discovery and precedence

`gsx generate` and `gsx info` start at the command's working directory (after
`-C`, when used). `gsx dev [dir]` starts at that optional directory, resolved
against the command working directory; without `dir`, it starts at the working
directory. Each command walks upward and uses the first `gsx.toml` it finds.
The walk stops at the nearest ancestor containing `.git`; outside a Git
repository, it stops at the nearest Go module root. The boundary directory is
included. With neither `.git` nor `go.mod`, only the starting directory is
checked.

A nearer file replaces an ancestor file completely. Config files are not
merged, and gsx never continues above the project boundary to find a global
config.

For those three commands, malformed TOML, unknown keys, and invalid values are
hard configuration errors. `gsx fmt` instead discovers configuration from each
file's directory and falls back to `.editorconfig` or built-in formatting when
a file is missing or unusable. Editor formatting uses the same per-file,
best-effort behavior.

For settings that affect generated output, precedence is:

```text
programmatic option > environment override > gsx.toml > built-in default
```

Currently, `GSX_MINIFY` is the environment override. Development commands and
formatting have their own precedence rules in the sections below.

When `generate`, `dev`, or `info` loads the file, unknown keys are errors,
including misspelled keys inside a table. TOML keeps bare keys in the most
recent table, so put every top-level key before the first table header:

```toml
# Correct: class_merger is top-level.
class_merger = "example.com/app/twcfg.Merge"

[minify]
css = "full"
```

Putting `class_merger` after `[minify]` makes it `minify.class_merger`; strict
decoding reports that nested key as unknown instead of silently accepting it.

## Development commands `[dev]` {#dev-development-loop}

Use `[dev]` when the default `gsx dev` commands do not fit your project:

```toml
[dev]
web = ["pnpm", "vite"]
build = ["go", "build", "-tags", "dev", "-o", "tmp/app", "."]
run = ["tmp/app"]
log = "tmp/dev.log"
```

Command arrays run directly without a shell, so each array item is one exact
argument. Keep `build` and `run` pointed at the same binary.

| Key | Default | Effect |
|---|---|---|
| `web` | `["npx", "vite"]` | Front-end command. |
| `build` | `go build` to a per-project binary | Backend build command. |
| `run` | the default built binary | Backend command. |
| `log` | off | File that also receives backend output. |
| `no_web` | `false` | Disable the front-end command. |
| `host` | from `VITE_DEV_URL`, otherwise `localhost` | Hostname used in `VITE_DEV_URL`. |

An existing `VITE_DEV_URL` can supply the hostname when `host` is unset. The
port still comes from `VITE_PORT` or automatic selection.

Command-line flags override `[dev]`; `[dev]` overrides the defaults. See
[`gsx dev`](./cli.md#gsx-dev) for one-off overrides.

## Pipeline filters

Filters are exported Go functions that receive the piped value as their first
subject argument. A leading `context.Context` is optional, and the function may
return either `R` or `(R, error)`:

```go
func Slug(value string) string
func Lookup(ctx context.Context, value string, locale string) (string, error)
```

An error stops rendering and is returned by the component. See
[Pipelines](./syntax/pipelines.md) for call syntax and the built-in filter list.

### Named filters `[filters]` {#filters-named-pipeline-filters}

Use `[filters]` when template code should use a short, stable name:

```toml
[filters]
url = "github.com/jackielii/structpages.URLFor"
slug = "example.com/app/textutil.Slug"
```

Each value names an exported top-level function as
`"<package-import-path>.<Function>"`. Closures, methods, and unexported
functions cannot be registered.

```gsx
<a href={ page |> url }>Open</a>
<span>{ title |> slug }</span>
```

Named filters have higher precedence than package filters with the same name.
A programmatic `gen.WithFilter` registration has higher precedence than the
file entry.

### Filter packages `filter_packages`

Use `filter_packages` to register every compatible exported function in a
helper package:

```toml
filter_packages = [
  "example.com/app/templatefuncs",
  "example.com/app/productfilters",
]
```

`ProductName` becomes `productName`; only the first rune is lowercased.
Packages are applied in order, and a later package wins a name collision.

The built-in `std` package is always present at the lowest precedence. Your
packages can replace one built-in name without removing the other built-ins.
Named `[filters]` entries are applied after package filters and therefore win
the same name.

## Type renderers `[renderers]` {#renderers-type-directed-value-rendering}

Use a renderer when a named Go type cannot be displayed directly, such as a
database wrapper type:

```toml
[renderers]
"github.com/jackc/pgx/v5/pgtype.Text" = "example.com/app/viewfmt.PgText"
```

```go
func PgText(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}
```

The key is the exact named type, written as `"<package-path>.<Type>"`. Prefix
it with `*` to register the pointer type; value and pointer registrations are
separate.

A renderer must be an exported top-level function with one of these shapes:

```go
func(T) R
func(T) (R, error)
func(context.Context, T) R
func(context.Context, T) (R, error)
```

`T` must exactly match the registered key. `R` must be directly renderable;
renderers run once and do not chain. A returned error stops rendering.

The renderer applies when the type is written in text, attributes, and other
rendering positions. It does not change ordinary Go component arguments. See
[Interpolation](./syntax/interpolation.md) for the normal typed-value rules.

`gsx.RawJS` and `gsx.RawCSS` may be registered too. The renderer replaces their
normal trusted passthrough everywhere that value renders.

A programmatic `gen.WithRenderer` for the same type overrides the file entry.

## URL attributes

Dynamic, interpolated, and forwarded values on classified URL attributes
receive scheme checks before normal attribute escaping. Quoted URL literals on
native elements are trusted author text. The built-in names are:

`href`, `src`, `action`, `formaction`, `poster`, `cite`, `ping`, `data`,
`background`, `manifest`, `xlink:href`, `srcset`, and `imagesrcset`.

Project rules extend this set; they cannot downgrade a built-in URL attribute.
See [Escaping](./syntax/escaping.md#url-attributes) for the allowed schemes and
the different navigation, image, and source-set sinks.

### Exact and prefix rules `[[url_attrs]]`

Register a framework or project attribute that carries a URL:

```toml
[[url_attrs]]
name = "v-bind:href"

[[url_attrs]]
prefix = "data-url-"
```

Each rule sets exactly one field:

| Field | Match |
|---|---|
| `name` | Exact attribute name, case-insensitive. |
| `prefix` | Attribute names beginning with the prefix, case-insensitive. |

### Presets `url_presets` {#url_presets-named-opt-in-rulesets}

Use the `htmx` preset when htmx method attributes carry application URLs:

```toml
url_presets = ["htmx"]
```

It classifies `hx-get`, `hx-post`, `hx-put`, `hx-delete`, and `hx-patch` as URL
attributes. Other htmx attributes such as `hx-target`, `hx-swap`, and
`hx-trigger` stay plain. Unknown preset names are configuration errors.

`url_presets` is top-level, so place it before `[filters]`, `[formatter]`, or
any other table.

## Formatter `[formatter]` {#formatter--gsx-fmt--editor-formatting}

Use `[formatter]` to keep `gsx fmt` and editor format-on-save consistent:

```toml
[formatter]
print_width = 100
tab_width = 4
imports = "goimports"
```

| Key | Default | Values |
|---|---|---|
| `print_width` | `120` | Positive target line width. |
| `tab_width` | `2` | Columns used when measuring a tab. Output indentation remains tabs. |
| `imports` | `"goimports"` | `"goimports"` or `"gofmt"`. |

Width and tab precedence is:

```text
[formatter] > .editorconfig > built-in default
```

There is no CLI, environment, or programmatic width/tab option. An unset key in
`[formatter]` can still come from `.editorconfig`.

Import mode uses a separate order:

```text
gsx fmt -imports / -no-imports > [formatter].imports > goimports
```

`goimports` removes unused imports and normalizes declarations. `gofmt` formats
the imports already present without removing, merging, or regrouping them.

### `.editorconfig`

For `.gsx` files, gsx reads two EditorConfig settings:

| Setting | Formatter setting |
|---|---|
| `max_line_length` | `print_width`; `off` falls back to gsx's default. |
| `tab_width` | `tab_width`; falls back to `indent_size` when needed. |

`indent_style` does not change gsx output: indentation remains tabs.

<a id="minify--asset-minification-level"></a>

## Asset minification `[minify]` {#minify-asset-minification-level}

Use `[minify]` to minify authored `<style>` CSS and `<script>` JavaScript:

```toml
[minify]
css = "full"
js = "full"
```

| Value | Result |
|---|---|
| `none` | No minification. This is the default. |
| `full` | Run the full CSS or JavaScript minifier. |

At `full`, CSS containing `@{...}` holes uses gsx's built-in hole-aware safe
pass. A `<script>` body containing holes remains unminified.

CSS and JavaScript are configured independently. `GSX_MINIFY=none` or
`GSX_MINIFY=full` overrides both file values, and `gen.WithMinifyLevel` in a
custom binary overrides the environment.

Custom CSS or JavaScript minifier functions remain a Go extension; see
[Extensions](./extensions.md#minify-level).

## Class merging `class_merger` {#class_merger-tailwind-aware-class-merge-strategy}

Use `class_merger` when exact-token deduplication is not enough, such as when
Tailwind utilities `px-4` and `px-8` should collapse to `px-8`:

```toml
class_merger = "example.com/app/twcfg.Merge"
```

The named exported package-level symbol must have exactly this type:

```go
func([]string) string
```

When the merger is invoked, it receives one raw class string per contributing
source. A Tailwind wrapper can pass that slice directly to the configured
merger:

```go
package twcfg

import "github.com/jackielii/tailwind-merge-go/pkg/twmerge"

var merger = twmerge.CreateTwMerge(twmerge.GetDefaultConfig())

func Merge(classes []string) string { return merger(classes) }
```

With multiple sources, the default strategy removes exact duplicate tokens with
the later occurrence winning. A single source is preserved verbatim. A
programmatic `gen.WithClassMerger` overrides `class_merger`.

`class_merger` is top-level, so place it before the first table header. See
[Styling](./syntax/styling.md#tailwind-aware-class-merging) for where class
sources merge.

## Complete example

This is a typical multi-section file. Top-level keys come first:

```toml
filter_packages = ["example.com/app/templatefuncs"]
url_presets = ["htmx"]
class_merger = "example.com/app/twcfg.Merge"

[dev]
web = ["pnpm", "vite"]

[filters]
url = "github.com/jackielii/structpages.URLFor"

[formatter]
print_width = 100
imports = "goimports"

[minify]
css = "full"
js = "full"
```

## What stays in Go

Only function-valued hooks need a project-owned `gen.Main` binary:

- custom CSS or JavaScript formatters;
- custom CSS or JavaScript minifiers;
- a custom props field matcher.

See [Extensions](./extensions.md) for setup and option details. Declarative
filters, renderers, URL rules, minification levels, and class-merger references
can stay in `gsx.toml`.

## Inspect the resolved configuration

Use the human view while diagnosing a project:

```bash
gsx info
```

It shows the active config path and selected resolved settings. For scripts,
use the JSON view:

```bash
gsx info --json
```

The human and JSON commands expose different, incomplete inspection views;
neither is an exhaustive dump of every setting. Do not parse the human output
in automation.
